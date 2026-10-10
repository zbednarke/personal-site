package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

// Regression tests for the PR #83 review. All data is fictional.

// Finding 2: images are downscaled on upload, and a thread whose history the
// API refuses for good is marked stuck and offers a fresh thread.
func TestWorkbenchImagesAndStuckThreads(t *testing.T) {
	h := newWBHarness(t, nil,
		fakeTurn{Status: 400},
		fakeTurn{Blocks: []fakeBlock{{Text: "Fresh start."}}, Input: 100, Output: 5})
	thread := h.newThread()
	big := image.NewRGBA(image.Rect(0, 0, 4000, 3000))
	for y := 0; y < 3000; y++ {
		for x := 0; x < 4000; x++ {
			big.Set(x, y, color.RGBA{uint8(x / 16), uint8(y / 12), 120, 255})
		}
	}
	var buf bytes.Buffer
	png.Encode(&buf, big)
	code, att := h.do("POST", wbBase+"/threads/"+thread+"/attachments", buf.Bytes(), map[string]string{"Content-Type": "image/png", "X-Workbench-Upload": "1"})
	if code != 201 || att["contentType"] != "image/jpeg" {
		t.Fatalf("upload downscaled: %d %v", code, att)
	}
	var name string
	h.app.db.QueryRow(context.Background(), `SELECT object_name FROM wb_attachments WHERE id=$1`, att["id"]).Scan(&name)
	stored, _ := h.objects.Get(context.Background(), name)
	cfg, _, err := image.DecodeConfig(bytes.NewReader(stored))
	if err != nil || cfg.Width != 1568 || cfg.Height != 1176 {
		t.Fatalf("stored image is at most 1568 px: %+v %v", cfg, err)
	}

	h.call("POST", wbBase+"/threads/"+thread+"/messages", map[string]any{"clientId": "stuck-0001-fixture", "text": "What is in this?", "attachmentIds": []string{att["id"].(string)}}, 201)
	h.wait()
	snap := h.call("GET", wbBase+"/threads/"+thread, nil, 200)
	if stuck, _ := snap["thread"].(map[string]any)["stuck"].(string); !strings.Contains(stuck, "400") {
		t.Fatalf("a non-retryable 4xx marks the thread: %v", snap["thread"])
	}
	h.send(thread, "Hello again?")
	h.wait()
	if h.model.callCount() != 1 {
		t.Fatalf("a stuck thread makes no more model calls: %d", h.model.callCount())
	}
	events := h.events(thread, 0)
	var last map[string]any
	json.Unmarshal(events[len(events)-1].Payload, &last)
	if last["freshThread"] != true || !strings.Contains(last["message"].(string), "fresh thread") {
		t.Fatalf("the sheet is told to offer a fresh thread: %v", last)
	}
	// A fresh thread works.
	fresh := h.newThread()
	h.send(fresh, "Starting over")
	h.wait()
	if h.model.callCount() != 2 {
		t.Fatalf("a fresh thread runs: %d", h.model.callCount())
	}
}

// Finding 2: an old image is replaced in the replayed history once it falls
// behind the last few turns, and thinking after it is dropped.
func TestWorkbenchOldImagesLeaveTheReplay(t *testing.T) {
	turns := []fakeTurn{}
	for i := 0; i < 5; i++ {
		turns = append(turns, fakeTurn{Blocks: []fakeBlock{{Text: fmt.Sprintf("Reply %d.", i)}}, Input: 100, Output: 5})
	}
	h := newWBHarness(t, nil, turns...)
	thread := h.newThread()
	small := image.NewRGBA(image.Rect(0, 0, 20, 20))
	var buf bytes.Buffer
	png.Encode(&buf, small)
	_, att := h.do("POST", wbBase+"/threads/"+thread+"/attachments", buf.Bytes(), map[string]string{"Content-Type": "image/png", "X-Workbench-Upload": "1"})
	h.call("POST", wbBase+"/threads/"+thread+"/messages", map[string]any{"clientId": "image-0001-fixture", "text": "Look", "attachmentIds": []string{att["id"].(string)}}, 201)
	h.wait()
	if !strings.Contains(fmt.Sprint(h.model.lastBody()["messages"]), "base64") {
		t.Fatal("a new image is sent inline")
	}
	for i := 0; i < 4; i++ {
		h.send(thread, fmt.Sprintf("Next %d", i))
		h.wait()
	}
	body := fmt.Sprint(h.model.lastBody()["messages"])
	if strings.Contains(body, "base64") || !strings.Contains(body, wbImagePlaceholder) {
		t.Fatalf("an old image becomes a placeholder: %s", body)
	}
	thinking := h.model.lastBody()["thinking"].(map[string]any)
	if thinking["block_binding"].(map[string]any)["prefix_mismatch_behavior"] != "drop_block" {
		t.Fatalf("requests degrade instead of failing on an invalidated block: %v", thinking)
	}
}

