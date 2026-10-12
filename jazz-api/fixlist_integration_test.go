package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

// All fixture data is fictional.

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

type fixFixture struct {
	t    *testing.T
	h    *wbHarness
	user uuid.UUID
	horn uuid.UUID
	ctx  context.Context
}

func newFixFixture(t *testing.T) *fixFixture {
	h := newWBHarness(t, nil)
	ctx := context.Background()
	user, err := h.app.userID(context.WithValue(ctx, userSubjectKey, "owner"))
	if err != nil {
		t.Fatal(err)
	}
	f := &fixFixture{t: t, h: h, user: user, horn: uuid.New(), ctx: ctx}
	f.exec(`INSERT INTO trumpet_horns(id,user_id,maker,model) VALUES($1,$2,'Fixture','Model A')`, f.horn, user)
	return f
}

func (f *fixFixture) exec(sql string, args ...any) {
	f.t.Helper()
	if _, err := f.h.app.db.Exec(f.ctx, sql, args...); err != nil {
		f.t.Fatalf("%s: %v", sql, err)
	}
}

func (f *fixFixture) run(external, status, errText string, started time.Time, sources ...[3]string) uuid.UUID {
	id := uuid.New()
	f.exec(`INSERT INTO trumpet_runs(id,user_id,external_id,kind,status,error,started_at,completed_at) VALUES($1,$2,$3,'combined',$4,$5,$6,$6::timestamptz+interval '30 minutes')`, id, f.user, external, status, errText, started)
	for _, s := range sources {
		f.exec(`INSERT INTO trumpet_run_sources(run_id,source,status,note,domain) VALUES($1,$2,$3,$4,$5)`, id, s[0], s[1], s[2], strings.TrimPrefix(s[0], "Live pages / "))
	}
	return id
}

func (f *fixFixture) listing(verification, lastError string, attempt time.Time) uuid.UUID {
	id := uuid.New()
	f.exec(`INSERT INTO trumpet_listings(id,user_id,horn_id,source,title,status,discovery_type,verification_state,last_check_attempt,last_check_error,check_failures,canonical_url)
		VALUES($1,$2,$3,'Fixture shop','Fixture listing (private title)','active','new listing',$4,$5,$6,1,$7)`, id, f.user, f.horn, verification, attempt, lastError, "https://shop.example/"+id.String())
	return id
}

func (f *fixFixture) itemsByKey(d fixDigest) map[string]fixItem {
	out := map[string]fixItem{}
	for _, it := range d.Items {
		out[it.Key] = it
	}
	return out
}

