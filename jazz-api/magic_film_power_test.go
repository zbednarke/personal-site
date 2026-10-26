package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"golang.org/x/oauth2"
	"google.golang.org/api/idtoken"
)

type filmPowerTransport func(*http.Request) (*http.Response, error)

func (f filmPowerTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestFilmVMWakeTransitions(t *testing.T) {
	for _, tc := range []struct {
		name      string
		statuses  []string
		startCode int
		wantErr   bool
		wantStart int
	}{
		{"off", []string{"TERMINATED"}, 200, false, 1},
		{"running", []string{"RUNNING"}, 0, false, 0},
		{"booting", []string{"STAGING"}, 0, false, 0},
		{"shutting down must retry", []string{"STOPPING"}, 0, true, 0},
		{"concurrent start", []string{"TERMINATED", "PROVISIONING"}, 409, false, 1},
		{"concurrent start bad request", []string{"TERMINATED", "RUNNING"}, 400, false, 1},
		{"conflict still off", []string{"TERMINATED", "TERMINATED"}, 409, true, 1},
		{"denied", []string{"TERMINATED"}, 403, true, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			starts, reads := 0, 0
			app := &application{cfg: config{FilmPower: filmPowerConfig{Enabled: true, Project: "test-project", Zone: "us-central1-a", Instance: "jazz-film-private"}}, tokenSource: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "private-token"})}
			app.httpClient = &http.Client{Transport: filmPowerTransport(func(r *http.Request) (*http.Response, error) {
				if r.URL.Host != "compute.googleapis.com" || r.Header.Get("Authorization") != "Bearer private-token" {
					t.Fatal("unexpected credential destination")
				}
				code, body := 200, ""
				switch r.Method {
				case "GET":
					if reads >= len(tc.statuses) {
						t.Fatal("unexpected status read")
					}
					body = `{"status":"` + tc.statuses[reads] + `"}`
					reads++
				case "POST":
					if !strings.HasSuffix(r.URL.Path, "/start") {
						t.Fatal("unexpected mutation")
					}
					starts++
					code = tc.startCode
					body = "sensitive upstream error"
				default:
					t.Fatal("unexpected method")
				}
				return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
			})}
			err := app.wakePrivateFilmVM(context.Background())
			if (err != nil) != tc.wantErr || starts != tc.wantStart {
				t.Fatalf("wake=%v starts=%d", err, starts)
			}
			if err != nil && strings.Contains(err.Error(), "sensitive") {
				t.Fatal("upstream details leaked")
			}
		})
	}
}

func TestFilmWakeAuthorizationBeforeDatabaseAccess(t *testing.T) {
	app := &application{cfg: config{FilmPower: filmPowerConfig{Enabled: true}}}
	for _, h := range []string{"", "Bearer invalid", "Basic wrong", "Bearer " + strings.Repeat("a", 8192)} {
		r := httptest.NewRequest("POST", "/internal/magic-film/reconcile", nil)
		r.Header.Set("Authorization", h)
		w := httptest.NewRecorder()
		app.reconcilePrivateFilmVM(w, r)
		if w.Code != 401 {
			t.Fatalf("unauthenticated request: %d", w.Code)
		}
	}
	app.cfg.FilmPower.Enabled = false
	w := httptest.NewRecorder()
	app.reconcilePrivateFilmVM(w, httptest.NewRequest("POST", "/", nil))
	if w.Code != 404 {
		t.Fatal("disabled wake endpoint exposed")
	}
	for _, tc := range []struct {
		issuer, email string
		verified      any
		ok            bool
	}{
		{"https://accounts.google.com", "wake@test.iam.gserviceaccount.com", true, true},
		{"accounts.google.com", "wake@test.iam.gserviceaccount.com", true, true},
		{"https://evil.test", "wake@test.iam.gserviceaccount.com", true, false},
		{"accounts.google.com", "other@test.iam.gserviceaccount.com", true, false},
		{"accounts.google.com", "wake@test.iam.gserviceaccount.com", "true", false},
		{"accounts.google.com", "wake@test.iam.gserviceaccount.com", false, false},
	} {
		p := &idtoken.Payload{Issuer: tc.issuer, Claims: map[string]any{"email": tc.email, "email_verified": tc.verified}}
		if schedulerFilmIdentity(p, "wake@test.iam.gserviceaccount.com") != tc.ok {
			t.Fatalf("identity accepted incorrectly: %+v", tc)
		}
	}
}

