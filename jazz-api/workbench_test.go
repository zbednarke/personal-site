package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
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
	if wbTitleFrom("  First line\nsecond") != "First line" {
		t.Fatal("thread titles come from the first line")
	}
}

// A mid-output fallback: the echo omits thinking and tool_use blocks before
// the last fallback block, keeps text, and only post-boundary tool calls run.
func TestWorkbenchFallbackEcho(t *testing.T) {
	var m anthropic.BetaMessage
	raw := `{"role":"assistant","content":[
		{"type":"thinking","thinking":"","signature":"sig-before"},
		{"type":"text","text":"Blue Bossa starts on "},
		{"type":"tool_use","id":"toolu_before","name":"site_status","input":{}},
		{"type":"fallback","from":{"model":"claude-opus-5-5"},"to":{"model":"claude-opus-4-8"}},
		{"type":"thinking","thinking":"","signature":"sig-after"},
		{"type":"text","text":"the four chord."},
		{"type":"tool_use","id":"toolu_after","name":"jazz_repertoire","input":{}}]}`
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatal(err)
	}
	echo, _ := json.Marshal(wbEchoParam(m))
	var got struct {
		Content []struct {
			Type      string `json:"type"`
			ID        string `json:"id"`
			Signature string `json:"signature"`
			Text      string `json:"text"`
		} `json:"content"`
	}
	json.Unmarshal(echo, &got)
	kinds := []string{}
	for _, c := range got.Content {
		kinds = append(kinds, c.Type+":"+c.ID+c.Signature+c.Text)
	}
	want := []string{"text:Blue Bossa starts on ", "thinking:sig-after", "text:the four chord.", "tool_use:toolu_after"}
	if strings.Join(kinds, "|") != strings.Join(want, "|") {
		t.Fatalf("echo:\n got %v\nwant %v", kinds, want)
	}
	uses := wbToolUses(m)
	if len(uses) != 1 || uses[0].ID != "toolu_after" {
		t.Fatalf("only tool calls after the last fallback run: %v", uses)
	}
	if text := wbMessageText(m); text != "Blue Bossa starts on the four chord." {
		t.Fatalf("the visible reply keeps the partial and its continuation: %q", text)
	}
	// Without a fallback nothing is dropped.
	var plain anthropic.BetaMessage
	json.Unmarshal([]byte(`{"role":"assistant","content":[{"type":"thinking","thinking":"","signature":"s"},{"type":"text","text":"a"},{"type":"text","text":"b"},{"type":"tool_use","id":"t1","name":"site_status","input":{}}]}`), &plain)
	if p := wbEchoParam(plain); len(p.Content) != 4 || len(wbToolUses(plain)) != 1 || wbMessageText(plain) != "a\n\nb" {
		t.Fatalf("plain echo: %d blocks, %q", len(p.Content), wbMessageText(plain))
	}
}

// History compaction: old images become placeholders and thinking blocks
// after the first edit are removed; newer turns are untouched.
func TestWorkbenchCompactTurns(t *testing.T) {
	img := `{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAAA"}}`
	think := `{"type":"thinking","thinking":"","signature":"s"}`
	rows := []wbTurnRow{
		{0, "user", []byte(`{"role":"user","content":[{"type":"text","text":"first"}]}`)},
		{1, "assistant", []byte(`{"role":"assistant","content":[` + think + `,{"type":"text","text":"ok"}]}`)},
		{2, "user", []byte(`{"role":"user","content":[` + img + `,{"type":"text","text":"look"}]}`)},
		{3, "assistant", []byte(`{"role":"assistant","content":[` + think + `,{"type":"text","text":"seen"}]}`)},
		{4, "user", []byte(`{"role":"user","content":[` + img + `]}`)},
		{5, "assistant", []byte(`{"role":"assistant","content":[` + think + `]}`)},
	}
	out, changed := wbCompactTurns(rows, 2)
	if fmt.Sprint(changed) != "[2 3 5]" {
		t.Fatalf("changed rows: %v", changed)
	}
	if string(out[1].Param) != string(rows[1].Param) || string(out[4].Param) != string(rows[4].Param) {
		t.Fatal("turns before the edit and the newest images are untouched")
	}
	if !strings.Contains(string(out[2].Param), wbImagePlaceholder) || strings.Contains(string(out[2].Param), "AAAA") {
		t.Fatalf("old image replaced: %s", out[2].Param)
	}
	for _, i := range []int{3, 5} {
		if strings.Contains(string(out[i].Param), "thinking") {
			t.Fatalf("thinking after the edit removed: %s", out[i].Param)
		}
	}
	if !strings.Contains(string(out[5].Param), "[…]") {
		t.Fatalf("an emptied turn keeps a text block: %s", out[5].Param)
	}
	if _, again := wbCompactTurns(out, 2); len(again) != 0 {
		t.Fatalf("compaction is stable once applied: %v", again)
	}
	if est := wbPromptEstimate([][]byte{rows[2].Param}); est < wbImageTokens || est > wbImageTokens+20 {
		t.Fatalf("estimate counts an image once, not its base64: %d", est)
	}
}

// Uploaded images are validated and downscaled to what the model sees.
func TestWorkbenchPrepareImage(t *testing.T) {
	big := image.NewRGBA(image.Rect(0, 0, 3200, 1800))
	for y := 0; y < 1800; y++ {
		for x := 0; x < 3200; x++ {
			big.Set(x, y, color.RGBA{uint8(x), uint8(y), 90, 255})
		}
	}
	var buf bytes.Buffer
	png.Encode(&buf, big)
	out, ct, err := wbPrepareImage(buf.Bytes())
	if err != nil || ct != "image/jpeg" {
		t.Fatalf("%v %s", err, ct)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(out))
	if err != nil || cfg.Width != 1568 || cfg.Height != 882 {
		t.Fatalf("downscaled to 1568 px: %+v %v", cfg, err)
	}
	small := image.NewNRGBA(image.Rect(0, 0, 40, 30))
	small.Set(1, 1, color.NRGBA{255, 0, 0, 128})
	buf.Reset()
	png.Encode(&buf, small)
	if out, ct, err := wbPrepareImage(buf.Bytes()); err != nil || ct != "image/png" || !bytes.Equal(out, buf.Bytes()) {
		t.Fatalf("a small PNG passes through: %v %s", err, ct)
	}
	if _, _, err := wbPrepareImage([]byte("GIF89a not really")); err == nil {
		t.Fatal("unreadable images are refused")
	}
}
