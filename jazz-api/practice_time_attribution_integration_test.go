package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// A take moved to another section keeps its practice time where it was
// recorded. Every reader of practice time (Archive, Sessions, Repertoire week
// and per tune, tune history, the section list the Workbench reports from)
// must agree, and none may count the take a second time where it is filed.
func TestMovedTakePracticeTimeAgreesEverywhere(t *testing.T) {
	url := os.Getenv("JAZZ_LAYOUT_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set JAZZ_LAYOUT_TEST_DATABASE_URL to a disposable PostgreSQL database")
	}
	ctx := context.WithValue(context.Background(), userSubjectKey, "attribution-test")
	root, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	schema := "attribution_" + uuid.New().String()[:8]
	if _, err = root.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer root.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	db, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	app := &application{db: db, logger: slog.Default()}
	user, err := app.userID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	const day, week = "2026-10-07", "2026-10-05"
	const takeMS = 600000
	session, recordedIn, filedIn := uuid.New(), uuid.New(), uuid.New()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO repertoire_settings(user_id) VALUES ($1) ON CONFLICT DO NOTHING`, user)
	exec(`INSERT INTO practice_sessions(id,user_id,title,started_at) VALUES ($1,$2,'Daily',$3::date)`, session, user, day)
	exec(`INSERT INTO practice_blocks(id,session_id,user_id,practice_date,block_key,position,title,category,track,target_minutes,elapsed_ms,tune_id) VALUES
		($1,$3,$4,$5::date,'tune-a',0,'Tune A','repertoire','jazz',30,0,'tune-a'),
		($2,$3,$4,$5::date,'tune-b',1,'Tune B','repertoire','jazz',30,0,'tune-b')`, recordedIn, filedIn, session, user, day)
	exec(`INSERT INTO recordings(id,user_id,practice_session_id,practice_block_id,practice_time_block_id,section_assignment_revision,bucket,object_name,content_type,expected_size_bytes,size_bytes,duration_ms,recorded_at,status)
		VALUES ($1,$2,$3,$4,$5,1,'test','attribution/take.wav','audio/wav',100,100,$6,$7::date + time '18:00','ready')`,
		uuid.New(), user, session.String(), filedIn, recordedIn, takeMS, day)

	// Archive calendar.
	from, to := mustDate(t, day), mustDate(t, day)
	days, err := app.loadArchiveCalendar(ctx, user, from, to)
	if err != nil || len(days) != 1 || days[0].TotalDurationMS != takeMS {
		t.Fatalf("archive day = %+v, %v; want %d", days, err, takeMS)
	}
	// Sessions list.
	w := httptest.NewRecorder()
	app.listPracticeSessions(w, httptest.NewRequest("GET", "/", nil).WithContext(ctx))
	var sessions struct {
		Sessions []practiceSession `json:"sessions"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &sessions); err != nil || len(sessions.Sessions) != 1 || sessions.Sessions[0].TotalDurationMS != takeMS {
		t.Fatalf("sessions = %s, %v; want %d", w.Body.String(), err, takeMS)
	}
	// Repertoire: per tune this week, and the week's total.
	practice, err := loadTunePractice(ctx, db, user, week, "")
	if err != nil || practice["tune-a"].WeekPracticeMS != takeMS || practice["tune-b"].WeekPracticeMS != 0 {
		t.Fatalf("tune practice = %+v, %v; want all %d on tune-a", practice, err, takeMS)
	}
	rep, err := app.loadRepertoire(ctx, user, day, false)
	if err != nil || rep.Week.PracticeMS != takeMS {
		t.Fatalf("repertoire week = %+v, %v; want %d", rep.Week, err, takeMS)
	}
	// Tune history days.
	for tune, want := range map[string]int64{"tune-a": takeMS, "tune-b": 0} {
		history, _, err := app.loadTuneHistoryDays(ctx, user, tune, "", 30)
		if err != nil {
			t.Fatal(err)
		}
		var got int64
		for _, d := range history {
			got += d.PracticeMS
		}
		if got != want {
			t.Fatalf("%s history practice = %d, want %d", tune, got, want)
		}
	}
	// Sections, as the Today page and the Workbench report read them.
	blocks, err := app.loadPracticeBlocks(ctx, user, session, day)
	if err != nil {
		t.Fatal(err)
	}
	var total int
	for _, b := range blocks {
		want := 0
		if b.ID == recordedIn {
			want = takeMS
		}
		if b.ElapsedMS != want {
			t.Fatalf("section %s practice = %d, want %d", b.Title, b.ElapsedMS, want)
		}
		total += b.ElapsedMS
	}
	if total != takeMS {
		t.Fatalf("sections total %d, want %d", total, takeMS)
	}
}
