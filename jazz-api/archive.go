package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
)

type archiveCalendarDay struct {
	Date            string `json:"date"`
	TotalDurationMS int64  `json:"totalDurationMs"`
	RecordingCount  int    `json:"recordingCount"`
	SectionCount    int    `json:"sectionCount"`
	SessionCount    int    `json:"sessionCount"`
	HasNotes        bool   `json:"hasNotes"`
}

type archiveSession struct {
	practiceSession
	Blocks     []practiceBlock    `json:"blocks"`
	Activities []practiceActivity `json:"activities"`
}

type archiveDayResponse struct {
	Date            string           `json:"date"`
	TotalDurationMS int64            `json:"totalDurationMs"`
	RecordingCount  int              `json:"recordingCount"`
	SectionCount    int              `json:"sectionCount"`
	Sessions        []archiveSession `json:"sessions"`
	Recordings      []recordingRow   `json:"recordings"`
}

func parseArchiveDate(value string) (time.Time, error) {
	date, err := time.Parse("2006-01-02", value)
	if err != nil || date.Format("2006-01-02") != value {
		return time.Time{}, errors.New("archive date is invalid")
	}
	return date, nil
}

func archiveRange(r *http.Request) (time.Time, time.Time, error) {
	from, err := parseArchiveDate(r.URL.Query().Get("from"))
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	to, err := parseArchiveDate(r.URL.Query().Get("to"))
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	if to.Before(from) || to.Sub(from) > 370*24*time.Hour {
		return time.Time{}, time.Time{}, errors.New("archive date range is invalid")
	}
	return from, to, nil
}

func (app *application) archiveCalendar(w http.ResponseWriter, r *http.Request) {
	from, to, err := archiveRange(r)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	userID, err := app.userID(r.Context())
	if err != nil {
		app.serverError(w, err)
		return
	}
	days, err := app.loadArchiveCalendar(r.Context(), userID, from, to)
	if err != nil {
		app.serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"from": from.Format("2006-01-02"),
		"to":   to.Format("2006-01-02"),
		"days": days,
	})
}