// Finding 4: proposals after reading outside content are flagged, and GitHub
// writes are never approvable from a notification.
func TestWorkbenchApprovalsAfterExternalContent(t *testing.T) {
	h := newWBHarness(t, nil,
		fakeTurn{Blocks: []fakeBlock{{ToolName: "github_list_issues", ToolInput: map[string]any{}}}, Stop: "tool_use", Input: 100, Output: 5},
		fakeTurn{Blocks: []fakeBlock{
			{ToolName: "github_comment", ToolInput: map[string]any{"number": 7, "body": "Fictional comment."}},
			{ToolName: "jazz_add_practice_block", ToolInput: map[string]any{"date": "2026-01-03", "title": "Sample scales", "minutes": 10, "type": "scales"}},
		}, Stop: "tool_use", Input: 100, Output: 5},
		fakeTurn{Blocks: []fakeBlock{{Text: "Proposed."}}, Input: 100, Output: 5},
		fakeTurn{Blocks: []fakeBlock{{ToolName: "jazz_add_practice_block", ToolInput: map[string]any{"date": "2026-01-04", "title": "Sample tones", "minutes": 10, "type": "fundamentals"}}}, Stop: "tool_use", Input: 100, Output: 5},
		fakeTurn{Blocks: []fakeBlock{{Text: "Proposed."}}, Input: 100, Output: 5},
	)
	h.call("PUT", wbBase+"/push/subscriptions/phone-0001", map[string]any{"subscription": map[string]any{"endpoint": "https://fcm.googleapis.com/fcm/send/fixture", "keys": map[string]any{"p256dh": "k", "auth": "a"}}}, 200)
	thread := h.newThread()
	h.send(thread, "Read the issues and follow up")
	h.wait()
	h.send(thread, "Also add tones tomorrow")
	h.wait()
	approvals := h.call("GET", wbBase+"/threads/"+thread, nil, 200)["approvals"].([]any)
	flagged := map[string]bool{}
	for _, raw := range approvals {
		a := raw.(map[string]any)
		d := a["detail"].(map[string]any)
		flagged[a["title"].(string)] = fmt.Sprint(d["afterExternal"]) == "[GitHub]"
	}
	if !flagged["Comment on #7"] || !flagged["Add “Sample scales” (10 min) to 2026-01-03"] || flagged["Add “Sample tones” (10 min) to 2026-01-04"] {
		t.Fatalf("only proposals after reading GitHub in that turn are flagged: %v", flagged)
	}
	h.pusher.mu.Lock()
	defer h.pusher.mu.Unlock()
	actions := []bool{}
	for _, p := range h.pusher.sent {
		actions = append(actions, p.Actions)
	}
	// Comment (GitHub) no, scales (after external) no, tones yes.
	if fmt.Sprint(actions) != "[false false true]" {
		t.Fatalf("notification actions: %v", actions)
	}
}

// Finding 5: saving drafts while sending never deadlocks.
func TestWorkbenchDraftsAndSendsDoNotDeadlock(t *testing.T) {
	h := newWBHarness(t, func(c *wbConfig) { c.AnthropicKey = "" })
	h.app.wb.llm = nil // no model: messages are stored and the turn ends at once
	thread := h.newThread()
	var wg sync.WaitGroup
	failures := make(chan string, 200)
	for i := 0; i < 40; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			if code, out := h.do("PUT", wbBase+"/threads/"+thread+"/draft", map[string]any{"text": fmt.Sprintf("draft %d", i), "deviceId": "laptop-001"}, nil); code != 200 {
				failures <- fmt.Sprintf("draft %d: %d %v", i, code, out)
			}
		}(i)
		go func(i int) {
			defer wg.Done()
			if code, out := h.do("POST", wbBase+"/threads/"+thread+"/messages", map[string]any{"clientId": fmt.Sprintf("dl-%08d-fixture", i), "text": "x", "deviceId": "phone-0001"}, nil); code != 201 {
				failures <- fmt.Sprintf("send %d: %d %v", i, code, out)
			}
		}(i)
	}
	wg.Wait()
	close(failures)
	for f := range failures {
		t.Error(f)
	}
}

