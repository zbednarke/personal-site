package main

import (
	"errors"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const maxGuideToneDrillMS = 4 * 60 * 60 * 1000

type guideToneDrill struct {
	ID                uuid.UUID  `json:"id"`
	PracticeSessionID *uuid.UUID `json:"practiceSessionId,omitempty"`
	PracticeBlockID   *uuid.UUID `json:"practiceBlockId,omitempty"`
	TuneID            string     `json:"tuneId"`
	Instrument        string     `json:"instrument"`
	Mode              string     `json:"mode"`
	Tempo             int        `json:"tempo"`
	ElapsedMS         int        `json:"elapsedMs"`
	StartedAt         time.Time  `json:"startedAt"`
	EndedAt           *time.Time `json:"endedAt,omitempty"`
	Status            string     `json:"status"`
}

type createGuideToneDrillRequest struct {
	PracticeSessionID string `json:"practiceSessionId"`
	PracticeBlockID   string `json:"practiceBlockId"`
	TuneID            string `json:"tuneId"`
	Instrument        string `json:"instrument"`
	Mode              string `json:"mode"`
	Tempo             int    `json:"tempo"`
	StartedAt         string `json:"startedAt"`
}

type updateGuideToneDrillRequest struct {
	ElapsedMS int     `json:"elapsedMs"`
	EndedAt   *string `json:"endedAt"`
}

type createGuideToneAttemptRequest struct {
	MeasureNumber      int      `json:"measureNumber"`
	ChordIndex         int      `json:"chordIndex"`
	ChordSymbol        string   `json:"chordSymbol"`
	TargetDegree       int      `json:"targetDegree"`
	ExpectedPitchClass int      `json:"expectedPitchClass"`
	PlayedMIDI         *int     `json:"playedMidi"`
	PlayedPitchClass   *int     `json:"playedPitchClass"`
	Cents              *float64 `json:"cents"`
	Correct            bool     `json:"correct"`
	ResponseMS         int      `json:"responseMs"`
	OccurredAt         string   `json:"occurredAt"`
}

type guideToneSummary struct {
	TuneID            string  `json:"tuneId"`
	DrillCount        int     `json:"drillCount"`
	AttemptCount      int     `json:"attemptCount"`
	CorrectCount      int     `json:"correctCount"`
	Accuracy          float64 `json:"accuracy"`
	AverageResponseMS int     `json:"averageResponseMs"`
}

func normalizeGuideToneDrill(input createGuideToneDrillRequest) (createGuideToneDrillRequest, time.Time, error) {
	input.TuneID = strings.TrimSpace(input.TuneID)
	input.Instrument = strings.TrimSpace(input.Instrument)
	input.Mode = strings.TrimSpace(input.Mode)
	if input.TuneID != "blue-bossa" {
		return input, time.Time{}, errors.New("guide-tone tune is invalid")
	}
	if input.Instrument != "bb-trumpet" && input.Instrument != "concert" {
		return input, time.Time{}, errors.New("guide-tone instrument is invalid")
	}
	if input.Mode != "learn" && input.Mode != "tempo" {
		return input, time.Time{}, errors.New("guide-tone mode is invalid")
	}
	if input.Tempo < 40 || input.Tempo > 240 {
		return input, time.Time{}, errors.New("guide-tone tempo is invalid")
	}
	startedAt, err := time.Parse(time.RFC3339, input.StartedAt)
	if err != nil {
		return input, time.Time{}, errors.New("guide-tone start time is invalid")
	}
	return input, startedAt, nil
}

func validateGuideToneAttempt(input createGuideToneAttemptRequest) (time.Time, error) {
	input.ChordSymbol = strings.TrimSpace(input.ChordSymbol)
	if input.MeasureNumber < 1 || input.MeasureNumber > 128 || input.ChordIndex < 0 || input.ChordIndex > 8 ||
		len(input.ChordSymbol) < 1 || len(input.ChordSymbol) > 40 || (input.TargetDegree != 3 && input.TargetDegree != 7) ||
		input.ExpectedPitchClass < 0 || input.ExpectedPitchClass > 11 || input.ResponseMS < 0 || input.ResponseMS > 120000 {
		return time.Time{}, errors.New("guide-tone attempt is invalid")
	}
	if (input.PlayedMIDI == nil) != (input.PlayedPitchClass == nil) {
		return time.Time{}, errors.New("guide-tone played pitch is incomplete")
	}
	if input.PlayedMIDI != nil && (*input.PlayedMIDI < 0 || *input.PlayedMIDI > 127 || *input.PlayedPitchClass < 0 || *input.PlayedPitchClass > 11) {
		return time.Time{}, errors.New("guide-tone played pitch is invalid")
	}
	if input.Correct && input.PlayedPitchClass == nil {
		return time.Time{}, errors.New("a correct guide-tone attempt requires a played pitch")
	}
	if input.PlayedPitchClass != nil && input.Correct != (*input.PlayedPitchClass == input.ExpectedPitchClass) {
		return time.Time{}, errors.New("guide-tone result does not match the played pitch")
	}
	if input.Cents != nil && (math.IsNaN(*input.Cents) || math.IsInf(*input.Cents, 0) || *input.Cents < -100 || *input.Cents > 100) {
		return time.Time{}, errors.New("guide-tone intonation is invalid")
	}
	occurredAt, err := time.Parse(time.RFC3339, input.OccurredAt)
	if err != nil {
		return time.Time{}, errors.New("guide-tone attempt time is invalid")
	}
	return occurredAt, nil
}

func (app *application) createGuideToneDrill(w http.ResponseWriter, r *http.Request) {
	var input createGuideToneDrillRequest
	if err := readJSON(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	input, startedAt, err := normalizeGuideToneDrill(input)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	userID, err := app.userID(r.Context())
	if err != nil {
		app.serverError(w, err)
		return
	}
	var practiceSessionID *uuid.UUID
	if input.PracticeSessionID != "" {
		parsed, parseErr := uuid.Parse(input.PracticeSessionID)
		if parseErr != nil || !app.practiceSessionBelongs(r.Context(), userID, parsed) {
			writeError(w, http.StatusUnprocessableEntity, "practice session is invalid")
			return
		}
		practiceSessionID = &parsed
	}
	var practiceBlockID *uuid.UUID
	if input.PracticeBlockID != "" {
		parsed, parseErr := uuid.Parse(input.PracticeBlockID)
		if parseErr != nil {
			writeError(w, http.StatusUnprocessableEntity, "practice block is invalid")
			return
		}
		var blockSessionID uuid.UUID
		if queryErr := app.db.QueryRow(r.Context(), `SELECT session_id FROM practice_blocks WHERE id=$1 AND user_id=$2`, parsed, userID).Scan(&blockSessionID); queryErr != nil {
			if errors.Is(queryErr, pgx.ErrNoRows) {
				writeError(w, http.StatusUnprocessableEntity, "practice block is invalid")
				return
			}
			app.serverError(w, queryErr)
			return
		}
		if practiceSessionID != nil && blockSessionID != *practiceSessionID {
			writeError(w, http.StatusUnprocessableEntity, "practice block does not belong to the practice session")
			return
		}
		practiceBlockID = &parsed
		if practiceSessionID == nil {
			practiceSessionID = &blockSessionID
		}
	}
	drill := guideToneDrill{
		ID: uuid.New(), PracticeSessionID: practiceSessionID, PracticeBlockID: practiceBlockID, TuneID: input.TuneID, Instrument: input.Instrument,
		Mode: input.Mode, Tempo: input.Tempo, StartedAt: startedAt, Status: "active",
	}
	err = app.db.QueryRow(r.Context(), `
		INSERT INTO guide_tone_drills (id,user_id,practice_session_id,practice_block_id,tune_id,instrument,mode,tempo,started_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		RETURNING id,practice_session_id,practice_block_id,tune_id,instrument,mode,tempo,elapsed_ms,started_at,ended_at,status`,
		drill.ID, userID, drill.PracticeSessionID, drill.PracticeBlockID, drill.TuneID, drill.Instrument, drill.Mode, drill.Tempo, drill.StartedAt).
		Scan(&drill.ID, &drill.PracticeSessionID, &drill.PracticeBlockID, &drill.TuneID, &drill.Instrument, &drill.Mode, &drill.Tempo, &drill.ElapsedMS, &drill.StartedAt, &drill.EndedAt, &drill.Status)
	if err != nil {
		app.serverError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, drill)
}

func (app *application) updateGuideToneDrill(w http.ResponseWriter, r *http.Request) {
	drillID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid guide-tone drill id")
		return
	}
	var input updateGuideToneDrillRequest
	if err := readJSON(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if input.ElapsedMS < 0 || input.ElapsedMS > maxGuideToneDrillMS {
		writeError(w, http.StatusUnprocessableEntity, "guide-tone elapsed time is invalid")
		return
	}
	var endedAt *time.Time
	if input.EndedAt != nil {
		parsed, parseErr := time.Parse(time.RFC3339, *input.EndedAt)
		if parseErr != nil {
			writeError(w, http.StatusUnprocessableEntity, "guide-tone end time is invalid")
			return
		}
		endedAt = &parsed
	}
	userID, err := app.userID(r.Context())
	if err != nil {
		app.serverError(w, err)
		return
	}
	var drill guideToneDrill
	err = app.db.QueryRow(r.Context(), `
		UPDATE guide_tone_drills SET elapsed_ms=GREATEST(elapsed_ms,$1),ended_at=COALESCE($2,ended_at),
		       status=CASE WHEN $2::timestamptz IS NULL THEN status ELSE 'completed' END,updated_at=now()
		WHERE id=$3 AND user_id=$4
		RETURNING id,practice_session_id,practice_block_id,tune_id,instrument,mode,tempo,elapsed_ms,started_at,ended_at,status`,
		input.ElapsedMS, endedAt, drillID, userID).
		Scan(&drill.ID, &drill.PracticeSessionID, &drill.PracticeBlockID, &drill.TuneID, &drill.Instrument, &drill.Mode, &drill.Tempo, &drill.ElapsedMS, &drill.StartedAt, &drill.EndedAt, &drill.Status)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "guide-tone drill not found")
		return
	}
	if err != nil {
		app.serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, drill)
}

