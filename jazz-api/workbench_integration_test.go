package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

// Workbench against Postgres with a fake Messages API. All data is fictional.

func TestWorkbenchEventLogIsGaplessAndResumable(t *testing.T) {
	h := newWBHarness(t, nil)
	thread := h.newThread()
	id := uuid.MustParse(thread)
	ctx := context.Background()
	// Concurrent writers (drafts from three devices, server events) must still
	// produce ids 1..N with no gaps and no duplicates.
	var wg sync.WaitGroup
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%3 == 0 {
				h.call("PUT", wbBase+"/threads/"+thread+"/draft", map[string]any{"text": fmt.Sprintf("draft %d", i), "deviceId": fmt.Sprintf("device-%04d", i%3)}, 200)
			} else if _, err := h.app.wbEmit(ctx, id, "test.event", map[string]any{"n": i}); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	all := h.events(thread, 0)
	if len(all) != 30 {
		t.Fatalf("want 30 events, got %d", len(all))
	}
	for i, e := range all {
		if e.ID != int64(i+1) {
			t.Fatalf("event %d has id %d: ids must be gapless and ordered", i, e.ID)
		}
	}
	// Resume from 17: exactly the events after it, over a real stream too.
	if got := h.events(thread, 17); len(got) != 13 || got[0].ID != 18 {
		t.Fatalf("resume from 17: %d events starting at %v", len(got), got)
	}
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	frames, err := h.subscribe(sctx, thread, 17)
	if err != nil {
		t.Fatal(err)
	}
	first := <-frames
	if first.ID != 18 {
		t.Fatalf("Last-Event-ID 17 must resume at 18, got %d", first.ID)
	}
	// The thread snapshot names the id to stream from.
	snap := h.call("GET", wbBase+"/threads/"+thread, nil, 200)
	if snap["thread"].(map[string]any)["lastEventId"].(float64) != 30 {
		t.Fatalf("snapshot lastEventId: %v", snap["thread"])
	}
	draft := snap["draft"].(map[string]any)
	if draft["rev"].(float64) != 10 || !strings.HasPrefix(draft["text"].(string), "draft ") {
		t.Fatalf("draft synced with a revision per save: %v", draft)
	}
	if code, _ := h.do("GET", wbBase+"/threads/"+thread+"/events", nil, map[string]string{"Last-Event-ID": "nope"}); code != 400 {
		t.Fatalf("a malformed Last-Event-ID is rejected, got %d", code)
	}
}

