package main

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"io"
	"math"
	"net/http"
	"time"
)

type trainingAttempt struct {
	ID         string `json:"id"`
	SessionID  string `json:"sessionId"`
	Key        string `json:"key"`
	Instrument string `json:"instrument"`
	Mode       string `json:"mode"`
	Tempo      int    `json:"tempo"`
	Round      int    `json:"round"`
	StartedAt  string `json:"startedAt"`
	DurationMS int    `json:"durationMs"`
	SampleRate int    `json:"sampleRate"`
	AudioBytes int    `json:"audioBytes"`
	Result     struct {
		Status  string `json:"status"`
		Correct int    `json:"correct"`
	} `json:"result"`
}

func validTrainingAttempt(raw json.RawMessage, id string) bool {
	var a trainingAttempt
	if len(raw) > 128000 || json.Unmarshal(raw, &a) != nil || a.ID != id {
		return false
	}
	if _, err := uuid.Parse(a.SessionID); err != nil {
		return false
	}
	if _, err := time.Parse(time.RFC3339Nano, a.StartedAt); err != nil {
		return false
	}
	allowed := map[string]bool{"C": true, "B♭": true, "A♭": true, "G♭": true, "E": true, "D": true, "B": true, "A": true, "G": true, "F": true, "E♭": true, "D♭": true}
	statuses := map[string]bool{"pass": true, "needs-work": true, "not-graded": true, "interrupted": true}
	return allowed[a.Key] && (a.Instrument == "bb-trumpet" || a.Instrument == "concert") && (a.Mode == "lesson" || a.Mode == "learn") && a.Tempo >= 40 && a.Tempo <= 160 && a.Round >= 1 && a.Round <= 10000 && a.DurationMS >= 0 && a.DurationMS <= 24000 && a.SampleRate >= 8000 && a.SampleRate <= 192000 && a.AudioBytes >= 44 && a.AudioBytes <= 12*1024*1024 && statuses[a.Result.Status] && a.Result.Correct >= 0 && a.Result.Correct <= 7
}
func (app *application) saveTrainingAttempt(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, 400, "invalid attempt")
		return
	}
	var raw json.RawMessage
	if readJSON(w, r, &raw) != nil || !validTrainingAttempt(raw, id.String()) {
		writeError(w, 422, "invalid Study 1 attempt")
		return
	}
	user, err := app.userID(r.Context())
	if err != nil {
		app.serverError(w, err)
		return
	}
	_, err = app.db.Exec(r.Context(), `INSERT INTO training_attempts(user_id,id,payload) VALUES($1,$2,$3) ON CONFLICT(user_id,id) DO NOTHING`, user, id, raw)
	if err != nil {
		app.serverError(w, err)
		return
	}
	var same bool
	if err = app.db.QueryRow(r.Context(), `SELECT payload=$3::jsonb FROM training_attempts WHERE user_id=$1 AND id=$2`, user, id, raw).Scan(&same); err != nil {
		app.serverError(w, err)
		return
	}
	if !same {
		writeError(w, 409, "attempt already saved with different results")
		return
	}
	writeJSON(w, 200, map[string]any{"id": id})
}
func (app *application) listTrainingAttempts(w http.ResponseWriter, r *http.Request) {
	user, err := app.userID(r.Context())
	if err != nil {
		app.serverError(w, err)
		return
	}
	after := uuid.Nil
	if value := r.URL.Query().Get("after"); value != "" {
		after, err = uuid.Parse(value)
		if err != nil {
			writeError(w, 400, "invalid cursor")
			return
		}
	}
	rows, err := app.db.Query(r.Context(), `SELECT id,payload,audio_object IS NOT NULL FROM training_attempts WHERE user_id=$1 AND id>$2 ORDER BY id LIMIT 200`, user, after)
	if err != nil {
		app.serverError(w, err)
		return
	}
	defer rows.Close()
	result := []map[string]any{}
	next := ""
	for rows.Next() {
		var id uuid.UUID
		var payload json.RawMessage
		var audio bool
		if err = rows.Scan(&id, &payload, &audio); err != nil {
			app.serverError(w, err)
			return
		}
		result = append(result, map[string]any{"attempt": payload, "audioReady": audio})
		next = id.String()
	}
	if err = rows.Err(); err != nil {
		app.serverError(w, err)
		return
	}
	if len(result) < 200 {
		next = ""
	}
	writeJSON(w, 200, map[string]any{"attempts": result, "next": next})
}
func (app *application) trainingAudio(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, 400, "invalid attempt")
		return
	}
	user, err := app.userID(r.Context())
	if err != nil {
		app.serverError(w, err)
		return
	}
	var raw json.RawMessage
	var object *string
	err = app.db.QueryRow(r.Context(), `SELECT payload,audio_object FROM training_attempts WHERE user_id=$1 AND id=$2`, user, id).Scan(&raw, &object)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, 404, "attempt not found")
		return
	}
	if err != nil {
		app.serverError(w, err)
		return
	}
	if r.Method == http.MethodGet {
		if object == nil {
			writeError(w, 404, "audio is not synced yet")
			return
		}
		reader, err := app.storage.Bucket(app.cfg.Bucket).Object(*object).NewReader(r.Context())
		if err != nil {
			app.serverError(w, err)
			return
		}
		defer reader.Close()
		w.Header().Set("Content-Type", "audio/wav")
		w.Header().Set("Cache-Control", "private, no-store")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`inline; filename="study-1-%s.wav"`, id))
		io.Copy(w, reader)
		return
	}
	var a trainingAttempt
	json.Unmarshal(raw, &a)
	if r.ContentLength != int64(a.AudioBytes) {
		writeError(w, 422, "audio size does not match saved attempt")
		return
	}
	if object != nil {
		writeJSON(w, 200, map[string]bool{"synced": true})
		return
	}
	// Short WAV trials are bounded; inspect their header before storing any audio.
	data, err := io.ReadAll(io.LimitReader(r.Body, int64(a.AudioBytes)+1))
	if err != nil || len(data) != a.AudioBytes || len(data) < 44 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		writeError(w, 422, "invalid WAV audio")
		return
	}
	if string(data[12:16]) != "fmt " || binary.LittleEndian.Uint32(data[16:20]) != 16 || binary.LittleEndian.Uint16(data[20:22]) != 1 || binary.LittleEndian.Uint16(data[22:24]) != 1 || int(binary.LittleEndian.Uint32(data[24:28])) != a.SampleRate || binary.LittleEndian.Uint16(data[34:36]) != 16 || string(data[36:40]) != "data" || int(binary.LittleEndian.Uint32(data[40:44])) != len(data)-44 || int(binary.LittleEndian.Uint32(data[4:8])) != len(data)-8 || (len(data)-44)%2 != 0 {
		writeError(w, 422, "audio must be mono 16-bit PCM WAV matching the attempt")
		return
	}
	answerMS := math.Max(0, float64(len(data)-44)/2/float64(a.SampleRate)*1000-240000/float64(a.Tempo))
	if math.Abs(answerMS-float64(a.DurationMS)) > 2 {
		writeError(w, 422, "audio duration does not match the attempt")
		return
	}
	name := fmt.Sprintf("users/%s/training/study-one/%s.wav", user, id)
	writer := app.storage.Bucket(app.cfg.Bucket).Object(name).NewWriter(r.Context())
	writer.ContentType = "audio/wav"
	if _, err = writer.Write(data); err != nil {
		writer.Close()
		app.serverError(w, err)
		return
	}
	if err = writer.Close(); err != nil {
		app.serverError(w, err)
		return
	}
	_, err = app.db.Exec(r.Context(), `UPDATE training_attempts SET audio_object=$3 WHERE user_id=$1 AND id=$2`, user, id, name)
	if err != nil {
		app.serverError(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"synced": true})
}