func TestFilmPowerConfiguration(t *testing.T) {
	t.Setenv("MAGIC_FILM_VM_POWER_CONTROL", "1")
	for k, v := range map[string]string{"MAGIC_FILM_VM_PROJECT": "test-project", "MAGIC_FILM_VM_ZONE": "us-central1-a", "MAGIC_FILM_VM_NAME": "film-worker", "MAGIC_FILM_WAKE_CALLER": "film-wake@test-project.iam.gserviceaccount.com", "MAGIC_FILM_WAKE_AUDIENCE": "https://jazz-api-abc-uc.a.run.app", "MAGIC_FILM_IDLE_SECONDS": "300"} {
		t.Setenv(k, v)
	}
	if _, err := loadFilmPowerConfig(false); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ key, value string }{
		{"MAGIC_FILM_IDLE_SECONDS", "0"}, {"MAGIC_FILM_IDLE_SECONDS", "1801"},
		{"MAGIC_FILM_VM_NAME", "../../other-vm"}, {"MAGIC_FILM_WAKE_CALLER", "@test-project.iam.gserviceaccount.com"},
		{"MAGIC_FILM_WAKE_AUDIENCE", "https://jazz-api-abc-uc.a.run.app/other"},
		{"MAGIC_FILM_WAKE_AUDIENCE", "https://evil.test"},
	} {
		t.Run(tc.key+tc.value, func(t *testing.T) {
			t.Setenv(tc.key, tc.value)
			if _, err := loadFilmPowerConfig(false); err == nil {
				t.Fatal("invalid power config accepted")
			}
		})
	}
}

