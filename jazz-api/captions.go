package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const (
	captionAnalysisVersion = "google-stt-v2-long-en-us-v3"
	captionMaxAttempts     = 3
	captionLeaseDuration   = 2 * time.Minute
)

var speechOperationName = regexp.MustCompile(`^projects/[a-z0-9-]+/locations/global/operations/[A-Za-z0-9_-]+$`)

type captionSegment struct {
	StartMS    int64   `json:"startMs"`
	EndMS      int64   `json:"endMs"`
	Text       string  `json:"text"`
	Confidence float64 `json:"confidence,omitempty"`
}

type captionResponse struct {
	Status      string           `json:"status"`
	Version     string           `json:"version"`
	RecordingID uuid.UUID        `json:"recordingId"`
	Segments    []captionSegment `json:"segments,omitempty"`
	ErrorCode   string           `json:"errorCode,omitempty"`
}

type captionJob struct {
	ID          uuid.UUID
	RecordingID uuid.UUID
	ObjectName  string
	Generation  int64
	Attempts    int
	LeaseToken  uuid.UUID
	DurationMS  int64
}

// errCaptionDeadline ends a job whose Speech operation never finished.
var errCaptionDeadline = errors.New("speech recognition did not finish in time")

// captionPollInterval is how often a Speech operation is checked (tests shorten it).
var captionPollInterval = 5 * time.Second

// captionDeadline is how long a take's Speech operation may run: four times
// its audio plus five minutes. Batch recognition is normally far faster.
func captionDeadline(durationMS int64) time.Duration {
	return 4*time.Duration(max(durationMS, 0))*time.Millisecond + 5*time.Minute
}

type captionAlternative struct {
	Transcript string  `json:"transcript"`
	Confidence float64 `json:"confidence"`
	Words      []struct {
		Start string `json:"startOffset"`
		End   string `json:"endOffset"`
	} `json:"words"`
}
type captionRecognitionResult struct {
	Alternatives []captionAlternative `json:"alternatives"`
	ResultEnd    string               `json:"resultEndOffset"`
}
type captionTranscript struct {
	Results []captionRecognitionResult `json:"results"`
}
type captionFileResult struct {
	Error        json.RawMessage   `json:"error"`
	Transcript   captionTranscript `json:"transcript"`
	InlineResult struct {
		Transcript captionTranscript `json:"transcript"`
	} `json:"inlineResult"`
}
type captionOperation struct {
	Done     bool            `json:"done"`
	Error    json.RawMessage `json:"error"`
	Response struct {
		Results map[string]captionFileResult `json:"results"`
	} `json:"response"`
}

func (app *application) createRecordingCaptions(w http.ResponseWriter, r *http.Request) {
	recordingID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid recording id")
		return
	}
	userID, err := app.userID(r.Context())
	if err != nil {
		app.serverError(w, err)
		return
	}
	response, status, err := app.enqueueCaption(r.Context(), userID, recordingID)
	if err != nil {
		app.serverError(w, err)
		return
	}
	if status != 0 {
		writeError(w, status, longToneAssetError(status))
		return
	}
	writeJSON(w, http.StatusAccepted, response)
}

func (app *application) createDayCaptions(w http.ResponseWriter, r *http.Request) {
	date := r.PathValue("date")
	if _, err := time.Parse("2006-01-02", date); err != nil {
		writeError(w, http.StatusBadRequest, "invalid practice date")
		return
	}
	userID, err := app.userID(r.Context())
	if err != nil {
		app.serverError(w, err)
		return
	}
	recordings, err := app.clipStudioRecordings(r, userID, date)
	if err != nil {
		app.serverError(w, err)
		return
	}
	queued := 0
	for _, recording := range recordings {
		response, status, enqueueErr := app.enqueueCaption(r.Context(), userID, recording.ID)
		if enqueueErr != nil {
			app.serverError(w, enqueueErr)
			return
		}
		if status != 0 {
			continue // still uploading, or deleted meanwhile: nothing to transcribe yet
		}
		if response.Status == "pending" || response.Status == "running" {
			queued++
		}
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"date": date, "recordingCount": len(recordings), "queued": queued})
}

