package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const (
	longToneChunkDurationMS = int64(60_000)
	longToneMaxRangeMS      = int64(120_000)
	longToneMaxAttempts     = 3
	longToneLeaseDuration   = 90 * time.Second
	longToneDiagnosticBytes = 32 << 10
)

type longToneAnalysisRequest struct {
	Asset string `json:"asset"`
}

type longToneAnalysisResponse struct {
	AnalysisID      *uuid.UUID      `json:"analysisId,omitempty"`
	Status          string          `json:"status"`
	Asset           string          `json:"asset"`
	Version         string          `json:"version"`
	DurationMS      int64           `json:"durationMs"`
	ChunkDurationMS int64           `json:"chunkDurationMs"`
	Error           string          `json:"error,omitempty"`
	Retryable       *bool           `json:"retryable,omitempty"`
	Frames          []longToneFrame `json:"frames,omitempty"`
}

type longToneAsset struct {
	RecordingID uuid.UUID
	UserID      uuid.UUID
	Asset       string
	ObjectName  string
	Generation  int64
	Checksum    string
	DurationMS  int64
}

type longToneJob struct {
	ID         uuid.UUID
	Asset      string
	ObjectName string
	Generation int64
	Attempts   int
	LeaseToken uuid.UUID
}

type longToneDecoder func(context.Context, string, string, func(io.Reader) (int64, error)) (int64, error)
type longToneURLSigner func(context.Context, string, int64, time.Time) (string, error)

