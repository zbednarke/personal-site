package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func fakeCodex(t *testing.T, login, execution string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "codex-launcher")
	script := "#!/bin/sh\nif [ \"$1\" = login ]; then\n" + login + "\nexit 0\nfi\n" + execution + "\n"
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MAGIC_FILM_CODEX_LAUNCHER", path)
	return path
}

func TestCodexFilmPlanUsesSubscriptionAndStructuredEvidence(t *testing.T) {
	workspace := t.TempDir()
	t.Setenv("OPENAI_API_KEY", "must-not-reach-cli")
	t.Setenv("CODEX_API_KEY", "must-not-reach-cli")
	t.Setenv("DATABASE_URL", "must-not-reach-cli")
	fakeCodex(t, "echo 'Logged in using ChatGPT'", `
test -z "$OPENAI_API_KEY$CODEX_API_KEY$DATABASE_URL" || exit 9
printf '%s\n' "$@" > `+fmt.Sprintf("%q", filepath.Join(workspace, "args"))+`
cat > `+fmt.Sprintf("%q", filepath.Join(workspace, "prompt"))+`
while [ "$#" -gt 0 ]; do
 if [ "$1" = --output-last-message ]; then shift; output=$1; fi
 shift
done
printf '%s' '{"title":"Test film","summary":"Curated","reviews":[{"candidateId":"manual","reason":"Selected"}],"clips":[{"candidateId":"manual","startMs":1000,"endMs":26000,"reason":"Phrase"}]}' > "$output"
`)
	manifest := testMagicManifest()
	manifest.Recordings = []magicFilmRecording{{ID: "take-1", AudioObject: "private-object-path"}}
	manifest.Candidates[0].Frames = []string{"frame-000.jpg"}
	if err := os.WriteFile(filepath.Join(workspace, "frame-000.jpg"), []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	plan, err := requestCodexMagicFilmPlan(context.Background(), "codex-default", "medium", manifest, "quiet", workspace)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := validateMagicFilmPlan(plan, manifest); err != nil {
		t.Fatal(err)
	}
	args, _ := os.ReadFile(filepath.Join(workspace, "args"))
	for _, required := range []string{"--output-schema", "--image", "--sandbox\nread-only", `forced_login_method="chatgpt"`, `model_provider="openai"`, "--ignore-user-config"} {
		if !strings.Contains(string(args), required) {
			t.Fatalf("missing %s", required)
		}
	}
	prompt, _ := os.ReadFile(filepath.Join(workspace, "prompt"))
	if strings.Contains(string(prompt), "private-object-path") || !strings.Contains(string(prompt), `"direction":"quiet"`) {
		t.Fatal("incorrect prompt redaction/direction")
	}
}

func TestCodexQuotaPausesAndNeverUsesStalePlan(t *testing.T) {
	workspace := t.TempDir()
	os.MkdirAll(filepath.Join(workspace, "codex-output"), 0700)
	output := filepath.Join(workspace, "codex-output", "plan.json")
	os.WriteFile(output, []byte(`{"title":"stale"}`), 0600)
	fakeCodex(t, "echo 'Logged in using ChatGPT'", `echo '{"type":"error","message":"usage_limit_reached"}'; exit 1`)
	_, err := requestCodexMagicFilmPlan(context.Background(), "codex-default", "medium", testMagicManifest(), "", workspace)
	var pause *magicFilmPause
	if !errors.As(err, &pause) || pause.Status != "waiting_usage" || time.Until(pause.RetryAt) < 29*time.Minute {
		t.Fatalf("expected durable pause, got %v", err)
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatal("old output survived")
	}
}

func TestCodexRejectsAPIKeyLogin(t *testing.T) {
	fakeCodex(t, "echo 'Logged in using an API key'", "exit 99")
	_, err := requestCodexMagicFilmPlan(context.Background(), "codex-default", "medium", testMagicManifest(), "", t.TempDir())
	var pause *magicFilmPause
	if !errors.As(err, &pause) || pause.Status != "sign_in_required" {
		t.Fatalf("expected subscription sign-in, got %v", err)
	}
}

func TestCodexCLIErrorClassification(t *testing.T) {
	for _, text := range []string{"refresh_token_expired", "401 Unauthorized", "Please log in"} {
		if p := codexPauseFromOutput(text, time.Now()); p == nil || p.Status != "sign_in_required" {
			t.Fatalf("unrecognized auth failure %s", text)
		}
	}
	if p := codexPauseFromOutput("unknown model", time.Now()); p != nil {
		t.Fatal("configuration failures must not retry as usage limits")
	}
	var log magicCodexLog
	log.Write([]byte(strings.Repeat("x", 128<<10)))
	log.Write([]byte("usage_limit_reached"))
	if len(log.data) > 64<<10 || codexPauseFromOutput(string(log.data), time.Now()) == nil {
		t.Fatal("terminal error was lost")
	}
}

func TestPrivateFilmPausePersistenceAndSingleWorker(t *testing.T) {
	ctx := context.WithValue(context.Background(), userSubjectKey, "codex-test-owner")
	db := captionTestDB(t, ctx)
	app := &application{db: db, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	owner, err := app.userID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	id := uuid.New()
	manifest, _ := json.Marshal(testMagicManifest())
	_, err = db.Exec(ctx, `INSERT INTO magic_film_jobs(id,user_id,client_id,practice_date,target_seconds,provider,manifest) VALUES($1,$2,$3,'2026-10-26',60,'codex',$4)`, id, owner, uuid.New(), manifest)
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	t.Setenv("MAGIC_FILM_PRIVATE_WORKER", "1")
	t.Setenv("MAGIC_FILM_WORKSPACE", workspace)
	fakeCodex(t, "echo 'Not logged in'", "exit 99")
	checkpoint := filepath.Join(workspace, id.String(), "progress")
	os.MkdirAll(filepath.Dir(checkpoint), 0750)
	os.WriteFile(checkpoint, []byte("preserved"), 0600)
	if err := app.runMagicFilmJob(ctx, id.String()); err != nil {
		t.Fatal(err)
	}
	job, err := scanMagicFilmJob(db.QueryRow(ctx, `SELECT `+magicFilmJobColumns+` FROM magic_film_jobs WHERE id=$1`, id))
	if err != nil || job.Status != "sign_in_required" || job.NextAttemptAt == nil || job.FinishedAt != nil {
		t.Fatalf("pause not persisted: %+v %v", job, err)
	}
	if data, err := os.ReadFile(checkpoint); err != nil || string(data) != "preserved" {
		t.Fatal("checkpoint lost during pause")
	}
	app.cleanPrivateFilmWorkspaces(ctx)
	if _, err := os.Stat(checkpoint); err != nil {
		t.Fatal("cleanup removed paused evidence")
	}
	_, err = db.Exec(ctx, `INSERT INTO magic_film_jobs(id,user_id,client_id,practice_date,target_seconds,provider,manifest) VALUES($1,$2,$3,'2026-10-26',60,'codex',$4)`, uuid.New(), owner, uuid.New(), manifest)
	if err == nil {
		t.Fatal("paused job allowed duplicate active film")
	}
	conn, err := db.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock(762913045)`); err != nil {
		t.Fatal(err)
	}
	defer conn.Exec(ctx, `SELECT pg_advisory_unlock(762913045)`)
	if err := app.runPrivateMagicFilmWorker(ctx); err == nil || !strings.Contains(err.Error(), "another private film worker") {
		t.Fatalf("missing serialization: %v", err)
	}
	request := httptest.NewRequest("POST", "/v1/studio/magic-films/"+id.String()+"/cancel", nil).WithContext(ctx)
	request.SetPathValue("id", id.String())
	recorder := httptest.NewRecorder()
	app.cancelMagicFilm(recorder, request)
	if recorder.Code != 200 {
		t.Fatalf("cancel paused film: %d %s", recorder.Code, recorder.Body.String())
	}
	var status string
	if err := db.QueryRow(ctx, `SELECT status FROM magic_film_jobs WHERE id=$1`, id).Scan(&status); err != nil || status != "cancelled" {
		t.Fatalf("paused cancellation: %s %v", status, err)
	}
	app.cleanPrivateFilmWorkspaces(ctx)
	if _, err := os.Stat(checkpoint); !os.IsNotExist(err) {
		t.Fatal("cancelled evidence was not cleaned")
	}
}

func TestPrivateFilmSavedPlanSkipsCodexAfterRestart(t *testing.T) {
	ctx := context.WithValue(context.Background(), userSubjectKey, "codex-resume-owner")
	db := captionTestDB(t, ctx)
	app := &application{db: db, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	owner, err := app.userID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	marker := filepath.Join(workspace, "unexpected-model-call")
	fakeCodex(t, "touch "+fmt.Sprintf("%q", marker), "touch "+fmt.Sprintf("%q", marker)+"; exit 99")
	ffmpeg := filepath.Join(workspace, "ffmpeg")
	os.WriteFile(ffmpeg, []byte("#!/bin/sh\nexit 1\n"), 0700)
	t.Setenv("FFMPEG_PATH", ffmpeg)
	t.Setenv("MAGIC_FILM_PRIVATE_WORKER", "1")
	t.Setenv("MAGIC_FILM_WORKSPACE", workspace)
	id := uuid.New()
	manifest, _ := json.Marshal(testMagicManifest())
	plan, _ := json.Marshal(magicFilmPlan{Title: "Saved", Reviews: []magicFilmReview{{CandidateID: "manual", Reason: "Curated"}}, Clips: []magicFilmCut{{CandidateID: "manual", StartMS: 1000, EndMS: 26000, Reason: "Phrase"}}})
	_, err = db.Exec(ctx, `INSERT INTO magic_film_jobs(id,user_id,client_id,practice_date,target_seconds,provider,manifest,plan) VALUES($1,$2,$3,'2026-10-26',60,'codex',$4,$5)`, id, owner, uuid.New(), manifest, plan)
	if err != nil {
		t.Fatal(err)
	}
	// Rendering deliberately stops at FFmpeg. The model and its login check
	// must not be called again for a previously validated and saved plan.
	if err := app.runMagicFilmJob(ctx, id.String()); err == nil || !strings.Contains(err.Error(), "ffmpeg") {
		t.Fatalf("expected render attempt, got %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("resume invoked Codex again")
	}
	var retained []byte
	if err := db.QueryRow(ctx, `SELECT plan FROM magic_film_jobs WHERE id=$1`, id).Scan(&retained); err != nil || len(retained) == 0 {
		t.Fatal("saved plan was discarded")
	}
}

// Explicitly opt in on the private VM as magic-film. CI never receives a
// subscription session. This uses synthetic metadata and one generated image.
func TestPrivateCodexLiveSmoke(t *testing.T) {
	if os.Getenv("MAGIC_FILM_LIVE_CODEX_SMOKE") != "1" {
		t.Skip("private worker sign-in required; synthetic opt-in smoke test")
	}
	workspace, err := os.MkdirTemp(envOr("MAGIC_FILM_WORKSPACE", "/var/lib/magic-film/jobs"), "smoke-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(workspace)
	if err := os.Chmod(workspace, 0750|os.ModeSetgid); err != nil {
		t.Fatal(err)
	}
	frame := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			frame.Set(x, y, color.RGBA{uint8(x * 4), uint8(y * 4), 80, 255})
		}
	}
	file, err := os.OpenFile(filepath.Join(workspace, "frame-000.png"), os.O_CREATE|os.O_WRONLY, 0640)
	if err != nil {
		t.Fatal(err)
	}
	err = png.Encode(file, frame)
	file.Close()
	if err != nil {
		t.Fatal(err)
	}
	manifest := testMagicManifest()
	manifest.Candidates[0].Title = "Synthetic subscription smoke test, not a real recording"
	manifest.Candidates[0].Frames = []string{"frame-000.png"}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	plan, err := requestCodexMagicFilmPlan(ctx, "codex-default", "medium", manifest, "", workspace)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := validateMagicFilmPlan(plan, manifest); err != nil {
		t.Fatal(err)
	}
	t.Logf("Subscription-authenticated image/JSON plan passed existing validation (%d clips)", len(plan.Clips))
}