func (app *application) enqueueCaption(ctx context.Context, userID, recordingID uuid.UUID) (captionResponse, int, error) {
	source, status, err := app.loadLongToneAsset(ctx, userID, recordingID, "audio")
	if err != nil || status != 0 {
		return captionResponse{}, status, err
	}
	response := captionResponse{Status: "pending", Version: captionAnalysisVersion, RecordingID: recordingID}
	err = app.db.QueryRow(ctx, `INSERT INTO recording_caption_jobs
		(id,recording_id,user_id,source_object_name,source_generation,source_checksum,analysis_version,status)
		VALUES($1,$2,$3,$4,$5,$6,$7,'pending')
		ON CONFLICT (recording_id,source_object_name,source_generation,source_checksum,analysis_version)
		DO UPDATE SET available_at=CASE WHEN recording_caption_jobs.status='failed' THEN now() ELSE recording_caption_jobs.available_at END,
		status=CASE WHEN recording_caption_jobs.status='failed' THEN 'pending' ELSE recording_caption_jobs.status END,
		attempts=CASE WHEN recording_caption_jobs.status='failed' THEN 0 ELSE recording_caption_jobs.attempts END,
		retryable=CASE WHEN recording_caption_jobs.status='failed' THEN false ELSE recording_caption_jobs.retryable END,
		error_code=CASE WHEN recording_caption_jobs.status='failed' THEN NULL ELSE recording_caption_jobs.error_code END,
		updated_at=now()
		RETURNING status,segments,COALESCE(error_code,'')`, uuid.New(), recordingID, userID, source.ObjectName, source.Generation, source.Checksum, captionAnalysisVersion).
		Scan(&response.Status, &response.Segments, &response.ErrorCode)
	return response, 0, err
}

