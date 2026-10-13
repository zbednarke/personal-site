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
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestLongToneAnalysisLifecycle(t *testing.T) {
	databaseURL := os.Getenv("JAZZ_LONG_TONE_TEST_DATABASE_URL")
	if databaseURL == "" {
		databaseURL = os.Getenv("JAZZ_LAYOUT_TEST_DATABASE_URL")
	}
	if databaseURL == "" {
		t.Skip("set JAZZ_LONG_TONE_TEST_DATABASE_URL to a disposable PostgreSQL database")
	}
	root, err := pgxpool.New(context.Background(), databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	schema := "long_tone_test_" + uuid.New().String()[:8]
	if _, err = root.Exec(context.Background(), "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer root.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	db, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}

	ctx := context.WithValue(context.Background(), userSubjectKey, "long-tone-owner")
	app := &application{db: db, logger: slog.Default()}
	userID, err := app.userID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	recordingID := uuid.New()
	_, err = db.Exec(ctx, `INSERT INTO recordings
		(id,user_id,bucket,object_name,object_generation,content_type,expected_size_bytes,size_bytes,duration_ms,
		recorded_at,uploaded_at,status,checksum,media_kind)
		VALUES($1,$2,'test','owner/take.wav',41,'audio/wav',100,100,2000,now(),now(),'ready','crc32c:one','audio')`, recordingID, userID)
	if err != nil {
		t.Fatal(err)
	}

	post := func(requestCtx context.Context) (int, longToneAnalysisResponse) {
		t.Helper()
		body, _ := json.Marshal(longToneAnalysisRequest{Asset: "audio"})
		r := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body)).WithContext(requestCtx)
		r.SetPathValue("id", recordingID.String())
		w := httptest.NewRecorder()
		app.createLongToneAnalysis(w, r)
		return w.Code, decodeResponse(t, w)
	}
	code, first := post(ctx)
	if code != http.StatusAccepted || first.Status != "pending" || first.AnalysisID == nil {
		t.Fatalf("first POST: %d %+v", code, first)
	}
	code, duplicate := post(ctx)
	if code != http.StatusAccepted || duplicate.AnalysisID == nil || *duplicate.AnalysisID != *first.AnalysisID {
		t.Fatalf("duplicate POST did not reuse job: %d %+v", code, duplicate)
	}

	firstClaim, err := app.claimLongToneJob(ctx)
	if err != nil || firstClaim == nil {
		t.Fatalf("first claim: %+v %v", firstClaim, err)
	}
	if _, err = db.Exec(ctx, `UPDATE long_tone_analysis_jobs SET leased_until=now()-interval '1 second' WHERE id=$1`, firstClaim.ID); err != nil {
		t.Fatal(err)
	}
	secondClaim, err := app.claimLongToneJob(ctx)
	if err != nil || secondClaim == nil || secondClaim.LeaseToken == firstClaim.LeaseToken {
		t.Fatalf("reclaim: %+v %v", secondClaim, err)
	}
	app.longToneSigner = func(_ context.Context, object string, generation int64, _ time.Time) (string, error) {
		if object != "owner/take.wav" || generation != 41 {
			t.Fatalf("unpinned source: %q generation %d", object, generation)
		}
		return "fixture://generation/41", nil
	}
	app.longToneDecoder = func(ctx context.Context, _ string, _ string, analyze func(io.Reader) (int64, error)) (int64, error) {
		return analyze(bytes.NewReader(makeLongTonePCM(2 * time.Second)))
	}
	app.processLongToneJob(ctx, *firstClaim)
	var chunks int
	if err = db.QueryRow(ctx, `SELECT count(*) FROM long_tone_analysis_chunks WHERE analysis_id=$1`, firstClaim.ID).Scan(&chunks); err != nil {
		t.Fatal(err)
	}
	if chunks != 0 {
		t.Fatalf("stale worker wrote %d chunks", chunks)
	}
	app.processLongToneJob(ctx, *secondClaim)

	get := func(requestCtx context.Context, query string) (int, longToneAnalysisResponse) {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, "/?"+query, nil).WithContext(requestCtx)
		r.SetPathValue("id", recordingID.String())
		w := httptest.NewRecorder()
		app.getLongToneAnalysis(w, r)
		if w.Code != http.StatusOK {
			return w.Code, longToneAnalysisResponse{}
		}
		return w.Code, decodeResponse(t, w)
	}
	code, ready := get(ctx, "asset=audio&startMs=500&endMs=700")
	if code != http.StatusOK || ready.Status != "ready" || len(ready.Frames) < 3 {
		t.Fatalf("ready range: %d status=%s frames=%d", code, ready.Status, len(ready.Frames))
	}
	if ready.Frames[0].TimeMS >= 500 || ready.Frames[len(ready.Frames)-1].TimeMS <= 700 {
		t.Fatalf("range lacks edge neighbors: %d..%d", ready.Frames[0].TimeMS, ready.Frames[len(ready.Frames)-1].TimeMS)
	}
	code, _ = get(ctx, "asset=audio&startMs=0&endMs=120001")
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("oversize range GET=%d, want 422", code)
	}
	if _, err = db.Exec(ctx, `UPDATE recordings SET status='uploading' WHERE id=$1`, recordingID); err != nil {
		t.Fatal(err)
	}
	code, _ = get(ctx, "asset=audio")
	if code != http.StatusConflict {
		t.Fatalf("uploading recording GET=%d, want 409", code)
	}
	if _, err = db.Exec(ctx, `UPDATE recordings SET status='ready' WHERE id=$1`, recordingID); err != nil {
		t.Fatal(err)
	}
	code, _ = get(context.WithValue(context.Background(), userSubjectKey, "different-owner"), "asset=audio")
	if code != http.StatusNotFound {
		t.Fatalf("other owner GET=%d, want 404", code)
	}

	// Object name participates in identity even when generation and checksum
	// happen to collide with an earlier source.
	if _, err = db.Exec(ctx, `UPDATE recordings SET object_name='owner/replaced.wav' WHERE id=$1`, recordingID); err != nil {
		t.Fatal(err)
	}
	code, missing := get(ctx, "asset=audio")
	if code != http.StatusOK || missing.Status != "missing" || missing.AnalysisID != nil {
		t.Fatalf("source replacement did not invalidate result: %d %+v", code, missing)
	}
	code, replacement := post(ctx)
	if code != http.StatusAccepted || replacement.AnalysisID == nil || *replacement.AnalysisID == *first.AnalysisID {
		t.Fatalf("same generation/checksum with a new object name reused old analysis: %d %+v", code, replacement)
	}

	// Exhaust the automatic budget, then make two explicit retry requests. The
	// row lock lets only the first reset the budget; both return the same job.
	exhausted, err := app.claimLongToneJob(ctx)
	if err != nil || exhausted == nil {
		t.Fatalf("claim replacement: %+v %v", exhausted, err)
	}
	exhausted.Attempts = longToneMaxAttempts
	if _, err = db.Exec(ctx, `UPDATE long_tone_analysis_jobs SET attempts=$2 WHERE id=$1`, exhausted.ID, longToneMaxAttempts); err != nil {
		t.Fatal(err)
	}
	app.finishLongToneFailure(ctx, *exhausted, "decoder_failed", true)
	if _, err = db.Exec(ctx, `INSERT INTO long_tone_analysis_chunks(analysis_id,chunk_start_ms,chunk_end_ms,frames)
		VALUES($1,0,0,'[]')`, exhausted.ID); err != nil {
		t.Fatal(err)
	}
	type postResult struct {
		code     int
		response longToneAnalysisResponse
		err      error
	}
	results := make(chan postResult, 2)
	for i := 0; i < 2; i++ {
		go func() {
			body, _ := json.Marshal(longToneAnalysisRequest{Asset: "audio"})
			r := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body)).WithContext(ctx)
			r.SetPathValue("id", recordingID.String())
			w := httptest.NewRecorder()
			app.createLongToneAnalysis(w, r)
			var response longToneAnalysisResponse
			decodeErr := json.Unmarshal(w.Body.Bytes(), &response)
			results <- postResult{code: w.Code, response: response, err: decodeErr}
		}()
	}
	for i := 0; i < 2; i++ {
		result := <-results
		if result.err != nil || result.code != http.StatusAccepted || result.response.AnalysisID == nil || *result.response.AnalysisID != exhausted.ID {
			t.Fatalf("explicit retry result: code=%d response=%+v err=%v", result.code, result.response, result.err)
		}
	}
	var attempts, remainingChunks int
	var retryStatus string
	if err = db.QueryRow(ctx, `SELECT status,attempts FROM long_tone_analysis_jobs WHERE id=$1`, exhausted.ID).Scan(&retryStatus, &attempts); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(ctx, `SELECT count(*) FROM long_tone_analysis_chunks WHERE analysis_id=$1`, exhausted.ID).Scan(&remainingChunks); err != nil {
		t.Fatal(err)
	}
	if retryStatus != "pending" || attempts != 0 || remainingChunks != 0 {
		t.Fatalf("explicit retry status=%s attempts=%d chunks=%d", retryStatus, attempts, remainingChunks)
	}
	retryClaim, err := app.claimLongToneJob(ctx)
	if err != nil || retryClaim == nil || retryClaim.ID != exhausted.ID {
		t.Fatalf("claim explicit retry: %+v %v", retryClaim, err)
	}
	app.longToneSigner = func(_ context.Context, object string, generation int64, _ time.Time) (string, error) {
		if object != "owner/replaced.wav" || generation != 41 {
			t.Fatalf("retry source: %q generation %d", object, generation)
		}
		return "fixture://replacement/41", nil
	}
	app.processLongToneJob(ctx, *retryClaim)
	if err = db.QueryRow(ctx, `SELECT status FROM long_tone_analysis_jobs WHERE id=$1`, exhausted.ID).Scan(&retryStatus); err != nil {
		t.Fatal(err)
	}
	if retryStatus != "ready" {
		t.Fatalf("explicit retry finished as %s", retryStatus)
	}
	if _, err = db.Exec(ctx, `UPDATE recordings SET status='deleted' WHERE id=$1`, recordingID); err != nil {
		t.Fatal(err)
	}
	code, _ = get(ctx, "asset=audio")
	if code != http.StatusNotFound {
		t.Fatalf("deleted recording GET=%d, want 404", code)
	}
}
