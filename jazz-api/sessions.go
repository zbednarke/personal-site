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

type practiceSession struct {
	ID             uuid.UUID          `json:"id"`
	Title          string             `json:"title"`
	Summary        string             `json:"summary,omitempty"`
	StartedAt      time.Time          `json:"startedAt"`
	EndedAt        *time.Time         `json:"endedAt,omitempty"`
	Status         string             `json:"status"`
	ActivityCount  int                `json:"activityCount"`
	RecordingCount int                `json:"recordingCount"`
	TotalMinutes   int                `json:"totalMinutes"`
	Activities     []practiceActivity `json:"activities,omitempty"`
}

type practiceActivity struct {
	ID              uuid.UUID `json:"id"`
	Category        string    `json:"category"`
	Title           string    `json:"title"`
	DurationMinutes int       `json:"durationMinutes"`
	Notes           string    `json:"notes,omitempty"`
	OccurredAt      time.Time `json:"occurredAt"`
}

type createSessionRequest struct {
	Title     string `json:"title"`
	Summary   string `json:"summary"`
	StartedAt string `json:"startedAt"`
}

type updateSessionRequest struct {
	Title   *string `json:"title"`
	Summary *string `json:"summary"`
	Status  *string `json:"status"`
	EndedAt *string `json:"endedAt"`
}

type createActivityRequest struct {
	Category        string `json:"category"`
	Title           string `json:"title"`
	DurationMinutes int    `json:"durationMinutes"`
	Notes           string `json:"notes"`
	OccurredAt      string `json:"occurredAt"`
}

func (app *application) listPracticeSessions(w http.ResponseWriter, r *http.Request) {
	userID, err := app.userID(r.Context())
	if err != nil {
		app.serverError(w, err)
		return
	}
	rows, err := app.db.Query(r.Context(), `
		SELECT ps.id, ps.title, COALESCE(ps.summary,''), ps.started_at, ps.ended_at, ps.status,
		       COUNT(DISTINCT pa.id)::int, COALESCE(SUM(pa.duration_minutes),0)::int,
		       (SELECT COUNT(*)::int FROM recordings r WHERE r.user_id=ps.user_id AND r.practice_session_id=ps.id::text AND r.status <> 'deleted')
		FROM practice_sessions ps
		LEFT JOIN practice_activities pa ON pa.session_id=ps.id
		WHERE ps.user_id=$1
		GROUP BY ps.id
		ORDER BY (ps.status='active') DESC, ps.started_at DESC
		LIMIT 50`, userID)
	if err != nil {
		app.serverError(w, err)
		return
	}
	defer rows.Close()
	sessions := make([]practiceSession, 0)
	for rows.Next() {
		var session practiceSession
		if err := rows.Scan(&session.ID, &session.Title, &session.Summary, &session.StartedAt, &session.EndedAt, &session.Status,
			&session.ActivityCount, &session.TotalMinutes, &session.RecordingCount); err != nil {
			app.serverError(w, err)
			return
		}
		sessions = append(sessions, session)
	}
	if err := rows.Err(); err != nil {
		app.serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": sessions})
}

func (app *application) createPracticeSession(w http.ResponseWriter, r *http.Request) {
	var input createSessionRequest
	if err := readJSON(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	title := strings.TrimSpace(input.Title)
	summary := strings.TrimSpace(input.Summary)
	if len(title) < 1 || len(title) > 120 || len(summary) > 2000 {
		writeError(w, http.StatusUnprocessableEntity, "session title or notes are invalid")
		return
	}
	startedAt := time.Now()
	if input.StartedAt != "" {
		var err error
		startedAt, err = time.Parse(time.RFC3339, input.StartedAt)
		if err != nil {
			writeError(w, http.StatusUnprocessableEntity, "session start time is invalid")
			return
		}
	}
	userID, err := app.userID(r.Context())
	if err != nil {
		app.serverError(w, err)
		return
	}

	var session practiceSession
	err = app.db.QueryRow(r.Context(), `
		SELECT id,title,COALESCE(summary,''),started_at,ended_at,status
		FROM practice_sessions WHERE user_id=$1 AND status='active'`, userID).
		Scan(&session.ID, &session.Title, &session.Summary, &session.StartedAt, &session.EndedAt, &session.Status)
	if err == nil {
		writeJSON(w, http.StatusOK, session)
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		app.serverError(w, err)
		return
	}

	session.ID = uuid.New()
	err = app.db.QueryRow(r.Context(), `
		INSERT INTO practice_sessions (id,user_id,title,summary,started_at,status)
		VALUES ($1,$2,$3,NULLIF($4,''),$5,'active')
		RETURNING id,title,COALESCE(summary,''),started_at,ended_at,status`,
		session.ID, userID, title, summary, startedAt).
		Scan(&session.ID, &session.Title, &session.Summary, &session.StartedAt, &session.EndedAt, &session.Status)
	if err != nil {
		app.serverError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, session)
}

func (app *application) getPracticeSession(w http.ResponseWriter, r *http.Request) {
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
	var session practiceSession
	err = app.db.QueryRow(r.Context(), `
		SELECT ps.id,ps.title,COALESCE(ps.summary,''),ps.started_at,ps.ended_at,ps.status,
		       (SELECT COUNT(*)::int FROM practice_activities pa WHERE pa.session_id=ps.id),
		       (SELECT COALESCE(SUM(duration_minutes),0)::int FROM practice_activities pa WHERE pa.session_id=ps.id),
		       (SELECT COUNT(*)::int FROM recordings r WHERE r.user_id=ps.user_id AND r.practice_session_id=ps.id::text AND r.status <> 'deleted')
		FROM practice_sessions ps WHERE ps.id=$1 AND ps.user_id=$2`, sessionID, userID).
		Scan(&session.ID, &session.Title, &session.Summary, &session.StartedAt, &session.EndedAt, &session.Status,
			&session.ActivityCount, &session.TotalMinutes, &session.RecordingCount)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "practice session not found")
		return
	}
	if err != nil {
		app.serverError(w, err)
		return
	}
	rows, err := app.db.Query(r.Context(), `
		SELECT id,category,title,duration_minutes,COALESCE(notes,''),occurred_at
		FROM practice_activities WHERE session_id=$1 AND user_id=$2 ORDER BY occurred_at,created_at`, sessionID, userID)
	if err != nil {
		app.serverError(w, err)
		return
	}
	defer rows.Close()
	session.Activities = make([]practiceActivity, 0)
	for rows.Next() {
		var activity practiceActivity
		if err := rows.Scan(&activity.ID, &activity.Category, &activity.Title, &activity.DurationMinutes, &activity.Notes, &activity.OccurredAt); err != nil {
			app.serverError(w, err)
			return
		}
		session.Activities = append(session.Activities, activity)
	}
	writeJSON(w, http.StatusOK, session)
}

