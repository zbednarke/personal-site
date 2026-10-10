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
	"strconv"
	"strings"
	"sync"
	"testing"

	"cloud.google.com/go/storage"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/oauth2"
	"google.golang.org/api/option"
)

// fakeGCS answers the JSON API calls the recording handlers make (object
// attributes and deletes) for objects the test declares as uploaded.
type fakeGCS struct {
	mu       sync.Mutex
	objects  map[string]map[string]string // name -> metadata
	sizes    map[string]int64
	deleted  []string
	attrsHit int
}

func (f *fakeGCS) put(name string, size int64, metadata map[string]string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.objects[name] = metadata
	f.sizes[name] = size
}

func (f *fakeGCS) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	name, ok := strings.CutPrefix(r.URL.Path, "/storage/v1/b/test-bucket/o/")
	if !ok {
		http.NotFound(w, r)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	metadata, exists := f.objects[name]
	switch r.Method {
	case http.MethodDelete:
		f.deleted = append(f.deleted, name)
		if !exists {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		delete(f.objects, name)
		w.WriteHeader(http.StatusNoContent)
	case http.MethodGet:
		f.attrsHit++
		if !exists {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"bucket": "test-bucket", "name": name, "size": strconv.FormatInt(f.sizes[name], 10),
			"generation": "7", "crc32c": "AAAAAA==", "metadata": metadata,
		})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func TestRecordingFxAssetLifecycle(t *testing.T) {
	url := os.Getenv("JAZZ_LAYOUT_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set JAZZ_LAYOUT_TEST_DATABASE_URL to a disposable PostgreSQL database")
	}
	ctx := context.WithValue(context.Background(), userSubjectKey, "fx-test")
	db, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	schema := "fx_test_" + uuid.New().String()[:8]
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
	for run := 0; run < 2; run++ {
		if err := migrate(ctx, isolated); err != nil {
			t.Fatalf("startup migration %d: %v", run+1, err)
		}
	}
	var shareCheck string
	if err := isolated.QueryRow(ctx, `SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conrelid='recording_shares'::regclass AND conname='recording_shares_asset_check'`).Scan(&shareCheck); err != nil || !strings.Contains(shareCheck, "fx") {
		t.Fatalf("share asset check was not widened: %q %v", shareCheck, err)
	}

	gcs := &fakeGCS{objects: map[string]map[string]string{}, sizes: map[string]int64{}}
	gcsServer := httptest.NewServer(gcs)
	defer gcsServer.Close()
	storageClient, err := storage.NewClient(ctx, option.WithEndpoint(gcsServer.URL+"/storage/v1/"), option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}
	defer storageClient.Close()
	var uploadSessions []string
	app := &application{
		db: isolated, logger: slog.Default(), storage: storageClient,
		cfg:         config{Bucket: "test-bucket", PublicShareBaseURL: "https://example.test/r"},
		tokenSource: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "test-only"}),
		httpClient: &http.Client{Transport: layoutTestTransport(func(r *http.Request) (*http.Response, error) {
			uploadSessions = append(uploadSessions, r.URL.Query().Get("name"))
			return &http.Response{StatusCode: 200, Header: http.Header{"Location": []string{"https://storage.example.test/upload/" + strconv.Itoa(len(uploadSessions))}}, Body: io.NopCloser(strings.NewReader(""))}, nil
		})},
	}
	userID, err := app.userID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	call := func(handler http.HandlerFunc, method, target, id string, body any, want int) []byte {
		t.Helper()
		var reader io.Reader = http.NoBody
		if body != nil {
			encoded, _ := json.Marshal(body)
			reader = bytes.NewReader(encoded)
		}
		r := httptest.NewRequest(method, target, reader).WithContext(ctx)
		if id != "" {
			r.SetPathValue("id", id)
		}
		w := httptest.NewRecorder()
		handler(w, r)
		if w.Code != want {
			t.Fatalf("%s %s: got %d want %d: %s", method, target, w.Code, want, w.Body.String())
		}
		return w.Body.Bytes()
	}

	capture := map[string]any{"contentType": "audio/wav", "sizeBytes": 100, "durationMs": 5000, "recordedAt": "2026-10-01T12:00:00Z",
		"fxContentType": "audio/wav", "fxSizeBytes": 120, "fxPreset": "big-hall"}
	badCapture := map[string]any{"contentType": "audio/wav", "sizeBytes": 100, "durationMs": 5000, "recordedAt": "2026-10-01T12:00:00Z",
		"fxContentType": "audio/wav", "fxSizeBytes": 120, "fxPreset": "../etc"}
	call(app.initRecording, "POST", "/", "", badCapture, http.StatusUnprocessableEntity)
	var initialized struct {
		ID           uuid.UUID `json:"id"`
		ObjectName   string    `json:"objectName"`
		FxUploadURL  string    `json:"fxUploadUrl"`
		FxObjectName string    `json:"fxObjectName"`
	}
	if err := json.Unmarshal(call(app.initRecording, "POST", "/", "", capture, http.StatusCreated), &initialized); err != nil {
		t.Fatal(err)
	}
	id := initialized.ID.String()
	if initialized.FxUploadURL == "" || !strings.HasSuffix(initialized.FxObjectName, "/"+id+"/fx-mix.wav") || len(uploadSessions) != 2 {
		t.Fatalf("fx upload session missing: %+v sessions=%v", initialized, uploadSessions)
	}
	listedFx := func() (string, string) {
		t.Helper()
		var result struct {
			Recordings []recordingRow `json:"recordings"`
		}
		if err := json.Unmarshal(call(app.listRecordings, "GET", "/", "", nil, http.StatusOK), &result); err != nil || len(result.Recordings) != 1 {
			t.Fatalf("list: %v %+v", err, result)
		}
		return result.Recordings[0].Status, result.Recordings[0].FxContentType
	}

	// A wrong asset kind in the stored metadata must not verify as the fx mix.
	gcs.put(initialized.FxObjectName, 120, map[string]string{"recordingId": id, "userId": userID.String(), "assetKind": "audio"})
	call(app.completeRecording, "POST", "/", id, map[string]string{"asset": "fx"}, http.StatusConflict)

	// The dry master alone makes the take ready; the pending fx stays hidden.
	gcs.put(initialized.ObjectName, 100, map[string]string{"recordingId": id, "userId": userID.String(), "assetKind": "audio"})
	call(app.completeRecording, "POST", "/", id, map[string]string{"asset": "audio"}, http.StatusOK)
	if status, fxType := listedFx(); status != "ready" || fxType != "" {
		t.Fatalf("dry take: status %q fx %q", status, fxType)
	}
	call(app.recordingShareURL, "POST", "/?asset=fx", id, nil, http.StatusUnprocessableEntity)

	gcs.put(initialized.FxObjectName, 120, map[string]string{"recordingId": id, "userId": userID.String(), "assetKind": "fx"})
	call(app.completeRecording, "POST", "/", id, map[string]string{"asset": "fx"}, http.StatusOK)
	if status, fxType := listedFx(); status != "ready" || fxType != "audio/wav" {
		t.Fatalf("verified fx: status %q fx %q", status, fxType)
	}
	var share struct {
		URL   string `json:"url"`
		Asset string `json:"asset"`
	}
	if err := json.Unmarshal(call(app.recordingShareURL, "POST", "/?asset=fx", id, nil, http.StatusOK), &share); err != nil || share.Asset != "fx" {
		t.Fatalf("fx share: %v %+v", err, share)
	}

	// Discarding the fx mix deletes only that object and revokes its link.
	call(app.discardRecordingFx, "DELETE", "/", id, nil, http.StatusNoContent)
	if len(gcs.deleted) != 1 || gcs.deleted[0] != initialized.FxObjectName {
		t.Fatalf("discard deleted %v", gcs.deleted)
	}
	if status, fxType := listedFx(); status != "ready" || fxType != "" {
		t.Fatalf("after discard: status %q fx %q", status, fxType)
	}
	var activeShares int
	if err := isolated.QueryRow(ctx, `SELECT COUNT(*) FROM recording_shares WHERE recording_id=$1 AND revoked_at IS NULL`, initialized.ID).Scan(&activeShares); err != nil || activeShares != 0 {
		t.Fatalf("fx share survived discard: %d %v", activeShares, err)
	}
	call(app.discardRecordingFx, "DELETE", "/", id, nil, http.StatusNoContent)
	call(app.completeRecording, "POST", "/", id, map[string]string{"asset": "fx"}, http.StatusUnprocessableEntity)
	other := httptest.NewRequest("DELETE", "/", http.NoBody).WithContext(context.WithValue(ctx, userSubjectKey, "someone-else"))
	other.SetPathValue("id", id)
	otherResponse := httptest.NewRecorder()
	app.discardRecordingFx(otherResponse, other)
	if otherResponse.Code != http.StatusNotFound {
		t.Fatalf("another user discarded the fx mix: %d", otherResponse.Code)
	}

	// Section takes report a verified fx mix too.
	sessionID := uuid.New()
	blockID := uuid.New()
	if _, err := isolated.Exec(ctx, `INSERT INTO practice_sessions (id,user_id,title,started_at,status) VALUES ($1,$2,'Test',now(),'active')`, sessionID, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := isolated.Exec(ctx, `INSERT INTO practice_blocks (id,session_id,user_id,practice_date,block_key,position,title,category,track,target_minutes) VALUES ($1,$2,$3,'2026-10-01','warmup',0,'Warmup','technique','trumpet',10)`, blockID, sessionID, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := isolated.Exec(ctx, `INSERT INTO recordings (id,user_id,practice_session_id,practice_block_id,bucket,object_name,content_type,expected_size_bytes,duration_ms,recorded_at,status,fx_object_name,fx_content_type,fx_expected_size_bytes,fx_size_bytes,fx_preset,fx_uploaded_at)
		VALUES ($1,$2,$3,$4,'test-bucket','dry.wav','audio/wav',10,1000,now(),'ready','fx.wav','audio/wav',12,12,'space-echo',now())`, uuid.New(), userID, sessionID.String(), blockID); err != nil {
		t.Fatal(err)
	}
	takes, err := app.loadBlockRecordings(ctx, userID, blockID)
	if err != nil || len(takes) != 1 || takes[0].FxContentType != "audio/wav" || takes[0].FxPreset != "space-echo" || takes[0].FxSizeBytes != 12 {
		t.Fatalf("section take fx: %v %+v", err, takes)
	}
}
