package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestNormalizeLongToneAsset(t *testing.T) {
	for _, asset := range []string{"audio", "VIDEO", " video "} {
		if _, err := normalizeLongToneAsset(asset); err != nil {
			t.Fatalf("normalizeLongToneAsset(%q): %v", asset, err)
		}
	}
	for _, asset := range []string{"", "wav", "movie"} {
		if _, err := normalizeLongToneAsset(asset); err == nil {
			t.Fatalf("normalizeLongToneAsset(%q) succeeded", asset)
		}
	}
}

func TestDecodeLongToneWithFFmpegPreservesDelayedAudioTimeline(t *testing.T) {
	ffmpeg, err := exec.LookPath(envOr("FFMPEG_PATH", "ffmpeg"))
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	path := filepath.Join(t.TempDir(), "delayed.mp4")
	command := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "color=c=black:s=32x32:r=10:d=3",
		"-itsoffset", "1", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=2",
		"-map", "0:v:0", "-map", "1:a:0", "-c:v", "libx264", "-c:a", "aac", "-shortest", path)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("create delayed fixture: %v: %s", err, output)
	}
	var firstSignal int64 = -1
	analyze := func(reader io.Reader) (int64, error) {
		data, err := io.ReadAll(reader)
		if err != nil {
			return 0, err
		}
		for sample := 0; sample+4 <= len(data); sample += 4 {
			bits := binary.LittleEndian.Uint32(data[sample:])
			if math.Abs(float64(math.Float32frombits(bits))) > 0.01 {
				firstSignal = int64(sample/4) * 1000 / longToneSampleRate
				break
			}
		}
		return int64(len(data)/4) * 1000 / longToneSampleRate, nil
	}
	duration, err := decodeLongToneWithFFmpeg(context.Background(), path, "video", analyze)
	if err != nil {
		t.Fatal(err)
	}
	if firstSignal < 950 || firstSignal > 1050 {
		t.Fatalf("first signal at %dms, want delayed presentation time near 1000ms", firstSignal)
	}
	if duration < 2950 || duration > 3100 {
		t.Fatalf("decoded duration=%dms, want approximately 3000ms", duration)
	}
}

func TestDecodeLongToneWithFFmpegReportsMissingAudio(t *testing.T) {
	ffmpeg, err := exec.LookPath(envOr("FFMPEG_PATH", "ffmpeg"))
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	path := filepath.Join(t.TempDir(), "silent.mp4")
	command := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "color=s=32x32:d=0.2", "-an", path)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("create fixture: %v: %s", err, output)
	}
	_, err = decodeLongToneWithFFmpeg(context.Background(), path, "video", func(io.Reader) (int64, error) { return 0, nil })
	if !errors.Is(err, errLongToneNoAudio) {
		t.Fatalf("error=%v, want errLongToneNoAudio", err)
	}
}

func TestDecodeLongToneWithFFmpegAcceptsLiveWebMWithoutDurationMetadata(t *testing.T) {
	ffmpeg, err := exec.LookPath(envOr("FFMPEG_PATH", "ffmpeg"))
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	path := filepath.Join(t.TempDir(), "media-recorder-like.webm")
	command := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i",
		"sine=frequency=440:sample_rate=48000:duration=1", "-c:a", "libopus", "-f", "webm", "-live", "1", path)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("create live WebM: %v: %s", err, output)
	}
	if end, err := probeLongTonePresentationEnd(context.Background(), path); err != nil || end != 0 {
		t.Fatalf("live WebM presentation end=%f err=%v, want unknown and usable", end, err)
	}
	duration, err := decodeLongToneWithFFmpeg(context.Background(), path, "audio", func(reader io.Reader) (int64, error) {
		contents, err := io.ReadAll(reader)
		return int64(len(contents)/4) * 1000 / longToneSampleRate, err
	})
	if err != nil || duration < 980 || duration > 1020 {
		t.Fatalf("live WebM duration=%dms err=%v", duration, err)
	}
}

func TestLongToneRangeValidation(t *testing.T) {
	// The database-backed integration test exercises the handler response. Keep
	// this boundary visible in the fast suite as part of the public contract.
	if end, start := int64(120001), int64(0); end-start <= longToneMaxRangeMS {
		t.Fatal("range limit does not reject more than two minutes")
	}
}

func TestLongToneDecoderBuffersAreBounded(t *testing.T) {
	exact := &longToneCappedReader{reader: bytes.NewReader([]byte("1234")), remaining: 4}
	contents, err := io.ReadAll(exact)
	if err != nil || string(contents) != "1234" {
		t.Fatalf("exact cap: contents=%q err=%v", contents, err)
	}
	over := &longToneCappedReader{reader: bytes.NewReader([]byte("12345")), remaining: 4}
	contents, err = io.ReadAll(over)
	if !errors.Is(err, errLongToneTooLong) || string(contents) != "1234" {
		t.Fatalf("over cap: contents=%q err=%v", contents, err)
	}
	tail := longToneTailBuffer{max: 4}
	_, _ = tail.Write([]byte("abc"))
	_, _ = tail.Write([]byte("def"))
	if string(tail.data) != "cdef" {
		t.Fatalf("diagnostic tail=%q", tail.data)
	}
	limited := longToneLimitedBuffer{max: 4}
	if _, err := limited.Write([]byte("12345")); err == nil || len(limited.data) > 4 {
		t.Fatalf("limited diagnostic buffer grew to %d bytes, err=%v", len(limited.data), err)
	}
}

func TestLongToneUnknownDurationPolicy(t *testing.T) {
	if !longToneAllowsUnknownDuration("opus", "matroska,webm") {
		t.Fatal("live MediaRecorder Opus/WebM must be allowed to decode to EOF")
	}
	if longToneAllowsUnknownDuration("aac", "mov,mp4,m4a,3gp,3g2,mj2") {
		t.Fatal("AAC without an authoritative presentation end must not decode padding")
	}
}

func makeLongTonePCM(duration time.Duration) []byte {
	samples := int(duration.Seconds() * longToneSampleRate)
	result := make([]byte, samples*4)
	for i := 0; i < samples; i++ {
		value := float32(0.25 * math.Sin(2*math.Pi*440*float64(i)/longToneSampleRate))
		binary.LittleEndian.PutUint32(result[i*4:], math.Float32bits(value))
	}
	return result
}

func decodeResponse(t *testing.T, recorder *httptest.ResponseRecorder) longToneAnalysisResponse {
	t.Helper()
	var response longToneAnalysisResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	return response
}
