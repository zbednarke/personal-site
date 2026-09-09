package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

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
	layout(blockLayoutRequest{BlockIDs: []uuid.UUID{b}, RemoveID: &a}, "layout-test", 204)
	if got := bootstrap(); len(got) != 1 || got[0].ID != b {
		t.Fatal("deleted default reappeared")
	}
	var retained uuid.UUID
	if err := isolated.QueryRow(ctx, `SELECT practice_block_id FROM recordings WHERE id=$1 AND status='ready'`, recordingID).Scan(&retained); err != nil || retained != a {
		t.Fatalf("recording was not retained: %v", err)
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
}