func TestFilmQueueWakeAndIdleShutdown(t *testing.T) {
	ctx := context.WithValue(context.Background(), userSubjectKey, "film-power-owner")
	db := captionTestDB(t, ctx)
	app := &application{db: db, logger: slog.New(slog.NewTextHandler(io.Discard, nil)), cfg: config{FilmPower: filmPowerConfig{Enabled: true, IdleTimeout: 5 * time.Minute}}}
	owner, err := app.userID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	id := uuid.New()
	_, err = db.Exec(ctx, `INSERT INTO magic_film_jobs(id,user_id,client_id,practice_date,target_seconds,provider,manifest) VALUES($1,$2,$3,'2026-10-26',60,'codex','{}')`, id, owner, uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	stops, wakes := 0, 0
	app.magicFilmShutdown = func(context.Context) error { stops++; return nil }
	app.magicFilmVMWake = func(context.Context) error { wakes++; return errors.New("temporary control plane failure") }
	app.magicFilmWakeAuth = func(*http.Request) bool { return true }
	now := time.Now()
	for _, tc := range []struct {
		status, provider, delay string
		cancelled, wantWork     bool
	}{
		{"queued", "codex", "0 minutes", false, true},
		{"preparing", "codex", "0 minutes", false, true},
		{"editing", "codex", "0 minutes", false, true},
		{"rendering", "codex", "0 minutes", false, true},
		{"waiting_usage", "codex", "-1 minute", false, true},
		{"waiting_usage", "codex", "30 minutes", false, false},
		{"sign_in_required", "codex", "-1 minute", false, false},
		{"queued", "codex", "0 minutes", true, false},
		{"queued", "api", "0 minutes", false, false},
		{"complete", "codex", "0 minutes", false, false},
	} {
		_, err = db.Exec(ctx, `UPDATE magic_film_jobs SET status=$2,provider=$3,next_attempt_at=now()+$4::interval,cancel_requested=$5 WHERE id=$1`, id, tc.status, tc.provider, tc.delay, tc.cancelled)
		if err != nil {
			t.Fatal(err)
		}
		pending, err := app.privateFilmHasRunnableWork(ctx)
		if err != nil || pending != tc.wantWork {
			t.Fatalf("%+v: pending=%v %v", tc, pending, err)
		}
		oldStops := stops
		stopped, err := app.stopIdlePrivateFilmVM(ctx, now.Add(-6*time.Minute), now)
		if err != nil || stopped == tc.wantWork || stops-oldStops != btoi(!tc.wantWork) {
			t.Fatalf("unsafe idle decision %+v: stopped=%v %v", tc, stopped, err)
		}
		oldWakes := wakes
		w := httptest.NewRecorder()
		app.reconcilePrivateFilmVM(w, httptest.NewRequest("POST", "/", nil))
		if tc.wantWork && (w.Code != 503 || wakes-oldWakes != 1) {
			t.Fatal("wake failure must remain retryable")
		}
		if !tc.wantWork && (w.Code != 200 || wakes != oldWakes) {
			t.Fatal("idle/paused queue woke VM")
		}
	}
	oldStops := stops
	if stopped, err := app.stopIdlePrivateFilmVM(ctx, now.Add(-time.Minute), now); stopped || err != nil || stops != oldStops {
		t.Fatal("idle grace ignored")
	}
	db.Close()
	if stopped, err := app.stopIdlePrivateFilmVM(ctx, now.Add(-6*time.Minute), now); stopped || err == nil || stops != oldStops {
		t.Fatal("database failure must inhibit shutdown")
	}
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

func TestFilmRequestSurvivesWakeFailureAndIdempotentRetry(t *testing.T) {
	ctx := context.WithValue(context.Background(), userSubjectKey, "film-wake-request-owner")
	db := captionTestDB(t, ctx)
	app := &application{db: db, logger: slog.New(slog.NewTextHandler(io.Discard, nil)), cfg: config{Bucket: "test", MagicFilmProvider: "codex", FilmPower: filmPowerConfig{Enabled: true}}}
	owner, err := app.userID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	recording := uuid.New()
	if _, err := db.Exec(ctx, `INSERT INTO recordings(id,user_id,bucket,object_name,object_generation,checksum,uploaded_at,content_type,expected_size_bytes,size_bytes,duration_ms,recorded_at,status)
 VALUES($1,$2,'test','film/take.wav',1,'abc',now(),'audio/wav',100,100,90000,'2026-10-01 12:00:00-07','ready')`, recording, owner); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO clip_candidates(id,user_id,recording_id,start_ms,end_ms,score,source,review_status,title)
 VALUES($1,$2,$3,1000,31000,0.9,'manual','kept','Ballad')`, uuid.New(), owner, recording); err != nil {
		t.Fatal(err)
	}
	wakes := 0
	app.magicFilmVMWake = func(context.Context) error {
		wakes++
		var count int
		if err := db.QueryRow(ctx, `SELECT count(*) FROM magic_film_jobs WHERE status='queued' AND provider='codex'`).Scan(&count); err != nil || count != 1 {
			t.Fatal("wake happened before durable enqueue", err)
		}
		return errors.New("temporary wake failure")
	}
	app.magicFilmInvoke = func(context.Context, uuid.UUID) (string, error) { t.Fatal("paid fallback invoked"); return "", nil }
	body := `{"clientId":"` + uuid.NewString() + `","date":"2026-10-01","targetSeconds":60}`
	var first uuid.UUID
	for index, wantCode := range []int{http.StatusAccepted, http.StatusOK} {
		w := httptest.NewRecorder()
		app.createMagicFilm(w, httptest.NewRequest("POST", "/", strings.NewReader(body)).WithContext(ctx))
		var job magicFilmJob
		if err := json.Unmarshal(w.Body.Bytes(), &job); err != nil || w.Code != wantCode || job.Status != "queued" {
			t.Fatalf("durable request: %d %s %v", w.Code, w.Body.String(), err)
		}
		if index == 0 {
			first = job.ID
		} else if job.ID != first {
			t.Fatal("idempotent retry created another film")
		}
	}
	if wakes != 2 {
		t.Fatalf("wake attempts=%d", wakes)
	}
}

func TestFilmArrivingDuringAcceptedShutdownWaitsForReconciliation(t *testing.T) {
	ctx := context.WithValue(context.Background(), userSubjectKey, "film-shutdown-race-owner")
	db := captionTestDB(t, ctx)
	t.Setenv("MAGIC_FILM_WORKSPACE", t.TempDir())
	app := &application{db: db, logger: slog.New(slog.NewTextHandler(io.Discard, nil)), cfg: config{FilmPower: filmPowerConfig{Enabled: true}}}
	owner, err := app.userID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	workerCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	id := uuid.New()
	app.magicFilmShutdown = func(context.Context) error {
		// Queue becomes nonempty immediately after the last idle check.
		_, err := db.Exec(ctx, `INSERT INTO magic_film_jobs(id,user_id,client_id,practice_date,target_seconds,provider,manifest) VALUES($1,$2,$3,'2026-10-26',60,'codex','{}')`, id, owner, uuid.New())
		if err != nil {
			t.Fatal(err)
		}
		cancel() // Guest shutdown sends SIGTERM to the worker.
		return nil
	}
	if err := app.runPrivateMagicFilmWorker(workerCtx); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := db.QueryRow(ctx, `SELECT status FROM magic_film_jobs WHERE id=$1`, id).Scan(&status); err != nil || status != "queued" {
		t.Fatalf("job claimed during shutdown: %s %v", status, err)
	}
	wakes := 0
	app.magicFilmWakeAuth = func(*http.Request) bool { return true }
	app.magicFilmVMWake = func(context.Context) error { wakes++; return nil }
	w := httptest.NewRecorder()
	app.reconcilePrivateFilmVM(w, httptest.NewRequest("POST", "/", nil))
	if w.Code != http.StatusOK || wakes != 1 {
		t.Fatal("shutdown race stranded the queued film")
	}
}
