package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type magicFilmCandidate struct {
	ID          string          `json:"id"`
	RecordingID string          `json:"recordingId"`
	StartMS     int             `json:"startMs"`
	EndMS       int             `json:"endMs"`
	Manual      bool            `json:"manual"`
	Liked       bool            `json:"liked"`
	Title       string          `json:"title"`
	Notes       string          `json:"notes"`
	Reasons     json.RawMessage `json:"reasons"`
	Activity    *magicActivity  `json:"activity,omitempty"`
	Frames      []string        `json:"frames,omitempty"`
}

type magicFilmRecording struct {
	ID          string `json:"id"`
	DurationMS  int    `json:"durationMs"`
	Title       string `json:"title"`
	AudioObject string `json:"audioObject"`
	VideoObject string `json:"videoObject,omitempty"`
}

type magicFilmManifest struct {
	Date          string               `json:"date"`
	TargetSeconds int                  `json:"targetSeconds"`
	Candidates    []magicFilmCandidate `json:"candidates"`
	Recordings    []magicFilmRecording `json:"recordings"`
}

type magicFilmCreateRequest struct {
	ClientID      string `json:"clientId"`
	Date          string `json:"date"`
	TargetSeconds int    `json:"targetSeconds"`
}

type magicFilmJob struct {
	ID              uuid.UUID       `json:"id"`
	Date            string          `json:"date"`
	TargetSeconds   int             `json:"targetSeconds"`
	Status          string          `json:"status"`
	Message         string          `json:"message"`
	Model           string          `json:"model"`
	Effort          string          `json:"effort"`
	Project         json.RawMessage `json:"project,omitempty"`
	Summary         string          `json:"summary,omitempty"`
	Reviews         json.RawMessage `json:"reviews,omitempty"`
	DurationMS      *int            `json:"durationMs,omitempty"`
	Error           string          `json:"error,omitempty"`
	CancelRequested bool            `json:"cancelRequested"`
	CreatedAt       time.Time       `json:"createdAt"`
	StartedAt       *time.Time      `json:"startedAt,omitempty"`
	FinishedAt      *time.Time      `json:"finishedAt,omitempty"`
	HasVideo        bool            `json:"hasVideo"`
	HasPoster       bool            `json:"hasPoster"`
}

const magicFilmJobColumns = `id,practice_date::text,target_seconds,status,message,model,effort,COALESCE(project,'null'::jsonb),summary,reviews,duration_ms,error,cancel_requested,created_at,started_at,finished_at,output_object IS NOT NULL,poster_object IS NOT NULL`

func scanMagicFilmJob(row pgx.Row) (magicFilmJob, error) {
	var job magicFilmJob
	err := row.Scan(&job.ID, &job.Date, &job.TargetSeconds, &job.Status, &job.Message, &job.Model, &job.Effort,
		&job.Project, &job.Summary, &job.Reviews, &job.DurationMS, &job.Error, &job.CancelRequested,
		&job.CreatedAt, &job.StartedAt, &job.FinishedAt, &job.HasVideo, &job.HasPoster)
	if string(job.Project) == "null" {
		job.Project = nil
	}
	return job, err
}

func validMagicTarget(value int) bool {
	return value == 60 || value == 120 || value == 180 || value == 300 || value == 600
}

func (app *application) magicFilmManifest(ctx context.Context, userID uuid.UUID, date string, target int) (magicFilmManifest, error) {
	manifest := magicFilmManifest{Date: date, TargetSeconds: target, Candidates: []magicFilmCandidate{}, Recordings: []magicFilmRecording{}}
	rows, err := app.db.Query(ctx, `
		SELECT c.id::text,c.recording_id::text,c.start_ms,c.end_ms,c.source='manual',c.review_status='kept',
		       COALESCE(NULLIF(c.title,''),NULLIF(pb.title,''),NULLIF(ps.title,''),'Practice'),COALESCE(c.notes,''),c.reasons,
		       COALESCE(r.duration_ms,0),r.object_name,COALESCE(r.video_object_name,'')
		FROM clip_candidates c JOIN recordings r ON r.id=c.recording_id
		LEFT JOIN practice_blocks pb ON pb.id=r.practice_block_id AND pb.user_id=r.user_id
		LEFT JOIN practice_sessions ps ON ps.id::text=r.practice_session_id AND ps.user_id=r.user_id
		WHERE c.user_id=$1 AND r.status='ready' AND c.review_status<>'rejected'
		  AND COALESCE(pb.practice_date,(r.recorded_at AT TIME ZONE 'America/Los_Angeles')::date)=$2::date
		ORDER BY CASE c.review_status WHEN 'kept' THEN 0 ELSE 1 END,c.score DESC,c.start_ms`, userID, date)
	if err != nil {
		return manifest, err
	}
	defer rows.Close()
	recordings := map[string]magicFilmRecording{}
	for rows.Next() {
		var candidate magicFilmCandidate
		var recording magicFilmRecording
		if err := rows.Scan(&candidate.ID, &candidate.RecordingID, &candidate.StartMS, &candidate.EndMS, &candidate.Manual, &candidate.Liked,
			&candidate.Title, &candidate.Notes, &candidate.Reasons, &recording.DurationMS, &recording.AudioObject, &recording.VideoObject); err != nil {
			return manifest, err
		}
		recording.ID, recording.Title = candidate.RecordingID, candidate.Title
		if candidate.EndMS-candidate.StartMS < minimumClipDurationMS || candidate.StartMS < 0 || candidate.EndMS > recording.DurationMS {
			continue
		}
		candidate.Notes = clean(candidate.Notes, 2000)
		manifest.Candidates = append(manifest.Candidates, candidate)
		recordings[recording.ID] = recording
	}
	if err := rows.Err(); err != nil {
		return manifest, err
	}
	if len(manifest.Candidates) == 0 {
		return manifest, errors.New("no eligible moments; add manual moments or scan this day first")
	}
	if len(manifest.Candidates) > 500 {
		return manifest, errors.New("this day has too many candidate moments")
	}
	for _, recording := range recordings {
		manifest.Recordings = append(manifest.Recordings, recording)
	}
	return manifest, nil
}