func TestFixDigestFromTheDatabase(t *testing.T) {
	f := newFixFixture(t)
	now := time.Date(2026, 10, 12, 15, 10, 0, 0, time.UTC)
	today := time.Date(2026, 10, 12, 13, 17, 0, 0, time.UTC)
	// Today's daily run was partial; yesterday's succeeded; a hand-submitted
	// run failed today.
	f.run("adaptive-watch-today", "partial", "Reverb API search failed; Recheck failed, recorded for retry: shop.example / timed out; Discovery page budget (240) reached; remaining promising results retained as unverified leads", today,
		[3]string{"Reverb API", "failed", "Reverb API failed: HTTP 429"},
		[3]string{"eBay Browse API", "checked", "Run time budget reached. 3 searches"},
		[3]string{"Live pages / lead.example", "skipped", "Unverified search lead discovered; no live page accessed."},
		[3]string{"Maker search: Fixture", "failed", "Search provider failed: TimeoutError"})
	f.run("adaptive-watch-yesterday", "succeeded", "", today.AddDate(0, 0, -1),
		[3]string{"Reverb API", "checked", "Request budget reached before every term was searched. 6 searches"},
		[3]string{"Maker search: Fixture", "failed", "Search provider failed: TimeoutError"})
	f.run("chatgpt-manual-1", "failed", "client stopped", now.Add(-2*time.Hour))
	// Failed checks: two verified listings on one domain/reason, one lead,
	// one budget-capped check, one listing that later succeeded (ignored).
	f.listing("verified", "shop.example / timed out", now.Add(-90*time.Minute))
	f.listing("verified", "shop.example / timed out", now.Add(-80*time.Minute))
	f.listing("candidate", "lead.example / skipped; source blocked automated checks earlier in this run", now.Add(-70*time.Minute))
	f.listing("verified", "blocked.example / search-provider check budget reached", now.Add(-60*time.Minute))
	recovered := f.listing("verified", "fine.example / HTTP 503", now.Add(-5*time.Hour))
	f.exec(`UPDATE trumpet_listings SET last_checked=$2 WHERE id=$1`, recovered, now.Add(-time.Hour))
	gone := f.listing("verified", "shop.example / timed out", now.Add(-26*time.Hour))
	f.exec(`INSERT INTO trumpet_events(listing_id,user_id,kind,old_status,new_status,meaningful,occurred_at) VALUES($1,$2,'status change','active','unverifiable',false,$3)`, gone, f.user, now.Add(-3*time.Hour))
	// Magic Film: one failure today, one yesterday; one stuck job.
	for i, at := range []time.Time{now.Add(-4 * time.Hour), now.Add(-30 * time.Hour)} {
		f.exec(`INSERT INTO magic_film_jobs(id,user_id,client_id,practice_date,target_seconds,status,error,manifest,finished_at,updated_at) VALUES($1,$2,$3,'2026-10-11',60,'failed',$4,'{}',$5,$5)`,
			uuid.New(), f.user, uuid.New(), []string{"render stopped at 12 s", "render stopped at 40 s"}[i], at)
	}
	f.exec(`INSERT INTO magic_film_jobs(id,user_id,client_id,practice_date,target_seconds,status,manifest,updated_at) VALUES($1,$2,$3,'2026-10-12',60,'rendering','{}',$4)`, uuid.New(), f.user, uuid.New(), now.Add(-2*time.Hour))
	// API counters: three 5xx today against one yesterday, two slow today.
	f.exec(`INSERT INTO api_route_counters(hour,service,route,requests,errors_5xx,slow) VALUES
		($1,'jazz-api','POST /v1/studio/renders',40,3,2), ($2,'jazz-api','POST /v1/studio/renders',30,1,0), ($1,'workbench','GET /v1/workbench/threads',90,0,0)`,
		now.Add(-3*time.Hour).Truncate(time.Hour), now.Add(-30*time.Hour).Truncate(time.Hour))

	d, err := f.h.app.computeFixDigest(f.ctx, f.user, now)
	if err != nil {
		t.Fatal(err)
	}
	if !d.Run.Found || d.Run.Missing || d.Run.Status != "partial" || len(d.Run.Notes) != 4 || d.Run.ExpectedDay != "2026-10-12" {
		t.Fatalf("run: %+v", d.Run)
	}
	items := f.itemsByKey(d)
	want := map[string][2]int64{
		"run:partial": {1, 0},
		"run:failed":  {1, 0},
		"sources:failed:Reverb API failed: HTTP 429":                               {1, 0},
		"sources:failed:Search provider failed: TimeoutError":                      {1, 1},
		"sources:skipped:Unverified search lead discovered; no live page accessed": {1, 0},
		"checks:shop.example:timed out":                                            {2, 1},
		"checks:blocked.example:search-provider check budget reached":              {1, 0},
		"checks:leads":                              {1, 0},
		"checks:unverifiable":                       {1, 0},
		"market:reverb-budget":                      {0, 1},
		"market:ebay-time":                          {1, 0},
		"market:page-budget":                        {1, 0},
		"market:openai-verify-cap":                  {1, 0},
		"films:failed:render stopped at N s":        {1, 1},
		"films:stuck":                               {1, 0},
		"api:5xx:jazz-api:POST /v1/studio/renders":  {3, 1},
		"api:slow:jazz-api:POST /v1/studio/renders": {2, 0},
	}
	for key, counts := range want {
		it, ok := items[key]
		if !ok || it.Count != counts[0] || it.Previous != counts[1] {
			t.Errorf("%s: got %+v, want count %d previous %d", key, it, counts[0], counts[1])
		}
	}
	if _, ok := items["checks:fine.example:HTTP 503"]; ok {
		t.Error("a listing checked successfully since is not a failed check")
	}
	if _, ok := items["api:5xx:workbench:GET /v1/workbench/threads"]; ok {
		t.Error("routes without 5xx or slow responses are left out")
	}
	if !items["sources:skipped:Unverified search lead discovered; no live page accessed"].Info || !items["checks:leads"].Info {
		t.Error("an unopened lead is context, not a fault")
	}
	if d.FixNext == nil || d.FixNext.Key != "run:failed" || d.AllClear || d.ToFix < 10 {
		t.Fatalf("fix next %+v, to fix %d", d.FixNext, d.ToFix)
	}
	if !strings.Contains(d.Verdict, "next: trumpet run failed") {
		t.Fatalf("verdict: %q", d.Verdict)
	}
	if got := d.Items[0].Section; got != "run" {
		t.Fatalf("sections are in a fixed order, got %s first", got)
	}
	if b, _ := json.Marshal(d); strings.Contains(string(b), "private title") {
		t.Fatal("listing text never enters the digest")
	}
}

