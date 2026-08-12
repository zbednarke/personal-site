package main

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type practiceBlock struct {
	ID             uuid.UUID               `json:"id"`
	SessionID      uuid.UUID               `json:"practiceSessionId"`
	PracticeDate   string                  `json:"practiceDate"`
	BlockKey       string                  `json:"blockKey"`
	Position       int                     `json:"position"`
	Title          string                  `json:"title"`
	Instructions   string                  `json:"instructions"`
	Category       string                  `json:"category"`
	Track          string                  `json:"track"`
	TargetMinutes  int                     `json:"targetMinutes"`
	Notes          string                  `json:"notes"`
	ElapsedMS      int                     `json:"elapsedMs"`
	Status         string                  `json:"status"`
	TimerStartedAt *time.Time              `json:"timerStartedAt,omitempty"`
	CompletedAt    *time.Time              `json:"completedAt,omitempty"`
	UpdatedAt      time.Time               `json:"updatedAt"`
	Recordings     []blockRecordingSummary `json:"recordings"`
}

type blockRecordingSummary struct {
	ID               uuid.UUID `json:"id"`
	Status           string    `json:"status"`
	ContentType      string    `json:"contentType"`
	DurationMS       int       `json:"durationMs"`
	RecordedAt       time.Time `json:"recordedAt"`
	TakeNumber       int       `json:"takeNumber"`
	Notes            string    `json:"notes"`
	PracticeBlockID  uuid.UUID `json:"practiceBlockId"`
	MediaKind        string    `json:"mediaKind"`
	VideoContentType string    `json:"videoContentType,omitempty"`
	VideoWidth       int       `json:"videoWidth,omitempty"`
	VideoHeight      int       `json:"videoHeight,omitempty"`
}

type blockDefinition struct {
	BlockKey      string `json:"blockKey"`
	Position      int    `json:"position"`
	Title         string `json:"title"`
	Instructions  string `json:"instructions"`
	Category      string `json:"category"`
	Track         string `json:"track"`
	TargetMinutes int    `json:"targetMinutes"`
}

type bootstrapBlocksRequest struct {
	PracticeDate string            `json:"practiceDate"`
	Blocks       []blockDefinition `json:"blocks"`
}

type updateBlockRequest struct {
	Notes          *string `json:"notes"`
	ElapsedMS      *int    `json:"elapsedMs"`
	Status         *string `json:"status"`
	TimerStartedAt *string `json:"timerStartedAt"`
	CompletedAt    *string `json:"completedAt"`
}

const legacyCombinedFundamentalsKey = "articulation-flexibility"

