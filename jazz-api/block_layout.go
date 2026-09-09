package main

import (
	"errors"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type blockLayoutRequest struct {
	PracticeDate string      `json:"practiceDate"`
	BlockIDs     []uuid.UUID `json:"blockIds"`
	RemoveID     *uuid.UUID  `json:"removeId,omitempty"`
	RemoveIDs    []uuid.UUID `json:"removeIds,omitempty"`
}

// A layout lists every remaining block exactly once. Removal is explicit so a
// stale client cannot silently delete sections added on another device.
func (input blockLayoutRequest) removals() []uuid.UUID {
	ids := append([]uuid.UUID{}, input.RemoveIDs...)
	if input.RemoveID != nil {
		ids = append(ids, *input.RemoveID)
	}
	return ids
}

func validBlockLayout(input blockLayoutRequest, current []uuid.UUID) bool {
	seen := make(map[uuid.UUID]bool)
	for _, id := range input.BlockIDs {
		if seen[id] || id == uuid.Nil {
			return false
		}
		seen[id] = true
	}
	for _, id := range input.removals() {
		if id == uuid.Nil || seen[id] {
			return false
		}
		seen[id] = true
	}
	if len(seen) != len(current) {
		return false
	}
	for _, id := range current {
		if !seen[id] {
			return false
		}
	}
	return true
}

func (app *application) updatePracticeBlockLayout(w http.ResponseWriter, r *http.Request) {
	sessionID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, 400, "invalid practice session id")
		return
	}
	var input blockLayoutRequest
	if err := readJSON(w, r, &input); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	if !datePattern.MatchString(input.PracticeDate) || input.BlockIDs == nil || len(input.BlockIDs) > 100 || len(input.RemoveIDs) > 100 {
		writeError(w, 422, "practice layout is invalid")
		return
	}
	userID, err := app.userID(r.Context())
	if err != nil {
		app.serverError(w, err)
		return
	}
	tx, err := app.db.Begin(r.Context())
	if err != nil {
		app.serverError(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	var owner uuid.UUID
	err = tx.QueryRow(r.Context(), `SELECT id FROM practice_sessions WHERE id=$1 AND user_id=$2 FOR UPDATE`, sessionID, userID).Scan(&owner)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, 404, "practice session not found")
		return
	}
	if err != nil {
		app.serverError(w, err)
		return
	}
	rows, err := tx.Query(r.Context(), `SELECT id FROM practice_blocks WHERE session_id=$1 AND user_id=$2 AND practice_date=$3 AND removed_at IS NULL FOR UPDATE`, sessionID, userID, input.PracticeDate)
	if err != nil {
		app.serverError(w, err)
		return
	}
	current := []uuid.UUID{}
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			app.serverError(w, err)
			return
		}
		current = append(current, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		app.serverError(w, err)
		return
	}
	if !validBlockLayout(input, current) {
		writeError(w, 409, "The practice plan changed. Refresh and try again.")
		return
	}
	// Block rows are already locked. Recording initialization takes the same
	// row lock before inserting an upload, so deletion cannot race that insert.
	for _, id := range input.removals() {
		var busy bool
		if err := tx.QueryRow(r.Context(), `SELECT (status='running' AND timer_started_at > now()-interval '90 seconds') OR EXISTS(SELECT 1 FROM recordings WHERE practice_block_id=$1 AND status='uploading') FROM practice_blocks WHERE id=$1 AND user_id=$2`, id, userID).Scan(&busy); err != nil {
			app.serverError(w, err)
			return
		}
		if busy {
			writeError(w, http.StatusConflict, "A section is recording or uploading. Finish the take or upload before deleting it.")
			return
		}
	}
	for position, id := range input.BlockIDs {
		if _, err := tx.Exec(r.Context(), `UPDATE practice_blocks SET position=$1,updated_at=now() WHERE id=$2 AND user_id=$3`, position, id, userID); err != nil {
			app.serverError(w, err)
			return
		}
	}
	for _, removeID := range input.removals() {
		// Keep notes, practice history and recording associations intact.
		if _, err := tx.Exec(r.Context(), `UPDATE practice_blocks SET removed_at=now(),timer_started_at=NULL,status=CASE WHEN status='running' THEN 'paused' ELSE status END,updated_at=now() WHERE id=$1 AND user_id=$2`, removeID, userID); err != nil {
			app.serverError(w, err)
			return
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		app.serverError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
