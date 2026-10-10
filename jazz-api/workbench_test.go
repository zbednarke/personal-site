package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
)

// Workbench unit tests (no database). All data is fictional.

func TestWorkbenchCostFromUsage(t *testing.T) {
	var u anthropic.BetaUsage
	if err := json.Unmarshal([]byte(`{"input_tokens":1000,"output_tokens":500,"cache_read_input_tokens":10000,"cache_creation_input_tokens":2000}`), &u); err != nil {
		t.Fatal(err)
	}
	c := wbCost("claude-opus-5-5", u)
	// 1000*4 + 500*20 + 10000*0.2 + 2000*5 micro-dollars.
	if c.MicroUSD != 4000+10000+2000+10000 || c.Input != 1000 || c.CacheRead != 10000 {
		t.Fatalf("cost: %+v", c)
	}
	// A response that fell back is priced per hop, by each hop's model.
	if err := json.Unmarshal([]byte(`{"input_tokens":0,"output_tokens":0,"iterations":[
		{"type":"message","model":"claude-opus-5-5","input_tokens":100,"output_tokens":0,"cache_read_input_tokens":0,"cache_creation_input_tokens":0},
		{"type":"message","model":"claude-opus-4-8","input_tokens":100,"output_tokens":10,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}]}`), &u); err != nil {
		t.Fatal(err)
	}
	if c := wbCost("claude-opus-5-5", u); c.MicroUSD != 400+500+250 || c.Input != 200 || c.Output != 10 {
		t.Fatalf("fallback cost: %+v", c)
	}
}

func TestWorkbenchToolsAreStableAndStrict(t *testing.T) {
	a, _ := json.Marshal(wbTools())
	b, _ := json.Marshal(wbTools())
	if string(a) != string(b) {
		t.Fatal("the tool list must serialize identically every time (prompt caching)")
	}
	var tools []map[string]any
	json.Unmarshal(a, &tools)
	if len(tools) != len(wbToolSpecs) {
		t.Fatalf("tools: %d", len(tools))
	}
	seen := map[string]bool{}
	for _, tool := range tools {
		name := tool["name"].(string)
		schema := tool["input_schema"].(map[string]any)
		if schema["additionalProperties"] != false || tool["eager_input_streaming"] != true || seen[name] {
			t.Fatalf("tool %s: %v", name, tool)
		}
		seen[name] = true
	}
	// Every write is gated: approval, or an explicitly low-risk capture.
	for _, s := range wbToolSpecs {
		write := strings.Contains(s.Name, "create") || strings.Contains(s.Name, "comment") || strings.Contains(s.Name, "add_") || strings.Contains(s.Name, "mark") || strings.Contains(s.Name, "capture")
		if write && s.Policy != wbApprove && s.Policy != wbCapture {
			t.Fatalf("%s writes without approval", s.Name)
		}
	}
	if s, _ := wbSpec("github_create_issue"); s.Policy != wbApprove {
		t.Fatal("issues need approval")
	}
	if s, _ := wbSpec("commonplace_capture"); s.Policy != wbCapture {
		t.Fatal("captures are low-risk")
	}
}

func TestWorkbenchDescribeValidatesBeforeTheCard(t *testing.T) {
	app := &application{wb: newWorkbench(wbConfig{GitHubRepo: "fixture/site", GitHubAPI: "http://127.0.0.1:1"})}
	if _, _, err := app.wbDescribe("github_create_issue", json.RawMessage(`{"title":"Sample","body":"x"}`)); err == nil || !strings.Contains(err.Error(), "WORKBENCH_GITHUB_TOKEN") {
		t.Fatalf("without a token, issue writes degrade with a clear reason: %v", err)
	}
	if _, _, err := app.wbDescribe("jazz_add_practice_block", json.RawMessage(`{"date":"tomorrow","title":"x","minutes":5,"type":"tune"}`)); err == nil {
		t.Fatal("dates are validated before the owner sees a card")
	}
	if _, _, err := app.wbDescribe("jazz_add_practice_block", json.RawMessage(`{"date":"2026-01-02","title":"x","minutes":5,"type":"tune","extra":1}`)); err == nil || !strings.Contains(err.Error(), "INVALID_JSON") {
		t.Fatalf("unknown fields are refused: %v", err)
	}
	if _, _, err := app.wbDescribe("jazz_add_practice_block", json.RawMessage(`{"date":"2026-01-02","title":"Sample`)); err == nil {
		t.Fatal("a truncated eager-streamed input is refused")
	}
	title, _, err := app.wbDescribe("jazz_mark_tune", json.RawMessage(`{"tuneId":"sample-tune","milestone":"changes","status":"solid","keyKnown":"Bb"}`))
	if err != nil || title != "Update tune sample-tune: changes → solid; knows it in Bb" {
		t.Fatalf("tune card: %q %v", title, err)
	}
	if _, _, err := app.wbDescribe("jazz_mark_tune", json.RawMessage(`{"tuneId":"sample-tune","milestone":"changes"}`)); err == nil {
		t.Fatal("milestone without status")
	}
}

func TestWorkbenchHelpers(t *testing.T) {
	for _, ok := range []string{"https://fcm.googleapis.com/fcm/send/x", "https://web.push.apple.com/abc", "https://updates.push.services.mozilla.com/wpush/v2/x"} {
		if !wbPushEndpointAllowed(ok) {
			t.Fatalf("%s should be allowed", ok)
		}
	}
	for _, bad := range []string{"http://fcm.googleapis.com/x", "https://169.254.169.254/", "https://evil.example/googleapis.com", "https://user@fcm.googleapis.com/x"} {
		if wbPushEndpointAllowed(bad) {
			t.Fatalf("%s should be refused", bad)
		}
	}
	if e, err := wbEffort(""); e != "low" || err != nil {
		t.Fatal("chat and triage default to low effort")
	}
	if _, err := wbEffort("max"); err == nil {
		t.Fatal("max is not offered")
	}
	ctx := wbCleanContext(&wbPageContext{Page: "/jazz/", Selection: strings.Repeat("a", 5000), LocalDate: "not-a-date"})
	var c wbPageContext
	json.Unmarshal(ctx, &c)
	if len(c.Selection) != 1500 || c.LocalDate != "" || c.Page != "/jazz/" {
		t.Fatalf("context is bounded and validated: %d %q", len(c.Selection), c.LocalDate)
	}
	var m anthropic.BetaMessage
	json.Unmarshal([]byte(`{"content":[{"type":"text","text":"partial from the first model"},{"type":"fallback","from":{"model":"claude-opus-5-5"},"to":{"model":"claude-opus-4-8"}},{"type":"text","text":"Final answer."}]}`), &m)
	if got := wbMessageText(m); got != "Final answer." {
		t.Fatalf("text after a fallback boundary replaces the refused partial: %q", got)
	}
	if wbTitleFrom("  First line\nsecond") != "First line" {
		t.Fatal("thread titles come from the first line")
	}
}
