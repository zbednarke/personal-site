package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// Exercise the actual production renderer when updating its FFmpeg/runtime
// packages. Fixtures are synthetic; no provider, cloud storage or API credit
// is needed. Both browser video and audio-only slate paths must still work.
func TestMagicFilmRenderWithFFmpeg(t *testing.T) {
	for _, name := range []string{envOr("FFMPEG_PATH", "ffmpeg"), envOr("FFPROBE_PATH", "ffprobe")} {
		if _, err := exec.LookPath(name); err != nil {
			t.Skipf("media test dependency is not installed: %s", name)
		}
	}
	if _, err := os.Stat("/usr/share/fonts/truetype/dejavu/DejaVuSerif.ttf"); err != nil {
		t.Skip("production DejaVu font is not installed")
	}
	for _, video := range []bool{false, true} {
		name := "audio-only"
		if video {
			name = "video"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			workspace := t.TempDir()
			source := filepath.Join(workspace, "source.wav")
			args := []string{"-v", "error", "-y", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=2.2"}
			if video {
				source = filepath.Join(workspace, "source.mp4")
				args = append(args, "-f", "lavfi", "-i", "color=c=black:s=64x64:r=30:d=2.2", "-c:v", "libx264", "-threads", "2", "-c:a", "aac", "-shortest")
			}
			if _, err := commandOutput(ctx, workspace, 1, append(args, source)...); err != nil {
				t.Fatalf("create synthetic recording: %v", err)
			}
			recording := magicWorkerRecording{AudioURL: source}
			if video {
				recording.VideoURL = source
			}
			manifest := magicFilmManifest{Date: "2026-01-02", TargetSeconds: 60, Candidates: []magicFilmCandidate{
				{ID: "candidate", RecordingID: "recording", Title: "Synthetic practice", StartMS: 0, EndMS: 2000},
			}}
			plan := magicFilmPlan{Title: "Runtime compatibility", Clips: []magicFilmCut{{CandidateID: "candidate", StartMS: 0, EndMS: 2000}}}
			project, movie, poster, duration, err := renderMagicFilm(ctx, plan, manifest, map[string]magicWorkerRecording{"recording": recording}, workspace)
			if err != nil {
				t.Fatal(err)
			}
			if duration < 1950 || duration > 2200 || len(project.Clips) != 1 || project.Clips[0].RecordingID != "recording" {
				t.Fatalf("unexpected render metadata: duration=%d project=%+v", duration, project)
			}
			for _, file := range []string{movie, poster} {
				info, err := os.Stat(file)
				if err != nil || info.Size() == 0 {
					t.Fatalf("missing or empty render artifact %s: %v", file, err)
				}
			}
		})
	}
}
