package main

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Production already has the out-of-band line's tables (its migrations
// 021-025, now 039-043) with data in them, and every start replays every
// migration. Replaying must keep that data exactly, and the only change it
// may make is filling practice_time_block_id on takes saved while main's API
// (which does not know the column) was deployed.
func TestRestoredOutOfBandMigrationsReplayWithoutChangingData(t *testing.T) {
	url := os.Getenv("JAZZ_LAYOUT_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set JAZZ_LAYOUT_TEST_DATABASE_URL to a disposable PostgreSQL database")
	}
	ctx := context.Background()
	root, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	schema := "restored_oob_" + uuid.New().String()[:8]
	if _, err = root.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer root.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
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

	user, session := uuid.New(), uuid.New()
	source, destination, later := uuid.New(), uuid.New(), uuid.New()
	moved, mainEra := uuid.New(), uuid.New()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO app_users(id,auth_subject) VALUES ($1,'restored-oob')`, user)
	exec(`INSERT INTO practice_sessions(id,user_id,title,started_at,ended_at,status) VALUES ($1,$2,'Daily','2026-10-02','2026-10-02T23:00:00Z','completed')`, session, user)
	exec(`INSERT INTO practice_blocks(id,session_id,user_id,practice_date,block_key,position,title,category,track,target_minutes,elapsed_ms) VALUES
		($1,$4,$5,'2026-10-02','long-tones',0,'Long tones','fundamentals','sound',30,600000),
		($2,$4,$5,'2026-10-02','scales',1,'Scales','fundamentals','technique',30,0),
		($3,$4,$5,'2026-10-02','etude',2,'Etude','repertoire','reading',30,0)`, source, destination, later, session, user)
	// A take recorded under Long tones and moved to Scales on the out-of-band line.
	exec(`INSERT INTO recordings(id,user_id,practice_session_id,practice_block_id,practice_time_block_id,section_assignment_revision,bucket,object_name,object_generation,checksum,content_type,expected_size_bytes,size_bytes,duration_ms,recorded_at,status,client_capture_payload)
		VALUES ($1,$2,$3,$4,$5,1,'test','restored/moved.wav',7,'abc','audio/wav',100,100,600000,'2026-10-02T10:00:00Z','ready','{"clientCaptureId":"x"}')`, moved, user, session.String(), destination, source)
	// A take saved by main's API, which never set practice_time_block_id.
	exec(`INSERT INTO recordings(id,user_id,practice_session_id,practice_block_id,bucket,object_name,content_type,expected_size_bytes,size_bytes,duration_ms,recorded_at,status)
		VALUES ($1,$2,$3,$4,'test','restored/main-era.wav','audio/wav',100,100,1000,'2026-10-02T11:00:00Z','ready')`, mainEra, user, session.String(), later)
	exec(`INSERT INTO long_tone_analysis_jobs(id,recording_id,user_id,asset,source_object_name,source_generation,source_checksum,analysis_version,status,duration_ms)
		VALUES ($1,$2,$3,'audio','restored/moved.wav',7,'abc','long-tone-yin-v1','ready',600000)`, uuid.New(), moved, user)
	exec(`INSERT INTO recording_caption_jobs(id,recording_id,user_id,source_object_name,source_generation,source_checksum,analysis_version,status,segments)
		VALUES ($1,$2,$3,'restored/moved.wav',7,'abc','v1','ready','[{"startMs":0,"endMs":900,"text":"Warm-up"}]')`, uuid.New(), moved, user)
	exec(`INSERT INTO training_attempts(user_id,id,payload) VALUES ($1,$2,'{"key":"C"}')`, user, uuid.New())

	snapshot := func() string {
		t.Helper()
		var out string
		err := db.QueryRow(ctx, `SELECT concat_ws('|',
			(SELECT json_agg(r ORDER BY r.id)::text FROM recordings r),
			(SELECT json_agg(b ORDER BY b.id)::text FROM practice_blocks b),
			(SELECT json_agg(j ORDER BY j.id)::text FROM long_tone_analysis_jobs j),
			(SELECT json_agg(c ORDER BY c.id)::text FROM recording_caption_jobs c),
			(SELECT json_agg(a ORDER BY a.id)::text FROM training_attempts a))`).Scan(&out)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}

	if err := migrate(ctx, db); err != nil {
		t.Fatalf("first replay: %v", err)
	}
	var timeBlock uuid.UUID
	if err := db.QueryRow(ctx, `SELECT practice_time_block_id FROM recordings WHERE id=$1`, mainEra).Scan(&timeBlock); err != nil || timeBlock != later {
		t.Fatalf("main-era take time attribution = %v, %v; want its own section", timeBlock, err)
	}
	if err := db.QueryRow(ctx, `SELECT practice_time_block_id FROM recordings WHERE id=$1`, moved).Scan(&timeBlock); err != nil || timeBlock != source {
		t.Fatalf("moved take time attribution = %v, %v; want the section it was recorded in", timeBlock, err)
	}
	var destinationMS int
	if err := db.QueryRow(ctx, `SELECT elapsed_ms FROM practice_blocks WHERE id=$1`, destination).Scan(&destinationMS); err != nil || destinationMS != 0 {
		t.Fatalf("destination section practice time = %d, %v; a move must not credit it", destinationMS, err)
	}
	before := snapshot()
	for i := 0; i < 2; i++ {
		if err := migrate(ctx, db); err != nil {
			t.Fatalf("replay %d: %v", i+2, err)
		}
	}
	if after := snapshot(); after != before {
		t.Fatalf("replaying migrations changed data:\nbefore %s\nafter  %s", before, after)
	}
}