func (app *application) createGuideToneAttempt(w http.ResponseWriter, r *http.Request) {
	drillID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid guide-tone drill id")
		return
	}
	var input createGuideToneAttemptRequest
	if err := readJSON(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	input.ChordSymbol = strings.TrimSpace(input.ChordSymbol)
	occurredAt, err := validateGuideToneAttempt(input)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	userID, err := app.userID(r.Context())
	if err != nil {
		app.serverError(w, err)
		return
	}
	command, err := app.db.Exec(r.Context(), `
		INSERT INTO guide_tone_attempts
		(drill_id,user_id,measure_number,chord_index,chord_symbol,target_degree,expected_pitch_class,
		 played_midi,played_pitch_class,cents,correct,response_ms,occurred_at)
		SELECT $1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13
		WHERE EXISTS (SELECT 1 FROM guide_tone_drills WHERE id=$1 AND user_id=$2 AND status='active')`,
		drillID, userID, input.MeasureNumber, input.ChordIndex, input.ChordSymbol, input.TargetDegree, input.ExpectedPitchClass,
		input.PlayedMIDI, input.PlayedPitchClass, input.Cents, input.Correct, input.ResponseMS, occurredAt)
	if err != nil {
		app.serverError(w, err)
		return
	}
	if command.RowsAffected() == 0 {
		writeError(w, http.StatusConflict, "guide-tone drill is not active")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (app *application) guideToneDrillSummary(w http.ResponseWriter, r *http.Request) {
	tuneID := strings.TrimSpace(r.URL.Query().Get("tuneId"))
	if tuneID != "blue-bossa" {
		writeError(w, http.StatusUnprocessableEntity, "guide-tone tune is invalid")
		return
	}
	userID, err := app.userID(r.Context())
	if err != nil {
		app.serverError(w, err)
		return
	}
	summary := guideToneSummary{TuneID: tuneID}
	err = app.db.QueryRow(r.Context(), `
		SELECT (SELECT COUNT(*)::int FROM guide_tone_drills WHERE user_id=$1 AND tune_id=$2 AND elapsed_ms>0),
		       COUNT(*)::int,COUNT(*) FILTER (WHERE gta.correct)::int,
		       COALESCE(AVG(gta.response_ms) FILTER (WHERE gta.correct),0)::int
		FROM guide_tone_attempts gta
		JOIN guide_tone_drills gtd ON gtd.id=gta.drill_id AND gtd.user_id=gta.user_id
		WHERE gta.user_id=$1 AND gtd.tune_id=$2`, userID, tuneID).
		Scan(&summary.DrillCount, &summary.AttemptCount, &summary.CorrectCount, &summary.AverageResponseMS)
	if err != nil {
		app.serverError(w, err)
		return
	}
	if summary.AttemptCount > 0 {
		summary.Accuracy = math.Round((float64(summary.CorrectCount)/float64(summary.AttemptCount))*1000) / 10
	}
	writeJSON(w, http.StatusOK, summary)
}