func (app *application) createMagicFilm(w http.ResponseWriter, r *http.Request) {
	var input magicFilmCreateRequest
	if err := readJSON(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	clientID, err := uuid.Parse(input.ClientID)
	if err != nil || !datePattern.MatchString(input.Date) || !validMagicTarget(input.TargetSeconds) {
		writeError(w, http.StatusUnprocessableEntity, "choose a valid day and film length")
		return
	}
	if _, err := time.Parse("2006-01-02", input.Date); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "choose a valid practice day")
		return
	}
	if app.magicFilmInvoke == nil && (app.cfg.MagicFilmProject == "" || app.cfg.MagicFilmJob == "") {
		writeError(w, http.StatusServiceUnavailable, "Magic Film cloud worker is not configured")
		return
	}
	userID, err := app.userID(r.Context())
	if err != nil {
		app.serverError(w, err)
		return
	}
	if existing, err := scanMagicFilmJob(app.db.QueryRow(r.Context(), `SELECT `+magicFilmJobColumns+` FROM magic_film_jobs WHERE user_id=$1 AND client_id=$2`, userID, clientID)); err == nil {
		writeJSON(w, http.StatusOK, existing)
		return
	} else if !errors.Is(err, pgx.ErrNoRows) {
		app.serverError(w, err)
		return
	}
	manifest, err := app.magicFilmManifest(r.Context(), userID, input.Date, input.TargetSeconds)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		app.serverError(w, err)
		return
	}
	jobID := uuid.New()
	model := envOr("MAGIC_FILM_MODEL", "gpt-6-astra")
	_, err = app.db.Exec(r.Context(), `INSERT INTO magic_film_jobs(id,user_id,client_id,practice_date,target_seconds,status,message,model,effort,manifest)
		VALUES($1,$2,$3,$4::date,$5,'queued','Queued for an on-demand cloud worker',$6,'medium',$7)`, jobID, userID, clientID, input.Date, input.TargetSeconds, model, encoded)
	if err != nil {
		var dbErr *pgconn.PgError
		if errors.As(err, &dbErr) && dbErr.Code == "23505" {
			writeError(w, http.StatusConflict, "A Magic Film is already running. Wait for it or cancel it first.")
			return
		}
		app.serverError(w, err)
		return
	}
	ref, err := app.invokeMagicFilm(r.Context(), jobID)
	if err != nil {
		_, _ = app.db.Exec(r.Context(), `UPDATE magic_film_jobs SET status='failed',message='Cloud worker could not start',error=$2,finished_at=now(),updated_at=now() WHERE id=$1`, jobID, clean(err.Error(), 4000))
		writeError(w, http.StatusServiceUnavailable, "Cloud worker could not start; no render charges were incurred")
		return
	}
	_, _ = app.db.Exec(r.Context(), `UPDATE magic_film_jobs SET status='starting',message='Starting an on-demand cloud worker',invocation_ref=$2,updated_at=now() WHERE id=$1 AND status='queued'`, jobID, ref)
	job, err := scanMagicFilmJob(app.db.QueryRow(r.Context(), `SELECT `+magicFilmJobColumns+` FROM magic_film_jobs WHERE id=$1 AND user_id=$2`, jobID, userID))
	if err != nil {
		app.serverError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, job)
}

func (app *application) invokeMagicFilm(ctx context.Context, jobID uuid.UUID) (string, error) {
	if app.magicFilmInvoke != nil {
		return app.magicFilmInvoke(ctx, jobID)
	}
	token, err := app.tokenSource.Token()
	if err != nil {
		return "", err
	}
	endpoint := fmt.Sprintf("https://run.googleapis.com/v2/projects/%s/locations/%s/jobs/%s:run",
		url.PathEscape(app.cfg.MagicFilmProject), url.PathEscape(app.cfg.MagicFilmRegion), url.PathEscape(app.cfg.MagicFilmJob))
	body, _ := json.Marshal(map[string]any{"overrides": map[string]any{"taskCount": 1, "containerOverrides": []any{map[string]any{
		"env": []any{map[string]string{"name": "MAGIC_FILM_JOB_ID", "value": jobID.String()}},
	}}}})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token.AccessToken)
	req.Header.Set("Content-Type", "application/json")
	client := app.httpClient
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	response, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	responseBody, _ := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("Cloud Run jobs API returned %d: %s", response.StatusCode, strings.TrimSpace(string(responseBody)))
	}
	var operation struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(responseBody, &operation); err != nil || operation.Name == "" {
		return "", errors.New("Cloud Run jobs API returned no operation name")
	}
	return operation.Name, nil
}