func TestFixDigestFlagsAMissingDailyRun(t *testing.T) {
	f := newFixFixture(t)
	now := time.Date(2026, 10, 12, 16, 31, 0, 0, time.UTC)
	f.run("adaptive-watch-yesterday", "succeeded", "", time.Date(2026, 10, 11, 13, 17, 0, 0, time.UTC))
	d, err := f.h.app.computeFixDigest(f.ctx, f.user, now)
	if err != nil {
		t.Fatal(err)
	}
	if !d.Run.Missing || d.FixNext == nil || d.FixNext.Key != "run:missing" || d.Verdict != "1 to fix · next: daily run missing" {
		t.Fatalf("missing run: %+v %q", d.Run, d.Verdict)
	}
	// Before 16:30 UTC a late or long run is not called missing yet:
	// yesterday's is checked.
	early, err := f.h.app.computeFixDigest(f.ctx, f.user, time.Date(2026, 10, 12, 16, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if early.Run.Missing || !early.AllClear || early.Verdict != "All clear: the daily run succeeded and nothing failed." {
		t.Fatalf("before the deadline: %+v %q", early.Run, early.Verdict)
	}
}

func (f *fixFixture) subscribe(device string) {
	f.h.call("PUT", wbBase+"/push/subscriptions/"+device, map[string]any{"label": device, "subscription": map[string]any{"endpoint": "https://fcm.googleapis.com/fcm/send/fixture-" + device, "keys": map[string]any{"p256dh": "BFixtureKey", "auth": "fixtureauth"}}}, 200)
}

func TestFixListIsDeliveredOncePerDay(t *testing.T) {
	f := newFixFixture(t)
	f.subscribe("phone-0001")
	f.subscribe("laptop-0001")
	now := time.Date(2026, 10, 12, 13, 50, 0, 0, time.UTC)
	f.run("adaptive-watch-today", "succeeded", "", now.Add(-33*time.Minute), [3]string{"Reverb API", "checked", "6 searches"})

	// Five triggers at once (runner, fallbacks, sweep): one sends.
	var wg sync.WaitGroup
	results := make([]fixListResult, 5)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r, err := f.h.app.fixListDeliver(f.ctx, f.user, now, "test")
			if err != nil {
				t.Error(err)
			}
			results[i] = r
		}(i)
	}
	wg.Wait()
	sent := 0
	for _, r := range results {
		if r.Sent {
			sent++
		} else if !r.Replayed {
			t.Fatalf("not sent and not replayed: %+v", r)
		}
	}
	if sent != 1 || f.h.pusher.count("fixlist") != 2 {
		t.Fatalf("sent %d times, %d pushes", sent, f.h.pusher.count("fixlist"))
	}
	again, err := f.h.app.fixListDeliver(f.ctx, f.user, now.Add(3*time.Hour), "workbench-fallback")
	if err != nil || !again.Replayed || again.Reason != "already sent today" || !again.AllClear || again.Pushed != 2 {
		t.Fatalf("a later trigger replays: %+v %v", again, err)
	}
	f.h.pusher.mu.Lock()
	p := f.h.pusher.sent[0]
	f.h.pusher.mu.Unlock()
	if p.Title != "Fix list – 2026-10-12" || !p.Quiet || p.URL != "/jazz/#fixlist" {
		t.Fatalf("all-clear push: %+v", p)
	}

	// One visible message in the "Fix list" thread, and a note for the agent.
	var thread uuid.UUID
	var title string
	if err := f.h.app.db.QueryRow(f.ctx, `SELECT id,title FROM wb_threads WHERE user_id=$1`, f.user).Scan(&thread, &title); err != nil || title != "Fix list" {
		t.Fatalf("thread: %v %q", err, title)
	}
	var visible, notes int
	_ = f.h.app.db.QueryRow(f.ctx, `SELECT count(*) FILTER (WHERE role='assistant'), count(*) FILTER (WHERE role='note' AND handled_at IS NULL) FROM wb_messages WHERE thread_id=$1`, thread).Scan(&visible, &notes)
	if visible != 1 || notes != 1 {
		t.Fatalf("messages: %d visible, %d notes", visible, notes)
	}
	snap := f.h.call("GET", wbBase+"/threads/"+thread.String(), nil, 200)
	msgs := snap["messages"].([]any)
	if len(msgs) != 1 || !strings.HasPrefix(msgs[0].(map[string]any)["text"].(string), "**Fix list – 2026-10-12**") {
		t.Fatalf("snapshot: %v", msgs)
	}
	if evs := f.h.events(thread.String(), 0); len(evs) != 1 || evs[0].Type != "message.created" {
		t.Fatalf("events: %+v", evs)
	}
	if f.h.app.wbHasPendingUser(f.ctx, thread) {
		t.Fatal("the digest does not start an agent turn by itself")
	}

	// The next day: same thread, the old note superseded; yesterday's stored
	// counts are the comparison for failed checks.
	f.listing("verified", "shop.example / timed out", now.Add(20*time.Hour))
	next, err := f.h.app.fixListDeliver(f.ctx, f.user, now.Add(24*time.Hour+10*time.Minute), "runner")
	if err != nil || !next.Sent || next.AllClear || *next.ThreadID != thread {
		t.Fatalf("next day: %+v %v", next, err)
	}
	_ = f.h.app.db.QueryRow(f.ctx, `SELECT count(*) FILTER (WHERE role='assistant'), count(*) FILTER (WHERE role='note' AND handled_at IS NULL) FROM wb_messages WHERE thread_id=$1`, thread).Scan(&visible, &notes)
	if visible != 2 || notes != 1 {
		t.Fatalf("next day messages: %d visible, %d unread notes", visible, notes)
	}
	f.h.pusher.mu.Lock()
	last := f.h.pusher.sent[len(f.h.pusher.sent)-1]
	f.h.pusher.mu.Unlock()
	if last.Quiet || !strings.Contains(last.Body, "Checks 1 on 1 domain") || strings.Contains(last.Body, "timed out") {
		t.Fatalf("a day with failures: %+v", last)
	}

	// The page reads the latest digest and the recent verdicts.
	page := f.h.call("GET", wbBase+"/fixlist", nil, 200)
	digest := page["digest"].(map[string]any)
	if page["live"] != false || digest["day"] != "2026-10-13" || len(page["history"].([]any)) != 2 || page["threadId"] != thread.String() {
		t.Fatalf("page: %v", page)
	}
	older := f.h.call("GET", wbBase+"/fixlist?day=2026-10-12", nil, 200)
	if older["digest"].(map[string]any)["allClear"] != true {
		t.Fatalf("a given day: %v", older)
	}
	f.h.call("GET", wbBase+"/fixlist?day=2026-01-01", nil, 404)
	f.h.call("GET", wbBase+"/fixlist?day=bad", nil, 400)
}