func (app *application) getRecordingCaptions(w http.ResponseWriter, r *http.Request) {
	recordingID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid recording id")
		return
	}
	userID, err := app.userID(r.Context())
	if err != nil {
		app.serverError(w, err)
		return
	}
	source, status, loadErr := app.loadLongToneAsset(r.Context(), userID, recordingID, "audio")
	if loadErr != nil {
		app.serverError(w, loadErr)
		return
	}
	if status != 0 {
		writeError(w, status, longToneAssetError(status))
		return
	}
	response := captionResponse{Status: "missing", Version: captionAnalysisVersion, RecordingID: recordingID}
	err = app.db.QueryRow(r.Context(), `SELECT status,segments,COALESCE(error_code,'') FROM recording_caption_jobs
		WHERE user_id=$1 AND recording_id=$2 AND analysis_version=$3 AND source_object_name=$4 AND source_generation=$5 AND source_checksum=$6
		ORDER BY created_at DESC LIMIT 1`, userID, recordingID, captionAnalysisVersion, source.ObjectName, source.Generation, source.Checksum).
		Scan(&response.Status, &response.Segments, &response.ErrorCode)
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusOK, response)
		return
	}
	if err != nil {
		app.serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (app *application) runCaptionWorkers(ctx context.Context, count int) *sync.WaitGroup {
	var workers sync.WaitGroup
	for i := 0; i < count; i++ {
		workers.Add(1)
		go func() { defer workers.Done(); app.captionWorker(ctx) }()
	}
	return &workers
}

func (app *application) captionWorker(ctx context.Context) {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		job, err := app.claimCaptionJob(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			app.logger.Error("caption job claim failed", "error", err)
		} else if job != nil {
			app.processCaptionJob(ctx, *job)
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (app *application) claimCaptionJob(ctx context.Context) (*captionJob, error) {
	tx, err := app.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, `UPDATE recording_caption_jobs SET status='failed',error_code='worker_lost',retryable=true,lease_token=NULL,leased_until=NULL,updated_at=now() WHERE status='running' AND leased_until<now() AND attempts >= $1`, captionMaxAttempts)
	if err != nil {
		return nil, err
	}
	// A queued job whose take was deleted, replaced, or is no longer ready ends
	// here instead of waiting forever.
	_, err = tx.Exec(ctx, `UPDATE recording_caption_jobs j
		SET status='failed',error_code='source_changed',retryable=false,lease_token=NULL,leased_until=NULL,updated_at=now()
		WHERE j.status IN ('pending','running') AND NOT EXISTS (
			SELECT 1 FROM recordings r WHERE r.id=j.recording_id AND r.status='ready'
			  AND r.object_name=j.source_object_name AND r.object_generation=j.source_generation)`)
	if err != nil {
		return nil, err
	}
	var job captionJob
	// Takes still uploading, deleted, or replaced since the job was queued are
	// never transcribed (deleteRecording also removes their caption data).
	err = tx.QueryRow(ctx, `
		SELECT j.id,j.recording_id,j.source_object_name,j.source_generation,j.attempts,COALESCE(r.duration_ms,0)
		FROM recording_caption_jobs j
		JOIN recordings r ON r.id=j.recording_id AND r.user_id=j.user_id
		WHERE j.attempts<$1 AND j.analysis_version=$2 AND j.available_at<=now()
		  AND (j.status='pending' OR (j.status='running' AND j.leased_until<now()))
		  AND r.status='ready' AND r.object_name=j.source_object_name AND r.object_generation=j.source_generation
		ORDER BY j.created_at FOR UPDATE OF j SKIP LOCKED LIMIT 1`, captionMaxAttempts, captionAnalysisVersion).
		Scan(&job.ID, &job.RecordingID, &job.ObjectName, &job.Generation, &job.Attempts, &job.DurationMS)
	if errors.Is(err, pgx.ErrNoRows) {
		// Keep the sweeps above even when there is nothing to claim.
		return nil, tx.Commit(ctx)
	}
	if err != nil {
		return nil, err
	}
	job.LeaseToken = uuid.New()
	job.Attempts++
	_, err = tx.Exec(ctx, `UPDATE recording_caption_jobs SET status='running',attempts=$2,lease_token=$3,leased_until=now()+$4::interval,error_code=NULL,retryable=false,updated_at=now() WHERE id=$1`, job.ID, job.Attempts, job.LeaseToken, captionLeaseDuration.String())
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &job, nil
}

func (app *application) processCaptionJob(workerCtx context.Context, job captionJob) {
	ctx, cancel := context.WithCancel(workerCtx)
	defer cancel()
	go func() {
		ticker := time.NewTicker(captionLeaseDuration / 3)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				tag, err := app.db.Exec(ctx, `UPDATE recording_caption_jobs SET leased_until=now()+$3::interval,updated_at=now() WHERE id=$1 AND lease_token=$2 AND status='running'`, job.ID, job.LeaseToken, captionLeaseDuration.String())
				if err != nil || tag.RowsAffected() != 1 {
					cancel()
					return
				}
			}
		}
	}()
	speechObject, err := app.ensureSpeechAudio(ctx, job.ObjectName, job.Generation)
	var segments []captionSegment
	if err == nil {
		segments, err = app.transcribeGCS(ctx, speechObject, captionDeadline(job.DurationMS))
	}
	if err != nil {
		retryable := job.Attempts < captionMaxAttempts
		code := "recognition_failed"
		if errors.Is(err, context.Canceled) {
			code = "worker_lost"
		}
		if errors.Is(err, errCaptionDeadline) {
			// Waiting again would only repeat the wait: fail it for a manual retry.
			code, retryable = "recognition_timeout", false
		}
		nextStatus := "failed"
		if retryable {
			nextStatus = "pending"
		}
		_, _ = app.db.Exec(workerCtx, `UPDATE recording_caption_jobs SET status=$3,error_code=$4,retryable=$5,available_at=now()+interval '30 seconds',lease_token=NULL,leased_until=NULL,updated_at=now() WHERE id=$1 AND lease_token=$2`, job.ID, job.LeaseToken, nextStatus, code, retryable)
		return
	}
	raw, _ := json.Marshal(segments)
	_, err = app.db.Exec(workerCtx, `UPDATE recording_caption_jobs SET status='ready',segments=$3,error_code=NULL,retryable=false,lease_token=NULL,leased_until=NULL,completed_at=now(),updated_at=now() WHERE id=$1 AND lease_token=$2`, job.ID, job.LeaseToken, raw)
	if err != nil {
		app.logger.Error("caption save failed", "error", err)
	}
}

func speechAudioObjectName(source string, generation int64) string {
	ext := filepath.Ext(source)
	base := strings.TrimSuffix(source, ext)
	return fmt.Sprintf("%s.caption-%d.flac", base, generation)
}

