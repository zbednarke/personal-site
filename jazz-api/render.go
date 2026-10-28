package main

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const (
	maxRenderClips      = 24
	maxRenderDurationMS = 10 * 60 * 1000
	minRenderClipMS     = 500
	renderWidth         = 1920
	renderHeight        = 1080
	renderFrameRate     = 30
)

type renderClipRequest struct {
	RecordingID  string      `json:"recordingId"`
	StartMS      int         `json:"startMs"`
	EndMS        int         `json:"endMs"`
	VerticalCrop *renderCrop `json:"verticalCrop,omitempty"`
}

type renderCrop struct {
	CenterX float64 `json:"centerX"`
	CenterY float64 `json:"centerY"`
	Scale   float64 `json:"scale"`
}

func (c *renderCrop) valid() bool {
	return c == nil || (!math.IsNaN(c.CenterX) && !math.IsInf(c.CenterX, 0) && c.CenterX >= 0 && c.CenterX <= 1 &&
		!math.IsNaN(c.CenterY) && !math.IsInf(c.CenterY, 0) && c.CenterY >= 0 && c.CenterY <= 1 &&
		!math.IsNaN(c.Scale) && !math.IsInf(c.Scale, 0) && c.Scale >= .1 && c.Scale <= 1)
}

type renderMovieRequest struct {
	Title           string              `json:"title"`
	Clips           []renderClipRequest `json:"clips"`
	IncludeVertical bool                `json:"includeVertical,omitempty"`
}

type renderSource struct {
	RecordingID    uuid.UUID
	StartMS        int
	EndMS          int
	AudioObject    string
	VideoObject    string
	AudioURL       string
	VideoURL       string
	RecordingTitle string
	VerticalCrop   *renderCrop
}

