package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/oauth2"
)

func captionTestDB(t *testing.T, ctx context.Context) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("JAZZ_LAYOUT_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set JAZZ_LAYOUT_TEST_DATABASE_URL to a disposable PostgreSQL database")
	}
	root, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(root.Close)
	schema := "captions_" + uuid.New().String()[:8]
	if _, err = root.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE") })
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	db, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if err := migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	return db
}

// Caption requests answer 404 for unknown or someone else's takes and 409 for
// takes not ready yet, and never queue work for them. Deleting a take removes
// its transcript, its long-tone trace and its Speech sidecar, and workers
// never pick up a job whose take is gone or no longer ready.
func TestCaptionAccessAndDeletion(t *testing.T) {
	ctx := context.WithValue(context.Background(), userSubjectKey, "captions-owner")
	db := captionTestDB(t, ctx)
	objects := newMemoryObjects("/objects/")
	app := &application{db: db, logger: slog.Default(), cfg: config{Bucket: "test"}, objects: objects}
	owner, err := app.userID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	other, err := app.userID(context.WithValue(ctx, userSubjectKey, "captions-other"))
	if err != nil {
		t.Fatal(err)
	}
	insert := func(user uuid.UUID, object, status string) uuid.UUID {
		t.Helper()
		id := uuid.New()
		if _, err := db.Exec(ctx, `INSERT INTO recordings(id,user_id,bucket,object_name,object_generation,checksum,uploaded_at,content_type,expected_size_bytes,size_bytes,duration_ms,recorded_at,status)
			VALUES ($1,$2,'test',$3,7,'abc',now(),'audio/wav',100,100,90000,now(),$4)`, id, user, object, status); err != nil {
			t.Fatal(err)
		}
		return id
	}
	ready := insert(owner, "captions/ready.wav", "ready")
	uploading := insert(owner, "captions/uploading.wav", "uploading")
	foreign := insert(other, "captions/foreign.wav", "ready")
	call := func(handler http.HandlerFunc, method string, id uuid.UUID) int {
		t.Helper()
		r := httptest.NewRequest(method, "/", strings.NewReader("{}")).WithContext(ctx)
		r.SetPathValue("id", id.String())
		w := httptest.NewRecorder()
		handler(w, r)
		return w.Code
	}
	for _, c := range []struct {
		name string
		id   uuid.UUID
		post int
		get  int
	}{
		{"ready", ready, http.StatusAccepted, http.StatusOK},
		{"not ready", uploading, http.StatusConflict, http.StatusConflict},
		{"someone else's", foreign, http.StatusNotFound, http.StatusNotFound},
		{"unknown", uuid.New(), http.StatusNotFound, http.StatusNotFound},
	} {
		if got := call(app.createRecordingCaptions, "POST", c.id); got != c.post {
			t.Errorf("%s: POST captions = %d, want %d", c.name, got, c.post)
		}
		if got := call(app.getRecordingCaptions, "GET", c.id); got != c.get {
			t.Errorf("%s: GET captions = %d, want %d", c.name, got, c.get)
		}
	}
	var queued int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM recording_caption_jobs`).Scan(&queued); err != nil || queued != 1 {
		t.Fatalf("queued caption jobs = %d, %v; want only the ready take's", queued, err)
	}

	// A queued job is not claimed once its take stops being ready, and is closed.
	if _, err := db.Exec(ctx, `UPDATE recordings SET status='uploading' WHERE id=$1`, ready); err != nil {
		t.Fatal(err)
	}
	if job, err := app.claimCaptionJob(ctx); err != nil || job != nil {
		t.Fatalf("claimed a job for a take that is not ready: %+v, %v", job, err)
	}
	var code string
	if err := db.QueryRow(ctx, `SELECT status||':'||COALESCE(error_code,'') FROM recording_caption_jobs WHERE recording_id=$1`, ready).Scan(&code); err != nil || code != "failed:source_changed" {
		t.Fatalf("job for a changed take = %q, %v", code, err)
	}
	if _, err := db.Exec(ctx, `UPDATE recordings SET status='ready' WHERE id=$1`, ready); err != nil {
		t.Fatal(err)
	}
	if got := call(app.createRecordingCaptions, "POST", ready); got != http.StatusAccepted {
		t.Fatalf("retry after source_changed = %d", got)
	}
	job, err := app.claimCaptionJob(ctx)
	if err != nil || job == nil || job.RecordingID != ready || job.DurationMS != 90000 {
		t.Fatalf("claim = %+v, %v; want the ready take with its duration", job, err)
	}
	if _, err := db.Exec(ctx, `UPDATE recording_caption_jobs SET status='ready',segments='[{"startMs":0,"endMs":900,"text":"private words"}]',lease_token=NULL WHERE id=$1`, job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO long_tone_analysis_jobs(id,recording_id,user_id,asset,source_object_name,source_generation,source_checksum,analysis_version,status)
		VALUES ($1,$2,$3,'audio','captions/ready.wav',7,'abc','v','pending')`, uuid.New(), ready, owner); err != nil {
		t.Fatal(err)
	}
	sidecar := speechAudioObjectName("captions/ready.wav", 7)
	for _, name := range []string{"captions/ready.wav", sidecar} {
		if err := objects.Put(ctx, name, "audio/flac", nil, []byte("x")); err != nil {
			t.Fatal(err)
		}
	}

	// Deleting the take removes its transcript, trace and sidecar.
	if got := call(app.deleteRecording, "DELETE", ready); got != http.StatusNoContent {
		t.Fatalf("delete = %d", got)
	}
	if n := objects.count("captions/"); n != 0 {
		t.Fatalf("%d stored objects left after delete; the master and its Speech sidecar must go", n)
	}
	var captions, traces int
	if err := db.QueryRow(ctx, `SELECT (SELECT count(*) FROM recording_caption_jobs WHERE recording_id=$1),(SELECT count(*) FROM long_tone_analysis_jobs WHERE recording_id=$1)`, ready).Scan(&captions, &traces); err != nil || captions != 0 || traces != 0 {
		t.Fatalf("after delete: %d caption jobs, %d long-tone jobs, %v", captions, traces, err)
	}
	if got := call(app.getRecordingCaptions, "GET", ready); got != http.StatusNotFound {
		t.Fatalf("captions of a deleted take = %d, want 404", got)
	}
	// Workers never pick up long-tone work for a deleted take either.
	if _, err := db.Exec(ctx, `INSERT INTO long_tone_analysis_jobs(id,recording_id,user_id,asset,source_object_name,source_generation,source_checksum,analysis_version,status)
		VALUES ($1,$2,$3,'audio','captions/ready.wav',7,'abc','v','pending')`, uuid.New(), ready, owner); err != nil {
		t.Fatal(err)
	}
	if lt, err := app.claimLongToneJob(ctx); err != nil || lt != nil {
		t.Fatalf("claimed long-tone work for a deleted take: %+v, %v", lt, err)
	}
}

type pendingSpeech struct{}

func (pendingSpeech) RoundTrip(r *http.Request) (*http.Response, error) {
	body := `{"name":"projects/123456789012/locations/global/operations/op-1","done":false}`
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
}

// A Speech operation that never finishes ends at its deadline instead of
// holding the worker forever.
func TestCaptionPollingStopsAtDeadline(t *testing.T) {
	if got := captionDeadline(60000); got != 9*time.Minute {
		t.Fatalf("deadline for one minute of audio = %v, want 9m", got)
	}
	saved := captionPollInterval
	captionPollInterval = 10 * time.Millisecond
	defer func() { captionPollInterval = saved }()
	app := &application{cfg: config{Bucket: "test", ProjectID: "example"}, httpClient: &http.Client{Transport: pendingSpeech{}},
		speechTokenSource: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "test"})}
	started := time.Now()
	_, err := app.transcribeGCS(context.Background(), "take.caption-7.flac", 50*time.Millisecond)
	if !errors.Is(err, errCaptionDeadline) {
		t.Fatalf("err = %v, want the deadline", err)
	}
	if time.Since(started) > 5*time.Second {
		t.Fatalf("took %v", time.Since(started))
	}
}