func (app *application) updatePracticeSession(w http.ResponseWriter, r *http.Request) {
	sessionID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid practice session id")
		return
	}
	var input updateSessionRequest
	if err := readJSON(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	userID, err := app.userID(r.Context())
	if err != nil {
		app.serverError(w, err)
		return
	}
	var title, summary, status *string
	if input.Title != nil {
		value := strings.TrimSpace(*input.Title)
		if len(value) < 1 || len(value) > 120 {
			writeError(w, http.StatusUnprocessableEntity, "session title is invalid")
			return
		}
		title = &value
	}
	if input.Summary != nil {
		value := strings.TrimSpace(*input.Summary)
		if len(value) > 2000 {
			writeError(w, http.StatusUnprocessableEntity, "session notes are too long")
			return
		}
		summary = &value
	}
	if input.Status != nil {
		value := strings.TrimSpace(*input.Status)
		if value != "active" && value != "completed" {
			writeError(w, http.StatusUnprocessableEntity, "session status is invalid")
			return
		}
		status = &value
	}
	var endedAt *time.Time
	if input.EndedAt != nil && *input.EndedAt != "" {
		parsed, err := time.Parse(time.RFC3339, *input.EndedAt)
		if err != nil {
			writeError(w, http.StatusUnprocessableEntity, "session end time is invalid")
			return
		}
		endedAt = &parsed
	} else if status != nil && *status == "completed" {
		now := time.Now()
		endedAt = &now
	}

	command, err := app.db.Exec(r.Context(), `
		UPDATE practice_sessions SET
			title=COALESCE($1,title), summary=COALESCE($2,summary), status=COALESCE($3,status),
			ended_at=CASE WHEN COALESCE($3,status)='completed' THEN COALESCE($4,ended_at,now()) ELSE NULL END,
			updated_at=now()
		WHERE id=$5 AND user_id=$6`, title, summary, status, endedAt, sessionID, userID)
	if err != nil {
		app.serverError(w, err)
		return
	}
	if command.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "practice session not found")
		return
	}
	app.getPracticeSession(w, r)
}

func (app *application) createPracticeActivity(w http.ResponseWriter, r *http.Request) {
	sessionID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid practice session id")
		return
	}
	var input createActivityRequest
	if err := readJSON(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	input.Category = strings.TrimSpace(input.Category)
	input.Title = strings.TrimSpace(input.Title)
	input.Notes = strings.TrimSpace(input.Notes)
	if len(input.Category) < 1 || len(input.Category) > 40 || len(input.Title) < 1 || len(input.Title) > 160 ||
		input.DurationMinutes < 1 || input.DurationMinutes > 360 || len(input.Notes) > 1000 {
		writeError(w, http.StatusUnprocessableEntity, "practice activity is invalid")
		return
	}
	occurredAt := time.Now()
	if input.OccurredAt != "" {
		occurredAt, err = time.Parse(time.RFC3339, input.OccurredAt)
		if err != nil {
			writeError(w, http.StatusUnprocessableEntity, "activity time is invalid")
			return
		}
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
	activity := practiceActivity{ID: uuid.New(), Category: input.Category, Title: input.Title, DurationMinutes: input.DurationMinutes, Notes: input.Notes, OccurredAt: occurredAt}
	err = app.db.QueryRow(r.Context(), `
		INSERT INTO practice_activities (id,session_id,user_id,category,title,duration_minutes,notes,occurred_at)
		VALUES ($1,$2,$3,$4,$5,$6,NULLIF($7,''),$8)
		RETURNING id,category,title,duration_minutes,COALESCE(notes,''),occurred_at`,
		activity.ID, sessionID, userID, activity.Category, activity.Title, activity.DurationMinutes, activity.Notes, activity.OccurredAt).
		Scan(&activity.ID, &activity.Category, &activity.Title, &activity.DurationMinutes, &activity.Notes, &activity.OccurredAt)
	if err != nil {
		app.serverError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, activity)
}

func (app *application) practiceSessionBelongs(ctx context.Context, userID, sessionID uuid.UUID) bool {
	var exists bool
	if err := app.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM practice_sessions WHERE id=$1 AND user_id=$2)`, sessionID, userID).Scan(&exists); err != nil {
		return false
	}
	return exists
}
