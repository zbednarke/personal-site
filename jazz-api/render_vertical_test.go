package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

type fileRenderUpload struct{ *os.File }

func (f fileRenderUpload) CloseWithError(error) error { return f.Close() }

func mediaCommand(t *testing.T, args ...string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	data, err := exec.CommandContext(ctx, envOr("FFMPEG_PATH", "ffmpeg"), args...).CombinedOutput()
	if err != nil {
		t.Fatalf("synthetic media command: %v\n%s", err, data)
	}
	return data
}

func TestVerticalCropValidation(t *testing.T) {
	for _, c := range []*renderCrop{nil, {CenterX: .5, CenterY: .5, Scale: 1}, {CenterX: 0, CenterY: 1, Scale: .1}} {
		if !c.valid() {
			t.Fatal("valid crop rejected")
		}
	}
	for _, c := range []*renderCrop{{CenterX: -.1, Scale: 1}, {CenterY: 1.1, Scale: 1}, {Scale: 0}, {Scale: 1.1}, {CenterX: math.NaN(), Scale: 1}, {CenterY: math.Inf(1), Scale: 1}} {
		if c.valid() {
			t.Fatalf("invalid crop accepted: %+v", c)
		}
	}
}

func TestVerticalRenderWithFFmpeg(t *testing.T) {
	for _, program := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(program); err != nil {
			t.Skip(program + " not installed")
		}
	}
	workspace := t.TempDir()
	for _, tc := range []struct {
		name, filter string
		rotate       bool
		crops        []*renderCrop
		wantColors   []string
	}{
		{"landscape", "color=red:s=640x360:r=30:d=2.2,drawbox=x=213:y=0:w=214:h=360:color=green:t=fill,drawbox=x=427:y=0:w=213:h=360:color=blue:t=fill", false, []*renderCrop{{CenterX: 0, CenterY: .5, Scale: 1}, {CenterX: 1, CenterY: .5, Scale: 1}}, []string{"red", "blue"}},
		{"portrait", "color=green:s=360x640:r=30:d=1.2", false, []*renderCrop{nil}, []string{"green"}},
		{"square", "color=green:s=480x480:r=30:d=1.2", false, []*renderCrop{nil}, []string{"green"}},
		{"non-square-pixels", "color=red:s=640x360:r=30:d=1.2,drawbox=x=213:y=0:w=427:h=360:color=blue:t=fill,setsar=2", false, []*renderCrop{{CenterX: .25, CenterY: .5, Scale: 1}}, []string{"red"}},
		{"rotation", "color=red:s=640x360:r=30:d=1.2,drawbox=x=213:y=0:w=214:h=360:color=green:t=fill,drawbox=x=427:y=0:w=213:h=360:color=blue:t=fill", true, []*renderCrop{nil}, []string{"blue"}},
		{"off-frame-cuts", "color=red:s=640x360:r=30:d=2.2,drawbox=x=213:y=0:w=214:h=360:color=green:t=fill,drawbox=x=427:y=0:w=213:h=360:color=blue:t=fill", false, []*renderCrop{{CenterX: 0, CenterY: .5, Scale: 1}, {CenterX: 1, CenterY: .5, Scale: 1}}, []string{"red", "blue"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := filepath.Join(workspace, tc.name+".mp4")
			mediaCommand(t, "-v", "error", "-y", "-f", "lavfi", "-i", tc.filter, "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=2.2", "-c:v", "libx264", "-threads", "1", "-g", "300", "-c:a", "aac", "-shortest", source)
			if tc.rotate {
				rotated := filepath.Join(workspace, "rotated.mp4")
				mediaCommand(t, "-v", "error", "-y", "-display_rotation:v:0", "90", "-i", source, "-c", "copy", rotated)
				source = rotated
			}
			sources := make([]renderSource, 0, len(tc.crops))
			wantFrames := 30 * len(tc.crops)
			for index, crop := range tc.crops {
				duration := 1000
				if tc.name == "off-frame-cuts" {
					duration = []int{501, 733}[index]
					wantFrames = 38
				}
				sources = append(sources, renderSource{VideoURL: source, AudioURL: source, StartMS: index * 1000, EndMS: index*1000 + duration, VerticalCrop: crop})
			}
			paths := []string{filepath.Join(workspace, tc.name+"-full.mp4"), filepath.Join(workspace, tc.name+"-vertical.mp4")}
			ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
			defer cancel()
			err := runRenderPipeline(ctx, buildFFmpegPairedEncoderArgs(), 2, func(_ context.Context, index int) renderUpload {
				f, err := os.Create(paths[index])
				if err != nil {
					t.Fatal(err)
				}
				return fileRenderUpload{f}
			}, sources)
			if err != nil {
				t.Fatalf("dual render: %v", err)
			}
			for index, path := range paths {
				data, err := exec.Command("ffprobe", "-v", "error", "-show_streams", "-show_format", "-of", "json", path).Output()
				if err != nil {
					t.Fatal(err)
				}
				var probe struct {
					Streams []struct {
						CodecName     string `json:"codec_name"`
						Width, Height int
						SampleRate    string `json:"sample_rate"`
						StartTime     string `json:"start_time"`
					}
					Format struct{ Duration string }
				}
				if err := json.Unmarshal(data, &probe); err != nil {
					t.Fatal(err)
				}
				width, height := 1920, 1080
				if index == 1 {
					width, height = 1080, 1920
				}
				if len(probe.Streams) != 3 || probe.Streams[0].Width != width || probe.Streams[0].Height != height || probe.Streams[1].CodecName != "aac" || probe.Streams[2].CodecName != "alac" || probe.Streams[2].SampleRate != "48000" {
					t.Fatalf("wrong streams: %+v", probe.Streams)
				}
				videoStart, err := strconv.ParseFloat(probe.Streams[0].StartTime, 64)
				if err != nil {
					t.Fatal(err)
				}
				for _, stream := range probe.Streams[1:] {
					audioStart, err := strconv.ParseFloat(stream.StartTime, 64)
					if err != nil || math.Abs(audioStart-videoStart) > 1.0/30 {
						t.Fatalf("A/V start diverged: video=%f audio=%s %v streams=%+v", videoStart, stream.StartTime, err, probe.Streams)
					}
				}
				pcm := mediaCommand(t, "-v", "error", "-i", path, "-map", "0:a:1", "-c:a", "pcm_s16le", "-f", "s16le", "-")
				if len(pcm) != wantFrames*1600*2 {
					t.Fatalf("lossless audio duration: %d bytes", len(pcm))
				}
				// Count actual decoded frames; fragmented MP4 duration can include AAC priming.
				frames := mediaCommand(t, "-v", "error", "-i", path, "-map", "0:v:0", "-f", "framemd5", "-")
				count := 0
				for _, line := range strings.Split(string(frames), "\n") {
					if line != "" && !strings.HasPrefix(line, "#") {
						count++
					}
				}
				if count != wantFrames {
					t.Fatalf("frame count=%d want %d", count, wantFrames)
				}
			}
			for index, color := range tc.wantColors {
				sampleMS := index*1000 + 500
				if tc.name == "off-frame-cuts" {
					sampleMS = []int{250, 783}[index]
				}
				pixel := mediaCommand(t, "-v", "error", "-ss", formatFFmpegSeconds(sampleMS), "-i", paths[1], "-vf", "crop=2:2:900:200,scale=1:1,format=rgb24", "-frames:v", "1", "-f", "rawvideo", "-")
				if len(pixel) != 3 {
					t.Fatalf("pixel bytes=%d", len(pixel))
				}
				r, g, b := pixel[0], pixel[1], pixel[2]
				if (color == "red" && (r < 180 || g > 60 || b > 60)) || (color == "blue" && (b < 180 || r > 60 || g > 60)) || (color == "green" && (g < 90 || r > 60 || b > 60)) {
					t.Fatalf("wrong framed pixel for %s: %v", color, pixel)
				}
			}
			if tc.name == "landscape" {
				pixel := mediaCommand(t, "-v", "error", "-ss", "0.5", "-i", paths[0], "-vf", "crop=2:2:960:500,scale=1:1,format=rgb24", "-frames:v", "1", "-f", "rawvideo", "-")
				if len(pixel) != 3 || pixel[1] < 90 || pixel[0] > 60 || pixel[2] > 60 {
					t.Fatalf("full framing changed: %v", pixel)
				}
			}
		})
	}
	t.Run("mixed-aspects-and-legacy-full", func(t *testing.T) {
		sources := []renderSource{}
		for _, name := range []string{"landscape.mp4", "portrait.mp4", "non-square-pixels.mp4", "rotated.mp4"} {
			source := filepath.Join(workspace, name)
			sources = append(sources, renderSource{VideoURL: source, AudioURL: source, StartMS: 0, EndMS: 500})
		}
		for _, count := range []int{1, 2} {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			paths := []string{filepath.Join(workspace, fmt.Sprintf("mixed-%d-full.mp4", count)), filepath.Join(workspace, "mixed-vertical.mp4")}
			args := buildFFmpegRenderArgs(sources)
			var feeds []renderSource
			if count == 2 {
				args = buildFFmpegPairedEncoderArgs()
				feeds = sources
			}
			err := runRenderPipeline(ctx, args, count, func(_ context.Context, index int) renderUpload {
				f, err := os.Create(paths[index])
				if err != nil {
					t.Fatal(err)
				}
				return fileRenderUpload{f}
			}, feeds)
			if err != nil {
				t.Fatalf("mixed source render (%d outputs): %v", count, err)
			}
			for _, path := range paths[:count] {
				frames := mediaCommand(t, "-v", "error", "-i", path, "-map", "0:v:0", "-f", "framemd5", "-")
				frameCount := 0
				for _, line := range strings.Split(string(frames), "\n") {
					if line != "" && !strings.HasPrefix(line, "#") {
						frameCount++
					}
				}
				pcm := mediaCommand(t, "-v", "error", "-i", path, "-map", "0:a:1", "-c:a", "pcm_s16le", "-f", "s16le", "-")
				if frameCount != 60 || len(pcm) != 192000 {
					t.Fatalf("mixed timeline lost frames/samples: frames=%d audio=%d", frameCount, len(pcm))
				}
			}
		}
	})
}