func (app *application) bootstrapPracticeBlocks(w http.ResponseWriter, r *http.Request) {
	sessionID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid practice session id")
		return
	}
	var input bootstrapBlocksRequest
	if err := readJSON(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	filteredBlocks := make([]blockDefinition, 0, len(input.Blocks))
	for _, block := range input.Blocks {
		if strings.TrimSpace(block.BlockKey) == legacyCombinedFundamentalsKey {
			continue
		}
		filteredBlocks = append(filteredBlocks, block)
	}
	input.Blocks = filteredBlocks
	if len(input.Blocks) < 1 || len(input.Blocks) > 20 {
		writeError(w, http.StatusUnprocessableEntity, "practice blocks are invalid")
		return
	}
	if !datePattern.MatchString(input.PracticeDate) {
		writeError(w, http.StatusUnprocessableEntity, "practice date is invalid")
		return
	}
	seen := make(map[string]bool, len(input.Blocks))
	for index := range input.Blocks {
		block := &input.Blocks[index]
		block.BlockKey = strings.TrimSpace(block.BlockKey)
		block.Title = strings.TrimSpace(block.Title)
		block.Instructions = strings.TrimSpace(block.Instructions)
		block.Category = strings.TrimSpace(block.Category)
		block.Track = strings.TrimSpace(block.Track)
		if !validBlockKey(block.BlockKey) || seen[block.BlockKey] || block.Position < 0 || block.Position > 99 ||
			len(block.Title) < 1 || len(block.Title) > 160 || len(block.Instructions) > 2000 ||
			len(block.Category) < 1 || len(block.Category) > 40 || len(block.Track) < 1 || len(block.Track) > 30 ||
			block.TargetMinutes < 1 || block.TargetMinutes > 360 {
			writeError(w, http.StatusUnprocessableEntity, "practice blocks are invalid")
			return
		}
		seen[block.BlockKey] = true
	}
	userID, err := app.userID(r.Context())
	if err != nil {
		app.serverError(w, err)
		return
	}
	var active bool
	if err := app.db.QueryRow(r.Context(), `SELECT status='active' FROM practice_sessions WHERE id=$1 AND user_id=$2`, sessionID, userID).Scan(&active); errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "practice session not found")
		return
	} else if err != nil {
		app.serverError(w, err)
		return
	} else if !active {
		writeError(w, http.StatusConflict, "practice session is already completed")
		return
	}

	tx, err := app.db.Begin(r.Context())
	if err != nil {
		app.serverError(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	for _, block := range input.Blocks {
		_, err = tx.Exec(r.Context(), `
			INSERT INTO practice_blocks
			(id,session_id,user_id,practice_date,block_key,position,title,instructions,category,track,target_minutes)
			VALUES ($1,$2,$3,$4,$5,$6,$7,NULLIF($8,''),$9,$10,$11)
			ON CONFLICT (session_id,practice_date,block_key) DO UPDATE SET
			position=EXCLUDED.position,title=EXCLUDED.title,instructions=EXCLUDED.instructions,
			category=EXCLUDED.category,track=EXCLUDED.track,target_minutes=EXCLUDED.target_minutes,updated_at=now()`,
			uuid.New(), sessionID, userID, input.PracticeDate, block.BlockKey, block.Position, block.Title, block.Instructions, block.Category, block.Track, block.TargetMinutes)
		if err != nil {
			app.serverError(w, err)
			return
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		app.serverError(w, err)
		return
	}
	blocks, err := app.loadPracticeBlocks(r.Context(), userID, sessionID, input.PracticeDate)
	if err != nil {
		app.serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"blocks": blocks})
}

func (app *application) listPracticeBlocks(w http.ResponseWriter, r *http.Request) {
	sessionID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid practice session id")
		return
	}
	userID, err := app.userID(r.Context())
	if err != nil {
		app.serverError(w, err)
		return
	}
	practiceDate := r.URL.Query().Get("date")
	if !datePattern.MatchString(practiceDate) {
		writeError(w, http.StatusUnprocessableEntity, "practice date is invalid")
		return
	}
	blocks, err := app.loadPracticeBlocks(r.Context(), userID, sessionID, practiceDate)
	if err != nil {
		app.serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"blocks": blocks})
}

func (app *application) updatePracticeBlock(w http.ResponseWriter, r *http.Request) {
	blockID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid practice block id")
		return
	}
	var input updateBlockRequest
	if err := readJSON(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	userID, err := app.userID(r.Context())
	if err != nil {
		app.serverError(w, err)
		return
	}
	var block practiceBlock
	err = app.db.QueryRow(r.Context(), `
		SELECT id,session_id,practice_date::text,block_key,position,title,COALESCE(instructions,''),category,track,target_minutes,
		       COALESCE(notes,''),elapsed_ms,status,timer_started_at,completed_at,updated_at
		FROM practice_blocks WHERE id=$1 AND user_id=$2`, blockID, userID).
		Scan(&block.ID, &block.SessionID, &block.PracticeDate, &block.BlockKey, &block.Position, &block.Title, &block.Instructions, &block.Category,
			&block.Track, &block.TargetMinutes, &block.Notes, &block.ElapsedMS, &block.Status, &block.TimerStartedAt, &block.CompletedAt, &block.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "practice block not found")
		return
	}
	if err != nil {
		app.serverError(w, err)
		return
	}
	if input.Notes != nil {
		if len(*input.Notes) > 4000 {
			writeError(w, http.StatusUnprocessableEntity, "practice block notes are too long")
			return
		}
		block.Notes = *input.Notes
	}
	if input.ElapsedMS != nil {
		if *input.ElapsedMS < 0 || *input.ElapsedMS > 21600000 {
			writeError(w, http.StatusUnprocessableEntity, "practice timer is invalid")
			return
		}
		block.ElapsedMS = *input.ElapsedMS
	}
	if input.Status != nil {
		status := strings.TrimSpace(*input.Status)
		if status != "pending" && status != "running" && status != "paused" && status != "completed" {
			writeError(w, http.StatusUnprocessableEntity, "practice block status is invalid")
			return
		}
		block.Status = status
	}
	if input.TimerStartedAt != nil {
		block.TimerStartedAt = nil
		if *input.TimerStartedAt != "" {
			parsed, parseErr := time.Parse(time.RFC3339, *input.TimerStartedAt)
			if parseErr != nil {
				writeError(w, http.StatusUnprocessableEntity, "practice timer start is invalid")
				return
			}
			block.TimerStartedAt = &parsed
		}
	}
	if block.Status == "running" && block.TimerStartedAt == nil {
		now := time.Now()
		block.TimerStartedAt = &now
	}
	if block.Status != "running" {
		block.TimerStartedAt = nil
	}
	if input.CompletedAt != nil {
		block.CompletedAt = nil
		if *input.CompletedAt != "" {
			parsed, parseErr := time.Parse(time.RFC3339, *input.CompletedAt)
			if parseErr != nil {
				writeError(w, http.StatusUnprocessableEntity, "practice completion time is invalid")
				return
			}
			block.CompletedAt = &parsed
		}
	}
	if block.Status == "completed" && block.CompletedAt == nil {
		now := time.Now()
		block.CompletedAt = &now
	}
	if block.Status != "completed" {
		block.CompletedAt = nil
	}

	err = app.db.QueryRow(r.Context(), `
		UPDATE practice_blocks SET notes=NULLIF($1,''),elapsed_ms=$2,status=$3,timer_started_at=$4,completed_at=$5,updated_at=now()
		WHERE id=$6 AND user_id=$7
		RETURNING updated_at`, block.Notes, block.ElapsedMS, block.Status, block.TimerStartedAt, block.CompletedAt, block.ID, userID).Scan(&block.UpdatedAt)
	if err != nil {
		app.serverError(w, err)
		return
	}
	block.Recordings, err = app.loadBlockRecordings(r.Context(), userID, block.ID)
	if err != nil {
		app.serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, block)
}

