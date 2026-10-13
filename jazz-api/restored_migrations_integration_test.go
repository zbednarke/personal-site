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
	source, destination, later, warmUp, bossa := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	moved, mainEra, intoBossa, outOfBossa := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO app_users(id,auth_subject) VALUES ($1,'restored-oob')`, user)
	exec(`INSERT INTO practice_sessions(id,user_id,title,started_at,ended_at,status) VALUES ($1,$2,'Daily','2026-10-02','2026-10-02T23:00:00Z','completed')`, session, user)
	exec(`INSERT INTO practice_blocks(id,session_id,user_id,practice_date,block_key,position,title,category,track,target_minutes,elapsed_ms) VALUES
		($1,$6,$7,'2026-10-02','long-tones',0,'Long tones','fundamentals','sound',30,600000),
		($2,$6,$7,'2026-10-02','scales',1,'Scales','fundamentals','technique',30,0),
		($3,$6,$7,'2026-10-02','etude',2,'Etude','repertoire','reading',30,1000),
		($4,$6,$7,'2026-10-02','warm-up',3,'Warm-up','fundamentals','sound',10,60000),
		($5,$6,$7,'2026-10-02','blue-bossa-play',4,'Blue Bossa','repertoire','jazz',20,60000)`,
		source, destination, later, warmUp, bossa, session, user)
	// A take recorded under Long tones and moved to Scales on the out-of-band line.
	exec(`INSERT INTO recordings(id,user_id,practice_session_id,practice_block_id,practice_time_block_id,section_assignment_revision,bucket,object_name,object_generation,checksum,content_type,expected_size_bytes,size_bytes,duration_ms,recorded_at,status,client_capture_payload)
		VALUES ($1,$2,$3,$4,$5,1,'test','restored/moved.wav',7,'abc','audio/wav',100,100,600000,'2026-10-02T10:00:00Z','ready','{"clientCaptureId":"x"}')`, moved, user, session.String(), destination, source)
	// Takes moved into and out of Blue Bossa, as production holds them now: a
	// take belongs to its section's tune, so the one filed in Blue Bossa is
	// tagged blue-bossa and the one moved out of it is not.
	exec(`INSERT INTO recordings(id,user_id,practice_session_id,practice_block_id,practice_time_block_id,section_assignment_revision,bucket,object_name,content_type,expected_size_bytes,size_bytes,duration_ms,recorded_at,status,tune_id)
		VALUES ($1,$3,$4,$5,$6,1,'test','restored/into-bossa.wav','audio/wav',100,100,60000,'2026-10-02T12:00:00Z','ready','blue-bossa'),
		       ($2,$3,$4,$6,$5,1,'test','restored/out-of-bossa.wav','audio/wav',100,100,60000,'2026-10-02T13:00:00Z','ready',NULL)`,
		intoBossa, outOfBossa, user, session.String(), bossa, warmUp)
	// A take saved by main's API, which never set practice_time_block_id.
	exec(`INSERT INTO recordings(id,user_id,practice_session_id,practice_block_id,bucket,object_name,content_type,expected_size_bytes,size_bytes,duration_ms,recorded_at,status)
		VALUES ($1,$2,$3,$4,'test','restored/main-era.wav','audio/wav',100,100,1000,'2026-10-02T11:00:00Z','ready')`, mainEra, user, session.String(), later)
	exec(`INSERT INTO long_tone_analysis_jobs(id,recording_id,user_id,asset,source_object_name,source_generation,source_checksum,analysis_version,status,duration_ms)
		VALUES ($1,$2,$3,'audio','restored/moved.wav',7,'abc','long-tone-yin-v1','ready',600000)`, uuid.New(), moved, user)
	exec(`INSERT INTO recording_caption_jobs(id,recording_id,user_id,source_object_name,source_generation,source_checksum,analysis_version,status,segments)
		VALUES ($1,$2,$3,'restored/moved.wav',7,'abc','v1','ready','[{"startMs":0,"endMs":900,"text":"Warm-up"}]')`, uuid.New(), moved, user)
	exec(`INSERT INTO training_attempts(user_id,id,payload) VALUES ($1,$2,'{"key":"C"}')`, user, uuid.New())

	// Everything, except the one column the first replay is expected to fill.
	snapshot := func() string {
		t.Helper()
		var out string
		err := db.QueryRow(ctx, `SELECT concat_ws('|',
			(SELECT json_agg(to_jsonb(r) - 'practice_time_block_id' ORDER BY r.id)::text FROM recordings r),
			(SELECT json_agg(b ORDER BY b.id)::text FROM practice_blocks b),
			(SELECT json_agg(j ORDER BY j.id)::text FROM long_tone_analysis_jobs j),
			(SELECT json_agg(c ORDER BY c.id)::text FROM recording_caption_jobs c),
			(SELECT json_agg(a ORDER BY a.id)::text FROM training_attempts a))`).Scan(&out)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	timeBlocks := func() map[uuid.UUID]uuid.UUID {
		t.Helper()
		out := map[uuid.UUID]uuid.UUID{}
		rows, err := db.Query(ctx, `SELECT id,COALESCE(practice_time_block_id,'00000000-0000-0000-0000-000000000000') FROM recordings`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var id, block uuid.UUID
			if err := rows.Scan(&id, &block); err != nil {
				t.Fatal(err)
			}
			out[id] = block
		}
		return out
	}

	before := snapshot()
	for i := 1; i <= 3; i++ {
		if err := migrate(ctx, db); err != nil {
			t.Fatalf("replay %d: %v", i, err)
		}
		if after := snapshot(); after != before {
			t.Fatalf("replay %d changed data:\nbefore %s\nafter  %s", i, before, after)
		}
		want := map[uuid.UUID]uuid.UUID{moved: source, intoBossa: warmUp, outOfBossa: bossa, mainEra: later}
		if got := timeBlocks(); len(got) != len(want) {
			t.Fatalf("replay %d: time attribution %v", i, got)
		} else {
			for id, block := range want {
				if got[id] != block {
					t.Fatalf("replay %d: take %s time attribution %s, want %s", i, id, got[id], block)
				}
			}
		}
	}
	var destinationMS int
	if err := db.QueryRow(ctx, `SELECT elapsed_ms FROM practice_blocks WHERE id=$1`, destination).Scan(&destinationMS); err != nil || destinationMS != 0 {
		t.Fatalf("destination section practice time = %d, %v; a move must not credit it", destinationMS, err)
	}
}