func (app *application) createLongToneAnalysis(w http.ResponseWriter, r *http.Request) {
	var input longToneAnalysisRequest
	if err := readJSON(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	asset, err := normalizeLongToneAsset(input.Asset)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
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
	source, status, err := app.loadLongToneAsset(r.Context(), userID, recordingID, asset)
	if err != nil {
		app.serverError(w, err)
		return
	}
	if status != 0 {
		writeError(w, status, longToneAssetError(status))
		return
	}
	jobID := uuid.New()
	var response longToneAnalysisResponse
	tx, err := app.db.Begin(r.Context())
	if err != nil {
		app.serverError(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	command, err := tx.Exec(r.Context(), `
		INSERT INTO long_tone_analysis_jobs
			(id,recording_id,user_id,asset,source_object_name,source_generation,source_checksum,analysis_version,status,duration_ms)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,'pending',$9)
		ON CONFLICT (recording_id,asset,source_object_name,source_generation,source_checksum,analysis_version)
		DO NOTHING`,
		jobID, recordingID, userID, asset, source.ObjectName, source.Generation, source.Checksum,
		longToneAnalysisVersion, source.DurationMS)
	if err != nil {
		app.serverError(w, err)
		return
	}
	if command.RowsAffected() == 0 {
		err = tx.QueryRow(r.Context(), `SELECT id,status FROM long_tone_analysis_jobs
			WHERE recording_id=$1 AND asset=$2 AND source_object_name=$3 AND source_generation=$4
			  AND source_checksum=$5 AND analysis_version=$6 FOR UPDATE`, recordingID, asset,
			source.ObjectName, source.Generation, source.Checksum, longToneAnalysisVersion).Scan(&jobID, &response.Status)
		if err != nil {
			app.serverError(w, err)
			return
		}
		if response.Status == "failed" {
			var retryable bool
			if err = tx.QueryRow(r.Context(), `SELECT retryable FROM long_tone_analysis_jobs WHERE id=$1`, jobID).Scan(&retryable); err != nil {
				app.serverError(w, err)
				return
			}
			if retryable {
				if _, err = tx.Exec(r.Context(), `DELETE FROM long_tone_analysis_chunks WHERE analysis_id=$1`, jobID); err != nil {
					app.serverError(w, err)
					return
				}
				if _, err = tx.Exec(r.Context(), `UPDATE long_tone_analysis_jobs SET status='pending',attempts=0,
					available_at=now(),lease_token=NULL,leased_until=NULL,error_code=NULL,retryable=false,
					completed_at=NULL,updated_at=now() WHERE id=$1`, jobID); err != nil {
					app.serverError(w, err)
					return
				}
			}
		}
	}
	err = tx.QueryRow(r.Context(), `SELECT status,duration_ms,COALESCE(error_code,''),retryable
		FROM long_tone_analysis_jobs WHERE id=$1`, jobID).
		Scan(&response.Status, &response.DurationMS, &response.Error, &response.Retryable)
	if err != nil {
		app.serverError(w, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		app.serverError(w, err)
		return
	}
	response.AnalysisID = &jobID
	response.Asset = asset
	response.Version = longToneAnalysisVersion
	response.ChunkDurationMS = longToneChunkDurationMS
	code := http.StatusAccepted
	if response.Status == "ready" || response.Status == "failed" {
		code = http.StatusOK
	}
	writeJSON(w, code, response)
}

func (app *application) getLongToneAnalysis(w http.ResponseWriter, r *http.Request) {
	asset, err := normalizeLongToneAsset(r.URL.Query().Get("asset"))
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
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
	source, status, err := app.loadLongToneAsset(r.Context(), userID, recordingID, asset)
	if err != nil {
		app.serverError(w, err)
		return
	}
	if status != 0 {
		writeError(w, status, longToneAssetError(status))
		return
	}
	response := longToneAnalysisResponse{Status: "missing", Asset: asset, Version: longToneAnalysisVersion, DurationMS: source.DurationMS, ChunkDurationMS: longToneChunkDurationMS}
	err = app.db.QueryRow(r.Context(), `
		SELECT id,status,duration_ms,COALESCE(error_code,''),retryable
		FROM long_tone_analysis_jobs
		WHERE recording_id=$1 AND user_id=$2 AND asset=$3
		  AND source_object_name=$4 AND source_generation=$5
		  AND source_checksum=$6 AND analysis_version=$7`, recordingID, userID, asset,
		source.ObjectName, source.Generation, source.Checksum, longToneAnalysisVersion).
		Scan(&response.AnalysisID, &response.Status, &response.DurationMS, &response.Error, &response.Retryable)
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusOK, response)
		return
	}
	if err != nil {
		app.serverError(w, err)
		return
	}
	startText, endText := r.URL.Query().Get("startMs"), r.URL.Query().Get("endMs")
	if startText != "" || endText != "" {
		if startText == "" || endText == "" {
			writeError(w, http.StatusBadRequest, "startMs and endMs must be provided together")
			return
		}
		startMS, startErr := strconv.ParseInt(startText, 10, 64)
		endMS, endErr := strconv.ParseInt(endText, 10, 64)
		if startErr != nil || endErr != nil || startMS < 0 || endMS < startMS || endMS-startMS > longToneMaxRangeMS {
			writeError(w, http.StatusUnprocessableEntity, "analysis range is invalid or exceeds 120000ms")
			return
		}
		if response.Status == "ready" {
			response.Frames, err = app.loadLongToneFrames(r.Context(), *response.AnalysisID, startMS, endMS)
			if err != nil {
				app.serverError(w, err)
				return
			}
			if response.Frames == nil {
				response.Frames = []longToneFrame{}
			}
		}
	}
	writeJSON(w, http.StatusOK, response)
}

func normalizeLongToneAsset(asset string) (string, error) {
	asset = strings.ToLower(strings.TrimSpace(asset))
	if asset != "audio" && asset != "video" {
		return "", errors.New("asset must be audio or video")
	}
	return asset, nil
}

func longToneAssetError(status int) string {
	if status == http.StatusConflict {
		return "recording asset is not ready"
	}
	if status == http.StatusUnprocessableEntity {
		return "recording has no requested asset"
	}
	return "recording not found"
}

func (app *application) loadLongToneAsset(ctx context.Context, userID, recordingID uuid.UUID, asset string) (longToneAsset, int, error) {
	var source longToneAsset
	var recordingStatus, mediaKind, audioObject, audioChecksum, videoObject, videoChecksum string
	var audioGeneration, videoGeneration *int64
	var audioUploaded, videoUploaded bool
	err := app.db.QueryRow(ctx, `
		SELECT status,media_kind,object_name,object_generation,COALESCE(checksum,''),uploaded_at IS NOT NULL,
		       COALESCE(video_object_name,''),video_object_generation,COALESCE(video_checksum,''),video_uploaded_at IS NOT NULL,
		       COALESCE(duration_ms,0)
		FROM recordings WHERE id=$1 AND user_id=$2`, recordingID, userID).
		Scan(&recordingStatus, &mediaKind, &audioObject, &audioGeneration, &audioChecksum, &audioUploaded,
			&videoObject, &videoGeneration, &videoChecksum, &videoUploaded, &source.DurationMS)
	if errors.Is(err, pgx.ErrNoRows) {
		return source, http.StatusNotFound, nil
	}
	if err != nil {
		return source, 0, err
	}
	if recordingStatus == "deleted" {
		return source, http.StatusNotFound, nil
	}
	if asset == "video" {
		if mediaKind != "video" || videoObject == "" {
			return source, http.StatusUnprocessableEntity, nil
		}
		source.ObjectName, source.Checksum, source.Generation = videoObject, videoChecksum, pointerInt64(videoGeneration)
		if !videoUploaded || videoGeneration == nil || videoChecksum == "" {
			return source, http.StatusConflict, nil
		}
	} else {
		source.ObjectName, source.Checksum, source.Generation = audioObject, audioChecksum, pointerInt64(audioGeneration)
		if !audioUploaded || audioGeneration == nil || audioChecksum == "" {
			return source, http.StatusConflict, nil
		}
	}
	if recordingStatus != "ready" {
		return source, http.StatusConflict, nil
	}
	source.RecordingID, source.UserID, source.Asset = recordingID, userID, asset
	return source, 0, nil
}

func pointerInt64(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

func (app *application) loadLongToneFrames(ctx context.Context, analysisID uuid.UUID, startMS, endMS int64) ([]longToneFrame, error) {
	rows, err := app.db.Query(ctx, `
		SELECT frames FROM long_tone_analysis_chunks
		WHERE analysis_id=$1 AND chunk_end_ms >= $2 AND chunk_start_ms <= $3
		ORDER BY chunk_start_ms`, analysisID, max64(0, startMS-longToneChunkDurationMS), endMS+longToneChunkDurationMS)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var all []longToneFrame
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var frames []longToneFrame
		if err := json.Unmarshal(raw, &frames); err != nil {
			return nil, err
		}
		all = append(all, frames...)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var before *longToneFrame
	result := make([]longToneFrame, 0, len(all))
	for i := range all {
		frame := all[i]
		if frame.TimeMS < startMS {
			copy := frame
			before = &copy
			continue
		}
		if frame.TimeMS <= endMS {
			if before != nil {
				result = append(result, *before)
				before = nil
			}
			result = append(result, frame)
			continue
		}
		if before != nil {
			result = append(result, *before)
			before = nil
		}
		result = append(result, frame)
		break
	}
	if len(result) == 0 && before != nil {
		result = append(result, *before)
	}
	return result, nil
}

func (app *application) runLongToneWorkers(ctx context.Context, count int) *sync.WaitGroup {
	var workers sync.WaitGroup
	for i := 0; i < count; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			app.longToneWorker(ctx)
		}()
	}
	return &workers
}

func (app *application) longToneWorker(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		job, err := app.claimLongToneJob(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			app.logger.Error("long-tone job claim failed", "error", err)
		} else if job != nil {
			app.processLongToneJob(ctx, *job)
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (app *application) claimLongToneJob(ctx context.Context) (*longToneJob, error) {
	tx, err := app.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `UPDATE long_tone_analysis_jobs j
		SET status='failed',error_code='source_changed',retryable=false,lease_token=NULL,leased_until=NULL,updated_at=now()
		WHERE status IN ('pending','running') AND NOT EXISTS (
			SELECT 1 FROM recordings r WHERE r.id=j.recording_id AND r.status='ready' AND
			CASE WHEN j.asset='video' THEN
				r.video_uploaded_at IS NOT NULL AND r.video_object_name=j.source_object_name AND
				r.video_object_generation=j.source_generation AND r.video_checksum=j.source_checksum
			ELSE r.uploaded_at IS NOT NULL AND r.object_name=j.source_object_name AND
				r.object_generation=j.source_generation AND r.checksum=j.source_checksum END
		)`); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `UPDATE long_tone_analysis_jobs
		SET status='failed',error_code='worker_lost',retryable=true,lease_token=NULL,leased_until=NULL,updated_at=now()
		WHERE status='running' AND leased_until < now() AND attempts >= $1`, longToneMaxAttempts); err != nil {
		return nil, err
	}
	var job longToneJob
	err = tx.QueryRow(ctx, `
		SELECT id,asset,source_object_name,source_generation,attempts
		FROM long_tone_analysis_jobs
		WHERE attempts < $1 AND available_at <= now()
		  AND (status='pending' OR (status='running' AND leased_until < now()))
		  AND EXISTS (SELECT 1 FROM recordings r WHERE r.id=long_tone_analysis_jobs.recording_id AND r.status='ready' AND
			CASE WHEN long_tone_analysis_jobs.asset='video' THEN
				r.video_uploaded_at IS NOT NULL AND r.video_object_name=long_tone_analysis_jobs.source_object_name AND
				r.video_object_generation=long_tone_analysis_jobs.source_generation AND r.video_checksum=long_tone_analysis_jobs.source_checksum
			ELSE r.uploaded_at IS NOT NULL AND r.object_name=long_tone_analysis_jobs.source_object_name AND
				r.object_generation=long_tone_analysis_jobs.source_generation AND r.checksum=long_tone_analysis_jobs.source_checksum END)
		ORDER BY created_at FOR UPDATE SKIP LOCKED LIMIT 1`, longToneMaxAttempts).
		Scan(&job.ID, &job.Asset, &job.ObjectName, &job.Generation, &job.Attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		// Keep the sweeps above even when there is nothing to claim.
		return nil, tx.Commit(ctx)
	}
	if err != nil {
		return nil, err
	}
	job.LeaseToken = uuid.New()
	job.Attempts++
	if _, err = tx.Exec(ctx, `DELETE FROM long_tone_analysis_chunks WHERE analysis_id=$1`, job.ID); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `UPDATE long_tone_analysis_jobs SET status='running',attempts=$2,lease_token=$3,
		leased_until=now()+$4::interval,error_code=NULL,retryable=false,updated_at=now() WHERE id=$1`,
		job.ID, job.Attempts, job.LeaseToken, longToneLeaseDuration.String()); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &job, nil
}