type failingRenderUpload struct {
	ctx           context.Context
	fail, aborted bool
}

func (u *failingRenderUpload) Write(p []byte) (int, error) {
	if u.fail {
		return 0, errors.New("simulated upload failure")
	}
	select {
	case <-u.ctx.Done():
		return 0, u.ctx.Err()
	case <-time.After(time.Millisecond):
		return len(p), nil
	}
}
func (u *failingRenderUpload) Close() error               { return nil }
func (u *failingRenderUpload) CloseWithError(error) error { u.aborted = true; return nil }

func TestRenderUploadFailureAndCancellationStopAllStreams(t *testing.T) {
	for _, mode := range []string{"upload-failure", "request-cancel", "staged-upload-failure", "staged-request-cancel", "source-failure"} {
		t.Run(mode, func(t *testing.T) {
			cancelRequest := strings.Contains(mode, "request-cancel")
			staged := strings.HasPrefix(mode, "staged-") || mode == "source-failure"
			launcher := filepath.Join(t.TempDir(), "ffmpeg")
			// exec avoids an orphaned shell child holding output descriptors open.
			script := "#!/usr/bin/python3\nimport os,sys\nwhile True:\n os.write(1,b'a'*65536)\n if 'nut' not in sys.argv: os.write(3,b'b'*65536)\n"
			if mode == "source-failure" {
				script = "#!/usr/bin/python3\nimport os,sys\nif 'nut' in sys.argv: sys.exit(42)\nwhile True:\n os.write(1,b'a'*65536)\n os.write(3,b'b'*65536)\n"
			}
			if err := os.WriteFile(launcher, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("FFMPEG_PATH", launcher)
			timeout := 3 * time.Second
			if cancelRequest {
				timeout = 50 * time.Millisecond
			}
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()
			uploads := make([]*failingRenderUpload, 2)
			start := time.Now()
			var feeds []renderSource
			if staged {
				feeds = []renderSource{{StartMS: 0, EndMS: 1000}}
			}
			err := runRenderPipeline(ctx, nil, 2, func(uploadCtx context.Context, index int) renderUpload {
				uploads[index] = &failingRenderUpload{ctx: uploadCtx, fail: index == 1 && !cancelRequest && mode != "source-failure"}
				return uploads[index]
			}, feeds)
			if err == nil || time.Since(start) > 2*time.Second {
				t.Fatalf("render did not stop promptly: %v", err)
			}
			for _, upload := range uploads {
				if !upload.aborted {
					t.Fatal("upload not aborted")
				}
			}
		})
	}
}

func TestVerticalRenderRejectsInvalidCropAndForeignSource(t *testing.T) {
	ctx := context.WithValue(context.Background(), userSubjectKey, "crop-owner")
	db := captionTestDB(t, ctx)
	app := &application{db: db, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	foreign, err := app.userID(context.WithValue(ctx, userSubjectKey, "other-crop-owner"))
	if err != nil {
		t.Fatal(err)
	}
	id := uuid.New()
	_, err = db.Exec(ctx, `INSERT INTO recordings(id,user_id,bucket,object_name,video_object_name,media_kind,object_generation,checksum,uploaded_at,content_type,expected_size_bytes,size_bytes,duration_ms,recorded_at,status) VALUES($1,$2,'test','audio.wav','video.mp4','video',1,'abc',now(),'audio/wav',100,100,10000,now(),'ready')`, id, foreign)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		crop string
		code int
	}{
		{`{"centerX":0.5,"centerY":0.5,"scale":0}`, 422},
		{`{"centerX":0.5,"centerY":0.5,"scale":1}`, 404},
	} {
		body := `{"title":"Test","includeVertical":true,"clips":[{"recordingId":"` + id.String() + `","startMs":0,"endMs":1000,"verticalCrop":` + tc.crop + `}]}`
		w := httptest.NewRecorder()
		app.renderClipStudioMovie(w, httptest.NewRequest("POST", "/", strings.NewReader(body)).WithContext(ctx))
		if w.Code != tc.code {
			t.Fatalf("render validation=%d want %d: %s", w.Code, tc.code, w.Body.String())
		}
	}
}

// Exercise both the largest clip count (decoder/filter memory) and a seek far
// past a sparse keyframe (moving pictures must not turn into a frozen tail).
func TestDualRenderAtMaximumClipCount(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	workspace := t.TempDir()
	source := filepath.Join(workspace, "motion.mp4")
	mediaCommand(t, "-v", "error", "-y", "-f", "lavfi", "-i", "testsrc2=s=1280x720:r=30:d=6", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=6", "-c:v", "libx264", "-threads", "1", "-g", "300", "-keyint_min", "300", "-sc_threshold", "0", "-c:a", "aac", source)
	sources := make([]renderSource, maxRenderClips)
	for index := range sources {
		sources[index] = renderSource{VideoURL: source, AudioURL: source, StartMS: 4200, EndMS: 4700, VerticalCrop: &renderCrop{CenterX: float64(index%3) / 2, CenterY: .5, Scale: 1}}
	}
	paths := []string{filepath.Join(workspace, "full.mp4"), filepath.Join(workspace, "vertical.mp4")}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	start := time.Now()
	err := runRenderPipeline(ctx, buildFFmpegPairedEncoderArgs(), 2, func(_ context.Context, index int) renderUpload {
		f, err := os.Create(paths[index])
		if err != nil {
			t.Fatal(err)
		}
		return fileRenderUpload{f}
	}, sources)
	if err != nil {
		t.Fatalf("maximum-clip render: %v", err)
	}
	t.Logf("24-clip, 12-second dual render completed in %s", time.Since(start))
	for _, path := range paths {
		hashes := mediaCommand(t, "-v", "error", "-i", path, "-map", "0:v:0", "-f", "framemd5", "-")
		frames := strings.Split(strings.TrimSpace(string(hashes)), "\n")
		count := 0
		unique := map[string]bool{}
		for _, frame := range frames {
			if strings.HasPrefix(frame, "#") {
				continue
			}
			count++
			if count <= 15 {
				fields := strings.Split(frame, ",")
				unique[strings.TrimSpace(fields[len(fields)-1])] = true
			}
		}
		if count != maxRenderClips*15 || len(unique) < 10 {
			t.Fatalf("wrong duration or frozen late seek: frames=%d first-clip unique=%d", count, len(unique))
		}
	}
}
