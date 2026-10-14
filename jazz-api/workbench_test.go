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
	// jazz_add_tune uses the Repertoire page's own validation before the card.
	title, detail, err := app.wbDescribe("jazz_add_tune", json.RawMessage(`{"title":"  Sample Ballad ","category":"ballad","referenceArtist":"Sample Artist","referenceTitle":"Sample Album (1999)","concertKey":"db"}`))
	if err != nil || title != "Add tune “Sample Ballad” to the ballad list (current set)" ||
		detail["title"] != "Sample Ballad" || detail["chosen"] != true || detail["concertKey"] != "Db" ||
		detail["body"] != "Title: Sample Ballad\nList: ballad\nIn the current set: yes\nReference artist: Sample Artist\nReference recording: Sample Album (1999)\nReference link: (none)\nConcert key: Db\nNotes: (none)" {
		t.Fatalf("add tune card: %q %v %v", title, detail, err)
	}
	if title, _, err := app.wbDescribe("jazz_add_tune", json.RawMessage(`{"title":"Sample Pop","category":"pop","chosen":false}`)); err != nil || title != "Add tune “Sample Pop” to the pop list (not in the current set)" {
		t.Fatalf("add tune outside the set: %q %v", title, err)
	}
	for input, want := range map[string]string{
		`{"title":"   ","category":"ballad"}`:                                   "title must be 1 to 160",
		`{"title":"Sample","category":"swing"}`:                                 "category is invalid",
		`{"title":"Sample","category":"ballad","referenceUrl":"http://x.test"}`: "https://",
		`{"title":"Sample","category":"ballad","concertKey":"H#"}`:              "concert key",
		`{"title":"Sample","category":"ballad","tuneId":"sample"}`:              "INVALID_JSON",
	} {
		if _, _, err := app.wbDescribe("jazz_add_tune", json.RawMessage(input)); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s: want %q, got %v", input, want, err)
		}
	}
	if s, _ := wbSpec("jazz_add_tune"); s.Policy != wbApprove || fmt.Sprint(s.Properties["category"].(map[string]any)["enum"]) != "[ballad upbeat pop standard]" {
		t.Fatalf("adding a tune needs approval and offers the page's categories: %+v", s)
	}
}

// Keys that differ only in case would be read one way by Go (last
// case-insensitive match wins) and stored another way by jsonb, so they are
// refused anywhere in a tool input.
func TestWorkbenchDecodeRefusesCaseVariantKeys(t *testing.T) {
	var in wbAddTuneInput
	for _, raw := range []string{
		`{"title":"Shown Title","Title":"Hidden Title","category":"ballad"}`,
		`{"Title":"Hidden Title","title":"Shown Title","category":"ballad"}`,
		`{"title":"A","title":"B","category":"ballad"}`,
		`{"title":"A","category":"ballad","concertKey":"C","concert\u212aey":"D"}`, // Kelvin sign: Go matches it to k
		`{"title":"A","category":"ballad","notes":"x","NOTES":"y"}`,
	} {
		if err := wbDecode([]byte(raw), &in); err == nil || !strings.Contains(err.Error(), "INVALID_JSON: duplicate key") {
			t.Fatalf("%s: %v", raw, err)
		}
	}
	var triage wbTriageInput
	if err := wbDecode([]byte(`{"idea":"x","category":"keep_it","reason":"r","action":{"tool":"commonplace_capture","input":{"text":"a","TEXT":"b"}}}`), &triage); err == nil {
		t.Fatal("nested objects are checked too")
	}
	if err := wbDecode([]byte(`{"title":"Sample","category":"ballad","notes":"{\"title\":1,\"Title\":2}"}`), &in); err != nil || in.Title != "Sample" {
		t.Fatalf("a string that looks like JSON is just text: %v", err)
	}
}

