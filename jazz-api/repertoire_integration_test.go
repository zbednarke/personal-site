package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRepertoireIntegration(t *testing.T) {
	url := os.Getenv("JAZZ_LAYOUT_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set JAZZ_LAYOUT_TEST_DATABASE_URL to a disposable PostgreSQL database")
	}
	ctx := context.WithValue(context.Background(), userSubjectKey, "repertoire-test")
	db, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	schema := "repertoire_test_" + uuid.New().String()[:8]
	if _, err = db.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer db.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	isolated, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer isolated.Close()
	for range 2 {
		if err := migrate(ctx, isolated); err != nil {
			t.Fatal(err)
		}
	}
	app := &application{db: isolated, logger: slog.Default()}
	userID, err := app.userID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := isolated.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	scalar := func(dest any, sql string, args ...any) {
		t.Helper()
		if err := isolated.QueryRow(ctx, sql, args...).Scan(dest); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	call := func(handler http.HandlerFunc, method, target string, path map[string]string, body any, want int) []byte {
		t.Helper()
		var reader *bytes.Reader
		if body == nil {
			reader = bytes.NewReader(nil)
		} else {
			encoded, _ := json.Marshal(body)
			reader = bytes.NewReader(encoded)
		}
		r := httptest.NewRequest(method, target, reader).WithContext(ctx)
		for key, value := range path {
			r.SetPathValue(key, value)
		}
		w := httptest.NewRecorder()
		handler(w, r)
		if w.Code != want {
			t.Fatalf("%s %s: got %d want %d: %s", method, target, w.Code, want, w.Body.String())
		}
		return w.Body.Bytes()
	}
	sessionID := uuid.New()
	exec(`INSERT INTO practice_sessions (id,user_id,title,started_at,status) VALUES ($1,$2,'Test',now(),'active')`, sessionID, userID)

	// 1. The one-time backfill links pre-existing blocks and never undoes a later unlink.
	exec(`DELETE FROM jazz_backfills`)
	guideTones, autumn, prelude := uuid.New(), uuid.New(), uuid.New()
	for id, key := range map[uuid.UUID]string{guideTones: "blue-bossa-guide-tones", autumn: "custom-autumn", prelude: "easy-to-love-transcription-2026-09-10"} {
		exec(`INSERT INTO practice_blocks (id,session_id,user_id,practice_date,block_key,position,title,category,track,target_minutes,elapsed_ms) VALUES ($1,$2,$3,'2026-09-10',$4,0,$4,'repertoire','musician',10,60000)`, id, sessionID, userID, key)
	}
	exec(`INSERT INTO recordings (id,user_id,practice_session_id,practice_block_id,bucket,object_name,content_type,expected_size_bytes,duration_ms,recorded_at,status,tune_id) VALUES ($1,$2,$3,$4,'test','a.wav','audio/wav',10,90000,now(),'ready','autumn-leaves')`, uuid.New(), userID, sessionID.String(), autumn)
	if err := migrate(ctx, isolated); err != nil {
		t.Fatal(err)
	}
	var linked string
	for id, want := range map[uuid.UUID]string{guideTones: "blue-bossa", autumn: "autumn-leaves", prelude: "prelude-to-a-kiss"} {
		scalar(&linked, `SELECT COALESCE(tune_id,'') FROM practice_blocks WHERE id=$1`, id)
		if linked != want {
			t.Fatalf("backfill linked %s to %q, want %q", id, linked, want)
		}
	}
	exec(`UPDATE practice_blocks SET tune_id=NULL WHERE id=$1`, guideTones)
	if err := migrate(ctx, isolated); err != nil {
		t.Fatal(err)
	}
	scalar(&linked, `SELECT COALESCE(tune_id,'') FROM practice_blocks WHERE id=$1`, guideTones)
	var markers int
	scalar(&markers, `SELECT COUNT(*) FROM jazz_backfills WHERE name='024_practice_block_tunes'`)
	if linked != "" || markers != 1 {
		t.Fatalf("restart re-ran the backfill: %q %d", linked, markers)
	}
	exec(`UPDATE practice_blocks SET tune_id='blue-bossa' WHERE id=$1`, guideTones)

	// 2. Seeding is lazy, one-time and imports legacy roadmap stages.
	exec(`INSERT INTO campaign_state (user_id,data,revision) VALUES ($1,$2,1)`, userID, `{"version":1,"repertoire":{"blue-bossa":3,"solar":1,"summertime":0}}`)
	load := func(today string) repertoireResponse {
		t.Helper()
		var response repertoireResponse
		if err := json.Unmarshal(call(app.getRepertoire, "GET", "/?today="+today, nil, nil, 200), &response); err != nil {
			t.Fatal(err)
		}
		return response
	}
	find := func(response repertoireResponse, tuneID string) *repertoireTune {
		for index := range response.Tunes {
			if response.Tunes[index].TuneID == tuneID {
				return &response.Tunes[index]
			}
		}
		return nil
	}
	first := load("2026-10-09")
	if len(first.Tunes) != 25 { // 23 seeds + Solar (legacy stage 1) + Prelude (backfilled practice)
		t.Fatalf("seeded %d tunes", len(first.Tunes))
	}
	blue, solar := find(first, "blue-bossa"), find(first, "solar")
	if blue == nil || blue.Milestones["melodyByEar"] != "learning" || blue.Milestones["changes"] != "learning" || blue.Milestones["improvise"] != "not_started" || blue.SessionCount != 1 {
		t.Fatalf("blue bossa import: %+v", blue)
	}
	if solar == nil || solar.Category != "standard" || solar.Chosen || solar.Milestones["melodyByEar"] != "learning" {
		t.Fatalf("solar import: %+v", solar)
	}
	if find(first, "summertime") != nil || find(first, "prelude-to-a-kiss") == nil || find(first, "prelude-to-a-kiss").Chosen {
		t.Fatal("conditional legacy rows are wrong")
	}
	if autumnTune := find(first, "autumn-leaves"); autumnTune.TotalPracticeMS != 90000 || autumnTune.TakeCount != 1 || autumnTune.PracticeStatus != "learning" {
		t.Fatalf("autumn leaves practice: %+v", autumnTune)
	}
	if first.Settings.SetTargetDate != "2027-03-20" || first.Week.Start != "2026-10-05" {
		t.Fatalf("settings/week: %+v %+v", first.Settings, first.Week)
	}
	archive := func(tuneID string, revision int64) {
		call(app.updateRepertoireTune, "PATCH", "/", map[string]string{"tuneId": tuneID}, map[string]any{"expectedRevision": revision, "clientMutationId": uuid.NewString(), "archived": true}, 200)
	}
	archive("golden-hour", find(first, "golden-hour").Revision)
	second := load("2026-10-09")
	if len(second.Tunes) != 24 || find(second, "golden-hour") != nil {
		t.Fatal("archived seed came back or a seed was duplicated")
	}

	// 3. Practice on a linked block derives the tune's state with no manual entry.
	bootstrap := func(date, mode string, blocks []blockDefinition, want int) []practiceBlock {
		t.Helper()
		body := call(app.bootstrapPracticeBlocks, "POST", "/", map[string]string{"id": sessionID.String()}, bootstrapBlocksRequest{Mode: mode, PracticeDate: date, Blocks: blocks}, want)
		var result struct {
			Blocks []practiceBlock `json:"blocks"`
		}
		_ = json.Unmarshal(body, &result)
		return result.Blocks
	}
	skylarkDef := blockDefinition{BlockKey: "tune-skylark-abc123", Title: "Skylark: melody by ear", Category: "repertoire", Track: "musician", TargetMinutes: 15, TuneID: "skylark"}
	blocks := bootstrap("2026-10-09", "add", []blockDefinition{skylarkDef}, 200)
	var skylarkBlock practiceBlock
	for _, block := range blocks {
		if block.BlockKey == skylarkDef.BlockKey {
			skylarkBlock = block
		}
	}
	if skylarkBlock.TuneID != "skylark" {
		t.Fatalf("bootstrap lost the tune link: %+v", blocks)
	}
	call(app.updatePracticeBlock, "PATCH", "/", map[string]string{"id": skylarkBlock.ID.String()}, map[string]any{"elapsedMs": 30000}, 200)
	exec(`INSERT INTO recordings (id,user_id,practice_session_id,practice_block_id,bucket,object_name,content_type,expected_size_bytes,duration_ms,recorded_at,status) VALUES ($1,$2,$3,$4,'test','b.wav','audio/wav',10,120000,now(),'ready')`, uuid.New(), userID, sessionID.String(), skylarkBlock.ID)
	practiced := find(load("2026-10-09"), "skylark")
	if practiced.PracticeStatus != "learning" || practiced.LastPracticedDate != "2026-10-09" || practiced.WeekPracticeMS != 120000 || practiced.TakeCount != 1 || practiced.Milestones["melodyByEar"] != "not_started" {
		t.Fatalf("skylark after practice: %+v", practiced)
	}
	if week := load("2026-10-09").Week; week.PracticeMS != 120000 || week.JazzPracticeMS != 120000 {
		t.Fatalf("week practice split: %+v", week)
	}
	if find(load("2026-10-12"), "skylark").WeekPracticeMS != 0 {
		t.Fatal("last week's practice counted this week")
	}

	// 4. Carry-forward keeps the link on the next practice day.
	next := bootstrap("2026-10-10", "initialize", []blockDefinition{{BlockKey: "warmup", Title: "Warmup", Category: "technique", Track: "trumpet", TargetMinutes: 10}}, 200)
	carried := false
	for _, block := range next {
		carried = carried || (block.BlockKey == skylarkDef.BlockKey && block.TuneID == "skylark")
	}
	if !carried {
		t.Fatalf("carry-forward dropped the tune link: %+v", next)
	}

	// 5. Revisions protect concurrent edits; mutation ids make retries idempotent.
	solid := "solid"
	mutation := uuid.NewString()
	edit := map[string]any{"expectedRevision": practiced.Revision, "clientMutationId": mutation, "practiceBlockId": skylarkBlock.ID.String(), "milestones": map[string]any{"melodyByEar": solid}, "keysKnown": []string{"Eb", "F"}}
	var edited repertoireTune
	_ = json.Unmarshal(call(app.updateRepertoireTune, "PATCH", "/?today=2026-10-09", map[string]string{"tuneId": "skylark"}, edit, 200), &edited)
	if edited.Revision != practiced.Revision+1 || edited.Milestones["melodyByEar"] != "solid" || edited.Milestones["keysKnown"] != "solid" {
		t.Fatalf("patch: %+v", edited)
	}
	call(app.updateRepertoireTune, "PATCH", "/", map[string]string{"tuneId": "skylark"}, edit, 200)
	var eventCount int
	var revision int64
	scalar(&eventCount, `SELECT COUNT(*) FROM repertoire_events WHERE tune_id='skylark' AND practice_block_id=$1`, skylarkBlock.ID)
	scalar(&revision, `SELECT revision FROM repertoire_tunes WHERE tune_id='skylark'`)
	if eventCount != 3 || revision != edited.Revision {
		t.Fatalf("retry was not idempotent: %d events, revision %d", eventCount, revision)
	}
	stale := map[string]any{"expectedRevision": practiced.Revision, "clientMutationId": uuid.NewString(), "title": "Skylark!"}
	var conflict struct {
		Tune repertoireTune `json:"tune"`
	}
	_ = json.Unmarshal(call(app.updateRepertoireTune, "PATCH", "/", map[string]string{"tuneId": "skylark"}, stale, 409), &conflict)
	if conflict.Tune.Revision != edited.Revision {
		t.Fatal("conflict did not return the current tune")
	}

	// 6. Unknown tunes are rejected when added and dropped during initialization.
	bootstrap("2026-10-10", "add", []blockDefinition{{BlockKey: "custom-x", Title: "X", Category: "repertoire", Track: "musician", TargetMinutes: 5, TuneID: "not-a-tune"}}, 422)
	bootstrap("2026-10-10", "add", []blockDefinition{{BlockKey: "custom-gh", Title: "GH", Category: "repertoire", Track: "musician", TargetMinutes: 5, TuneID: "golden-hour"}}, 422)
	dated := bootstrap("2026-10-10", "initialize", []blockDefinition{{BlockKey: "dated", Title: "Dated", Category: "repertoire", Track: "musician", TargetMinutes: 5, TuneID: "not-a-tune", DayOnly: true}}, 200)
	for _, block := range dated {
		if block.BlockKey == "dated" && block.TuneID != "" {
			t.Fatal("initialize kept an unknown tune")
		}
	}
	call(app.updatePracticeBlock, "PATCH", "/", map[string]string{"id": skylarkBlock.ID.String()}, map[string]any{"tuneId": "not-a-tune"}, 422)

	// 7. A removed section still counts in the tune's history.
	exec(`UPDATE practice_blocks SET removed_at=now() WHERE id=$1`, skylarkBlock.ID)
	if find(load("2026-10-09"), "skylark").TotalPracticeMS != 120000 {
		t.Fatal("removing the section erased tune practice")
	}
	var history struct {
		Days   []tuneHistoryDay   `json:"days"`
		Events []tuneHistoryEvent `json:"events"`
	}
	_ = json.Unmarshal(call(app.repertoireTuneHistory, "GET", "/", map[string]string{"tuneId": "skylark"}, nil, 200), &history)
	if len(history.Days) != 1 || !history.Days[0].Blocks[0].Removed || history.Days[0].Minutes != 2 || len(history.Days[0].Recordings) != 1 {
		t.Fatalf("history days: %+v", history.Days)
	}
	if len(history.Events) == 0 || history.Events[0].PracticeDate != "2026-10-09" {
		t.Fatalf("history events do not link to the practice day: %+v", history.Events)
	}

	// A take belongs to its section's tune, so relinking the section moves it,
	// and the tune card and history agree.
	relinked := uuid.New()
	exec(`INSERT INTO practice_blocks (id,session_id,user_id,practice_date,block_key,position,title,category,track,target_minutes,elapsed_ms,tune_id) VALUES ($1,$2,$3,'2026-10-09','relink',9,'Relink','repertoire','musician',10,0,'old-folks')`, relinked, sessionID, userID)
	exec(`INSERT INTO recordings (id,user_id,practice_session_id,practice_block_id,bucket,object_name,content_type,expected_size_bytes,duration_ms,recorded_at,status,tune_id) VALUES ($1,$2,$3,$4,'test','c.wav','audio/wav',10,60000,now(),'ready','old-folks')`, uuid.New(), userID, sessionID.String(), relinked)
	call(app.updatePracticeBlock, "PATCH", "/", map[string]string{"id": relinked.String()}, map[string]any{"tuneId": "bye-bye-blackbird"}, 200)
	afterRelink := load("2026-10-09")
	if find(afterRelink, "old-folks").TakeCount != 0 || find(afterRelink, "bye-bye-blackbird").TakeCount != 1 || find(afterRelink, "bye-bye-blackbird").TotalPracticeMS != 60000 {
		t.Fatalf("relinked take: old folks %+v blackbird %+v", find(afterRelink, "old-folks"), find(afterRelink, "bye-bye-blackbird"))
	}
	var oldFolks struct {
		Days []tuneHistoryDay `json:"days"`
	}
	_ = json.Unmarshal(call(app.repertoireTuneHistory, "GET", "/", map[string]string{"tuneId": "old-folks"}, nil, 200), &oldFolks)
	if len(oldFolks.Days) != 0 {
		t.Fatalf("relinked take still in the old tune's history: %+v", oldFolks.Days)
	}
	if week := load("2026-10-09").Week; week.RepertoirePracticeMS != 180000 || week.JazzPracticeMS != 180000 {
		t.Fatalf("week repertoire share: %+v", week)
	}

	// 8. Orphan tune practice is reported and can be adopted with its exact id.
	exec(`INSERT INTO practice_blocks (id,session_id,user_id,practice_date,block_key,position,title,category,track,target_minutes,elapsed_ms,tune_id) VALUES ($1,$2,$3,'2026-10-08','old-tune',5,'Old tune','repertoire','musician',10,300000,'mystery-tune')`, uuid.New(), sessionID, userID)
	orphans := load("2026-10-09").Unlinked
	if len(orphans) != 1 || orphans[0].TuneID != "mystery-tune" || orphans[0].TotalPracticeMS != 300000 {
		t.Fatalf("unlinked practice: %+v", orphans)
	}
	var adopted repertoireTune
	create := map[string]any{"tuneId": "mystery-tune", "title": "Mystery Tune", "category": "standard", "clientMutationId": uuid.NewString()}
	_ = json.Unmarshal(call(app.createRepertoireTune, "POST", "/?today=2026-10-09", nil, create, 201), &adopted)
	if adopted.TotalPracticeMS != 300000 || adopted.PracticeStatus != "learning" {
		t.Fatalf("adopted tune: %+v", adopted)
	}
	call(app.createRepertoireTune, "POST", "/", nil, create, 200)
	if len(load("2026-10-09").Unlinked) != 0 {
		t.Fatal("adopted practice is still unlinked")
	}

	// Creating over an archived slug offers a restore; active collisions get a suffix.
	var archivedConflict map[string]any
	_ = json.Unmarshal(call(app.createRepertoireTune, "POST", "/", nil, map[string]any{"title": "Golden Hour", "category": "pop", "clientMutationId": uuid.NewString()}, 409), &archivedConflict)
	if archivedConflict["archived"] != true || archivedConflict["tuneId"] != "golden-hour" {
		t.Fatalf("archived conflict: %v", archivedConflict)
	}
	var duplicate repertoireTune
	_ = json.Unmarshal(call(app.createRepertoireTune, "POST", "/", nil, map[string]any{"title": "Skylark", "category": "ballad", "chosen": false, "clientMutationId": uuid.NewString()}, 201), &duplicate)
	if duplicate.TuneID != "skylark-2" || duplicate.Chosen || duplicate.Position != 11 {
		t.Fatalf("suffixed duplicate: %+v", duplicate)
	}
}
