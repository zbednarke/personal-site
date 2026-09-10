package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/oauth2"
)

type layoutTestTransport func(*http.Request) (*http.Response, error)

func (transport layoutTestTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

func TestPracticeLayoutPersistence(t *testing.T) {
	url := os.Getenv("JAZZ_LAYOUT_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set JAZZ_LAYOUT_TEST_DATABASE_URL to a disposable PostgreSQL database")
	}
	ctx := context.WithValue(context.Background(), userSubjectKey, "layout-test")
	db, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	schema := "layout_test_" + uuid.New().String()[:8]
	if _, err = db.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer db.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	isolated, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer isolated.Close()
	if err := migrate(ctx, isolated); err != nil {
		t.Fatal(err)
	}
	app := &application{db: isolated, logger: slog.Default()}
	userID, err := app.userID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sessionID := uuid.New()
	if _, err := isolated.Exec(ctx, `INSERT INTO practice_sessions (id,user_id,title,started_at,status) VALUES ($1,$2,'Test',now(),'active')`, sessionID, userID); err != nil {
		t.Fatal(err)
	}
	definitions := bootstrapBlocksRequest{PracticeDate: "2026-09-08", Blocks: []blockDefinition{
		{BlockKey: "warmup", Position: 0, Title: "Warmup", Category: "technique", Track: "trumpet", TargetMinutes: 10},
		{BlockKey: "tune", Position: 1, Title: "Tune", Category: "repertoire", Track: "musician", TargetMinutes: 10},
	}}
	bootstrap := func() []practiceBlock {
		t.Helper()
		body, _ := json.Marshal(definitions)
		r := httptest.NewRequest("POST", "/", bytes.NewReader(body)).WithContext(ctx)
		r.SetPathValue("id", sessionID.String())
		w := httptest.NewRecorder()
		app.bootstrapPracticeBlocks(w, r)
		if w.Code != 200 {
			t.Fatalf("bootstrap: %d %s", w.Code, w.Body.String())
		}
		var result struct {
			Blocks []practiceBlock `json:"blocks"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result.Blocks
	}
	layout := func(input blockLayoutRequest, subject string, want int) {
		t.Helper()
		input.PracticeDate = definitions.PracticeDate
		body, _ := json.Marshal(input)
		r := httptest.NewRequest("PUT", "/", bytes.NewReader(body)).WithContext(context.WithValue(ctx, userSubjectKey, subject))
		r.SetPathValue("id", sessionID.String())
		w := httptest.NewRecorder()
		app.updatePracticeBlockLayout(w, r)
		if w.Code != want {
			t.Fatalf("layout: got %d want %d: %s", w.Code, want, w.Body.String())
		}
	}
	blocks := bootstrap()
	a, b := blocks[0].ID, blocks[1].ID
	layout(blockLayoutRequest{BlockIDs: []uuid.UUID{b, a}}, "other-user", 404)
	layout(blockLayoutRequest{BlockIDs: []uuid.UUID{b, a}}, "layout-test", 204)
	if got := bootstrap(); len(got) != 2 || got[0].ID != b {
		t.Fatal("bootstrap reset saved order")
	}
	recordingID := uuid.New()
	if _, err := isolated.Exec(ctx, `INSERT INTO recordings (id,user_id,practice_session_id,practice_block_id,bucket,object_name,content_type,expected_size_bytes,duration_ms,recorded_at,status) VALUES ($1,$2,$3,$4,'test','test.wav','audio/wav',10,1000,now(),'ready')`, recordingID, userID, sessionID.String(), a); err != nil {
		t.Fatal(err)
	}
	if _, err := isolated.Exec(ctx, `UPDATE practice_blocks SET elapsed_ms=1000,status='running',timer_started_at=now()-interval '30 seconds' WHERE id=$1`, a); err != nil {
		t.Fatal(err)
	}
	// Saving multiple removals must be all-or-nothing when any section is busy.
	layout(blockLayoutRequest{BlockIDs: []uuid.UUID{}, RemoveIDs: []uuid.UUID{a, b}}, "layout-test", 409)
	var removedCount int
	if err := isolated.QueryRow(ctx, `SELECT COUNT(*) FROM practice_blocks WHERE removed_at IS NOT NULL`).Scan(&removedCount); err != nil || removedCount != 0 {
		t.Fatal("busy save partially removed sections")
	}
	if _, err := isolated.Exec(ctx, `UPDATE practice_blocks SET status='paused',timer_started_at=NULL,elapsed_ms=31000 WHERE id=$1`, a); err != nil {
		t.Fatal(err)
	}
	if _, err := isolated.Exec(ctx, `UPDATE recordings SET status='uploading' WHERE id=$1`, recordingID); err != nil {
		t.Fatal(err)
	}
	layout(blockLayoutRequest{BlockIDs: []uuid.UUID{}, RemoveIDs: []uuid.UUID{a, b}}, "layout-test", 409)
	if _, err := isolated.Exec(ctx, `UPDATE recordings SET status='ready' WHERE id=$1`, recordingID); err != nil {
		t.Fatal(err)
	}
	// A crashed tab's expired heartbeat must not block deletion forever.
	if _, err := isolated.Exec(ctx, `UPDATE practice_blocks SET status='running',timer_started_at=now()-interval '5 minutes' WHERE id=$1`, a); err != nil {
		t.Fatal(err)
	}
	layout(blockLayoutRequest{BlockIDs: []uuid.UUID{b}, RemoveID: &a}, "layout-test", 204)
	history, err := app.loadPracticeBlocksForView(ctx, userID, sessionID, definitions.PracticeDate, true)
	if err != nil || len(history) != 2 {
		t.Fatalf("history lost sections: %v", err)
	}
	for _, block := range history {
		if block.ID == a && (block.Status != "paused" || block.TimerStartedAt != nil || block.ElapsedMS < 31000) {
			t.Fatalf("removed timer lost history: %+v", block)
		}
	}
	patchRequest := httptest.NewRequest("PATCH", "/", bytes.NewBufferString(`{"status":"running"}`)).WithContext(ctx)
	patchRequest.SetPathValue("id", a.String())
	patchResponse := httptest.NewRecorder()
	app.updatePracticeBlock(patchResponse, patchRequest)
	if patchResponse.Code != 404 {
		t.Fatalf("removed section accepted a stale write: %d", patchResponse.Code)
	}
	if got := bootstrap(); len(got) != 1 || got[0].ID != b {
		t.Fatal("deleted default reappeared")
	}
	var retained uuid.UUID
	if err := isolated.QueryRow(ctx, `SELECT practice_block_id FROM recordings WHERE id=$1 AND status='ready'`, recordingID).Scan(&retained); err != nil || retained != a {
		t.Fatalf("recording was not retained: %v", err)
	}
	// A take captured on another device before removal can still upload to history.
	app.cfg.Bucket = "test-bucket"
	app.tokenSource = oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "test-only"})
	app.httpClient = &http.Client{Transport: layoutTestTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{"Location": []string{"https://storage.example.test/upload"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
	})}
	capture, _ := json.Marshal(map[string]any{"contentType": "audio/wav", "sizeBytes": 100, "durationMs": 5000, "recordedAt": "2026-09-08T12:00:00Z", "practiceSessionId": sessionID.String(), "practiceBlockId": a.String()})
	captureRequest := httptest.NewRequest("POST", "/", bytes.NewReader(capture)).WithContext(ctx)
	captureResponse := httptest.NewRecorder()
	app.initRecording(captureResponse, captureRequest)
	if captureResponse.Code != 201 {
		t.Fatalf("late capture lost: %d %s", captureResponse.Code, captureResponse.Body.String())
	}
	var captureResult struct {
		ID             uuid.UUID `json:"id"`
		SectionRemoved bool      `json:"sectionRemoved"`
	}
	if err := json.Unmarshal(captureResponse.Body.Bytes(), &captureResult); err != nil || !captureResult.SectionRemoved {
		t.Fatal("late capture was not marked archived")
	}
	if err := isolated.QueryRow(ctx, `SELECT practice_block_id FROM recordings WHERE id=$1`, captureResult.ID).Scan(&retained); err != nil || retained != a {
		t.Fatal("late capture lost section context")
	}
	layout(blockLayoutRequest{BlockIDs: []uuid.UUID{a, b}}, "layout-test", 409)
	layout(blockLayoutRequest{BlockIDs: []uuid.UUID{}, RemoveID: &b}, "layout-test", 204)
	if got := bootstrap(); len(got) != 0 {
		t.Fatal("empty plan was repopulated")
	}
	definitions.Blocks = []blockDefinition{{BlockKey: "custom-new", Title: "New section", Category: "technique", Track: "trumpet", TargetMinutes: 5}}
	if got := bootstrap(); len(got) != 1 || got[0].BlockKey != "custom-new" {
		t.Fatal("could not add to empty plan")
	}
	definitions.Blocks = append(definitions.Blocks, blockDefinition{BlockKey: "custom-second", Position: 1, Title: "Second", Category: "technique", Track: "trumpet", TargetMinutes: 5})
	finalBlocks := bootstrap()
	layout(blockLayoutRequest{BlockIDs: []uuid.UUID{}, RemoveIDs: []uuid.UUID{finalBlocks[0].ID, finalBlocks[1].ID}}, "layout-test", 204)
	if got := bootstrap(); len(got) != 0 {
		t.Fatal("batch deletion failed")
	}
	// An empty previous day must remain empty, including across skipped dates.
	definitions.PracticeDate = "2026-09-10"
	definitions.Mode = "initialize"
	definitions.Blocks = []blockDefinition{{BlockKey: "factory", Title: "Factory", Category: "technique", Track: "trumpet", TargetMinutes: 10}}
	if got := bootstrap(); len(got) != 0 {
		t.Fatal("empty previous day restored defaults")
	}
	definitions.Mode = "add"
	definitions.Blocks = []blockDefinition{
		{BlockKey: "custom-long", Title: "Long tones", Instructions: "My instructions", Category: "technique", Track: "trumpet", TargetMinutes: 20},
		{BlockKey: "custom-next", Title: "Next", Category: "technique", Track: "trumpet", TargetMinutes: 7},
		{BlockKey: "appointment", Title: "Transcription", Category: "repertoire", Track: "musician", TargetMinutes: 20, DayOnly: true},
	}
	added := bootstrap()
	layout(blockLayoutRequest{BlockIDs: []uuid.UUID{added[1].ID, added[0].ID, added[2].ID}}, "layout-test", 204)
	if _, err := isolated.Exec(ctx, `UPDATE practice_blocks SET notes='Yesterday only',elapsed_ms=9000,status='completed',completed_at=now() WHERE id=$1`, added[0].ID); err != nil {
		t.Fatal(err)
	}
	oldSession := sessionID
	if _, err := isolated.Exec(ctx, `UPDATE practice_sessions SET status='completed',ended_at=now() WHERE id=$1`, sessionID); err != nil {
		t.Fatal(err)
	}
	sessionID = uuid.New()
	if _, err := isolated.Exec(ctx, `INSERT INTO practice_sessions(id,user_id,title,started_at,status) VALUES ($1,$2,'Tomorrow',now(),'active')`, sessionID, userID); err != nil {
		t.Fatal(err)
	}
	definitions.PracticeDate = "2026-09-12"
	definitions.Mode = "initialize"
	definitions.Blocks = []blockDefinition{{BlockKey: "factory", Title: "Factory", Category: "technique", Track: "trumpet", TargetMinutes: 10}}
	inherited := bootstrap()
	if len(inherited) != 2 || inherited[0].BlockKey != "custom-next" || inherited[1].Title != "Long tones" || inherited[1].TargetMinutes != 20 || inherited[1].Instructions != "My instructions" {
		t.Fatalf("wrong inheritance: %+v", inherited)
	}
	if inherited[1].ID == added[0].ID || inherited[1].Notes != "" || inherited[1].ElapsedMS != 0 || inherited[1].Status != "pending" || len(inherited[1].Recordings) != 0 {
		t.Fatal("copied historical progress")
	}
	if got := bootstrap(); len(got) != 2 || got[1].ID != inherited[1].ID {
		t.Fatal("reload duplicated or replaced blocks")
	}
	var oldNotes string
	if err := isolated.QueryRow(ctx, `SELECT notes FROM practice_blocks WHERE session_id=$1 AND id=$2`, oldSession, added[0].ID).Scan(&oldNotes); err != nil || oldNotes != "Yesterday only" {
		t.Fatal("previous day was altered")
	}
	definitions.Blocks = append(definitions.Blocks, blockDefinition{BlockKey: "today-only", Title: "Lesson", Category: "repertoire", Track: "musician", TargetMinutes: 15, DayOnly: true})
	if got := bootstrap(); len(got) != 3 {
		t.Fatal("dated addition not included")
	}
	if got := bootstrap(); len(got) != 3 {
		t.Fatal("dated addition duplicated")
	}

}