func (app *application) listMagicFilms(w http.ResponseWriter, r *http.Request) {
	userID, err := app.userID(r.Context())
	if err != nil {
		app.serverError(w, err)
		return
	}
	date := r.URL.Query().Get("date")
	if date != "" && !datePattern.MatchString(date) {
		writeError(w, http.StatusBadRequest, "invalid practice date")
		return
	}
	rows, err := app.db.Query(r.Context(), `SELECT `+magicFilmJobColumns+` FROM magic_film_jobs WHERE user_id=$1 AND ($2='' OR practice_date=$2::date) ORDER BY created_at DESC LIMIT 50`, userID, date)
	if err != nil {
		app.serverError(w, err)
		return
	}
	defer rows.Close()
	jobs := []magicFilmJob{}
	for rows.Next() {
		job, err := scanMagicFilmJob(rows)
		if err != nil {
			app.serverError(w, err)
			return
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		app.serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobs": jobs})
}

func (app *application) getMagicFilm(w http.ResponseWriter, r *http.Request) {
	userID, err := app.userID(r.Context())
	if err != nil {
		app.serverError(w, err)
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid film id")
		return
	}
	job, err := scanMagicFilmJob(app.db.QueryRow(r.Context(), `SELECT `+magicFilmJobColumns+` FROM magic_film_jobs WHERE id=$1 AND user_id=$2`, id, userID))
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "film not found")
		return
	}
	if err != nil {
		app.serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (app *application) cancelMagicFilm(w http.ResponseWriter, r *http.Request) {
	userID, err := app.userID(r.Context())
	if err != nil {
		app.serverError(w, err)
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid film id")
		return
	}
	tag, err := app.db.Exec(r.Context(), `UPDATE magic_film_jobs SET cancel_requested=true,
		status=CASE WHEN status IN ('queued','starting') THEN 'cancelled' ELSE status END,
		message=CASE WHEN status IN ('queued','starting') THEN 'Cancelled before rendering' ELSE 'Cancellation requested' END,
		finished_at=CASE WHEN status IN ('queued','starting') THEN now() ELSE finished_at END,updated_at=now()
		WHERE id=$1 AND user_id=$2 AND status IN ('queued','starting','preparing','editing','rendering')`, id, userID)
	if err != nil {
		app.serverError(w, err)
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusConflict, "film is not running")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"cancelRequested": true})
}

func (app *application) openMagicFilmMedia(w http.ResponseWriter, r *http.Request) {
	userID, err := app.userID(r.Context())
	if err != nil {
		app.serverError(w, err)
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid film id")
		return
	}
	asset := r.URL.Query().Get("asset")
	column, contentType, extension := "output_object", "video/mp4", ".mp4"
	if asset == "poster" {
		column, contentType, extension = "poster_object", "image/jpeg", ".jpg"
	} else if asset != "video" && asset != "" {
		writeError(w, http.StatusBadRequest, "invalid film asset")
		return
	}
	var objectName string
	err = app.db.QueryRow(r.Context(), `SELECT COALESCE(`+column+`,'') FROM magic_film_jobs WHERE id=$1 AND user_id=$2 AND status='complete'`, id, userID).Scan(&objectName)
	if errors.Is(err, pgx.ErrNoRows) || objectName == "" {
		writeError(w, http.StatusNotFound, "film asset not found")
		return
	}
	if err != nil {
		app.serverError(w, err)
		return
	}
	values := url.Values{"response-content-type": {contentType}}
	if asset != "poster" && r.URL.Query().Get("download") == "1" {
		values.Set("response-content-disposition", `attachment; filename="magic-film-`+id.String()+extension+`"`)
	}
	signed, err := app.signedRecordingObjectURL(r.Context(), objectName, time.Now().Add(time.Hour), values)
	if err != nil {
		app.serverError(w, err)
		return
	}
	http.Redirect(w, r, signed, http.StatusFound)
}

func (app *application) magicFilmRoutes(mux *http.ServeMux) {
	routes := map[string]http.HandlerFunc{
		"POST /v1/studio/magic-films":             app.createMagicFilm,
		"GET /v1/studio/magic-films":              app.listMagicFilms,
		"GET /v1/studio/magic-films/{id}":         app.getMagicFilm,
		"POST /v1/studio/magic-films/{id}/cancel": app.cancelMagicFilm,
		"GET /v1/studio/magic-films/{id}/media":   app.openMagicFilmMedia,
	}
	for pattern, handler := range routes {
		mux.Handle(pattern, repertoirePrivacy(app.authenticate(handler)))
	}
}
