package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Host-side tools. Site data tools call the existing handlers in-process
// (wbInvoke), so they get exactly the validation and side effects the pages
// get, without an HTTP hop. Each tool has a policy:
//   read     runs immediately;
//   capture  low-risk write, runs immediately (Moments, notes);
//   approve  shown as a confirmation card, runs only when the owner approves.

type wbPolicy string

const (
	wbRead    wbPolicy = "read"
	wbCapture wbPolicy = "capture"
	wbApprove wbPolicy = "approve"
)

type wbToolSpec struct {
	Name        string
	Description string
	Policy      wbPolicy
	Properties  map[string]any
	Required    []string
}

func str(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
func enum(desc string, values ...string) map[string]any {
	return map[string]any{"type": "string", "enum": values, "description": desc}
}

var wbWriteActions = []string{"github_create_issue", "commonplace_capture", "jazz_add_note", "jazz_add_practice_block"}

var wbToolSpecs = []wbToolSpec{
	{Name: "triage_idea", Policy: "triage", Description: "Sort a half-formed idea into one category and propose the follow-up as a one-tap card. do_now and spec_it propose github_create_issue; keep_it proposes commonplace_capture or jazz_add_note; ask_me asks one question (no action).",
		Properties: map[string]any{
			"idea":     str("The idea, restated in one or two sentences."),
			"category": enum("The triage category.", "do_now", "spec_it", "keep_it", "ask_me"),
			"reason":   str("One sentence: why this category."),
			"question": str("ask_me only: the single clarifying question."),
			"action": map[string]any{"type": "object", "description": "The proposed follow-up (not for ask_me): a write tool name and its input.",
				"properties": map[string]any{"tool": enum("Write tool to run on confirm.", wbWriteActions...), "input": map[string]any{"type": "object", "description": "That tool's input."}},
				"required":   []string{"tool", "input"}},
		}, Required: []string{"idea", "category", "reason"}},
	{Name: "github_list_issues", Policy: wbRead, Description: "List issues in the site's GitHub repository (not pull requests). Optional free-text query searches titles and bodies.",
		Properties: map[string]any{"state": enum("Default open.", "open", "closed", "all"), "labels": str("Comma-separated label names."), "query": str("Search text."), "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 30}}},
	{Name: "github_create_issue", Policy: wbApprove, Description: "Create an issue in the site's GitHub repository. Needs the owner's approval.",
		Properties: map[string]any{"title": str("Issue title, under 120 characters."), "body": str("Markdown body."), "labels": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "maxItems": 5}},
		Required:   []string{"title", "body"}},
	{Name: "github_comment", Policy: wbApprove, Description: "Comment on an issue or pull request in the site's repository. Needs the owner's approval.",
		Properties: map[string]any{"number": map[string]any{"type": "integer", "minimum": 1}, "body": str("Markdown comment.")}, Required: []string{"number", "body"}},
	{Name: "site_status", Policy: wbRead, Description: "Read CI on main, open pull requests with their checks, and the latest deploy. Shown to the owner as a status card.",
		Properties: map[string]any{}},
	{Name: "jazz_today", Policy: wbRead, Description: "Read the practice plan (sections with ids, minutes, notes, linked tunes) for a date. Defaults to today in the owner's time zone.",
		Properties: map[string]any{"date": str("YYYY-MM-DD.")}},
	{Name: "jazz_repertoire", Policy: wbRead, Description: "Read the repertoire: tune ids, titles, milestones and keys known.",
		Properties: map[string]any{}},
	{Name: "jazz_add_practice_block", Policy: wbApprove, Description: "Add a section to the practice plan for a date. Needs the owner's approval.",
		Properties: map[string]any{
			"date": str("YYYY-MM-DD."), "title": str("Section title."), "minutes": map[string]any{"type": "integer", "minimum": 1, "maximum": 360},
			"type":         enum("Kind of section.", "fundamentals", "technique", "scales", "listening", "tune", "improvisation", "other"),
			"instructions": str("Optional instructions."), "tuneId": str("Optional repertoire tune id to link (from jazz_repertoire)."),
			"oneDay": map[string]any{"type": "boolean", "description": "Only on that date; do not carry forward."},
		}, Required: []string{"date", "title", "minutes", "type"}},
	{Name: "jazz_mark_tune", Policy: wbApprove, Description: "Update a repertoire tune: a milestone's status, whether it is in the current set, a key learned, or a note appended. Needs the owner's approval.",
		Properties: map[string]any{
			"tuneId":    str("Tune id from jazz_repertoire."),
			"milestone": enum("Milestone to set.", "melodyByEar", "lyrics", "changes", "transcription", "improvise", "gigReady"),
			"status":    enum("Milestone status.", "not_started", "learning", "solid"),
			"chosen":    map[string]any{"type": "boolean", "description": "In the current set."},
			"keyKnown":  str("A key to add to keys known, e.g. Bb."),
			"note":      str("Text to append to the tune's notes."),
		}, Required: []string{"tuneId"}},
	{Name: "jazz_add_note", Policy: wbCapture, Description: "Append a note to a practice section or a recorded take. Runs immediately.",
		Properties: map[string]any{"target": enum("What the id refers to.", "block", "recording"), "id": str("Section or take id."), "note": str("Note text.")},
		Required:   []string{"target", "id", "note"}},
	{Name: "commonplace_capture", Policy: wbCapture, Description: "Save a Commonplace Moment (an idea, quote, dream or conversation) exactly as the owner gave it. Runs immediately.",
		Properties: map[string]any{"text": str("The words, verbatim."), "kind": enum("Default idea.", "idea", "quote", "dream", "conversation"), "title": str("Optional short title."), "why": str("Optional: why it matters, in the owner's words.")},
		Required:   []string{"text"}},
}

func wbSpec(name string) (wbToolSpec, bool) {
	for _, s := range wbToolSpecs {
		if s.Name == name {
			return s, true
		}
	}
	return wbToolSpec{}, false
}

// wbTools is deterministic (fixed order) so the cached prefix stays stable.
func wbTools() []anthropic.BetaToolUnionParam {
	out := make([]anthropic.BetaToolUnionParam, 0, len(wbToolSpecs))
	for _, s := range wbToolSpecs {
		t := anthropic.BetaToolParam{
			Name:                s.Name,
			Description:         anthropic.String(s.Description),
			InputSchema:         anthropic.BetaToolInputSchemaParam{Properties: s.Properties, Required: s.Required, ExtraFields: map[string]any{"additionalProperties": false}},
			EagerInputStreaming: anthropic.Bool(true),
		}
		out = append(out, anthropic.BetaToolUnionParam{OfTool: &t})
	}
	return out
}

func wbUntrusted(source string, v any) string {
	b, _ := json.MarshalIndent(v, "", " ")
	return fmt.Sprintf("<untrusted_data source=%q>\n%s\n</untrusted_data>", source, b)
}

// wbDecode parses a tool input strictly: eager input streaming means the
// API no longer validates it, so a truncated or malformed input is an error.
func wbDecode(raw []byte, out any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("INVALID_JSON: %v", err)
	}
	return nil
}

// ---- Running tool calls --------------------------------------------------------------

func (app *application) wbRunTools(ctx context.Context, thread, user uuid.UUID, uses []anthropic.BetaContentBlockUnion) []anthropic.BetaContentBlockParamUnion {
	results := make([]anthropic.BetaContentBlockParamUnion, 0, len(uses))
	for _, tu := range uses {
		_, _ = app.wbEmit(ctx, thread, "tool.started", map[string]any{"toolUseId": tu.ID, "name": tu.Name})
		text, card, err := app.wbRunTool(ctx, thread, user, tu.ID, tu.Name, tu.Input)
		finished := map[string]any{"toolUseId": tu.ID, "name": tu.Name, "ok": err == nil}
		if card != nil {
			finished["card"] = card
		}
		if err != nil {
			finished["error"] = err.Error()
			text = "Error: " + err.Error()
		}
		_, _ = app.wbEmit(ctx, thread, "tool.finished", finished)
		results = append(results, anthropic.NewBetaToolResultBlock(tu.ID, text, err != nil))
	}
	return results
}

func (app *application) wbRunTool(ctx context.Context, thread, user uuid.UUID, toolUseID, name string, input json.RawMessage) (string, any, error) {
	spec, ok := wbSpec(name)
	if !ok {
		return "", nil, fmt.Errorf("unknown tool %q", name)
	}
	if name == "triage_idea" {
		return app.wbTriage(ctx, thread, user, toolUseID, input)
	}
	if spec.Policy == wbApprove {
		title, detail, err := app.wbDescribe(name, input)
		if err != nil {
			return "", nil, err
		}
		a, err := app.wbPropose(ctx, thread, user, "tool", name, input, toolUseID, title, detail)
		if err != nil {
			return "", nil, err
		}
		return fmt.Sprintf("Proposed to the owner as a confirmation card (approval %s): %q. It runs only if he approves. Do not propose it again.", a.ID, title), nil, nil
	}
	return app.wbExecute(ctx, user, toolUseID, name, input)
}

// wbDescribe validates a write tool's input up front (so the owner never
// approves something that cannot run) and renders the card's title.
func (app *application) wbDescribe(name string, input json.RawMessage) (string, map[string]any, error) {
	switch name {
	case "github_create_issue":
		var in wbIssueInput
		if err := wbDecode(input, &in); err != nil {
			return "", nil, err
		}
		if err := in.validate(); err != nil {
			return "", nil, err
		}
		if !app.wb.github.canWrite() {
			return "", nil, errors.New("GitHub writes are not configured on the server (WORKBENCH_GITHUB_TOKEN is unset). Give the owner the draft to file by hand")
		}
		return "Create issue: " + in.Title, map[string]any{"title": in.Title, "body": in.Body, "labels": in.Labels}, nil
	case "github_comment":
		var in wbCommentInput
		if err := wbDecode(input, &in); err != nil {
			return "", nil, err
		}
		if in.Number < 1 || strings.TrimSpace(in.Body) == "" || len(in.Body) > 60000 {
			return "", nil, errors.New("number and a non-empty body are required")
		}
		if !app.wb.github.canWrite() {
			return "", nil, errors.New("GitHub writes are not configured on the server (WORKBENCH_GITHUB_TOKEN is unset)")
		}
		return fmt.Sprintf("Comment on #%d", in.Number), map[string]any{"number": in.Number, "body": in.Body}, nil
	case "jazz_add_practice_block":
		var in wbBlockInput
		if err := wbDecode(input, &in); err != nil {
			return "", nil, err
		}
		if err := in.validate(); err != nil {
			return "", nil, err
		}
		return fmt.Sprintf("Add “%s” (%d min) to %s", in.Title, in.Minutes, in.Date), map[string]any{"date": in.Date, "title": in.Title, "minutes": in.Minutes, "type": in.Type, "instructions": in.Instructions, "tuneId": in.TuneID}, nil
	case "jazz_mark_tune":
		var in wbMarkTuneInput
		if err := wbDecode(input, &in); err != nil {
			return "", nil, err
		}
		changes, err := in.describe()
		if err != nil {
			return "", nil, err
		}
		return "Update tune " + in.TuneID + ": " + changes, map[string]any{"tuneId": in.TuneID, "changes": changes}, nil
	case "commonplace_capture":
		var in wbCaptureInput
		if err := wbDecode(input, &in); err != nil {
			return "", nil, err
		}
		if strings.TrimSpace(in.Text) == "" {
			return "", nil, errors.New("text is required")
		}
		return "Save a Moment: " + wbCleanText(firstNonEmpty(in.Title, in.Text), 80), map[string]any{"text": in.Text, "kind": in.Kind, "title": in.Title, "why": in.Why}, nil
	case "jazz_add_note":
		var in wbNoteInput
		if err := wbDecode(input, &in); err != nil {
			return "", nil, err
		}
		if err := in.validate(); err != nil {
			return "", nil, err
		}
		return "Add a note to a " + map[string]string{"block": "practice section", "recording": "take"}[in.Target], map[string]any{"target": in.Target, "id": in.ID, "note": in.Note}, nil
	}
	return "", nil, fmt.Errorf("%s is not a write tool", name)
}

type wbTriageInput struct {
	Idea     string `json:"idea"`
	Category string `json:"category"`
	Reason   string `json:"reason"`
	Question string `json:"question"`
	Action   *struct {
		Tool  string          `json:"tool"`
		Input json.RawMessage `json:"input"`
	} `json:"action"`
}

func (app *application) wbTriage(ctx context.Context, thread, user uuid.UUID, toolUseID string, input json.RawMessage) (string, any, error) {
	var in wbTriageInput
	if err := wbDecode(input, &in); err != nil {
		return "", nil, err
	}
	labels := map[string]string{"do_now": "Do now", "spec_it": "Spec it", "keep_it": "Keep it", "ask_me": "Ask me"}
	if labels[in.Category] == "" {
		return "", nil, errors.New("category must be do_now, spec_it, keep_it or ask_me")
	}
	if in.Category == "ask_me" {
		if strings.TrimSpace(in.Question) == "" {
			return "", nil, errors.New("ask_me needs the question")
		}
		card := map[string]any{"kind": "triage", "category": in.Category, "label": labels[in.Category], "idea": in.Idea, "reason": in.Reason, "question": in.Question}
		return "Shown to the owner as a question card. Wait for his answer.", card, nil
	}
	if in.Action == nil {
		return "", nil, errors.New("do_now, spec_it and keep_it need an action")
	}
	allowed := map[string][]string{"do_now": {"github_create_issue"}, "spec_it": {"github_create_issue"}, "keep_it": {"commonplace_capture", "jazz_add_note", "jazz_add_practice_block"}}
	ok := false
	for _, t := range allowed[in.Category] {
		ok = ok || t == in.Action.Tool
	}
	if !ok {
		return "", nil, fmt.Errorf("%s cannot use %s", in.Category, in.Action.Tool)
	}
	title, detail, err := app.wbDescribe(in.Action.Tool, in.Action.Input)
	if err != nil {
		return "", nil, err
	}
	detail = map[string]any{"category": in.Category, "label": labels[in.Category], "idea": in.Idea, "reason": in.Reason, "action": map[string]any{"tool": in.Action.Tool, "title": title, "detail": detail}}
	a, err := app.wbPropose(ctx, thread, user, "triage", in.Action.Tool, in.Action.Input, toolUseID, labels[in.Category]+" · "+title, detail)
	if err != nil {
		return "", nil, err
	}
	return fmt.Sprintf("Triage card shown (approval %s): %s → %s. It runs when the owner confirms. Do not propose it again.", a.ID, labels[in.Category], title), nil, nil
}

// ---- Approvals ----------------------------------------------------------------------

func (app *application) wbPropose(ctx context.Context, thread, user uuid.UUID, kind, tool string, input json.RawMessage, toolUseID, title string, detail map[string]any) (wbApproval, error) {
	detailJSON, _ := json.Marshal(detail)
	if len(input) == 0 {
		input = json.RawMessage(`{}`)
	}
	tx, err := app.db.Begin(ctx)
	if err != nil {
		return wbApproval{}, err
	}
	defer tx.Rollback(ctx)
	a, err := wbScanApproval(tx.QueryRow(ctx, `INSERT INTO wb_approvals AS a (id,thread_id,kind,tool_name,tool_input,tool_use_id,title,detail)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8) RETURNING `+wbApprovalColumns, uuid.New(), thread, kind, tool, []byte(input), toolUseID, wbCleanText(title, 300), detailJSON))
	if err != nil {
		return a, err
	}
	if _, err := app.wbAppendTx(ctx, tx, thread, "approval.requested", map[string]any{"approval": a}); err != nil {
		return a, err
	}
	if err := tx.Commit(ctx); err != nil {
		return a, err
	}
	app.wb.hub.notify(thread)
	app.wbPushApproval(ctx, user, thread, a)
	return a, nil
}

// wbDecide records the first decision on an approval; later decisions lose
// and get the current state back. An approved action runs once, here.
func (app *application) wbDecide(ctx context.Context, user, id uuid.UUID, approve bool, device string) (wbApproval, bool, error) {
	status := "rejected"
	if approve {
		status = "approved"
	}
	tx, err := app.db.Begin(ctx)
	if err != nil {
		return wbApproval{}, false, err
	}
	defer tx.Rollback(ctx)
	a, err := wbScanApproval(tx.QueryRow(ctx, `UPDATE wb_approvals a SET status=$3, decided_by=$4, decided_at=now()
		FROM wb_threads t WHERE a.id=$1 AND a.thread_id=t.id AND t.user_id=$2 AND a.status='pending' RETURNING `+wbApprovalColumns, id, user, status, device))
	if errors.Is(err, pgx.ErrNoRows) {
		tx.Rollback(ctx)
		current, err := wbScanApproval(app.db.QueryRow(ctx, `SELECT `+wbApprovalColumns+` FROM wb_approvals a JOIN wb_threads t ON t.id=a.thread_id WHERE a.id=$1 AND t.user_id=$2`, id, user))
		return current, false, err
	} else if err != nil {
		return a, false, err
	}
	var thread uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT thread_id FROM wb_approvals WHERE id=$1`, id).Scan(&thread); err != nil {
		return a, false, err
	}
	if _, err := app.wbAppendTx(ctx, tx, thread, "approval.resolved", map[string]any{"id": a.ID, "status": a.Status, "decidedBy": device}); err != nil {
		return a, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return a, false, err
	}
	app.wb.hub.notify(thread)
	if !approve {
		app.wbNote(ctx, thread, fmt.Sprintf("The owner declined: %s.", a.Title))
		return a, true, nil
	}
	// The action outlives the request that approved it.
	run, cancel := context.WithTimeout(context.WithoutCancel(ctx), 45*time.Second)
	defer cancel()
	text, card, execErr := app.wbExecute(run, user, a.ID.String(), a.ToolName, a.Input)
	result := map[string]any{"text": text}
	if card != nil {
		result["card"] = card
	}
	final := "approved"
	if execErr != nil {
		final = "failed"
		result = map[string]any{"error": execErr.Error()}
	}
	resultJSON, _ := json.Marshal(result)
	a, err = wbScanApproval(app.db.QueryRow(run, `UPDATE wb_approvals a SET status=$2, result=$3 WHERE id=$1 RETURNING `+wbApprovalColumns, id, final, resultJSON))
	if err != nil {
		return a, true, err
	}
	_, _ = app.wbEmit(run, thread, "approval.result", map[string]any{"approval": a})
	if execErr != nil {
		app.wbNote(run, thread, fmt.Sprintf("The owner approved %q but it failed: %v", a.Title, execErr))
	} else {
		app.wbNote(run, thread, fmt.Sprintf("The owner approved %q. Result: %s", a.Title, wbCleanText(text, 1500)))
	}
	return a, true, nil
}

// wbNote queues server context for the next turn (not shown as a message).
func (app *application) wbNote(ctx context.Context, thread uuid.UUID, text string) {
	if _, err := app.db.Exec(ctx, `INSERT INTO wb_messages (id,thread_id,role,text) VALUES ($1,$2,'note',$3)`, uuid.New(), thread, wbCleanText(text, 4000)); err != nil {
		app.logger.Error("workbench note failed", "error", err)
	}
}

// ---- Executing actions ----------------------------------------------------------------

func (app *application) wbSubject(ctx context.Context, user uuid.UUID) (string, error) {
	var s string
	err := app.db.QueryRow(ctx, `SELECT auth_subject FROM app_users WHERE id=$1`, user).Scan(&s)
	return s, err
}

// wbInvoke calls an existing handler in-process as the owner.
func (app *application) wbInvoke(ctx context.Context, subject string, h http.HandlerFunc, method, target string, pathValues map[string]string, body any) (int, []byte) {
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req := httptest.NewRequestWithContext(context.WithValue(ctx, userSubjectKey, subject), method, target, rdr)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range pathValues {
		req.SetPathValue(k, v)
	}
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec.Code, rec.Body.Bytes()
}

func wbHandlerError(code int, body []byte) error {
	var e struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(body, &e)
	if e.Error == "" {
		e.Error = http.StatusText(code)
	}
	return fmt.Errorf("%s (%d)", e.Error, code)
}

func (app *application) wbExecute(ctx context.Context, user uuid.UUID, idemKey, name string, input json.RawMessage) (string, any, error) {
	subject, err := app.wbSubject(ctx, user)
	if err != nil {
		return "", nil, err
	}
	switch name {
	case "github_list_issues":
		var in struct {
			State  string `json:"state"`
			Labels string `json:"labels"`
			Query  string `json:"query"`
			Limit  int    `json:"limit"`
		}
		if err := wbDecode(input, &in); err != nil {
			return "", nil, err
		}
		issues, err := app.wb.github.listIssues(ctx, in.State, in.Labels, in.Query, in.Limit)
		if err != nil {
			return "", nil, err
		}
		return wbUntrusted("github", issues), nil, nil
	case "github_create_issue":
		var in wbIssueInput
		if err := wbDecode(input, &in); err != nil {
			return "", nil, err
		}
		if err := in.validate(); err != nil {
			return "", nil, err
		}
		issue, err := app.wb.github.createIssue(ctx, in)
		if err != nil {
			return "", nil, err
		}
		return fmt.Sprintf("Created issue #%d: %s", issue.Number, issue.URL), map[string]any{"kind": "issue", "number": issue.Number, "title": issue.Title, "url": issue.URL}, nil
	case "github_comment":
		var in wbCommentInput
		if err := wbDecode(input, &in); err != nil {
			return "", nil, err
		}
		url, err := app.wb.github.comment(ctx, in)
		if err != nil {
			return "", nil, err
		}
		return "Commented: " + url, map[string]any{"kind": "comment", "number": in.Number, "url": url}, nil
	case "site_status":
		card, err := app.wb.github.siteStatus(ctx)
		if err != nil {
			return "", nil, err
		}
		return wbUntrusted("github", card), card, nil
	case "jazz_today":
		var in struct {
			Date string `json:"date"`
		}
		if err := wbDecode(input, &in); err != nil {
			return "", nil, err
		}
		return app.wbJazzToday(ctx, subject, user, in.Date)
	case "jazz_repertoire":
		code, body := app.wbInvoke(ctx, subject, app.getRepertoire, "GET", "/v1/repertoire", nil, nil)
		if code != 200 {
			return "", nil, wbHandlerError(code, body)
		}
		var rep struct {
			Tunes []repertoireTune `json:"tunes"`
		}
		if err := json.Unmarshal(body, &rep); err != nil {
			return "", nil, err
		}
		type tune struct {
			TuneID     string            `json:"tuneId"`
			Title      string            `json:"title"`
			Chosen     bool              `json:"chosen"`
			Milestones map[string]string `json:"milestones"`
			KeysKnown  []string          `json:"keysKnown"`
		}
		out := make([]tune, 0, len(rep.Tunes))
		for _, t := range rep.Tunes {
			out = append(out, tune{t.TuneID, t.Title, t.Chosen, t.Milestones, t.KeysKnown})
		}
		return wbUntrusted("site_data", map[string]any{"tunes": out}), nil, nil
	case "jazz_add_practice_block":
		var in wbBlockInput
		if err := wbDecode(input, &in); err != nil {
			return "", nil, err
		}
		if err := in.validate(); err != nil {
			return "", nil, err
		}
		return app.wbAddBlock(ctx, subject, idemKey, in)
	case "jazz_mark_tune":
		var in wbMarkTuneInput
		if err := wbDecode(input, &in); err != nil {
			return "", nil, err
		}
		return app.wbMarkTune(ctx, subject, in)
	case "jazz_add_note":
		var in wbNoteInput
		if err := wbDecode(input, &in); err != nil {
			return "", nil, err
		}
		if err := in.validate(); err != nil {
			return "", nil, err
		}
		return app.wbAddNote(ctx, subject, user, in)
	case "commonplace_capture":
		var in wbCaptureInput
		if err := wbDecode(input, &in); err != nil {
			return "", nil, err
		}
		kind := firstNonEmpty(in.Kind, "idea")
		capture := uuid.NewSHA1(wbCaptureNamespace, []byte(idemKey))
		code, body := app.wbInvoke(ctx, subject, app.cpCapture, "POST", cpBase+"/moments", nil, map[string]any{
			"clientCaptureId": capture.String(), "text": in.Text, "kind": kind, "title": in.Title, "why": in.Why, "source": "text", "sourceDetail": "Workbench", "timezone": "America/Los_Angeles",
		})
		if code != 200 && code != 201 {
			return "", nil, wbHandlerError(code, body)
		}
		var full cpMomentFull
		_ = json.Unmarshal(body, &full)
		return "Saved Moment " + full.Moment.ID.String(), map[string]any{"kind": "moment", "id": full.Moment.ID, "url": "/commonplace/#m/" + full.Moment.ID.String(), "title": firstNonEmpty(in.Title, wbCleanText(in.Text, 80))}, nil
	}
	return "", nil, fmt.Errorf("unknown tool %q", name)
}

var wbCaptureNamespace = uuid.MustParse("6f1b2d1e-5a3c-4e0b-9d57-3c2a8f0e7b11")

func (app *application) wbToday(ctx context.Context) string {
	var today string
	_ = app.db.QueryRow(ctx, `SELECT timezone('America/Los_Angeles', now())::date::text`).Scan(&today)
	return today
}

func (app *application) wbJazzToday(ctx context.Context, subject string, user uuid.UUID, date string) (string, any, error) {
	if date == "" {
		date = app.wbToday(ctx)
	}
	if !datePattern.MatchString(date) {
		return "", nil, errors.New("date must be YYYY-MM-DD")
	}
	var session uuid.UUID
	err := app.db.QueryRow(ctx, `SELECT d.session_id FROM practice_day_layouts d JOIN practice_sessions s ON s.id=d.session_id
		WHERE d.user_id=$1 AND d.practice_date=$2 ORDER BY s.started_at DESC LIMIT 1`, user, date).Scan(&session)
	if errors.Is(err, pgx.ErrNoRows) {
		return wbUntrusted("site_data", map[string]any{"date": date, "sections": []any{}, "note": "No practice plan for this date yet."}), nil, nil
	} else if err != nil {
		return "", nil, err
	}
	code, body := app.wbInvoke(ctx, subject, app.listPracticeBlocks, "GET", "/v1/practice-sessions/"+session.String()+"/blocks?date="+date, map[string]string{"id": session.String()}, nil)
	if code != 200 {
		return "", nil, wbHandlerError(code, body)
	}
	var res struct {
		Blocks []practiceBlock `json:"blocks"`
	}
	if err := json.Unmarshal(body, &res); err != nil {
		return "", nil, err
	}
	type section struct {
		ID        uuid.UUID `json:"id"`
		Title     string    `json:"title"`
		Category  string    `json:"category"`
		Target    int       `json:"targetMinutes"`
		Practiced int       `json:"practicedMinutes"`
		Status    string    `json:"status"`
		TuneID    string    `json:"tuneId,omitempty"`
		Notes     string    `json:"notes,omitempty"`
		Takes     []string  `json:"takeIds,omitempty"`
	}
	out := []section{}
	for _, b := range res.Blocks {
		s := section{ID: b.ID, Title: b.Title, Category: b.Category, Target: b.TargetMinutes, Practiced: (max(b.ElapsedMS, b.RecordedMS) + 30000) / 60000, Status: b.Status, TuneID: b.TuneID, Notes: b.Notes}
		for _, r := range b.Recordings {
			s.Takes = append(s.Takes, r.ID.String())
		}
		out = append(out, s)
	}
	return wbUntrusted("site_data", map[string]any{"date": date, "sessionId": session, "sections": out}), nil, nil
}

type wbBlockInput struct {
	Date         string `json:"date"`
	Title        string `json:"title"`
	Minutes      int    `json:"minutes"`
	Type         string `json:"type"`
	Instructions string `json:"instructions"`
	TuneID       string `json:"tuneId"`
	OneDay       bool   `json:"oneDay"`
}

var wbBlockPresets = map[string][2]string{
	"fundamentals": {"fundamentals", "trumpet"}, "technique": {"technique", "trumpet"}, "scales": {"scales", "language"},
	"listening": {"listening", "musician"}, "tune": {"repertoire", "musician"}, "improvisation": {"improvisation", "language"}, "other": {"general", "musician"},
}

func (in *wbBlockInput) validate() error {
	in.Title = strings.TrimSpace(in.Title)
	if !datePattern.MatchString(in.Date) {
		return errors.New("date must be YYYY-MM-DD")
	}
	if in.Title == "" || len(in.Title) > 160 || in.Minutes < 1 || in.Minutes > 360 || len(in.Instructions) > 2000 {
		return errors.New("a title (up to 160 characters) and 1-360 minutes are required")
	}
	if _, ok := wbBlockPresets[in.Type]; !ok {
		in.Type = "other"
	}
	return nil
}

func (app *application) wbAddBlock(ctx context.Context, subject, idemKey string, in wbBlockInput) (string, any, error) {
	code, body := app.wbInvoke(ctx, subject, app.createPracticeSession, "POST", "/v1/practice-sessions", nil, map[string]any{"title": "Practice - " + in.Date})
	if code != 200 && code != 201 {
		return "", nil, wbHandlerError(code, body)
	}
	var session practiceSession
	if err := json.Unmarshal(body, &session); err != nil {
		return "", nil, err
	}
	preset := wbBlockPresets[in.Type]
	key := "custom-wb-" + uuid.NewSHA1(wbCaptureNamespace, []byte("block:"+idemKey)).String()[:18]
	block := map[string]any{"blockKey": key, "position": 0, "title": in.Title, "instructions": strings.TrimSpace(in.Instructions), "category": preset[0], "track": preset[1], "targetMinutes": in.Minutes, "dayOnly": in.OneDay}
	if in.TuneID != "" {
		block["tuneId"] = in.TuneID
	}
	id := session.ID.String()
	code, body = app.wbInvoke(ctx, subject, app.bootstrapPracticeBlocks, "POST", "/v1/practice-sessions/"+id+"/blocks", map[string]string{"id": id}, map[string]any{"mode": "add", "practiceDate": in.Date, "blocks": []any{block}})
	if code != 200 {
		return "", nil, wbHandlerError(code, body)
	}
	return fmt.Sprintf("Added %q (%d min) to %s.", in.Title, in.Minutes, in.Date), map[string]any{"kind": "practice", "date": in.Date, "title": in.Title, "minutes": in.Minutes, "url": "/jazz/"}, nil
}

type wbMarkTuneInput struct {
	TuneID    string `json:"tuneId"`
	Milestone string `json:"milestone"`
	Status    string `json:"status"`
	Chosen    *bool  `json:"chosen"`
	KeyKnown  string `json:"keyKnown"`
	Note      string `json:"note"`
}

var wbMilestones = map[string]bool{"melodyByEar": true, "lyrics": true, "changes": true, "transcription": true, "improvise": true, "gigReady": true}

func (in wbMarkTuneInput) describe() (string, error) {
	if strings.TrimSpace(in.TuneID) == "" || len(in.TuneID) > 120 {
		return "", errors.New("tuneId is required")
	}
	parts := []string{}
	if in.Milestone != "" || in.Status != "" {
		if !wbMilestones[in.Milestone] || !validMilestoneStatus(in.Status) {
			return "", errors.New("milestone and status must be set together, with known values")
		}
		parts = append(parts, fmt.Sprintf("%s → %s", in.Milestone, strings.ReplaceAll(in.Status, "_", " ")))
	}
	if in.Chosen != nil {
		parts = append(parts, map[bool]string{true: "add to the current set", false: "remove from the current set"}[*in.Chosen])
	}
	if k := strings.TrimSpace(in.KeyKnown); k != "" {
		parts = append(parts, "knows it in "+k)
	}
	if n := strings.TrimSpace(in.Note); n != "" {
		parts = append(parts, "note: "+wbCleanText(n, 60))
	}
	if len(parts) == 0 {
		return "", errors.New("nothing to change")
	}
	return strings.Join(parts, "; "), nil
}

func (app *application) wbMarkTune(ctx context.Context, subject string, in wbMarkTuneInput) (string, any, error) {
	changes, err := in.describe()
	if err != nil {
		return "", nil, err
	}
	code, body := app.wbInvoke(ctx, subject, app.getRepertoire, "GET", "/v1/repertoire", nil, nil)
	if code != 200 {
		return "", nil, wbHandlerError(code, body)
	}
	var rep struct {
		Tunes []repertoireTune `json:"tunes"`
	}
	if err := json.Unmarshal(body, &rep); err != nil {
		return "", nil, err
	}
	var current *repertoireTune
	for i := range rep.Tunes {
		if rep.Tunes[i].TuneID == in.TuneID {
			current = &rep.Tunes[i]
		}
	}
	if current == nil {
		return "", nil, errors.New("unknown tune id; read jazz_repertoire first")
	}
	patch := map[string]any{"expectedRevision": current.Revision}
	if in.Milestone != "" {
		patch["milestones"] = map[string]string{in.Milestone: in.Status}
	}
	if in.Chosen != nil {
		patch["chosen"] = *in.Chosen
	}
	if k := strings.TrimSpace(in.KeyKnown); k != "" {
		keys := append([]string{}, current.KeysKnown...)
		found := false
		for _, existing := range keys {
			found = found || strings.EqualFold(existing, k)
		}
		if !found {
			keys = append(keys, k)
			sort.Strings(keys)
		}
		patch["keysKnown"] = keys
	}
	if n := strings.TrimSpace(in.Note); n != "" {
		notes := strings.TrimSpace(current.Notes)
		if notes != "" {
			notes += "\n"
		}
		patch["notes"] = notes + n
	}
	code, body = app.wbInvoke(ctx, subject, app.updateRepertoireTune, "PATCH", "/v1/repertoire/tunes/"+in.TuneID, map[string]string{"tuneId": in.TuneID}, patch)
	if code != 200 {
		return "", nil, wbHandlerError(code, body)
	}
	return fmt.Sprintf("Updated %s: %s.", current.Title, changes), map[string]any{"kind": "tune", "tuneId": in.TuneID, "title": current.Title, "changes": changes, "url": "/jazz/"}, nil
}

type wbNoteInput struct {
	Target string `json:"target"`
	ID     string `json:"id"`
	Note   string `json:"note"`
}

func (in *wbNoteInput) validate() error {
	in.Note = strings.TrimSpace(in.Note)
	if in.Target != "block" && in.Target != "recording" {
		return errors.New("target must be block or recording")
	}
	if _, err := uuid.Parse(in.ID); err != nil {
		return errors.New("id must be a section or take id")
	}
	if in.Note == "" || len(in.Note) > 1500 {
		return errors.New("note must be 1-1500 characters")
	}
	return nil
}

func (app *application) wbAddNote(ctx context.Context, subject string, user uuid.UUID, in wbNoteInput) (string, any, error) {
	table, handler, path, key, limit := "practice_blocks", app.updatePracticeBlock, "/v1/practice-blocks/", "id", 4000
	if in.Target == "recording" {
		table, handler, path, limit = "recordings", app.updateRecording, "/v1/recordings/", maxTakeNoteBytes
	}
	var existing string
	err := app.db.QueryRow(ctx, `SELECT COALESCE(notes,'') FROM `+table+` WHERE id=$1 AND user_id=$2`, in.ID, user).Scan(&existing)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil, errors.New("not found")
	} else if err != nil {
		return "", nil, err
	}
	notes := strings.TrimSpace(existing)
	if notes != "" {
		notes += "\n"
	}
	notes += in.Note
	if len(notes) > limit {
		return "", nil, fmt.Errorf("notes would exceed %d bytes", limit)
	}
	code, body := app.wbInvoke(ctx, subject, handler, "PATCH", path+in.ID, map[string]string{key: in.ID}, map[string]any{"notes": notes})
	if code != 200 {
		return "", nil, wbHandlerError(code, body)
	}
	return "Note added.", map[string]any{"kind": "note", "target": in.Target, "id": in.ID, "note": in.Note, "url": "/jazz/"}, nil
}

type wbCaptureInput struct {
	Text  string `json:"text"`
	Kind  string `json:"kind"`
	Title string `json:"title"`
	Why   string `json:"why"`
}

var wbLabelPattern = regexp.MustCompile(`^[A-Za-z0-9 ._:/-]{1,50}$`)

type wbIssueInput struct {
	Title  string   `json:"title"`
	Body   string   `json:"body"`
	Labels []string `json:"labels"`
}

func (in *wbIssueInput) validate() error {
	in.Title = strings.TrimSpace(in.Title)
	if in.Title == "" || len(in.Title) > 250 || len(in.Body) > 60000 {
		return errors.New("a title (up to 250 characters) and a body are required")
	}
	if len(in.Labels) > 5 {
		return errors.New("at most 5 labels")
	}
	for _, l := range in.Labels {
		if !wbLabelPattern.MatchString(l) {
			return fmt.Errorf("invalid label %q", l)
		}
	}
	return nil
}

type wbCommentInput struct {
	Number int    `json:"number"`
	Body   string `json:"body"`
}
