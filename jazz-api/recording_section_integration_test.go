package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRecordingSectionMovePreservesPracticeAccounting(t *testing.T) {
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
	app := &application{db: db, logger: slog.Default()}
	userID, err := app.userID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sessionID, sourceID, targetID, recordingID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	if _, err := db.Exec(ctx, `INSERT INTO practice_sessions(id,user_id,title,started_at,status) VALUES($1,$2,'Practice',now(),'active')`, sessionID, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO practice_blocks(id,session_id,user_id,practice_date,block_key,position,title,category,track,target_minutes,elapsed_ms)
		VALUES ($1,$2,$3,'2026-09-18','warm-up',0,'Warm up','fundamentals','trumpet',10,60000),
		       ($4,$2,$3,'2026-09-18','long-tones',1,'Long tones','fundamentals','trumpet',10,0)`, sourceID, sessionID, userID, targetID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO recordings(id,user_id,practice_session_id,practice_block_id,practice_time_block_id,bucket,object_name,content_type,expected_size_bytes,duration_ms,recorded_at,status,notes)
		VALUES($1,$2,$3,$4,$4,'test','move.wav','audio/wav',10,30000,now(),'ready','Long tones section take')`, recordingID, userID, sessionID.String(), sourceID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO clip_day_analyses(user_id,practice_date,analysis_version,recording_count,recording_fingerprint) VALUES($1,'2026-09-18','waveform-v1',1,'before')`, userID); err != nil {
		t.Fatal(err)
	}

	move := func(payload map[string]any, want int) map[string]any {
		t.Helper()
		body, _ := json.Marshal(payload)
		req := httptest.NewRequest("PATCH", "/", bytes.NewReader(body)).WithContext(ctx)
		req.SetPathValue("id", recordingID.String())
		res := httptest.NewRecorder()
		app.updateRecordingSection(res, req)
		if res.Code != want {
			t.Fatalf("move status=%d want=%d body=%s", res.Code, want, res.Body.String())
		}
		out := map[string]any{}
		_ = json.Unmarshal(res.Body.Bytes(), &out)
		return out
	}
	payload := map[string]any{"practiceBlockId": targetID, "expectedPracticeBlockId": sourceID, "expectedRevision": 0}
	first := move(payload, 200)
	if first["practiceBlockTitle"] != "Long tones" || first["sectionAssignmentRevision"] != float64(1) {
		t.Fatalf("unexpected move: %#v", first)
	}
	// Network retries are idempotent and do not update derived state twice.
	retry := move(payload, 200)
	if retry["sectionAssignmentRevision"] != float64(1) {
		t.Fatalf("retry bumped revision: %#v", retry)
	}
	var assigned, timed uuid.UUID
	var revision int64
	if err := db.QueryRow(ctx, `SELECT practice_block_id,practice_time_block_id,section_assignment_revision FROM recordings WHERE id=$1`, recordingID).Scan(&assigned, &timed, &revision); err != nil {
		t.Fatal(err)
	}
	if assigned != targetID || timed != sourceID || revision != 1 {
		t.Fatalf("assignment=%s time=%s revision=%d", assigned, timed, revision)
	}
	blocks, err := app.loadPracticeBlocks(ctx, userID, sessionID, "2026-09-18")
	if err != nil {
		t.Fatal(err)
	}
	for _, block := range blocks {
		if block.ID == sourceID && (block.ElapsedMS != 60000 || block.RecordedMS != 0 || len(block.Recordings) != 0) {
			t.Fatalf("source summaries were not moved once: %+v", block)
		}
		if block.ID == targetID && (block.ElapsedMS != 0 || block.RecordedMS != 30000 || len(block.Recordings) != 1) {
			t.Fatalf("target summaries were not moved once: %+v", block)
		}
	}
	var outdated bool
	if err := db.QueryRow(ctx, `SELECT outdated_at IS NOT NULL FROM clip_day_analyses WHERE user_id=$1 AND practice_date='2026-09-18'`, userID).Scan(&outdated); err != nil || !outdated {
		t.Fatal("clip analysis was not marked outdated")
	}

	// Replayed repair migrations read a take's tune from the section it is
	// filed in now (a take belongs to its block's tune), while practice time
	// stays with the section it was recorded in.
	if _, err := db.Exec(ctx, `UPDATE practice_blocks SET block_key='blue-bossa-play' WHERE id=$1`, targetID); err != nil {
		t.Fatal(err)
	}
	if err := migrate(ctx, db); err != nil {
		t.Fatalf("repeat migration: %v", err)
	}
	var sourceElapsed, targetElapsed int
	if err := db.QueryRow(ctx, `SELECT elapsed_ms FROM practice_blocks WHERE id=$1`, sourceID).Scan(&sourceElapsed); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT elapsed_ms FROM practice_blocks WHERE id=$1`, targetID).Scan(&targetElapsed); err != nil {
		t.Fatal(err)
	}
	if sourceElapsed != 60000 || targetElapsed != 0 {
		t.Fatalf("timers changed after move/restart: source=%d target=%d", sourceElapsed, targetElapsed)
	}
	var tuneID, note string
	if err := db.QueryRow(ctx, `SELECT COALESCE(tune_id,''),COALESCE(notes,'') FROM recordings WHERE id=$1`, recordingID).Scan(&tuneID, &note); err != nil {
		t.Fatal(err)
	}
	if tuneID != "blue-bossa" || note != "" {
		t.Fatalf("restart rewrote preserved metadata: tune=%q note=%q", tuneID, note)
	}

	request := httptest.NewRequest("GET", "/", nil).WithContext(ctx)
	request.SetPathValue("id", sessionID.String())
	response := httptest.NewRecorder()
	app.getPracticeSession(response, request)
	var session practiceSession
	if err := json.Unmarshal(response.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	if session.TotalDurationMS != 60000 || session.RecordingCount != 1 {
		t.Fatalf("session totals changed: %+v", session)
	}
	calendar, err := app.loadArchiveCalendar(ctx, userID, mustDate(t, "2026-09-18"), mustDate(t, "2026-09-18"))
	if err != nil || len(calendar) != 1 || calendar[0].TotalDurationMS != 60000 {
		t.Fatalf("archive total changed: %+v %v", calendar, err)
	}
	if _, err := db.Exec(ctx, `UPDATE recordings SET status='uploading' WHERE id=$1`, recordingID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `UPDATE recordings SET status='ready' WHERE id=$1`, recordingID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT practice_block_id FROM recordings WHERE id=$1`, recordingID).Scan(&assigned); err != nil || assigned != targetID {
		t.Fatal("upload completion changed the latest assignment")
	}
	crossDayID := uuid.New()
	if _, err := db.Exec(ctx, `INSERT INTO practice_blocks(id,session_id,user_id,practice_date,block_key,position,title,category,track,target_minutes) VALUES($1,$2,$3,'2026-09-19','tomorrow',0,'Tomorrow','other','trumpet',5)`, crossDayID, sessionID, userID); err != nil {
		t.Fatal(err)
	}
	move(map[string]any{"practiceBlockId": crossDayID, "expectedPracticeBlockId": targetID, "expectedRevision": 1}, 404)
	otherCtx := context.WithValue(context.Background(), userSubjectKey, "recording-section-other")
	otherUser, err := app.userID(otherCtx)
	if err != nil {
		t.Fatal(err)
	}
	otherSession, otherBlock := uuid.New(), uuid.New()
	if _, err := db.Exec(ctx, `INSERT INTO practice_sessions(id,user_id,title,started_at,status) VALUES($1,$2,'Other',now(),'active')`, otherSession, otherUser); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO practice_blocks(id,session_id,user_id,practice_date,block_key,position,title,category,track,target_minutes) VALUES($1,$2,$3,'2026-09-18','other',0,'Other','other','trumpet',5)`, otherBlock, otherSession, otherUser); err != nil {
		t.Fatal(err)
	}
	move(map[string]any{"practiceBlockId": otherBlock, "expectedPracticeBlockId": targetID, "expectedRevision": 1}, 404)

	// A stale Undo cannot overwrite a newer assignment.
	undo := map[string]any{"practiceBlockId": sourceID, "expectedPracticeBlockId": targetID, "expectedRevision": 1}
	move(undo, 200)
	move(payload, 409)
	beforeBlocks := 0
	_ = db.QueryRow(ctx, `SELECT COUNT(*) FROM practice_blocks WHERE session_id=$1`, sessionID).Scan(&beforeBlocks)
	move(map[string]any{
		"practiceBlockId": uuid.New(), "expectedPracticeBlockId": sourceID, "expectedRevision": 2,
		"createTarget": map[string]any{"blockKey": "invalid-new", "title": "Invalid", "instructions": string(make([]byte, 2001)), "category": "fundamentals", "track": "trumpet", "targetMinutes": 5},
	}, 422)
	afterBlocks := 0
	_ = db.QueryRow(ctx, `SELECT COUNT(*) FROM practice_blocks WHERE session_id=$1`, sessionID).Scan(&afterBlocks)
	if beforeBlocks != afterBlocks {
		t.Fatal("failed target creation was not rolled back")
	}

	// A missing destination can be created atomically for this day.
	newID := uuid.New()
	created := move(map[string]any{
		"practiceBlockId": newID, "expectedPracticeBlockId": sourceID, "expectedRevision": 2,
		"createTarget": map[string]any{"blockKey": "custom-" + newID.String(), "title": "Soft attacks", "category": "fundamentals", "track": "trumpet", "targetMinutes": 5},
	}, 200)
	if created["practiceBlockTitle"] != "Soft attacks" {
		t.Fatalf("created target response: %#v", created)
	}
	var carries bool
	if err := db.QueryRow(ctx, `SELECT carry_forward FROM practice_blocks WHERE id=$1`, newID).Scan(&carries); err != nil || carries {
		t.Fatal("new assignment-only section would recur on a later practice day")
	}
}

func mustDate(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse("2006-01-02", value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}