// Finding 6: a drain stops new turns and cancels running ones, which are
// marked interrupted and re-answered by recovery.
func TestWorkbenchDrainAndResume(t *testing.T) {
	hold := make(chan struct{})
	h := newWBHarness(t, nil,
		fakeTurn{Blocks: []fakeBlock{{Text: "This answer is cut off by a deploy."}}, Input: 100, Output: 5, Hold: hold},
		fakeTurn{Blocks: []fakeBlock{{Text: "Picking up where I stopped."}}, Input: 100, Output: 5})
	runs, cancelRuns := context.WithCancel(context.Background())
	h.app.wb.baseCtx = runs
	thread := h.newThread()
	h.send(thread, "Explain the bridge")
	deadline := time.Now().Add(5 * time.Second)
	for h.model.callCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	h.app.wbDrain(cancelRuns, 100*time.Millisecond)
	close(hold)
	h.wait()
	var status string
	h.app.db.QueryRow(context.Background(), `SELECT status FROM wb_runs WHERE thread_id=$1`, thread).Scan(&status)
	if status != "interrupted" {
		t.Fatalf("the cancelled run is interrupted: %q", status)
	}
	// While draining, nothing new starts.
	h.send(thread, "Are you there?")
	h.wait()
	if h.model.callCount() != 1 {
		t.Fatalf("no turns while draining: %d", h.model.callCount())
	}
	// Another instance (not draining) recovers: the resume note and the waiting
	// message are answered in one turn.
	h.app.wb.mu.Lock()
	h.app.wb.draining = false
	h.app.wb.mu.Unlock()
	h.app.wb.baseCtx = context.Background()
	h.app.db.Exec(context.Background(), `UPDATE wb_threads SET run_state='idle', run_owner='' WHERE id=$1`, uuid.MustParse(thread))
	h.app.wbRecover(context.Background())
	h.wait()
	if h.model.callCount() != 2 {
		t.Fatalf("recovery re-answers: %d calls", h.model.callCount())
	}
	body := fmt.Sprint(h.model.lastBody()["messages"])
	if !strings.Contains(body, "previous turn was interrupted") || !strings.Contains(body, "Are you there?") {
		t.Fatalf("resume note and waiting message: %s", body)
	}
}

// Finding 6: a run that died after taking its message (no unanswered message
// left) is still re-answered.
func TestWorkbenchRecoveryRequeuesTakenMessages(t *testing.T) {
	h := newWBHarness(t, nil, fakeTurn{Blocks: []fakeBlock{{Text: "Answer, again."}}, Input: 100, Output: 5})
	thread := h.newThread()
	id := uuid.MustParse(thread)
	ctx := context.Background()
	run := uuid.New()
	h.app.db.Exec(ctx, `UPDATE wb_threads SET run_state='running', run_owner='dead', run_heartbeat=now()-interval '10 minutes' WHERE id=$1`, id)
	h.app.db.Exec(ctx, `INSERT INTO wb_runs (id,thread_id) VALUES ($1,$2)`, run, id)
	h.app.db.Exec(ctx, `INSERT INTO wb_messages (id,thread_id,role,text,client_id,handled_at,run_id) VALUES ($1,$2,'user','Taken but never answered','taken-0001',now(),$3)`, uuid.New(), id, run)
	h.app.db.Exec(ctx, `INSERT INTO wb_api_turns (thread_id,seq,role,param) VALUES ($1,0,'user',$2)`, id, `{"role":"user","content":[{"type":"text","text":"Taken but never answered"}]}`)
	h.app.wbRecover(ctx)
	h.wait()
	if h.model.callCount() != 1 || !strings.Contains(fmt.Sprint(h.model.lastBody()["messages"]), "previous turn was interrupted") {
		t.Fatalf("re-answered after recovery: %d calls", h.model.callCount())
	}
}