// ensureSpeechAudio converts the browser's 24-bit archival WAV to a compact,
// Speech-compatible 16-bit FLAC sidecar. Both sides stream through ffmpeg, so a
// long lesson never has to fit in the container's writable filesystem.
func (app *application) ensureSpeechAudio(ctx context.Context, source string, generation int64) (string, error) {
	destination := speechAudioObjectName(source, generation)
	object := app.storage.Bucket(app.cfg.Bucket).Object(destination)
	if attrs, err := object.Attrs(ctx); err == nil && attrs.Size > 0 {
		return destination, nil
	}

	sourceObject := app.storage.Bucket(app.cfg.Bucket).Object(source)
	if generation > 0 {
		sourceObject = sourceObject.Generation(generation)
	}
	reader, err := sourceObject.NewReader(ctx)
	if err != nil {
		return "", fmt.Errorf("open caption audio: %w", err)
	}
	defer reader.Close()

	writer := object.NewWriter(ctx)
	writer.ContentType = "audio/flac"
	command := exec.CommandContext(ctx, "ffmpeg", "-hide_banner", "-loglevel", "error", "-i", "pipe:0", "-vn", "-ac", "1", "-ar", "48000", "-sample_fmt", "s16", "-c:a", "flac", "-f", "flac", "pipe:1")
	command.Stdin = reader
	command.Stdout = writer
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err = command.Run(); err != nil {
		_ = writer.CloseWithError(err)
		_ = object.Delete(context.Background())
		return "", fmt.Errorf("convert caption audio: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	if err = writer.Close(); err != nil {
		_ = object.Delete(context.Background())
		return "", fmt.Errorf("store caption audio: %w", err)
	}
	return destination, nil
}

func (app *application) speechRequest(ctx context.Context, method, endpoint string, body any) ([]byte, error) {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return nil, err
	}
	token, err := app.speechTokenSource.Token()
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token.AccessToken)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := app.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	if response.StatusCode/100 != 2 {
		return nil, fmt.Errorf("speech API status %d", response.StatusCode)
	}
	return raw, nil
}

func (app *application) transcribeGCS(ctx context.Context, objectName string, limit time.Duration) ([]captionSegment, error) {
	deadline := time.Now().Add(limit)
	recognizer := fmt.Sprintf("projects/%s/locations/global/recognizers/_", app.cfg.ProjectID)
	body := map[string]any{"config": map[string]any{"autoDecodingConfig": map[string]any{}, "languageCodes": []string{"en-US"}, "model": "long", "features": map[string]any{"enableAutomaticPunctuation": true, "enableWordTimeOffsets": true}}, "files": []map[string]string{{"uri": "gs://" + app.cfg.Bucket + "/" + objectName}}, "recognitionOutputConfig": map[string]any{"inlineResponseConfig": map[string]any{}}}
	raw, err := app.speechRequest(ctx, "POST", "https://speech.googleapis.com/v2/"+recognizer+":batchRecognize", body)
	if err != nil {
		return nil, err
	}
	var operation struct {
		Name string `json:"name"`
	}
	if err = json.Unmarshal(raw, &operation); err != nil || operation.Name == "" {
		return nil, errors.New("speech operation missing")
	}
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(captionPollInterval):
		}
		if time.Now().After(deadline) {
			return nil, errCaptionDeadline
		}
		// Speech returns the numeric project number even when the request used the
		// project ID. Keep the path constrained to a global Speech operation.
		if !speechOperationName.MatchString(operation.Name) {
			return nil, errors.New("unexpected speech operation name")
		}
		raw, err = app.speechRequest(ctx, "GET", "https://speech.googleapis.com/v2/"+operation.Name, nil)
		if err != nil {
			return nil, err
		}
		var status captionOperation
		if err = json.Unmarshal(raw, &status); err != nil {
			return nil, err
		}
		if !status.Done {
			continue
		}
		if len(status.Error) > 0 {
			return nil, fmt.Errorf("speech operation failed")
		}
		for _, file := range status.Response.Results {
			if len(file.Error) > 0 {
				return nil, errors.New("speech file failed")
			}
		}
		return captionSegments(status), nil
	}
}

func captionSegments(status captionOperation) []captionSegment {
	segments := []captionSegment{}
	var cursor int64
	for _, file := range status.Response.Results {
		results := file.Transcript.Results
		if len(results) == 0 {
			results = file.InlineResult.Transcript.Results
		}
		for _, result := range results {
			if len(result.Alternatives) == 0 {
				continue
			}
			alt := result.Alternatives[0]
			text := strings.TrimSpace(alt.Transcript)
			if text == "" {
				continue
			}
			start := cursor
			end := parseSpeechDuration(result.ResultEnd)
			if len(alt.Words) > 0 {
				start = parseSpeechDuration(alt.Words[0].Start)
				end = parseSpeechDuration(alt.Words[len(alt.Words)-1].End)
			}
			if end <= start {
				end = start + 500
			}
			cursor = end
			segments = append(segments, captionSegment{StartMS: start, EndMS: end, Text: text, Confidence: alt.Confidence})
		}
	}
	return segments
}

func parseSpeechDuration(value string) int64 {
	value = strings.TrimSuffix(value, "s")
	if value == "" {
		return 0
	}
	var seconds float64
	_, _ = fmt.Sscanf(value, "%f", &seconds)
	return int64(seconds*1000 + .5)
}