func (app *application) processLongToneJob(workerCtx context.Context, job longToneJob) {
	ctx, cancel := context.WithCancel(workerCtx)
	defer cancel()
	lost := make(chan struct{})
	go app.heartbeatLongToneJob(ctx, job, lost, cancel)
	signer := app.longToneSigner
	if signer == nil {
		signer = app.signLongToneObjectURL
	}
	sourceURL, err := signer(ctx, job.ObjectName, job.Generation, time.Now().Add(5*time.Hour))
	if err != nil {
		app.finishLongToneFailure(workerCtx, job, "source_unavailable", true)
		return
	}
	decoder := app.longToneDecoder
	if decoder == nil {
		decoder = decodeLongToneWithFFmpeg
	}
	var chunkStart int64 = -1
	var chunkFrames []longToneFrame
	flush := func() error {
		if len(chunkFrames) == 0 {
			return nil
		}
		raw, err := json.Marshal(chunkFrames)
		if err != nil {
			return err
		}
		end := chunkFrames[len(chunkFrames)-1].TimeMS
		tx, err := app.db.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		var valid int
		err = tx.QueryRow(ctx, `SELECT 1 FROM long_tone_analysis_jobs j
			WHERE j.id=$1 AND j.lease_token=$2 AND j.status='running' AND j.leased_until > now()
			AND EXISTS (SELECT 1 FROM recordings r WHERE r.id=j.recording_id AND r.status='ready' AND
				CASE WHEN j.asset='video' THEN r.video_uploaded_at IS NOT NULL AND r.video_object_name=j.source_object_name AND
					r.video_object_generation=j.source_generation AND r.video_checksum=j.source_checksum
				ELSE r.uploaded_at IS NOT NULL AND r.object_name=j.source_object_name AND
					r.object_generation=j.source_generation AND r.checksum=j.source_checksum END)
			FOR UPDATE`, job.ID, job.LeaseToken).Scan(&valid)
		if errors.Is(err, pgx.ErrNoRows) {
			return errors.New("long-tone lease lost")
		}
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO long_tone_analysis_chunks(analysis_id,chunk_start_ms,chunk_end_ms,frames)
			VALUES($1,$2,$3,$4) ON CONFLICT (analysis_id,chunk_start_ms)
			DO UPDATE SET chunk_end_ms=EXCLUDED.chunk_end_ms,frames=EXCLUDED.frames`, job.ID, chunkStart, end, raw); err != nil {
			return err
		}
		if err = tx.Commit(ctx); err != nil {
			return err
		}
		chunkFrames = nil
		return nil
	}
	analyze := func(reader io.Reader) (int64, error) {
		return analyzeLongTonePCM(ctx, reader, func(frames []longToneFrame) error {
			for _, frame := range frames {
				start := (frame.TimeMS / longToneChunkDurationMS) * longToneChunkDurationMS
				if chunkStart >= 0 && start != chunkStart {
					if err := flush(); err != nil {
						return err
					}
				}
				if chunkStart < 0 || start != chunkStart {
					chunkStart = start
				}
				chunkFrames = append(chunkFrames, frame)
			}
			return nil
		})
	}
	duration, err := decoder(ctx, sourceURL, job.Asset, analyze)
	if err == nil {
		err = flush()
	}
	select {
	case <-lost:
		return
	default:
	}
	if err != nil {
		code := "decoder_failed"
		retryable := true
		if errors.Is(err, errLongToneNoAudio) {
			code, retryable = "no_audio", false
		} else if errors.Is(err, errLongToneTooLong) {
			code, retryable = "too_long", false
		} else if errors.Is(err, errLongToneTimelineUnavailable) {
			code, retryable = "timeline_unavailable", false
		}
		if errors.Is(err, context.Canceled) && workerCtx.Err() != nil {
			return
		}
		app.finishLongToneFailure(workerCtx, job, code, retryable)
		return
	}
	_, err = app.db.Exec(workerCtx, `UPDATE long_tone_analysis_jobs j SET status='ready',duration_ms=$3,
		lease_token=NULL,leased_until=NULL,error_code=NULL,retryable=false,completed_at=now(),updated_at=now()
		WHERE id=$1 AND lease_token=$2 AND status='running' AND leased_until > now()
		AND EXISTS (SELECT 1 FROM recordings r WHERE r.id=j.recording_id AND r.status='ready' AND
			CASE WHEN j.asset='video' THEN r.video_uploaded_at IS NOT NULL AND r.video_object_name=j.source_object_name AND
				r.video_object_generation=j.source_generation AND r.video_checksum=j.source_checksum
			ELSE r.uploaded_at IS NOT NULL AND r.object_name=j.source_object_name AND
				r.object_generation=j.source_generation AND r.checksum=j.source_checksum END)`, job.ID, job.LeaseToken, duration)
	if err != nil {
		app.logger.Error("long-tone job completion failed", "analysis_id", job.ID, "error", err)
	}
}

func (app *application) heartbeatLongToneJob(ctx context.Context, job longToneJob, lost chan<- struct{}, cancel context.CancelFunc) {
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			command, err := app.db.Exec(ctx, `UPDATE long_tone_analysis_jobs j SET leased_until=now()+$3::interval,updated_at=now()
				WHERE id=$1 AND lease_token=$2 AND status='running' AND EXISTS (
					SELECT 1 FROM recordings r WHERE r.id=j.recording_id AND r.status='ready' AND
					CASE WHEN j.asset='video' THEN r.video_uploaded_at IS NOT NULL AND r.video_object_name=j.source_object_name AND
						r.video_object_generation=j.source_generation AND r.video_checksum=j.source_checksum
					ELSE r.uploaded_at IS NOT NULL AND r.object_name=j.source_object_name AND
						r.object_generation=j.source_generation AND r.checksum=j.source_checksum END
				)`, job.ID, job.LeaseToken, longToneLeaseDuration.String())
			if err != nil || command.RowsAffected() != 1 {
				close(lost)
				cancel()
				return
			}
		}
	}
}

func (app *application) finishLongToneFailure(ctx context.Context, job longToneJob, code string, retryable bool) {
	status := "failed"
	available := time.Now()
	if retryable && job.Attempts < longToneMaxAttempts {
		status = "pending"
		available = available.Add(time.Duration(job.Attempts*job.Attempts) * 10 * time.Second)
	}
	_, err := app.db.Exec(ctx, `UPDATE long_tone_analysis_jobs SET status=$3,error_code=$4,retryable=$5,
		available_at=$6,lease_token=NULL,leased_until=NULL,updated_at=now()
		WHERE id=$1 AND lease_token=$2 AND status='running'`, job.ID, job.LeaseToken, status, code, retryable, available)
	if err != nil {
		app.logger.Error("long-tone job failure update failed", "analysis_id", job.ID, "error", err)
	}
}

var errLongToneNoAudio = errors.New("media has no audio stream")
var errLongToneTooLong = errors.New("decoded media exceeds maximum duration")
var errLongToneTimelineUnavailable = errors.New("audio presentation duration unavailable")

type longToneProbeResult struct {
	Streams []struct {
		CodecName  string      `json:"codec_name"`
		StartTime  string      `json:"start_time"`
		Duration   string      `json:"duration"`
		DurationTS json.Number `json:"duration_ts"`
		TimeBase   string      `json:"time_base"`
		Tags       struct {
			Duration string `json:"DURATION"`
		} `json:"tags"`
	} `json:"streams"`
	Format struct {
		StartTime  string `json:"start_time"`
		FormatName string `json:"format_name"`
	} `json:"format"`
}

type longToneLimitedBuffer struct {
	data []byte
	max  int
}

func (buffer *longToneLimitedBuffer) Write(contents []byte) (int, error) {
	if len(buffer.data)+len(contents) > buffer.max {
		return 0, errors.New("diagnostic output exceeded limit")
	}
	buffer.data = append(buffer.data, contents...)
	return len(contents), nil
}

type longToneTailBuffer struct {
	data []byte
	max  int
}

func (buffer *longToneTailBuffer) Write(contents []byte) (int, error) {
	written := len(contents)
	if len(contents) >= buffer.max {
		buffer.data = append(buffer.data[:0], contents[len(contents)-buffer.max:]...)
		return written, nil
	}
	overflow := len(buffer.data) + len(contents) - buffer.max
	if overflow > 0 {
		copy(buffer.data, buffer.data[overflow:])
		buffer.data = buffer.data[:len(buffer.data)-overflow]
	}
	buffer.data = append(buffer.data, contents...)
	return written, nil
}

type longToneCappedReader struct {
	reader    io.Reader
	remaining int64
}

func (reader *longToneCappedReader) Read(destination []byte) (int, error) {
	if reader.remaining > 0 {
		if int64(len(destination)) > reader.remaining {
			destination = destination[:reader.remaining]
		}
		n, err := reader.reader.Read(destination)
		reader.remaining -= int64(n)
		return n, err
	}
	var sentinel [1]byte
	n, err := reader.reader.Read(sentinel[:])
	if n > 0 {
		return 0, errLongToneTooLong
	}
	return 0, err
}

func decodeLongToneWithFFmpeg(ctx context.Context, sourceURL, asset string, analyze func(io.Reader) (int64, error)) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Hour+10*time.Minute)
	defer cancel()
	presentationEnd, err := probeLongTonePresentationEnd(ctx, sourceURL)
	if err != nil {
		return 0, err
	}
	if presentationEnd > float64(maxDurationMS)/1000 {
		return 0, errLongToneTooLong
	}
	args := []string{"-nostdin", "-hide_banner", "-loglevel", "error", "-copyts", "-start_at_zero", "-i", sourceURL,
		"-map", "0:a:0", "-vn", "-af", "aresample=16000:async=1:first_pts=0"}
	// MP4/AAC commonly decodes codec padding past the selected stream's
	// presentation end. Live MediaRecorder WebM files may have no duration at
	// all; those safely stream to codec EOF under the hard PCM byte cap below.
	if presentationEnd > 0 {
		args = append(args, "-t", strconv.FormatFloat(presentationEnd, 'f', 9, 64))
	}
	args = append(args, "-ac", "1", "-ar", "16000", "-f", "f32le", "pipe:1")
	command := exec.CommandContext(ctx, envOr("FFMPEG_PATH", "ffmpeg"), args...)
	stdout, err := command.StdoutPipe()
	if err != nil {
		return 0, err
	}
	stderr := longToneTailBuffer{max: longToneDiagnosticBytes}
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		return 0, err
	}
	maxPCMBytes := int64(maxDurationMS) * longToneSampleRate * 4 / 1000
	duration, analyzeErr := analyze(&longToneCappedReader{reader: stdout, remaining: maxPCMBytes})
	if analyzeErr != nil {
		_ = command.Process.Kill()
	}
	waitErr := command.Wait()
	if analyzeErr != nil {
		return 0, analyzeErr
	}
	if waitErr != nil {
		message := strings.ToLower(string(stderr.data))
		if strings.Contains(message, "matches no streams") || strings.Contains(message, "does not contain any stream") {
			return 0, errLongToneNoAudio
		}
		return 0, fmt.Errorf("ffmpeg decode failed")
	}
	if duration > int64(maxDurationMS) {
		return 0, errLongToneTooLong
	}
	return duration, nil
}

func probeLongTonePresentationEnd(ctx context.Context, sourceURL string) (float64, error) {
	ffprobePath := strings.TrimSpace(envOr("FFPROBE_PATH", ""))
	if ffprobePath == "" {
		ffmpegPath := envOr("FFMPEG_PATH", "ffmpeg")
		if filepath.Base(ffmpegPath) == "ffmpeg" && filepath.Dir(ffmpegPath) != "." {
			ffprobePath = filepath.Join(filepath.Dir(ffmpegPath), "ffprobe")
		} else {
			ffprobePath = "ffprobe"
		}
	}
	command := exec.CommandContext(ctx, ffprobePath, "-v", "error", "-select_streams", "a:0",
		"-show_entries", "stream=codec_name,start_time,duration,duration_ts,time_base:stream_tags=DURATION:format=start_time,format_name", "-of", "json", sourceURL)
	stdout := longToneLimitedBuffer{max: longToneDiagnosticBytes}
	stderr := longToneTailBuffer{max: longToneDiagnosticBytes}
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil {
		return 0, errors.New("ffprobe failed")
	}
	var result longToneProbeResult
	if err := json.Unmarshal(stdout.data, &result); err != nil {
		return 0, errors.New("invalid ffprobe response")
	}
	if len(result.Streams) == 0 {
		return 0, errLongToneNoAudio
	}
	stream := result.Streams[0]
	start, err := strconv.ParseFloat(stream.StartTime, 64)
	if err != nil {
		start = 0
	}
	formatStart, err := strconv.ParseFloat(result.Format.StartTime, 64)
	if err != nil {
		formatStart = 0
	}
	duration, durationErr := strconv.ParseFloat(stream.Duration, 64)
	if stream.DurationTS != "" && stream.TimeBase != "" {
		parts := strings.Split(stream.TimeBase, "/")
		durationTS, tsErr := strconv.ParseInt(string(stream.DurationTS), 10, 64)
		numerator, numeratorErr := strconv.ParseInt(parts[0], 10, 64)
		denominator, denominatorErr := strconv.ParseInt(parts[len(parts)-1], 10, 64)
		if tsErr == nil && numeratorErr == nil && denominatorErr == nil && denominator > 0 {
			duration = float64(durationTS) * float64(numerator) / float64(denominator)
			durationErr = nil
		}
	}
	if durationErr != nil && stream.Tags.Duration != "" {
		duration, durationErr = parseLongToneClockDuration(stream.Tags.Duration)
	}
	end := start - formatStart + duration
	if durationErr != nil || duration <= 0 || end <= 0 {
		if longToneAllowsUnknownDuration(stream.CodecName, result.Format.FormatName) {
			return 0, nil
		}
		return 0, errLongToneTimelineUnavailable
	}
	return end, nil
}

func longToneAllowsUnknownDuration(codecName, formatName string) bool {
	return codecName == "opus" && strings.Contains(formatName, "webm")
}

func parseLongToneClockDuration(value string) (float64, error) {
	parts := strings.Split(value, ":")
	if len(parts) != 3 {
		return 0, errors.New("invalid clock duration")
	}
	hours, err := strconv.ParseFloat(parts[0], 64)
	if err != nil {
		return 0, err
	}
	minutes, err := strconv.ParseFloat(parts[1], 64)
	if err != nil {
		return 0, err
	}
	seconds, err := strconv.ParseFloat(parts[2], 64)
	if err != nil {
		return 0, err
	}
	return hours*3600 + minutes*60 + seconds, nil
}

func (app *application) signLongToneObjectURL(ctx context.Context, objectName string, generation int64, expires time.Time) (string, error) {
	return app.signedRecordingObjectURL(ctx, objectName, expires, url.Values{"generation": {strconv.FormatInt(generation, 10)}})
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
