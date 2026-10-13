package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"testing"
)

type longToneCodecResult struct {
	durationMS int64
	frames     []longToneFrame
}

func TestLongToneCodecThresholdAndPitchAgreement(t *testing.T) {
	ffmpeg, err := exec.LookPath(envOr("FFMPEG_PATH", "ffmpeg"))
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}

	for _, test := range []struct {
		name       string
		durationMS int
		completed  bool
	}{
		{name: "below threshold", durationMS: 29980, completed: false},
		{name: "exact threshold", durationMS: 30000, completed: true},
		{name: "above threshold", durationMS: 30200, completed: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			pcm := codecTonePCM(440, test.durationMS, 0.2, 0)
			raw := analyzeLongToneCodecPCM(t, pcm)
			assertLongToneCodecResult(t, "PCM", raw, 440, test.completed)
			rawMedian := medianLongTonePitch(raw.frames, 69)

			for _, codec := range []struct {
				name string
				ext  string
				args []string
			}{
				{name: "AAC MP4", ext: ".m4a", args: []string{"-c:a", "aac", "-b:a", "128k"}},
				{name: "Opus WebM", ext: ".webm", args: []string{"-c:a", "libopus", "-b:a", "96k"}},
			} {
				t.Run(codec.name, func(t *testing.T) {
					encoded := encodeLongToneCodecFixture(t, ffmpeg, pcm, codec.ext, codec.args)
					got := decodeAndAnalyzeLongToneCodec(t, encoded)
					assertLongToneCodecResult(t, codec.name, got, 440, test.completed)
					median := medianLongTonePitch(got.frames, 69)
					if math.IsNaN(median) || math.IsNaN(rawMedian) {
						t.Fatal("missing voiced A4 frames for codec/PCM comparison")
					}
					if errorCents := math.Abs(1200 * math.Log2(median/rawMedian)); errorCents > 2 {
						t.Fatalf("median pitch differs from PCM by %.3f cents (codec %.3fHz, PCM %.3fHz)", errorCents, median, rawMedian)
					}
					// Container/codec delay and padding must not move source duration
					// enough to turn 29.98 seconds into a verified 30-second hold.
					if delta := absInt64(got.durationMS - int64(test.durationMS)); delta > 40 {
						t.Fatalf("decoded duration=%dms, source=%dms (delta %dms)", got.durationMS, test.durationMS, delta)
					}
				})
			}
		})
	}
}

func TestLongToneCodecContinuityResets(t *testing.T) {
	ffmpeg, err := exec.LookPath(envOr("FFMPEG_PATH", "ffmpeg"))
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	codecs := []struct {
		name string
		ext  string
		args []string
	}{
		{name: "AAC MP4", ext: ".m4a", args: []string{"-c:a", "aac", "-b:a", "128k"}},
		{name: "Opus WebM", ext: ".webm", args: []string{"-c:a", "libopus", "-b:a", "96k"}},
	}

	t.Run("breath", func(t *testing.T) {
		pcm := append(codecTonePCM(440, 15000, 0.2, 0), make([]byte, 200*longToneSampleRate/1000*4)...)
		pcm = append(pcm, codecTonePCM(440, 15000, 0.2, 0)...)
		assertLongToneCodecNotCompleted(t, "PCM", analyzeLongToneCodecPCM(t, pcm))
		for _, codec := range codecs {
			t.Run(codec.name, func(t *testing.T) {
				path := encodeLongToneCodecFixture(t, ffmpeg, pcm, codec.ext, codec.args)
				got := decodeAndAnalyzeLongToneCodec(t, path)
				assertLongToneCodecNotCompleted(t, codec.name, got)
				if median := medianLongTonePitch(got.frames, 69); math.IsNaN(median) || math.Abs(1200*math.Log2(median/440)) > 2 {
					t.Fatalf("median A4 pitch %.3fHz exceeds 2-cent tolerance", median)
				}
			})
		}
	})

	t.Run("legato note change", func(t *testing.T) {
		pcm := append(codecTonePCM(440, 15000, 0.2, 0), codecTonePCM(466.1637615, 15200, 0.2, 15000)...)
		assertLongToneCodecLegato(t, "PCM", analyzeLongToneCodecPCM(t, pcm))
		for _, codec := range codecs {
			t.Run(codec.name, func(t *testing.T) {
				path := encodeLongToneCodecFixture(t, ffmpeg, pcm, codec.ext, codec.args)
				assertLongToneCodecLegato(t, codec.name, decodeAndAnalyzeLongToneCodec(t, path))
			})
		}
	})
}