func (app *application) loadArchiveCalendar(ctx context.Context, userID uuid.UUID, from, to time.Time) ([]archiveCalendarDay, error) {
	rows, err := app.db.Query(ctx, `
		WITH bounds AS (SELECT $2::date AS from_date, $3::date AS to_date),
		block_recorded AS (
			SELECT pb.id, COALESCE(SUM(r.duration_ms) FILTER (WHERE r.status IN ('uploading','ready')),0)::bigint AS recorded_ms
			FROM practice_blocks pb
			LEFT JOIN recordings r ON r.practice_block_id=pb.id AND r.user_id=pb.user_id
			CROSS JOIN bounds b
			WHERE pb.user_id=$1 AND pb.practice_date BETWEEN b.from_date AND b.to_date
			GROUP BY pb.id
		),
		block_days AS (
			SELECT pb.practice_date AS day,
			       SUM(GREATEST(pb.elapsed_ms::bigint,br.recorded_ms))::bigint AS duration_ms,
			       COUNT(*)::int AS section_count
			FROM practice_blocks pb JOIN block_recorded br ON br.id=pb.id
			GROUP BY pb.practice_date
		),
		orphan_days AS (
			SELECT timezone('America/Los_Angeles',r.recorded_at)::date AS day,
			       COALESCE(SUM(r.duration_ms),0)::bigint AS duration_ms
			FROM recordings r CROSS JOIN bounds b
			WHERE r.user_id=$1 AND r.practice_block_id IS NULL AND r.status IN ('uploading','ready')
			  AND timezone('America/Los_Angeles',r.recorded_at)::date BETWEEN b.from_date AND b.to_date
			GROUP BY day
		),
		recording_days AS (
			SELECT COALESCE(pb.practice_date,timezone('America/Los_Angeles',r.recorded_at)::date) AS day,
			       COUNT(*)::int AS recording_count
			FROM recordings r
			LEFT JOIN practice_blocks pb ON pb.id=r.practice_block_id AND pb.user_id=r.user_id
			CROSS JOIN bounds b
			WHERE r.user_id=$1 AND r.status <> 'deleted'
			  AND COALESCE(pb.practice_date,timezone('America/Los_Angeles',r.recorded_at)::date) BETWEEN b.from_date AND b.to_date
			GROUP BY day
		),
		session_dates AS (
			SELECT ps.id, timezone('America/Los_Angeles',ps.started_at)::date AS day
			FROM practice_sessions ps CROSS JOIN bounds b
			WHERE ps.user_id=$1 AND timezone('America/Los_Angeles',ps.started_at)::date BETWEEN b.from_date AND b.to_date
			UNION
			SELECT pb.session_id, pb.practice_date FROM practice_blocks pb CROSS JOIN bounds b
			WHERE pb.user_id=$1 AND pb.practice_date BETWEEN b.from_date AND b.to_date
			UNION
			SELECT ps.id, timezone('America/Los_Angeles',r.recorded_at)::date
			FROM recordings r JOIN practice_sessions ps ON ps.id::text=r.practice_session_id AND ps.user_id=r.user_id
			CROSS JOIN bounds b
			WHERE r.user_id=$1 AND r.practice_block_id IS NULL AND r.status <> 'deleted'
			  AND timezone('America/Los_Angeles',r.recorded_at)::date BETWEEN b.from_date AND b.to_date
		),
		session_days AS (
			SELECT sd.day,COUNT(DISTINCT sd.id)::int AS session_count,
			       BOOL_OR(COALESCE(NULLIF(ps.summary,''),'') <> '') AS has_notes
			FROM session_dates sd JOIN practice_sessions ps ON ps.id=sd.id GROUP BY sd.day
		),
		note_days AS (
			SELECT pb.practice_date AS day FROM practice_blocks pb CROSS JOIN bounds b
			WHERE pb.user_id=$1 AND COALESCE(NULLIF(pb.notes,''),'') <> '' AND pb.practice_date BETWEEN b.from_date AND b.to_date
			UNION
			SELECT COALESCE(pb.practice_date,timezone('America/Los_Angeles',r.recorded_at)::date)
			FROM recordings r LEFT JOIN practice_blocks pb ON pb.id=r.practice_block_id AND pb.user_id=r.user_id CROSS JOIN bounds b
			WHERE r.user_id=$1 AND r.status <> 'deleted' AND COALESCE(NULLIF(r.notes,''),'') <> ''
			  AND COALESCE(pb.practice_date,timezone('America/Los_Angeles',r.recorded_at)::date) BETWEEN b.from_date AND b.to_date
		),
		all_days AS (
			SELECT day FROM block_days UNION SELECT day FROM orphan_days UNION SELECT day FROM recording_days
			UNION SELECT day FROM session_days UNION SELECT day FROM note_days
		)
		SELECT ad.day::text,
		       COALESCE(bd.duration_ms,0)+COALESCE(od.duration_ms,0),
		       COALESCE(rd.recording_count,0),COALESCE(bd.section_count,0),COALESCE(sd.session_count,0),
		       COALESCE(sd.has_notes,false) OR nd.day IS NOT NULL
		FROM all_days ad
		LEFT JOIN block_days bd ON bd.day=ad.day
		LEFT JOIN orphan_days od ON od.day=ad.day
		LEFT JOIN recording_days rd ON rd.day=ad.day
		LEFT JOIN session_days sd ON sd.day=ad.day
		LEFT JOIN note_days nd ON nd.day=ad.day
		ORDER BY ad.day`, userID, from.Format("2006-01-02"), to.Format("2006-01-02"))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	days := make([]archiveCalendarDay, 0)
	for rows.Next() {
		var day archiveCalendarDay
		if err := rows.Scan(&day.Date, &day.TotalDurationMS, &day.RecordingCount, &day.SectionCount, &day.SessionCount, &day.HasNotes); err != nil {
			return nil, err
		}
		days = append(days, day)
	}
	return days, rows.Err()
}