func (app *application) renderClipStudioMovie(w http.ResponseWriter, r *http.Request) {
	var input renderMovieRequest
	if err := readJSON(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	title := clean(input.Title, 120)
	if title == "" || len(input.Clips) == 0 || len(input.Clips) > maxRenderClips {
		writeError(w, http.StatusUnprocessableEntity, "render title or clip list is invalid")
		return
	}
	userID, err := app.userID(r.Context())
	if err != nil {
		app.serverError(w, err)
		return
	}
	sources := make([]renderSource, 0, len(input.Clips))
	totalDuration := 0
	for _, clip := range input.Clips {
		recordingID, parseErr := uuid.Parse(clip.RecordingID)
		if parseErr != nil || clip.StartMS < 0 || clip.EndMS <= clip.StartMS || clip.EndMS-clip.StartMS < minRenderClipMS {
			writeError(w, http.StatusUnprocessableEntity, "render clip boundaries are invalid")
			return
		}
		if !clip.VerticalCrop.valid() {
			writeError(w, http.StatusUnprocessableEntity, "vertical crop must have centers within the image and a size between 10% and 100%")
			return
		}
		var source renderSource
		var durationMS int
		var mediaKind string
		err = app.db.QueryRow(r.Context(), `
			SELECT r.object_name,COALESCE(r.video_object_name,''),COALESCE(r.duration_ms,0),COALESCE(r.media_kind,'audio'),COALESCE(pb.title,'')
			FROM recordings r LEFT JOIN practice_blocks pb ON pb.id=r.practice_block_id AND pb.user_id=r.user_id
			WHERE r.id=$1 AND r.user_id=$2 AND r.status='ready'`, recordingID, userID).
			Scan(&source.AudioObject, &source.VideoObject, &durationMS, &mediaKind, &source.RecordingTitle)
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "render recording not found")
			return
		}
		if err != nil {
			app.serverError(w, err)
			return
		}
		if mediaKind != "video" || source.VideoObject == "" || clip.EndMS > durationMS {
			writeError(w, http.StatusUnprocessableEntity, "every rendered clip must have synchronized video and lossless audio")
			return
		}
		totalDuration += clip.EndMS - clip.StartMS
		if totalDuration > maxRenderDurationMS {
			writeError(w, http.StatusUnprocessableEntity, "render output cannot exceed ten minutes")
			return
		}
		source.RecordingID = recordingID
		source.StartMS = clip.StartMS
		source.EndMS = clip.EndMS
		source.VerticalCrop = clip.VerticalCrop
		sources = append(sources, source)
	}
	if input.IncludeVertical {
		frames := 0
		for _, source := range sources {
			frames += renderFrameCount(source)
		}
		totalDuration = int(math.Round(float64(frames) * 1000 / renderFrameRate))
		if totalDuration > maxRenderDurationMS {
			writeError(w, http.StatusUnprocessableEntity, "render output cannot exceed ten minutes after frame alignment")
			return
		}
	}
	expires := time.Now().Add(2 * time.Hour)
	for index := range sources {
		sources[index].VideoURL, err = app.signedRecordingObjectURL(r.Context(), sources[index].VideoObject, expires, nil)
		if err != nil {
			app.serverError(w, err)
			return
		}
		sources[index].AudioURL, err = app.signedRecordingObjectURL(r.Context(), sources[index].AudioObject, expires, nil)
		if err != nil {
			app.serverError(w, err)
			return
		}
	}
	renderID := uuid.New()
	filename := renderDownloadFilename(title)
	objectName := fmt.Sprintf("renders/%s/%s/%s", userID, renderID, filename)
	objectNames := []string{objectName}
	filenames := []string{filename}
	if input.IncludeVertical {
		verticalFilename := strings.TrimSuffix(filename, ".mp4") + "-vertical.mp4"
		filenames = append(filenames, verticalFilename)
		objectNames = append(objectNames, fmt.Sprintf("renders/%s/%s/%s", userID, renderID, verticalFilename))
	}
	if err := app.streamRenderedMovies(r.Context(), objectNames, renderID, sources); err != nil {
		app.logger.Error("clip render failed", "render_id", renderID, "user_id", userID, "error", err)
		writeError(w, http.StatusInternalServerError, "render failed; the source recordings were not changed")
		return
	}
	downloadExpires := time.Now().Add(time.Hour)
	outputs := make([]map[string]any, 0, len(objectNames))
	for index, name := range objectNames {
		downloadURL, err := app.signedRecordingObjectURL(r.Context(), name, downloadExpires, url.Values{
			"response-content-disposition": {`attachment; filename="` + filenames[index] + `"`},
			"response-content-type":        {"video/mp4"},
		})
		if err != nil {
			app.deleteRenderedObjects(objectNames)
			app.serverError(w, err)
			return
		}
		label, width, height := "Full", renderWidth, renderHeight
		if index == 1 {
			label, width, height = "Vertical", renderHeight, renderWidth
		}
		outputs = append(outputs, map[string]any{"label": label, "filename": filenames[index], "url": downloadURL, "width": width, "height": height, "expiresAt": downloadExpires})
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"id": renderID, "filename": filename, "url": outputs[0]["url"], "expiresAt": downloadExpires, "outputs": outputs,
		"retainedUntil": time.Now().Add(24 * time.Hour), "durationMs": totalDuration,
		"quality": map[string]any{"video": "1080p H.264, CRF 18 (fast render)", "audio": "AAC 320 kbps playback + lossless ALAC master, 48 kHz, original levels", "audioLossless": true},
	})
}

func buildFFmpegRenderArgs(sources []renderSource) []string {
	return buildFFmpegSourceArgs(sources, false)
}

func buildFFmpegClipArgs(source renderSource) []string {
	return buildFFmpegSourceArgs([]renderSource{source}, true)
}

