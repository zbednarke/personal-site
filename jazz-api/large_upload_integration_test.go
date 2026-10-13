package main

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/oauth2"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

type largeUploadTransport struct{}

func (largeUploadTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: 200, Header: http.Header{"Location": []string{"https://example.com/upload"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
}
func TestInitRecordingLargeVideo(t *testing.T) {
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
	for _, size := range []int64{2147483647, 2147483648, 3138700058} {
		payload := map[string]any{"mediaKind": "video", "contentType": "audio/wav", "sizeBytes": 699877676, "durationMs": 4860261, "recordedAt": "2026-09-24T17:00:00Z", "videoContentType": "video/webm", "videoSizeBytes": size, "videoWidth": 1920, "videoHeight": 1080, "videoFrameRate": 30}
		data, _ := json.Marshal(payload)
		req := httptest.NewRequest("POST", "/v1/recordings/init", bytes.NewReader(data)).WithContext(ctx)
		res := httptest.NewRecorder()
		app.initRecording(res, req)
		if res.Code != 201 {
			t.Fatalf("size %d: %d %s", size, res.Code, res.Body.String())
		}
		var result struct {
			ID string `json:"id"`
		}
		json.Unmarshal(res.Body.Bytes(), &result)
		var stored int64
		if err := db.QueryRow(ctx, "SELECT video_expected_size_bytes FROM recordings WHERE id=$1", result.ID).Scan(&stored); err != nil {
			t.Fatal(err)
		}
		if stored != size {
			t.Fatalf("stored %d want %d", stored, size)
		}
	}
	// Startup replays the migrations. Long recordings must survive a restart.
	if _, err := db.Exec(ctx, `UPDATE recordings SET duration_ms=14400000 WHERE video_expected_size_bytes=3138700058`); err != nil {
		t.Fatal(err)
	}
	for restart := 0; restart < 2; restart++ {
		if err := migrate(ctx, db); err != nil {
			t.Fatalf("restart %d with long recordings: %v", restart+1, err)
		}
	}
	var count int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM recordings WHERE duration_ms IN (4860261,14400000)`).Scan(&count); err != nil || count != 3 {
		t.Fatalf("long recordings changed after restart: count=%d err=%v", count, err)
	}
	if _, err := db.Exec(ctx, `UPDATE recordings SET duration_ms=14400001`); err == nil {
		t.Fatal("restart removed the four-hour duration limit")
	}

}