func (app *application) archiveDay(w http.ResponseWriter, r *http.Request) {
	date, err := parseArchiveDate(r.PathValue("date"))
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	userID, err := app.userID(r.Context())
	if err != nil {
		app.serverError(w, err)
		return
	}
	response, err := app.loadArchiveDay(r.Context(), userID, date)
	if err != nil {
		app.serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (app *application) loadArchiveDay(ctx context.Context, userID uuid.UUID, date time.Time) (archiveDayResponse, error) {
	dateKey := date.Format("2006-01-02")
	calendar, err := app.loadArchiveCalendar(ctx, userID, date, date)
	if err != nil {
		return archiveDayResponse{}, err
	}
	response := archiveDayResponse{Date: dateKey, Sessions: []archiveSession{}, Recordings: []recordingRow{}}
	if len(calendar) > 0 {
		response.TotalDurationMS = calendar[0].TotalDurationMS
		response.RecordingCount = calendar[0].RecordingCount
		response.SectionCount = calendar[0].SectionCount
	}

	rows, err := app.db.Query(ctx, `
		SELECT DISTINCT ps.id,ps.title,COALESCE(ps.summary,''),ps.started_at,ps.ended_at,ps.status
		FROM practice_sessions ps
		WHERE ps.user_id=$1 AND (
			timezone('America/Los_Angeles',ps.started_at)::date=$2::date
			OR EXISTS (SELECT 1 FROM practice_blocks pb WHERE pb.session_id=ps.id AND pb.user_id=ps.user_id AND pb.practice_date=$2::date)
			OR EXISTS (
				SELECT 1 FROM recordings r LEFT JOIN practice_blocks pb ON pb.id=r.practice_block_id AND pb.user_id=r.user_id
				WHERE r.user_id=ps.user_id AND r.practice_session_id=ps.id::text AND r.status <> 'deleted'
				  AND COALESCE(pb.practice_date,timezone('America/Los_Angeles',r.recorded_at)::date)=$2::date
			)
		)
		ORDER BY ps.started_at`, userID, dateKey)
	if err != nil {
		return archiveDayResponse{}, err
	}
	for rows.Next() {
		var session archiveSession
		if err := rows.Scan(&session.ID, &session.Title, &session.Summary, &session.StartedAt, &session.EndedAt, &session.Status); err != nil {
			rows.Close()
			return archiveDayResponse{}, err
		}
		response.Sessions = append(response.Sessions, session)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return archiveDayResponse{}, err
	}
	rows.Close()

	response.Recordings, err = app.loadArchiveRecordings(ctx, userID, dateKey)
	if err != nil {
		return archiveDayResponse{}, err
	}
	for index := range response.Sessions {
		session := &response.Sessions[index]
		session.Blocks, err = app.loadPracticeBlocks(ctx, userID, session.ID, dateKey)
		if err != nil {
			return archiveDayResponse{}, err
		}
		session.Activities, err = app.loadArchiveActivities(ctx, userID, session.ID, dateKey)
		if err != nil {
			return archiveDayResponse{}, err
		}
		for _, block := range session.Blocks {
			session.TotalDurationMS += int64(block.ElapsedMS)
		}
		for _, recording := range response.Recordings {
			if recording.SessionID == session.ID.String() {
				session.RecordingCount++
				if recording.BlockID == "" && (recording.Status == "ready" || recording.Status == "uploading") {
					session.TotalDurationMS += int64(recording.DurationMS)
				}
			}
		}
		session.TotalMinutes = int((session.TotalDurationMS + 30000) / 60000)
		session.ActivityCount = len(session.Activities)
	}
	return response, nil
}

func (app *application) loadArchiveActivities(ctx context.Context, userID, sessionID uuid.UUID, date string) ([]practiceActivity, error) {
	rows, err := app.db.Query(ctx, `
		SELECT id,category,title,duration_minutes,COALESCE(notes,''),occurred_at
		FROM practice_activities
		WHERE session_id=$1 AND user_id=$2 AND timezone('America/Los_Angeles',occurred_at)::date=$3::date
		ORDER BY occurred_at,created_at`, sessionID, userID, date)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	activities := make([]practiceActivity, 0)
	for rows.Next() {
		var activity practiceActivity
		if err := rows.Scan(&activity.ID, &activity.Category, &activity.Title, &activity.DurationMinutes, &activity.Notes, &activity.OccurredAt); err != nil {
			return nil, err
		}
		activities = append(activities, activity)
	}
	return activities, rows.Err()
}

func (app *application) loadArchiveRecordings(ctx context.Context, userID uuid.UUID, date string) ([]recordingRow, error) {
	rows, err := app.db.Query(ctx, `
		SELECT r.id,r.content_type,COALESCE(r.codec,''),COALESCE(r.size_bytes,r.expected_size_bytes),COALESCE(r.duration_ms,0),
		COALESCE(r.sample_rate,0),COALESCE(r.channels,0),r.recorded_at,r.status,
		COALESCE(r.tune_id,''),COALESCE(r.mission_id,''),r.skill_ids,COALESCE(r.take_number,0),COALESCE(r.notes,''),
		COALESCE(r.practice_session_id,''),COALESCE(ps.title,''),COALESCE(r.practice_block_id::text,''),
		COALESCE(pb.practice_date::text,''),COALESCE(pb.block_key,''),COALESCE(pb.title,''),COALESCE(pb.category,''),COALESCE(pb.track,''),r.object_name,
		COALESCE(r.media_kind,'audio'),COALESCE(r.video_content_type,''),COALESCE(r.video_codec,''),COALESCE(r.video_size_bytes,r.video_expected_size_bytes,0),
		COALESCE(r.video_width,0),COALESCE(r.video_height,0),COALESCE(r.video_frame_rate,0),COALESCE(r.video_object_name,''),
		COALESCE(r.fx_content_type,''),COALESCE(r.fx_size_bytes,r.fx_expected_size_bytes,0),COALESCE(r.fx_preset,''),COALESCE(r.fx_object_name,''),r.waveform_peaks
		FROM recordings r
		LEFT JOIN practice_sessions ps ON ps.id::text=r.practice_session_id AND ps.user_id=r.user_id
		LEFT JOIN practice_blocks pb ON pb.id=r.practice_block_id AND pb.user_id=r.user_id
		WHERE r.user_id=$1 AND r.status <> 'deleted'
		  AND COALESCE(pb.practice_date,timezone('America/Los_Angeles',r.recorded_at)::date)=$2::date
		ORDER BY r.recorded_at,r.id`, userID, date)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]recordingRow, 0)
	for rows.Next() {
		var item recordingRow
		var skills []byte
		var waveform []byte
		if err := rows.Scan(&item.ID, &item.ContentType, &item.Codec, &item.SizeBytes, &item.DurationMS, &item.SampleRate, &item.Channels,
			&item.RecordedAt, &item.Status, &item.TuneID, &item.MissionID, &skills, &item.TakeNumber, &item.Notes,
			&item.SessionID, &item.SessionTitle, &item.BlockID, &item.BlockDate, &item.BlockKey, &item.BlockTitle,
			&item.BlockCategory, &item.BlockTrack, &item.ObjectName, &item.MediaKind, &item.VideoContentType,
			&item.VideoCodec, &item.VideoSizeBytes, &item.VideoWidth, &item.VideoHeight, &item.VideoFrameRate,
			&item.VideoObjectName, &item.FxContentType, &item.FxSizeBytes, &item.FxPreset, &item.FxObjectName, &waveform); err != nil {
			return nil, err
		}
		if err := decodeSkills(skills, &item.SkillIDs); err != nil {
			return nil, fmt.Errorf("decode recording skills: %w", err)
		}
		if err := json.Unmarshal(waveform, &item.WaveformPeaks); err != nil {
			return nil, fmt.Errorf("decode recording waveform: %w", err)
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func decodeSkills(value []byte, destination *[]string) error {
	if len(value) == 0 {
		*destination = []string{}
		return nil
	}
	return json.Unmarshal(value, destination)
}
