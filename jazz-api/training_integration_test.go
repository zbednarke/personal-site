package main

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/oauth2"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestTrainingAttemptPersistence(t *testing.T) {
	url := os.Getenv("JAZZ_LAYOUT_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set JAZZ_LAYOUT_TEST_DATABASE_URL to a disposable PostgreSQL database")
	}
	ctx := context.WithValue(context.Background(), userSubjectKey, "recording-section-test")
	root, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	schema := "section_test_" + uuid.New().String()[:8]
	if _, err = root.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer root.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	db, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := migrate(ctx, db); err != nil {
		t.Fatal(err)
	}

	app := &application{db: db, logger: slog.Default(), cfg: config{Bucket: "test"}, tokenSource: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "test"}), httpClient: &http.Client{Transport: largeUploadTransport{}}}
	pendingID := uuid.New().String()
	pending := recordingInitRequest{ClientCaptureID: pendingID, MediaKind: "video", ContentType: "audio/wav", SizeBytes: 44, DurationMS: 1000, RecordedAt: "2026-09-28T12:00:00Z", VideoContentType: "video/mp4", VideoSizeBytes: 1000, VideoWidth: 1280, VideoHeight: 720, VideoFrameRate: 30}
	pendingRaw, _ := json.Marshal(pending)
	for i := 0; i < 3; i++ {
		r := httptest.NewRequest("POST", "/", bytes.NewReader(pendingRaw)).WithContext(ctx)
		w := httptest.NewRecorder()
		app.initRecording(w, r)
		want := 200
		if i == 0 {
			want = 201
		}
		if w.Code != want {
			t.Fatalf("pending retry %d: %d %s", i, w.Code, w.Body.String())
		}
	}
	var pendingCount int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM recordings WHERE id=$1`, pendingID).Scan(&pendingCount); err != nil || pendingCount != 1 {
		t.Fatalf("duplicate capture count %d %v", pendingCount, err)
	}
	// Completed session captures are returned without creating another recording.
	captureID := uuid.New()
	input := recordingInitRequest{ClientCaptureID: captureID.String(), ContentType: "audio/wav", SizeBytes: 44, DurationMS: 1000, RecordedAt: "2026-09-28T12:00:00Z"}
	captureRaw, _ := json.Marshal(input)
	owner, _ := app.userID(ctx)
	if _, err := db.Exec(ctx, `INSERT INTO recordings(id,user_id,bucket,object_name,content_type,expected_size_bytes,duration_ms,recorded_at,status,client_capture_payload) VALUES($1,$2,'test','session.wav','audio/wav',44,1000,now(),'ready',$3)`, captureID, owner, captureRaw); err != nil {
		t.Fatal(err)
	}
	for _, subject := range []string{"recording-section-test", "recording-section-test", "other-user"} {
		r := httptest.NewRequest("POST", "/", bytes.NewReader(captureRaw)).WithContext(context.WithValue(ctx, userSubjectKey, subject))
		w := httptest.NewRecorder()
		app.initRecording(w, r)
		want := 200
		if subject == "other-user" {
			want = 409
		}
		if w.Code != want {
			t.Fatalf("capture retry %d: %s", w.Code, w.Body.String())
		}
	}
	input.DurationMS = 2000
	changed, _ := json.Marshal(input)
	rConflict := httptest.NewRequest("POST", "/", bytes.NewReader(changed)).WithContext(ctx)
	wConflict := httptest.NewRecorder()
	app.initRecording(wConflict, rConflict)
	if wConflict.Code != 409 {
		t.Fatal("conflicting capture was accepted")
	}
	id := uuid.New().String()
	payload := map[string]any{"id": id, "sessionId": uuid.New().String(), "key": "A♭", "instrument": "bb-trumpet", "mode": "lesson", "tempo": 80, "round": 1, "startedAt": "2026-09-27T12:00:00Z", "durationMs": 12000, "sampleRate": 48000, "audioBytes": 1440044, "result": map[string]any{"status": "pass", "correct": 7}}
	save := func(want int) {
		t.Helper()
		data, _ := json.Marshal(payload)
		r := httptest.NewRequest("PUT", "/", bytes.NewReader(data)).WithContext(ctx)
		r.SetPathValue("id", id)
		w := httptest.NewRecorder()
		app.saveTrainingAttempt(w, r)
		if w.Code != want {
			t.Fatalf("save=%d want %d %s", w.Code, want, w.Body.String())
		}
	}
	save(200)
	save(200)
	var count int
	db.QueryRow(ctx, "SELECT count(*) FROM training_attempts").Scan(&count)
	if count != 1 {
		t.Fatal("retry duplicated attempt")
	}
	payload["tempo"] = 90
	save(409)
	payload["tempo"] = 80
	payload["key"] = "bad"
	save(422)
	payload["key"] = "A♭"
	for _, subject := range []string{"recording-section-test", "other-user"} {
		r := httptest.NewRequest("GET", "/", nil).WithContext(context.WithValue(ctx, userSubjectKey, subject))
		w := httptest.NewRecorder()
		app.listTrainingAttempts(w, r)
		if w.Code != 200 {
			t.Fatal(w.Body.String())
		}
		var result struct {
			Attempts []json.RawMessage `json:"attempts"`
		}
		json.Unmarshal(w.Body.Bytes(), &result)
		want := 1
		if subject == "other-user" {
			want = 0
		}
		if len(result.Attempts) != want {
			t.Fatal("incorrect owner isolation")
		}
	}
	r := httptest.NewRequest("PUT", "/", bytes.NewReader([]byte("bad"))).WithContext(ctx)
	r.SetPathValue("id", id)
	w := httptest.NewRecorder()
	app.trainingAudio(w, r)
	if w.Code != 422 {
		t.Fatalf("bad audio=%d", w.Code)
	}
	r = httptest.NewRequest("GET", "/", nil).WithContext(context.WithValue(ctx, userSubjectKey, "other-user"))
	r.SetPathValue("id", id)
	w = httptest.NewRecorder()
	app.trainingAudio(w, r)
	if w.Code != 404 {
		t.Fatalf("other user audio=%d", w.Code)
	}
	if err := migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
}