func TestLongToneCodecVideoUsesAudioPresentationEnd(t *testing.T) {
	ffmpeg, err := exec.LookPath(envOr("FFMPEG_PATH", "ffmpeg"))
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	pcm := codecTonePCM(440, 29980, 0.2, 0)
	path := encodeLongToneAACVideoFixture(t, ffmpeg, pcm, 30200)
	got := decodeAndAnalyzeLongToneCodecAsset(t, path, "video")
	assertLongToneCodecResult(t, "AAC video", got, 440, false)
	if delta := absInt64(got.durationMS - 29980); delta > 40 {
		t.Fatalf("decoded audio duration=%dms, want source audio end 29980ms; video is deliberately longer", got.durationMS)
	}
}

func codecTonePCM(frequency float64, durationMS int, amplitude float64, phaseOffsetMS int) []byte {
	samples := durationMS * longToneSampleRate / 1000
	phaseOffset := phaseOffsetMS * longToneSampleRate / 1000
	pcm := make([]byte, samples*4)
	for i := 0; i < samples; i++ {
		value := float32(amplitude * math.Sin(2*math.Pi*frequency*float64(i+phaseOffset)/longToneSampleRate))
		binary.LittleEndian.PutUint32(pcm[i*4:], math.Float32bits(value))
	}
	return pcm
}

func encodeLongToneCodecFixture(t *testing.T, ffmpeg string, pcm []byte, extension string, codecArgs []string) string {
	t.Helper()
	directory := t.TempDir()
	rawPath := filepath.Join(directory, "source.f32le")
	if err := os.WriteFile(rawPath, pcm, 0o600); err != nil {
		t.Fatal(err)
	}
	encodedPath := filepath.Join(directory, "encoded"+extension)
	args := []string{"-nostdin", "-hide_banner", "-loglevel", "error", "-y", "-f", "f32le", "-ar", "16000", "-ac", "1", "-i", rawPath}
	args = append(args, codecArgs...)
	args = append(args, encodedPath)
	command := exec.Command(ffmpeg, args...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("encode %s: %v: %s", extension, err, output)
	}
	return encodedPath
}

func encodeLongToneAACVideoFixture(t *testing.T, ffmpeg string, pcm []byte, videoDurationMS int) string {
	t.Helper()
	directory := t.TempDir()
	rawPath := filepath.Join(directory, "source.f32le")
	if err := os.WriteFile(rawPath, pcm, 0o600); err != nil {
		t.Fatal(err)
	}
	encodedPath := filepath.Join(directory, "encoded.mp4")
	videoDuration := float64(videoDurationMS) / 1000
	args := []string{
		"-nostdin", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "f32le", "-ar", "16000", "-ac", "1", "-i", rawPath,
		"-f", "lavfi", "-i", "color=c=black:s=32x32:r=5:d=" + formatCodecSeconds(videoDuration),
		"-map", "0:a:0", "-map", "1:v:0", "-c:a", "aac", "-b:a", "128k", "-c:v", "libx264", "-pix_fmt", "yuv420p", encodedPath,
	}
	command := exec.Command(ffmpeg, args...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("encode AAC video: %v: %s", err, output)
	}
	return encodedPath
}

func formatCodecSeconds(seconds float64) string {
	return strconv.FormatFloat(seconds, 'f', 3, 64)
}