func buildFFmpegSourceArgs(sources []renderSource, vertical bool) []string {
	args := []string{"-hide_banner", "-loglevel", "warning", "-xerror", "-filter_complex_threads", "2"}
	for _, source := range sources {
		start := formatFFmpegSeconds(source.StartMS)
		duration := formatFFmpegSeconds(source.EndMS - source.StartMS)
		// Browser-recorded MP4s can have sparse keyframes. With input-side
		// seeking, -t starts counting at the preceding keyframe while accurate
		// seeking discards that preroll. The result can contain only a few real
		// frames, after which tpad clones the last frame for the rest of the
		// requested clip. Leave the video input open and let the trim filter set
		// the exact clip duration; this preserves accurate seeking without
		// silently manufacturing long frozen regions.
		args = append(args, "-threads", "1", "-ss", start, "-i", source.VideoURL)
		args = append(args, "-threads", "1", "-ss", start, "-t", duration, "-i", source.AudioURL)
	}
	filters := make([]string, 0, len(sources)*2+1)
	concatInputs := strings.Builder{}
	verticalInputs := strings.Builder{}
	for index := range sources {
		videoInput, audioInput := index*2, index*2+1
		duration := formatFFmpegSeconds(sources[index].EndMS - sources[index].StartMS)
		videoLabel := fmt.Sprintf("%d:v:0", videoInput)
		// Normalize display pixels before branching. Concat also requires an
		// identical output SAR when landscape and portrait takes are mixed.
		filters = append(filters, fmt.Sprintf("[%s]scale=w='max(2,trunc(iw*if(gt(sar,0),sar,1)/2)*2)':h=ih,setsar=1[square%d]", videoLabel, index))
		videoLabel = fmt.Sprintf("square%d", index)
		if vertical {
			filters = append(filters, fmt.Sprintf("[%s]split=2[fullsrc%d][cropsrc%d]", videoLabel, index, index))
			videoLabel = fmt.Sprintf("fullsrc%d", index)
			crop := sources[index].VerticalCrop
			if crop == nil {
				crop = &renderCrop{CenterX: .5, CenterY: .5, Scale: 1}
			}
			filters = append(filters, fmt.Sprintf("[cropsrc%d]crop=w='max(2,trunc(min(iw,ih*9/16)*%.8f/2)*2)':h='max(2,trunc(min(ih,iw*16/9)*%.8f/2)*2)':x='max(0,min(iw-ow,iw*%.8f-ow/2))':y='max(0,min(ih-oh,ih*%.8f-oh/2))':exact=1,scale=1080:1920,setsar=1,fps=%d,format=yuv420p,setpts=PTS-STARTPTS,tpad=stop_mode=clone:stop_duration=%s,trim=duration=%s[vertical%d]", index, crop.Scale, crop.Scale, crop.CenterX, crop.CenterY, renderFrameRate, duration, duration, index))
			verticalInputs.WriteString(fmt.Sprintf("[vertical%d]", index))
		}
		filters = append(filters,
			// Normalize timestamps before any duration-sensitive filters. FFmpeg
			// versions differ in whether input-side accurate seeks preserve a
			// non-zero first PTS. Trimming before normalization can discard most of
			// a valid late-position clip and make tpad clone its last frame.
			fmt.Sprintf("[%s]scale=%d:%d:force_original_aspect_ratio=decrease,pad=%d:%d:(ow-iw)/2:(oh-ih)/2:color=black,setsar=1,fps=%d,format=yuv420p,setpts=PTS-STARTPTS,tpad=stop_mode=clone:stop_duration=%s,trim=duration=%s[v%d]", videoLabel, renderWidth, renderHeight, renderWidth, renderHeight, renderFrameRate, duration, duration, index),
			fmt.Sprintf("[%d:a:0]aresample=48000,aformat=sample_fmts=s32:channel_layouts=mono,apad=pad_dur=%s,atrim=duration=%s,asetpts=PTS-STARTPTS[a%d]", audioInput, duration, duration, index))
		concatInputs.WriteString(fmt.Sprintf("[v%d][a%d]", index, index))
	}
	filters = append(filters, fmt.Sprintf("%sconcat=n=%d:v=1:a=1[outv][joined]", concatInputs.String(), len(sources)))
	if vertical {
		filters = append(filters, fmt.Sprintf("%sconcat=n=%d:v=1:a=0[outvertical]", verticalInputs.String(), len(sources)))
		frames := renderFrameCount(sources[0])
		filters = append(filters,
			fmt.Sprintf("[outv]fps=30,tpad=stop_mode=clone:stop=%d,trim=end_frame=%d,setpts=N/(30*TB)[rawfull]", frames, frames),
			fmt.Sprintf("[outvertical]fps=30,tpad=stop_mode=clone:stop=%d,trim=end_frame=%d,setpts=N/(30*TB)[rawvertical]", frames, frames),
			fmt.Sprintf("[joined]apad=whole_len=%d,atrim=end_sample=%d,asetpts=PTS-STARTPTS[outraw]", frames*1600, frames*1600))
	} else {
		filters = append(filters, "[joined]asplit=2[outaac][outlossless]")
	}
	args = append(args, "-filter_complex", strings.Join(filters, ";"))
	if vertical {
		// One multiplexed pipe avoids cross-stream read/write ordering cycles.
		return append(args, "-map", "[rawfull]", "-map", "[rawvertical]", "-map", "[outraw]",
			"-c:v", "rawvideo", "-pix_fmt", "yuv420p", "-threads", "1", "-fps_mode:v", "passthrough", "-enc_time_base:v", "1:30",
			"-c:a", "pcm_s32le", "-ar", "48000", "-ac", "1", "-f", "nut", "pipe:1")
	}
	return append(args, renderOutputArgs("outv", "outaac", "outlossless", "pipe:1", false)...)
}