// What an approval runs is the canonical re-encoding of what the card shows:
// preparing that encoding again yields the same card and the same bytes.
func TestWorkbenchCardEqualsWhatRuns(t *testing.T) {
	app := &application{wb: newWorkbench(wbConfig{GitHubRepo: "fixture/site", GitHubAPI: "http://127.0.0.1:1", GitHubToken: "gh-test-token"})}
	inputs := map[string]string{
		"github_create_issue":     `{"title":"  Sample issue ","body":"Sample body","labels":["do-now"]}`,
		"github_comment":          `{"number":7,"body":"Sample comment"}`,
		"jazz_add_practice_block": `{"date":"2026-01-02","title":" Sample tones ","minutes":10,"type":"made-up","instructions":"Soft.","oneDay":true}`,
		"jazz_mark_tune":          `{"tuneId":"sample-tune","note":"Sample note that is longer than sixty characters, so the title clips it here."}`,
		"jazz_add_tune":           `{"title":" Sample Ballad ","category":"ballad","referenceUrl":"https://example.test/take","concertKey":"eb","notes":"Sample notes"}`,
		"commonplace_capture":     `{"text":"Sample idea","kind":"idea"}`,
		"jazz_add_note":           `{"target":"block","id":"6f1b2d1e-5a3c-4e0b-9d57-3c2a8f0e7b11","note":" Sample note "}`,
	}
	for _, s := range wbToolSpecs {
		if s.Policy == wbApprove && inputs[s.Name] == "" {
			t.Fatalf("approval tool %s has no case here", s.Name)
		}
	}
	for name, raw := range inputs {
		first, err := app.wbPrepare(name, json.RawMessage(raw))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		again, err := app.wbPrepare(name, first.Input)
		if err != nil || again.Title != first.Title || fmt.Sprint(again.Detail) != fmt.Sprint(first.Detail) || string(again.Input) != string(first.Input) {
			t.Fatalf("%s: the stored input must describe the same card: %v\n%+v\n%+v", name, err, first, again)
		}
	}
	// The reviewer's probe: the card and the run can no longer disagree.
	if _, err := app.wbPrepare("jazz_add_tune", json.RawMessage(`{"title":"Shown Title","Title":"Hidden Title","category":"ballad"}`)); err == nil {
		t.Fatal("case-variant keys are refused before any card")
	}
	p, _ := app.wbPrepare("jazz_add_tune", json.RawMessage(inputs["jazz_add_tune"]))
	var stored wbAddTuneInput
	if err := wbDecode(p.Input, &stored); err != nil || stored.Title != p.Detail["title"] || stored.Title != "Sample Ballad" || stored.ConcertKey != "Eb" || stored.Chosen == nil || !*stored.Chosen {
		t.Fatalf("the stored input is the validated one: %s %v", p.Input, err)
	}
	// jazz_mark_tune's title clips a note; its preview carries all of it.
	p, _ = app.wbPrepare("jazz_mark_tune", json.RawMessage(inputs["jazz_mark_tune"]))
	if p.Detail["body"] != "Sample note that is longer than sixty characters, so the title clips it here." {
		t.Fatalf("mark tune preview: %v", p.Detail)
	}
}

// Every field jazz_add_tune writes is on the card, unclipped.
func TestWorkbenchAddTuneCardShowsEverything(t *testing.T) {
	app := &application{wb: newWorkbench(wbConfig{GitHubRepo: "fixture/site", GitHubAPI: "http://127.0.0.1:1"})}
	title := strings.Repeat("Sample ", 22) + "Ballad" // 160 characters, the maximum
	notes := strings.Repeat("Sample practice note. ", 363) + "END"
	input, _ := json.Marshal(map[string]any{"title": title, "category": "standard", "chosen": false, "referenceArtist": strings.Repeat("A", 160),
		"referenceTitle": strings.Repeat("R", 160), "referenceUrl": "https://example.test/sample-take?t=42", "concertKey": "F#m", "notes": notes})
	p, err := app.wbPrepare("jazz_add_tune", input)
	if err != nil {
		t.Fatal(err)
	}
	if len([]rune(title)) != 160 || len(notes) > 8000 || len(notes) < 7900 {
		t.Fatalf("fixture sizes: %d %d", len([]rune(title)), len(notes))
	}
	if !strings.Contains(p.Title, "“"+title+"”") || len([]rune(p.Title)) > 300 {
		t.Fatalf("the card title holds the whole tune title and is never clipped (300 runes): %d %q", len([]rune(p.Title)), p.Title)
	}
	body := p.Detail["body"].(string)
	for _, want := range []string{"Title: " + title, "List: standard", "In the current set: no", "Reference artist: " + strings.Repeat("A", 160),
		"Reference recording: " + strings.Repeat("R", 160), "Reference link: https://example.test/sample-take?t=42", "Concert key: F#m", "Notes: " + notes} {
		if !strings.Contains(body, want) {
			t.Fatalf("the card preview is missing %.60q", want)
		}
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