func TestFixListMachineTriggerAndFallback(t *testing.T) {
	f := newFixFixture(t)
	token := strings.Repeat("m", 40)
	sum := sha256.Sum256([]byte(token))
	t.Setenv("TRUMPETS_MACHINE_TOKEN_SHA256", hex.EncodeToString(sum[:]))
	// The runner's token belongs to another subject; every trigger resolves
	// the same owner as the fallbacks (FIX_LIST_OWNER_SUBJECT first).
	t.Setenv("TRUMPETS_MACHINE_USER", "machine-subject")
	t.Setenv("FIX_LIST_OWNER_SUBJECT", "owner")
	t.Setenv("WORKBENCH_OWNER_SUBJECT", "")
	clock := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	f.h.app.fix.now = func() time.Time { return clock }
	post := func(auth string) (int, map[string]any) {
		r := httptest.NewRequest("POST", "/v1/trumpets/machine/fixlist", strings.NewReader(`{}`))
		r.Header.Set("Content-Type", "application/json")
		if auth != "" {
			r.Header.Set("Authorization", "Bearer "+auth)
		}
		w := httptest.NewRecorder()
		f.h.routes.ServeHTTP(w, r)
		out := map[string]any{}
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	if code, _ := post(strings.Repeat("x", 40)); code != 401 {
		t.Fatalf("machine token required: %d", code)
	}
	// The runner decides when to call (its scheduled window, or the explicit
	// flag); the server sends whenever asked, once per day, for the owner.
	if code, out := post(token); code != 200 || out["sent"] != true {
		t.Fatalf("the runner's trigger sends: %d %v", code, out)
	}
	var owned int
	_ = f.h.app.db.QueryRow(f.ctx, `SELECT count(*) FROM fix_list_digests WHERE user_id=$1 AND day='2026-10-12'`, f.user).Scan(&owned)
	if owned != 1 {
		t.Fatal("the machine trigger delivers the fix list owner's digest, not the token subject's")
	}
	if code, out := post(token); code != 200 || out["sent"] != false || out["replayed"] != true {
		t.Fatalf("and is idempotent: %d %v", code, out)
	}

	// The fallback: off until enabled, nothing before 16:30 UTC, then once.
	f.h.app.fix.enabled = true
	f.h.app.fix.wait = make(chan fixListResult, 4)
	clock = time.Date(2026, 10, 13, 16, 29, 0, 0, time.UTC)
	f.h.app.fixListMaybe(time.Now(), "workbench-fallback")
	clock = time.Date(2026, 10, 13, 16, 31, 0, 0, time.UTC)
	f.h.app.fixListMaybe(time.Now(), "workbench-fallback")
	select {
	case res := <-f.h.app.fix.wait:
		if !res.Sent || res.Day != "2026-10-13" || res.AllClear {
			t.Fatalf("fallback: %+v", res)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the fallback did not run")
	}
	f.h.app.fixListMaybe(time.Now(), "workbench-fallback")
	select {
	case res := <-f.h.app.fix.wait:
		t.Fatalf("the day is known to be sent; no second attempt: %+v", res)
	case <-time.After(200 * time.Millisecond):
	}
	var trigger string
	_ = f.h.app.db.QueryRow(f.ctx, `SELECT trigger FROM fix_list_digests WHERE user_id=$1 AND day='2026-10-13'`, f.user).Scan(&trigger)
	if trigger != "workbench-fallback" {
		t.Fatalf("trigger recorded: %q", trigger)
	}
	var missing bool
	var raw []byte
	_ = f.h.app.db.QueryRow(f.ctx, `SELECT digest FROM fix_list_digests WHERE user_id=$1 AND day='2026-10-13'`, f.user).Scan(&raw)
	var d fixDigest
	_ = json.Unmarshal(raw, &d)
	missing = d.Run.Missing
	if !missing {
		t.Fatal("a day caught by the fallback flags the missing run")
	}
}

func TestRouteCountersPersistAndFeedTheDigest(t *testing.T) {
	f := newFixFixture(t)
	f.h.app.counters = newRouteCounters("workbench")
	now := time.Now().UTC()
	for i := 0; i < 3; i++ {
		f.h.app.counters.record("GET /v1/workbench/status", 500, 11*time.Second, now)
	}
	if err := f.h.app.flushRouteCounters(f.ctx); err != nil {
		t.Fatal(err)
	}
	f.h.app.counters.record("GET /v1/workbench/status", 502, time.Millisecond, now)
	if err := f.h.app.flushRouteCounters(f.ctx); err != nil {
		t.Fatal(err)
	}
	var requests, errs, slow int64
	if err := f.h.app.db.QueryRow(f.ctx, `SELECT requests,errors_5xx,slow FROM api_route_counters WHERE service='workbench' AND route='GET /v1/workbench/status'`).Scan(&requests, &errs, &slow); err != nil {
		t.Fatal(err)
	}
	if requests != 4 || errs != 4 || slow != 3 {
		t.Fatalf("one row per hour and route, added up: %d %d %d", requests, errs, slow)
	}
	d, err := f.h.app.computeFixDigest(f.ctx, f.user, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if it := f.itemsByKey(d)["api:5xx:workbench:GET /v1/workbench/status"]; it.Count != 4 || it.Previous != 0 {
		t.Fatalf("digest reads the counters: %+v", it)
	}
}

// Delivered by a process without VAPID keys, the push waits for one with them.
func TestFixListPushWaitsForAProcessWithKeys(t *testing.T) {
	f := newFixFixture(t)
	f.subscribe("phone-0001")
	pusher := f.h.pusher
	f.h.app.wb.push = nil
	now := time.Now().UTC()
	res, err := f.h.app.fixListDeliver(f.ctx, f.user, now, "runner")
	if err != nil || !res.Sent || res.Pushed != 0 {
		t.Fatalf("delivered without a pusher: %+v %v", res, err)
	}
	f.h.app.wb.push = pusher
	f.h.app.fixListPendingPushes(f.ctx, now)
	f.h.app.fixListPendingPushes(f.ctx, now)
	var pushed int
	_ = f.h.app.db.QueryRow(f.ctx, `SELECT pushed FROM fix_list_digests WHERE user_id=$1`, f.user).Scan(&pushed)
	if pusher.count("fixlist") != 1 || pushed != 1 {
		t.Fatalf("pushed once by the sweep: %d (%d recorded)", pusher.count("fixlist"), pushed)
	}
}