func renderFrameCount(source renderSource) int {
	return ((source.EndMS-source.StartMS)*renderFrameRate + 999) / 1000
}

// The concat demuxer opens one source NUT pipe at a time. Each has identical
// normalized raw tracks; only the final movie is encoded and uploaded.
func buildFFmpegPairedEncoderArgs() []string {
	args := []string{"-hide_banner", "-loglevel", "warning", "-xerror", "-filter_complex_threads", "2"}
	args = append(args, "-threads", "1", "-probesize", "32", "-analyzeduration", "1", "-fpsprobesize", "0",
		"-f", "concat", "-safe", "0", "-protocol_whitelist", "file,pipe", "-i", "pipe:0",
		"-filter_complex", "[0:v:0]null[outv];[0:v:1]null[outvertical];[0:a:0]asplit=4[outaac][outlossless][verticalaac][verticallossless]")
	args = append(args, renderOutputArgs("outv", "outaac", "outlossless", "pipe:1", true)...)
	return append(args, renderOutputArgs("outvertical", "verticalaac", "verticallossless", "pipe:3", true)...)
}

func renderOutputArgs(video, aac, lossless, destination string, dual bool) []string {
	threads := "2"
	if dual {
		threads = "1"
	}
	args := []string{"-map", "[" + video + "]", "-map", "[" + aac + "]", "-map", "[" + lossless + "]",
		"-c:v", "libx264", "-preset", "veryfast", "-crf", "18", "-profile:v", "high", "-pix_fmt", "yuv420p", "-threads", threads,
		"-c:a:0", "aac", "-b:a:0", "320k", "-ar:a:0", "48000", "-metadata:s:a:0", "title=Compatibility AAC", "-metadata:s:a:0", "handler_name=Compatibility AAC", "-disposition:a:0", "default",
		"-c:a:1", "alac", "-ar:a:1", "48000", "-metadata:s:a:1", "title=Lossless ALAC Master", "-metadata:s:a:1", "handler_name=Lossless ALAC Master", "-disposition:a:1", "0",
		// Delay the first moov until encoder priming is known, so edit lists
		// align AAC/ALAC with B-frame video without a leading audio offset.
		"-movflags", "+frag_keyframe+delay_moov+default_base_moof", "-f", "mp4", destination}
	return args
}

func formatFFmpegSeconds(milliseconds int) string {
	return strconv.FormatFloat(float64(milliseconds)/1000, 'f', 3, 64)
}

func renderDownloadFilename(title string) string {
	name := strings.Trim(downloadFilenamePartPattern.ReplaceAllString(strings.ToLower(title), "-"), "-")
	if name == "" {
		name = "jazz-practice"
	}
	if len(name) > 72 {
		name = strings.Trim(name[:72], "-")
	}
	return name + ".mp4"
}

type cappedBuffer struct{ bytes.Buffer }

func (buffer *cappedBuffer) Write(contents []byte) (int, error) {
	const max = 16 << 10
	if buffer.Len() < max {
		remaining := max - buffer.Len()
		_, _ = buffer.Buffer.Write(contents[:min(len(contents), remaining)])
	}
	return len(contents), nil
}