func (app *application) loadPracticeBlocks(ctx context.Context, userID, sessionID uuid.UUID, practiceDate string) ([]practiceBlock, error) {
	var exists bool
	if err := app.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM practice_sessions WHERE id=$1 AND user_id=$2)`, sessionID, userID).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return []practiceBlock{}, nil
	}
	rows, err := app.db.Query(ctx, `
		SELECT id,session_id,practice_date::text,block_key,position,title,COALESCE(instructions,''),category,track,target_minutes,
		       COALESCE(notes,''),elapsed_ms,status,timer_started_at,completed_at,updated_at
		FROM practice_blocks WHERE session_id=$1 AND user_id=$2 AND practice_date=$3 ORDER BY position,id`, sessionID, userID, practiceDate)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	blocks := make([]practiceBlock, 0)
	for rows.Next() {
		var block practiceBlock
		if err := rows.Scan(&block.ID, &block.SessionID, &block.PracticeDate, &block.BlockKey, &block.Position, &block.Title, &block.Instructions, &block.Category,
			&block.Track, &block.TargetMinutes, &block.Notes, &block.ElapsedMS, &block.Status, &block.TimerStartedAt, &block.CompletedAt, &block.UpdatedAt); err != nil {
			return nil, err
		}
		blocks = append(blocks, block)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	for index := range blocks {
		blocks[index].Recordings, err = app.loadBlockRecordings(ctx, userID, blocks[index].ID)
		if err != nil {
			return nil, err
		}
	}
	return blocks, nil
}

func (app *application) loadBlockRecordings(ctx context.Context, userID, blockID uuid.UUID) ([]blockRecordingSummary, error) {
	rows, err := app.db.Query(ctx, `
		SELECT id,status,content_type,COALESCE(duration_ms,0),recorded_at,COALESCE(take_number,0),COALESCE(notes,''),practice_block_id,
		       COALESCE(media_kind,'audio'),COALESCE(video_content_type,''),COALESCE(video_width,0),COALESCE(video_height,0)
		FROM recordings WHERE user_id=$1 AND practice_block_id=$2 AND status <> 'deleted' ORDER BY recorded_at,id`, userID, blockID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	recordings := make([]blockRecordingSummary, 0)
	for rows.Next() {
		var recording blockRecordingSummary
		if err := rows.Scan(&recording.ID, &recording.Status, &recording.ContentType, &recording.DurationMS, &recording.RecordedAt,
			&recording.TakeNumber, &recording.Notes, &recording.PracticeBlockID, &recording.MediaKind, &recording.VideoContentType,
			&recording.VideoWidth, &recording.VideoHeight); err != nil {
			return nil, err
		}
		recordings = append(recordings, recording)
	}
	return recordings, rows.Err()
}

func validBlockKey(value string) bool {
	if len(value) < 1 || len(value) > 80 {
		return false
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '-' {
			return false
		}
	}
	return true
}