func TestWorkbenchTwoDevicesReceiveTheSameStreamedReply(t *testing.T) {
	hold := make(chan struct{})
	h := newWBHarness(t, nil, fakeTurn{Blocks: []fakeBlock{{Text: "Blue Bossa is in the key of C minor and starts on the four chord."}}, Input: 1200, Output: 80, Hold: hold})
	thread := h.newThread()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	phone, err := h.subscribe(ctx, thread, 0)
	if err != nil {
		t.Fatal(err)
	}
	laptop, err := h.subscribe(ctx, thread, 0)
	if err != nil {
		t.Fatal(err)
	}
	h.send(thread, "What key is Blue Bossa in?")
	// The reply is mid-stream: a third device joins late and resumes from an
	// id it already had; it must still converge on the same log.
	first := collect(t, phone, "message.delta", 5*time.Second)
	tablet, err := h.subscribe(ctx, thread, first[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	// A device that loads the snapshot mid-reply gets the text so far, up to
	// the snapshot's lastEventId, and continues from there.
	var firstDelta struct{ Text string }
	json.Unmarshal([]byte(first[len(first)-1].Data), &firstDelta)
	mid := h.call("GET", wbBase+"/threads/"+thread, nil, 200)
	midMsgs := mid["messages"].([]any)
	streaming := midMsgs[len(midMsgs)-1].(map[string]any)
	if streaming["status"] != "streaming" || streaming["text"] != firstDelta.Text || firstDelta.Text == "" {
		t.Fatalf("mid-stream snapshot: %v (first delta %q)", streaming, firstDelta.Text)
	}
	close(hold)
	a := append(first, collect(t, phone, "run.finished", 10*time.Second)...)
	b := collect(t, laptop, "run.finished", 10*time.Second)
	c := collect(t, tablet, "run.finished", 10*time.Second)
	if len(a) != len(b) {
		t.Fatalf("both devices see the same frames: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("frame %d differs:\n%v\n%v", i, a[i], b[i])
		}
	}
	if len(c) != len(a)-1 || c[0] != a[1] {
		t.Fatalf("late device resumes losslessly after id %d: got %d frames", first[0].ID, len(c))
	}
	var text strings.Builder
	deltas := 0
	for _, f := range a {
		if f.Type == "message.delta" {
			var d struct{ Text string }
			json.Unmarshal([]byte(f.Data), &d)
			text.WriteString(d.Text)
			deltas++
		}
	}
	if deltas < 2 || text.String() != "Blue Bossa is in the key of C minor and starts on the four chord." {
		t.Fatalf("streamed text arrives in several deltas and reassembles exactly (%d deltas): %q", deltas, text.String())
	}
	// The request used the agreed configuration.
	body := h.model.lastBody()
	if body["model"] != "claude-opus-5-5" || body["fallbacks"] != "default" {
		t.Fatalf("model and fallbacks: %v %v", body["model"], body["fallbacks"])
	}
	if body["thinking"].(map[string]any)["type"] != "adaptive" || body["output_config"].(map[string]any)["effort"] != "low" {
		t.Fatalf("adaptive thinking at low effort: %v %v", body["thinking"], body["output_config"])
	}
	sys := body["system"].([]any)[0].(map[string]any)
	if sys["cache_control"] == nil || body["cache_control"] == nil {
		t.Fatalf("cache breakpoints on the system prompt and the conversation")
	}
	if beta := h.model.headers[0].Get("anthropic-beta"); !strings.Contains(beta, "server-side-fallback-2026-07-01") {
		t.Fatalf("fallback beta header: %q", beta)
	}
	msgs := body["messages"].([]any)
	userText := msgs[0].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(userText, "<page_context>") || !strings.Contains(userText, `"viewport":"390x844"`) {
		t.Fatalf("page context travels with the message: %s", userText)
	}
	// Persisted: a device that loads later sees the finished reply.
	snap := h.call("GET", wbBase+"/threads/"+thread, nil, 200)
	ms := snap["messages"].([]any)
	if len(ms) != 2 || ms[1].(map[string]any)["text"] != "Blue Bossa is in the key of C minor and starts on the four chord." {
		t.Fatalf("snapshot messages: %v", ms)
	}
	if spend := snap["thread"].(map[string]any)["spendUsd"].(float64); math.Abs(spend-(1200*4+80*20)/1e6) > 1e-9 {
		t.Fatalf("thread spend from usage: %v", spend)
	}
}

func TestWorkbenchApprovalFirstDeviceWins(t *testing.T) {
	h := newWBHarness(t, nil,
		fakeTurn{Blocks: []fakeBlock{{Text: "I'll file it."}, {ToolName: "github_create_issue", ToolInput: map[string]any{"title": "Sample: calmer metronome", "body": "Fictional fixture.", "labels": []string{"jazz"}}}}, Stop: "tool_use", Input: 900, Output: 60},
		fakeTurn{Blocks: []fakeBlock{{Text: "Proposed. Approve it when you like."}}, Input: 1000, Output: 20},
	)
	// Two devices are subscribed for push.
	for _, d := range []string{"phone-0001", "laptop-001"} {
		h.call("PUT", wbBase+"/push/subscriptions/"+d, map[string]any{"label": d, "subscription": map[string]any{"endpoint": "https://fcm.googleapis.com/fcm/send/fixture-" + d, "keys": map[string]any{"p256dh": "BFixtureKey", "auth": "fixtureauth"}}}, 200)
	}
	thread := h.newThread()
	h.send(thread, "File an issue to make the metronome calmer")
	h.wait()
	snap := h.call("GET", wbBase+"/threads/"+thread, nil, 200)
	approvals := snap["approvals"].([]any)
	if len(approvals) != 1 {
		t.Fatalf("one approval card: %v", approvals)
	}
	a := approvals[0].(map[string]any)
	if a["status"] != "pending" || a["toolName"] != "github_create_issue" || h.github.issueCount() != 0 {
		t.Fatalf("nothing is written before approval: %v", a)
	}
	if h.pusher.count("approval") != 2 {
		t.Fatalf("approval pushed to both devices: %d", h.pusher.count("approval"))
	}
	// Both devices answer at once; exactly one wins.
	var wg sync.WaitGroup
	codes := make([]int, 2)
	for i, d := range []string{"phone-0001", "laptop-001"} {
		wg.Add(1)
		go func(i int, d string) {
			defer wg.Done()
			codes[i], _ = h.do("POST", wbBase+"/approvals/"+a["id"].(string), map[string]any{"decision": "approve", "deviceId": d}, nil)
		}(i, d)
	}
	wg.Wait()
	if !(codes[0] == 200 && codes[1] == 409 || codes[0] == 409 && codes[1] == 200) {
		t.Fatalf("first decision wins, the other gets 409: %v", codes)
	}
	if h.github.issueCount() != 1 {
		t.Fatalf("the issue is created exactly once, got %d", h.github.issueCount())
	}
	// A late reject also loses.
	if code, out := h.do("POST", wbBase+"/approvals/"+a["id"].(string), map[string]any{"decision": "reject", "deviceId": "tablet-01"}, nil); code != 409 || out["approval"].(map[string]any)["status"] != "approved" {
		t.Fatalf("late decision: %d %v", code, out)
	}
	types := eventTypes(h.events(thread, 0))
	count := func(name string) int {
		n := 0
		for _, ty := range types {
			if ty == name {
				n++
			}
		}
		return n
	}
	if count("approval.requested") != 1 || count("approval.resolved") != 1 || count("approval.result") != 1 {
		t.Fatalf("one requested, one resolved, one result event: %v", types)
	}
	// The outcome is queued as context for the next turn, not shown as a message.
	var note string
	if err := h.app.db.QueryRow(context.Background(), `SELECT text FROM wb_messages WHERE thread_id=$1 AND role='note'`, thread).Scan(&note); err != nil || !strings.Contains(note, "approved") || !strings.Contains(note, "#100") {
		t.Fatalf("approval note: %q %v", note, err)
	}
	// The tool result told the model it was only proposed.
	second := h.model.lastBody()["messages"].([]any)
	result := second[len(second)-1].(map[string]any)["content"].([]any)[0].(map[string]any)
	if result["type"] != "tool_result" || !strings.Contains(fmt.Sprint(result["content"]), "Proposed to the owner") {
		t.Fatalf("tool result: %v", result)
	}
}

func TestWorkbenchOutboxReplayIsIdempotent(t *testing.T) {
	h := newWBHarness(t, nil, fakeTurn{Blocks: []fakeBlock{{Text: "Got it."}}, Input: 100, Output: 5})
	thread := h.newThread()
	msg := map[string]any{"clientId": "outbox-0001-fixture", "text": "Sent while offline", "deviceId": "phone-0001"}
	// The outbox replays the same message several times, some concurrently.
	var wg sync.WaitGroup
	codes := make(chan int, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			code, _ := h.do("POST", wbBase+"/threads/"+thread+"/messages", msg, nil)
			codes <- code
		}()
	}
	wg.Wait()
	close(codes)
	created := 0
	for c := range codes {
		switch c {
		case 201:
			created++
		case 200:
		default:
			t.Fatalf("replay status %d", c)
		}
	}
	if created != 1 {
		t.Fatalf("exactly one replay creates the message, got %d", created)
	}
	h.wait()
	var n int
	h.app.db.QueryRow(context.Background(), `SELECT count(*) FROM wb_messages WHERE thread_id=$1 AND role='user'`, thread).Scan(&n)
	if n != 1 || h.model.callCount() != 1 {
		t.Fatalf("one message and one model call: %d messages, %d calls", n, h.model.callCount())
	}
	created = 0
	for _, ty := range eventTypes(h.events(thread, 0)) {
		if ty == "message.created" {
			created++
		}
	}
	if created != 1 {
		t.Fatalf("one message.created event, got %d", created)
	}
	// Voice notes replay the same way: same client id, same attachment.
	audio := append([]byte("OggS"), make([]byte, 64)...)
	headers := map[string]string{"Content-Type": "audio/ogg", "X-Workbench-Upload": "1", "X-Workbench-Client-Id": "voice-0001-fixture", "X-Workbench-Duration-Ms": "4200"}
	code1, a1 := h.do("POST", wbBase+"/threads/"+thread+"/attachments", audio, headers)
	code2, a2 := h.do("POST", wbBase+"/threads/"+thread+"/attachments", audio, headers)
	if code1 != 201 || code2 != 200 || a1["id"] != a2["id"] || h.objects.count("workbench/") != 1 {
		t.Fatalf("attachment replay: %d %d %v %v (%d objects)", code1, code2, a1["id"], a2["id"], h.objects.count("workbench/"))
	}
	// Without the upload header the raw body is refused (no simple CORS POST).
	if code, _ := h.do("POST", wbBase+"/threads/"+thread+"/attachments", audio, map[string]string{"Content-Type": "audio/ogg"}); code != 403 {
		t.Fatalf("upload without X-Workbench-Upload: %d", code)
	}
	h.model.push(fakeTurn{Blocks: []fakeBlock{{Text: "Voice note received."}}, Input: 100, Output: 5})
	h.call("POST", wbBase+"/threads/"+thread+"/messages", map[string]any{"clientId": "outbox-0002-fixture", "text": "", "attachmentIds": []string{a1["id"].(string)}}, 201)
	h.wait()
	msgs := h.model.lastBody()["messages"].([]any)
	if !strings.Contains(fmt.Sprint(msgs[len(msgs)-1]), "[voice note attached, 4 s; stored, not transcribed]") {
		t.Fatalf("voice notes are stored and announced, not transcribed: %v", msgs[len(msgs)-1])
	}
}

func TestWorkbenchMonthlyCapBlocksNewTurns(t *testing.T) {
	// $0.01 cap: one turn of 2,000 input + 200 output tokens costs $0.012.
	h := newWBHarness(t, func(c *wbConfig) { c.MonthlyCapUSD = 0.01 }, fakeTurn{Blocks: []fakeBlock{{Text: "Here you go."}}, Input: 2000, Output: 200})
	thread := h.newThread()
	h.send(thread, "First question")
	h.wait()
	spend := h.call("GET", wbBase+"/spend", nil, 200)
	if math.Abs(spend["spentUsd"].(float64)-0.012) > 1e-9 || spend["blocked"] != true {
		t.Fatalf("spend from usage, now over the cap: %v", spend)
	}
	h.send(thread, "Second question")
	h.wait()
	if h.model.callCount() != 1 {
		t.Fatalf("a capped month makes no model call, got %d calls", h.model.callCount())
	}
	events := h.events(thread, 0)
	last := events[len(events)-1]
	var p map[string]any
	json.Unmarshal(last.Payload, &p)
	if last.Type != "run.finished" || p["status"] != "capped" || !strings.Contains(p["message"].(string), "budget") {
		t.Fatalf("the capped turn says why: %s %v", last.Type, p)
	}
	var status string
	h.app.db.QueryRow(context.Background(), `SELECT status FROM wb_messages WHERE thread_id=$1 AND text='Second question'`, thread).Scan(&status)
	if status != "blocked" {
		t.Fatalf("the blocked message is marked: %q", status)
	}
	// Per-thread and per-run counters agree with the month.
	var threadMicro, runMicro int64
	h.app.db.QueryRow(context.Background(), `SELECT spend_micro_usd FROM wb_threads WHERE id=$1`, thread).Scan(&threadMicro)
	h.app.db.QueryRow(context.Background(), `SELECT sum(spend_micro_usd) FROM wb_runs WHERE thread_id=$1`, thread).Scan(&runMicro)
	if threadMicro != 12000 || runMicro != 12000 {
		t.Fatalf("thread %d and run %d micro-dollars", threadMicro, runMicro)
	}
}

func TestWorkbenchTriageAndSiteDataTools(t *testing.T) {
	h := newWBHarness(t, nil,
		// Turn 1: a low-risk capture runs immediately; triage proposes keep_it.
		fakeTurn{Blocks: []fakeBlock{
			{ToolName: "commonplace_capture", ToolInput: map[string]any{"text": "Sample idea: practise ballads at dawn.", "kind": "idea"}},
			{ToolName: "triage_idea", ToolInput: map[string]any{"idea": "Sample: keep a list of ballads", "category": "keep_it", "reason": "Not code.", "action": map[string]any{"tool": "commonplace_capture", "input": map[string]any{"text": "Sample: a list of ballads to learn.", "kind": "idea"}}}},
			{ToolName: "triage_idea", ToolInput: map[string]any{"idea": "Sample: something vague", "category": "ask_me", "reason": "Unclear.", "question": "Which page do you mean?"}},
			{ToolName: "github_list_issues", ToolInput: map[string]any{}},
			{ToolName: "site_status", ToolInput: map[string]any{}},
			{ToolName: "jazz_add_practice_block", ToolInput: map[string]any{"date": "2026-01-03", "title": "Sample long tones", "minutes": 15, "type": "fundamentals"}},
		}, Stop: "tool_use", Input: 500, Output: 100},
		fakeTurn{Blocks: []fakeBlock{{Text: "Done, and two cards are waiting."}}, Input: 600, Output: 10},
	)
	thread := h.newThread()
	h.send(thread, "Save this and sort the rest")
	h.wait()
	ctx := context.Background()
	var moments int
	h.app.db.QueryRow(ctx, `SELECT count(*) FROM cp_moments`).Scan(&moments)
	if moments != 1 {
		t.Fatalf("the capture ran immediately: %d Moments", moments)
	}
	var cards, statusCards int
	var results []map[string]any
	for _, e := range h.events(thread, 0) {
		if e.Type == "tool.finished" {
			var p map[string]any
			json.Unmarshal(e.Payload, &p)
			results = append(results, p)
			if c, ok := p["card"].(map[string]any); ok {
				cards++
				if c["kind"] == "status" {
					statusCards++
					ci := c["ci"].(map[string]any)
					if ci["conclusion"] != "failure" || len(c["prs"].([]any)) != 1 {
						t.Fatalf("status card: %v", c)
					}
				}
			}
		}
	}
	if len(results) != 6 || statusCards != 1 || cards != 3 {
		t.Fatalf("six tool steps; capture, question and status cards: %d %d %d", len(results), cards, statusCards)
	}
	for _, r := range results {
		if r["ok"] != true {
			t.Fatalf("tool failed: %v", r)
		}
	}
	// Untrusted data is framed as data.
	toolResults := h.model.lastBody()["messages"].([]any)
	last := fmt.Sprint(toolResults[len(toolResults)-1])
	if !strings.Contains(last, `<untrusted_data source=\"github\">`) && !strings.Contains(last, `<untrusted_data source="github">`) {
		t.Fatalf("GitHub content is wrapped as untrusted data: %s", last)
	}
	snap := h.call("GET", wbBase+"/threads/"+thread, nil, 200)
	approvals := snap["approvals"].([]any)
	if len(approvals) != 2 {
		t.Fatalf("triage keep_it and the practice section wait for approval: %v", approvals)
	}
	for _, raw := range approvals {
		a := raw.(map[string]any)
		h.call("POST", wbBase+"/approvals/"+a["id"].(string), map[string]any{"decision": "approve", "deviceId": "phone-0001"}, 200)
	}
	h.app.db.QueryRow(ctx, `SELECT count(*) FROM cp_moments`).Scan(&moments)
	var blocks int
	h.app.db.QueryRow(ctx, `SELECT count(*) FROM practice_blocks WHERE title='Sample long tones' AND practice_date='2026-01-03'`).Scan(&blocks)
	if moments != 2 || blocks != 1 {
		t.Fatalf("approved actions ran through the existing handlers: %d Moments, %d sections", moments, blocks)
	}
	// Reading today's plan for that date finds the new section by id.
	user, _ := h.app.userIDForSubject(ctx, "owner")
	text, _, err := h.app.wbExecute(ctx, user, "read-1", "jazz_today", json.RawMessage(`{"date":"2026-01-03"}`))
	if err != nil || !strings.Contains(text, "Sample long tones") || !strings.Contains(text, "untrusted_data") {
		t.Fatalf("jazz_today: %v %s", err, text)
	}
}

func TestWorkbenchOwnerAndPrivacy(t *testing.T) {
	h := newWBHarness(t, func(c *wbConfig) { c.OwnerSubject = "owner" })
	thread := h.newThread()
	if code, _ := h.do("GET", wbBase+"/threads", nil, map[string]string{"X-Jazz-User": "someone-else"}); code != 403 {
		t.Fatalf("another signed-in user is refused: %d", code)
	}
	if code, _ := h.do("GET", wbBase+"/threads", nil, map[string]string{"X-Jazz-Gateway-Key": "wrong"}); code != 401 {
		t.Fatalf("no gateway key: %d", code)
	}
	if code, _ := h.do("POST", wbBase+"/threads/"+thread+"/messages", map[string]any{"clientId": "xsite-0001", "text": "x"}, map[string]string{"Sec-Fetch-Site": "cross-site"}); code != 403 {
		t.Fatalf("cross-site write: %d", code)
	}
	if code, _ := h.do("POST", wbBase+"/threads/"+thread+"/messages", []byte(`{"clientId":"plain-0001","text":"x"}`), map[string]string{"Content-Type": "text/plain"}); code != 403 {
		t.Fatalf("simple-request body type: %d", code)
	}
	if code, _ := h.do("POST", wbBase+"/threads/"+thread+"/messages", map[string]any{"clientId": "origin-0001", "text": "x"}, map[string]string{"Origin": "https://attacker.example"}); code != 403 {
		t.Fatalf("foreign origin: %d", code)
	}
	other := uuid.NewString()
	if code, _ := h.do("GET", wbBase+"/threads/"+other, nil, nil); code != 404 {
		t.Fatalf("unknown thread: %d", code)
	}
	code, out := h.do("PUT", wbBase+"/push/subscriptions/phone-0001", map[string]any{"subscription": map[string]any{"endpoint": "https://169.254.169.254/latest", "keys": map[string]any{"p256dh": "k", "auth": "a"}}}, nil)
	if code != 422 {
		t.Fatalf("push endpoints must be real push services: %d %v", code, out)
	}
	_, list := h.do("GET", wbBase+"/threads", nil, nil)
	if w := list["threads"].([]any); len(w) != 1 {
		t.Fatalf("threads: %v", w)
	}
}

func TestWorkbenchRecoversInterruptedRuns(t *testing.T) {
	h := newWBHarness(t, nil, fakeTurn{Blocks: []fakeBlock{{Text: "Back again."}}, Input: 10, Output: 2})
	thread := h.newThread()
	id := uuid.MustParse(thread)
	ctx := context.Background()
	// Simulate an instance that died mid-turn: a stale lease, a running run,
	// an unanswered message and a transcript ending in an unanswered tool call.
	user, _ := h.app.userIDForSubject(ctx, "owner")
	_ = user
	run := uuid.New()
	h.app.db.Exec(ctx, `UPDATE wb_threads SET run_state='running', run_owner='dead', run_heartbeat=now()-interval '10 minutes' WHERE id=$1`, id)
	h.app.db.Exec(ctx, `INSERT INTO wb_runs (id,thread_id) VALUES ($1,$2)`, run, id)
	h.app.db.Exec(ctx, `INSERT INTO wb_api_turns (thread_id,seq,role,param) VALUES ($1,0,'user',$2),($1,1,'assistant',$3)`, id,
		`{"role":"user","content":[{"type":"text","text":"earlier"}]}`, `{"role":"assistant","content":[{"type":"tool_use","id":"toolu_dead","name":"site_status","input":{}}]}`)
	h.app.db.Exec(ctx, `INSERT INTO wb_messages (id,thread_id,role,text,client_id) VALUES ($1,$2,'user','Are you there?','recover-0001')`, uuid.New(), id)
	h.app.wbRecover(ctx)
	h.wait()
	var status string
	h.app.db.QueryRow(ctx, `SELECT status FROM wb_runs WHERE id=$1`, run).Scan(&status)
	if status != "interrupted" || h.model.callCount() != 1 {
		t.Fatalf("stale run interrupted and the waiting message answered: %s, %d calls", status, h.model.callCount())
	}
	msgs := h.model.lastBody()["messages"].([]any)
	resumed := msgs[2].(map[string]any)["content"].([]any)[0].(map[string]any)
	if resumed["type"] != "tool_result" || resumed["tool_use_id"] != "toolu_dead" {
		t.Fatalf("the dangling tool call gets a result first: %v", resumed)
	}
}

// userIDForSubject resolves (or creates) the app user for a gateway subject.
func (app *application) userIDForSubject(ctx context.Context, subject string) (uuid.UUID, error) {
	return app.userID(context.WithValue(ctx, userSubjectKey, subject))
}