func analyzeLongToneCodecPCM(t *testing.T, pcm []byte) longToneCodecResult {
	t.Helper()
	result := longToneCodecResult{}
	duration, err := analyzeLongTonePCM(context.Background(), bytes.NewReader(pcm), func(batch []longToneFrame) error {
		result.frames = append(result.frames, batch...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	result.durationMS = duration
	return result
}

func decodeAndAnalyzeLongToneCodec(t *testing.T, path string) longToneCodecResult {
	return decodeAndAnalyzeLongToneCodecAsset(t, path, "audio")
}

func decodeAndAnalyzeLongToneCodecAsset(t *testing.T, path, asset string) longToneCodecResult {
	t.Helper()
	result := longToneCodecResult{}
	duration, err := decodeLongToneWithFFmpeg(context.Background(), path, asset, func(reader io.Reader) (int64, error) {
		return analyzeLongTonePCM(context.Background(), reader, func(batch []longToneFrame) error {
			result.frames = append(result.frames, batch...)
			return nil
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	result.durationMS = duration
	return result
}

func assertLongToneCodecResult(t *testing.T, label string, result longToneCodecResult, wantHz float64, completed bool) {
	t.Helper()
	if len(result.frames) == 0 {
		t.Fatalf("%s emitted no frames", label)
	}
	last := result.frames[len(result.frames)-1]
	if got := last.State == "completed"; got != completed {
		t.Fatalf("%s completed=%v, want %v; duration=%dms final=%+v", label, got, completed, result.durationMS, last)
	}
	median := medianLongTonePitch(result.frames, 69)
	if errorCents := math.Abs(1200 * math.Log2(median/wantHz)); math.IsNaN(median) || errorCents > 2 {
		t.Fatalf("%s median pitch error %.3f cents (%.3fHz, want %.3fHz)", label, errorCents, median, wantHz)
	}
}

func assertLongToneCodecNotCompleted(t *testing.T, label string, result longToneCodecResult) {
	t.Helper()
	for _, frame := range result.frames {
		if frame.State == "completed" {
			t.Fatalf("%s falsely completed: %+v", label, frame)
		}
	}
}

func assertLongToneCodecLegato(t *testing.T, label string, result longToneCodecResult) {
	t.Helper()
	assertLongToneCodecNotCompleted(t, label, result)
	medianA := medianLongTonePitch(result.frames, 69)
	medianBFlat := medianLongTonePitch(result.frames, 70)
	if errorCents := math.Abs(1200 * math.Log2(medianA/440)); math.IsNaN(medianA) || errorCents > 2 {
		t.Fatalf("%s A4 median pitch error %.3f cents", label, errorCents)
	}
	if errorCents := math.Abs(1200 * math.Log2(medianBFlat/466.1637615)); math.IsNaN(medianBFlat) || errorCents > 2 {
		t.Fatalf("%s B-flat4 median pitch error %.3f cents", label, errorCents)
	}
	last := result.frames[len(result.frames)-1]
	if last.NoteMIDI == nil || *last.NoteMIDI != 70 || last.HoldStartMS == nil || *last.HoldStartMS < 14900 || *last.HoldStartMS > 15100 {
		t.Fatalf("%s did not reset to B-flat at legato boundary: %+v", label, last)
	}
}

func medianLongTonePitch(frames []longToneFrame, midi int) float64 {
	values := make([]float64, 0, len(frames))
	for _, frame := range frames {
		if frame.FrequencyHz > 0 && frame.NoteMIDI != nil && *frame.NoteMIDI == midi {
			values = append(values, frame.FrequencyHz)
		}
	}
	if len(values) == 0 {
		return math.NaN()
	}
	sort.Float64s(values)
	middle := len(values) / 2
	if len(values)%2 == 0 {
		return (values[middle-1] + values[middle]) / 2
	}
	return values[middle]
}

func absInt64(value int64) int64 {
	if value < 0 {
		return -value
	}
	return value
}
