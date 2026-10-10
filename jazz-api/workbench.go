package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Workbench, phase 1: an owner-only chat inside the private pages. The
// server owns all state; every device subscribes to the thread's event log
// (workbench_events.go); the agent loop runs here, not in a browser
// (workbench_agent.go). It runs as its own Cloud Run service from the same
// image (WORKBENCH_MODE=1), because the API service runs one request per
// instance and throttles CPU between requests, and Workbench needs
// long-lived event streams and a loop that keeps going after the page closes.

const wbBase = "/v1/workbench"

type wbConfig struct {
	OwnerSubject  string
	Model         string
	MonthlyCapUSD float64
	AnthropicKey  string
	GitHubToken   string
	GitHubRepo    string
	GitHubAPI     string
	VAPIDPublic   string
	VAPIDPrivate  string
	VAPIDSubject  string
	MaxIterations int
}

func loadWorkbenchConfig() wbConfig {
	capUSD, err := strconv.ParseFloat(envOr("WORKBENCH_MONTHLY_CAP_USD", "25"), 64)
	if err != nil || capUSD < 0 || math.IsNaN(capUSD) || math.IsInf(capUSD, 0) {
		capUSD = 25
	}
	return wbConfig{
		OwnerSubject:  strings.TrimSpace(os.Getenv("WORKBENCH_OWNER_SUBJECT")),
		Model:         envOr("WORKBENCH_MODEL", "claude-opus-5-5"),
		MonthlyCapUSD: capUSD,
		AnthropicKey:  strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY")),
		GitHubToken:   strings.TrimSpace(os.Getenv("WORKBENCH_GITHUB_TOKEN")),
		GitHubRepo:    envOr("WORKBENCH_GITHUB_REPO", "zbednarke/personal-site"),
		GitHubAPI:     strings.TrimRight(envOr("WORKBENCH_GITHUB_API", "https://api.github.com"), "/"),
		VAPIDPublic:   strings.TrimSpace(os.Getenv("WORKBENCH_VAPID_PUBLIC_KEY")),
		VAPIDPrivate:  strings.TrimSpace(os.Getenv("WORKBENCH_VAPID_PRIVATE_KEY")),
		VAPIDSubject:  envOr("WORKBENCH_VAPID_SUBJECT", "mailto:workbench@zachbednarke.com"),
		MaxIterations: 12,
	}
}

type workbench struct {
	cfg      wbConfig
	hub      *wbHub
	llm      *anthropic.Client // nil when no key is configured
	github   *wbGitHub
	push     wbPusher
	instance string
	// pollEvery bounds how long a stream waits without a notification before
	// it re-reads the log (a safety net under LISTEN/NOTIFY).
	pollEvery time.Duration
	heartbeat time.Duration
	runs      sync.WaitGroup
	baseCtx   context.Context
	// mu guards draining (set on SIGTERM) against new runs being added.
	mu       sync.Mutex
	draining bool
}

func newWorkbench(cfg wbConfig, opts ...option.RequestOption) *workbench {
	wb := &workbench{cfg: cfg, hub: newWBHub(), instance: uuid.NewString(), pollEvery: 10 * time.Second, heartbeat: 20 * time.Second, baseCtx: context.Background()}
	if cfg.AnthropicKey != "" {
		client := anthropic.NewClient(append([]option.RequestOption{option.WithAPIKey(cfg.AnthropicKey)}, opts...)...)
		wb.llm = &client
	}
	wb.github = &wbGitHub{token: cfg.GitHubToken, repo: cfg.GitHubRepo, api: cfg.GitHubAPI, http: &http.Client{Timeout: 15 * time.Second}}
	if cfg.VAPIDPublic != "" && cfg.VAPIDPrivate != "" {
		wb.push = &webPusher{public: cfg.VAPIDPublic, private: cfg.VAPIDPrivate, subject: cfg.VAPIDSubject}
	}
	return wb
}

// ---- Wire types -----------------------------------------------------------------

type wbPageContext struct {
	Page      string `json:"page,omitempty"`
	Title     string `json:"title,omitempty"`
	View      string `json:"view,omitempty"`
	Selection string `json:"selection,omitempty"`
	App       string `json:"app,omitempty"`
	Device    string `json:"device,omitempty"`
	Viewport  string `json:"viewport,omitempty"`
	Pointer   string `json:"pointer,omitempty"`
	LocalDate string `json:"localDate,omitempty"`
	Timezone  string `json:"timezone,omitempty"`
}

type wbMessage struct {
	ID          uuid.UUID       `json:"id"`
	Role        string          `json:"role"`
	Text        string          `json:"text"`
	Context     json.RawMessage `json:"context,omitempty"`
	Attachments []wbAttachment  `json:"attachments"`
	ClientID    string          `json:"clientId,omitempty"`
	Effort      string          `json:"effort,omitempty"`
	DeviceID    string          `json:"deviceId,omitempty"`
	Status      string          `json:"status"`
	CreatedAt   time.Time       `json:"createdAt"`
}

type wbAttachment struct {
	ID          uuid.UUID `json:"id"`
	Kind        string    `json:"kind"`
	ContentType string    `json:"contentType"`
	SizeBytes   int64     `json:"sizeBytes"`
	DurationMS  *int      `json:"durationMs,omitempty"`
	Transcript  string    `json:"transcript,omitempty"`
}

type wbThread struct {
	ID               uuid.UUID `json:"id"`
	Title            string    `json:"title"`
	LastEventID      int64     `json:"lastEventId"`
	Running          bool      `json:"running"`
	SpendUSD         float64   `json:"spendUsd"`
	PendingApprovals int       `json:"pendingApprovals"`
	// Stuck is set when the API refuses the thread's history for good.
	Stuck     string    `json:"stuck,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type wbApproval struct {
	ID        uuid.UUID       `json:"id"`
	Kind      string          `json:"kind"`
	ToolName  string          `json:"toolName"`
	Input     json.RawMessage `json:"input"`
	Title     string          `json:"title"`
	Detail    json.RawMessage `json:"detail"`
	Status    string          `json:"status"`
	DecidedBy string          `json:"decidedBy,omitempty"`
	DecidedAt *time.Time      `json:"decidedAt,omitempty"`
	Result    json.RawMessage `json:"result"`
	CreatedAt time.Time       `json:"createdAt"`
}

type wbDraft struct {
	Text      string    `json:"text"`
	Rev       int64     `json:"rev"`
	DeviceID  string    `json:"deviceId"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// ---- Routes and privacy -----------------------------------------------------------

func (app *application) workbenchRoutes(mux *http.ServeMux) {
	routes := map[string]http.HandlerFunc{
		"GET " + wbBase + "/threads":                        app.wbListThreads,
		"POST " + wbBase + "/threads":                       app.wbCreateThread,
		"GET " + wbBase + "/threads/{id}":                   app.wbGetThread,
		"PATCH " + wbBase + "/threads/{id}":                 app.wbPatchThread,
		"GET " + wbBase + "/threads/{id}/events":            app.wbStream,
		"POST " + wbBase + "/threads/{id}/messages":         app.wbPostMessage,
		"PUT " + wbBase + "/threads/{id}/draft":             app.wbPutDraft,
		"POST " + wbBase + "/threads/{id}/attachments":      app.wbUploadAttachment,
		"POST " + wbBase + "/threads/{id}/handoff":          app.wbHandoff,
		"GET " + wbBase + "/attachments/{id}":               app.wbAttachmentMedia,
		"POST " + wbBase + "/approvals/{id}":                app.wbDecideApproval,
		"GET " + wbBase + "/spend":                          app.wbSpend,
		"GET " + wbBase + "/status":                         app.wbStatus,
		"GET " + wbBase + "/push/key":                       app.wbPushKey,
		"PUT " + wbBase + "/push/subscriptions/{device}":    app.wbPutPushSubscription,
		"DELETE " + wbBase + "/push/subscriptions/{device}": app.wbDeletePushSubscription,
	}
	for path, h := range routes {
		mux.Handle(path, app.workbenchPrivacy(app.authenticate(app.workbenchOwner(h))))
	}
}

var wbUploadPath = regexp.MustCompile(`^/v1/workbench/threads/[0-9a-fA-F-]{36}/attachments$`)

// workbenchPrivacy: private and unindexed; every mutation is same-site with a
// body type that needs a CORS preflight (JSON, or a raw media upload that
// also carries X-Workbench-Upload).
func (app *application) workbenchPrivacy(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "private, no-store")
		w.Header().Set("X-Robots-Tag", "noindex, nofollow, noarchive")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if r.Method != "GET" && r.Method != "HEAD" {
			ct := strings.ToLower(strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0]))
			allowed := ct == "application/json" || (r.Method == "DELETE" && r.ContentLength <= 0) ||
				(r.Method == "POST" && wbUploadPath.MatchString(r.URL.Path) && r.Header.Get("X-Workbench-Upload") == "1" && (strings.HasPrefix(ct, "audio/") || strings.HasPrefix(ct, "image/") || ct == "video/webm"))
			if !allowed || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
				writeError(w, 403, "same-site request with an allowed body type required")
				return
			}
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			u, err := url.Parse(origin)
			if err != nil || (u.Host != r.Host && !(u.Scheme == "https" && u.Host == "zachbednarke.com")) {
				writeError(w, 403, "origin rejected")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// workbenchOwner re-checks the gateway identity on every call: when
// WORKBENCH_OWNER_SUBJECT is set, only that signed-in user gets through.
func (app *application) workbenchOwner(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if app.wb == nil {
			writeError(w, 503, "workbench is not enabled")
			return
		}
		subject, _ := r.Context().Value(userSubjectKey).(string)
		if owner := app.wb.cfg.OwnerSubject; owner != "" && subject != owner {
			writeError(w, 403, "workbench is owner-only")
			return
		}
		next(w, r)
	})
}

// ---- Validation -------------------------------------------------------------------

var wbClientIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{8,64}$`)

func wbCleanText(s string, limit int) string {
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "")
	}
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) > limit {
		r := []rune(s)
		s = string(r[:limit])
	}
	return s
}

func wbCleanContext(in *wbPageContext) json.RawMessage {
	if in == nil {
		return json.RawMessage(`{}`)
	}
	c := wbPageContext{
		Page: wbCleanText(in.Page, 200), Title: wbCleanText(in.Title, 200), View: wbCleanText(in.View, 200),
		Selection: wbCleanText(in.Selection, 1500), App: wbCleanText(in.App, 1500), Device: wbCleanText(in.Device, 20),
		Viewport: wbCleanText(in.Viewport, 20), Pointer: wbCleanText(in.Pointer, 20), LocalDate: wbCleanText(in.LocalDate, 10), Timezone: wbCleanText(in.Timezone, 64),
	}
	if c.LocalDate != "" && !datePattern.MatchString(c.LocalDate) {
		c.LocalDate = ""
	}
	b, _ := json.Marshal(c)
	return b
}

func wbEffort(e string) (string, error) {
	switch e {
	case "", "low":
		return "low", nil
	case "medium", "high", "xhigh":
		return e, nil
	}
	return "", errors.New("effort must be low, medium, high or xhigh")
}

func (app *application) wbThreadFor(w http.ResponseWriter, r *http.Request) (uuid.UUID, uuid.UUID, bool) {
	id, ok := cpPathID(w, r)
	if !ok {
		return uuid.Nil, uuid.Nil, false
	}
	user, ok := app.cpUser(w, r)
	if !ok {
		return uuid.Nil, uuid.Nil, false
	}
	var found bool
	if err := app.db.QueryRow(r.Context(), `SELECT true FROM wb_threads WHERE id=$1 AND user_id=$2`, id, user).Scan(&found); errors.Is(err, pgx.ErrNoRows) {
		writeError(w, 404, "thread not found")
		return uuid.Nil, uuid.Nil, false
	} else if err != nil {
		app.serverError(w, err)
		return uuid.Nil, uuid.Nil, false
	}
	return id, user, true
}

// ---- Threads ----------------------------------------------------------------------

const wbThreadColumns = `t.id,t.title,t.last_event_id,t.run_state='running',t.spend_micro_usd,
	(SELECT count(*)::int FROM wb_approvals a WHERE a.thread_id=t.id AND a.status='pending'),t.stuck_reason,t.created_at,t.updated_at`

func wbScanThread(row pgx.Row) (wbThread, error) {
	var t wbThread
	var micro int64
	err := row.Scan(&t.ID, &t.Title, &t.LastEventID, &t.Running, &micro, &t.PendingApprovals, &t.Stuck, &t.CreatedAt, &t.UpdatedAt)
	t.SpendUSD = float64(micro) / 1e6
	return t, err
}

func (app *application) wbListThreads(w http.ResponseWriter, r *http.Request) {
	user, ok := app.cpUser(w, r)
	if !ok {
		return
	}
	threads, err := wbCollect(wbRows(app.db.Query(r.Context(), `SELECT `+wbThreadColumns+` FROM wb_threads t WHERE t.user_id=$1 AND t.archived_at IS NULL ORDER BY t.updated_at DESC LIMIT 50`, user)), wbScanThread)
	if err != nil {
		app.serverError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"threads": threads})
}

func (app *application) wbCreateThread(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Title string `json:"title"`
	}
	if err := readJSON(w, r, &in); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	user, ok := app.cpUser(w, r)
	if !ok {
		return
	}
	id := uuid.New()
	t, err := wbScanThread(app.db.QueryRow(r.Context(), `WITH t AS (INSERT INTO wb_threads (id,user_id,title) VALUES ($1,$2,$3) RETURNING *) SELECT `+wbThreadColumns+` FROM t`, id, user, wbCleanText(in.Title, 160)))
	if err != nil {
		app.serverError(w, err)
		return
	}
	writeJSON(w, 201, t)
}

func (app *application) wbPatchThread(w http.ResponseWriter, r *http.Request) {
	id, _, ok := app.wbThreadFor(w, r)
	if !ok {
		return
	}
	var in struct {
		Title    *string `json:"title"`
		Archived *bool   `json:"archived"`
	}
	if err := readJSON(w, r, &in); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	if in.Title != nil {
		title := wbCleanText(*in.Title, 160)
		if _, err := app.db.Exec(r.Context(), `UPDATE wb_threads SET title=$2 WHERE id=$1`, id, title); err != nil {
			app.serverError(w, err)
			return
		}
		if _, err := app.wbEmit(r.Context(), id, "thread.updated", map[string]any{"title": title}); err != nil {
			app.serverError(w, err)
			return
		}
	}
	if in.Archived != nil {
		if _, err := app.db.Exec(r.Context(), `UPDATE wb_threads SET archived_at=CASE WHEN $2 THEN now() END WHERE id=$1`, id, *in.Archived); err != nil {
			app.serverError(w, err)
			return
		}
	}
	t, err := wbScanThread(app.db.QueryRow(r.Context(), `SELECT `+wbThreadColumns+` FROM wb_threads t WHERE t.id=$1`, id))
	if err != nil {
		app.serverError(w, err)
		return
	}
	writeJSON(w, 200, t)
}

const wbMessageColumns = `m.id,m.role,m.text,m.context,m.attachment_ids,coalesce(m.client_id,''),m.effort,m.device_id,m.status,m.created_at`

func wbScanMessage(row pgx.Row) (wbMessage, error) {
	var m wbMessage
	var ids []uuid.UUID
	var ctx []byte
	err := row.Scan(&m.ID, &m.Role, &m.Text, &ctx, &ids, &m.ClientID, &m.Effort, &m.DeviceID, &m.Status, &m.CreatedAt)
	m.Context = ctx
	m.Attachments = make([]wbAttachment, 0, len(ids))
	for _, id := range ids {
		m.Attachments = append(m.Attachments, wbAttachment{ID: id})
	}
	return m, err
}

func (app *application) wbFillAttachments(ctx context.Context, q cpDB, msgs []wbMessage) error {
	want := []uuid.UUID{}
	for _, m := range msgs {
		for _, a := range m.Attachments {
			want = append(want, a.ID)
		}
	}
	if len(want) == 0 {
		return nil
	}
	rows, err := wbCollect(wbRows(q.Query(ctx, `SELECT id,kind,content_type,size_bytes,duration_ms,transcript FROM wb_attachments WHERE id=ANY($1)`, want)), func(row pgx.Row) (wbAttachment, error) {
		var a wbAttachment
		err := row.Scan(&a.ID, &a.Kind, &a.ContentType, &a.SizeBytes, &a.DurationMS, &a.Transcript)
		return a, err
	})
	if err != nil {
		return err
	}
	byID := map[uuid.UUID]wbAttachment{}
	for _, a := range rows {
		byID[a.ID] = a
	}
	for i := range msgs {
		for j, a := range msgs[i].Attachments {
			if full, ok := byID[a.ID]; ok {
				msgs[i].Attachments[j] = full
			}
		}
	}
	return nil
}

const wbApprovalColumns = `a.id,a.kind,a.tool_name,a.tool_input,a.title,a.detail,a.status,a.decided_by,a.decided_at,a.result,a.created_at`

func wbScanApproval(row pgx.Row) (wbApproval, error) {
	var a wbApproval
	var input, detail, result []byte
	err := row.Scan(&a.ID, &a.Kind, &a.ToolName, &input, &a.Title, &detail, &a.Status, &a.DecidedBy, &a.DecidedAt, &result, &a.CreatedAt)
	a.Input, a.Detail, a.Result = input, detail, result
	return a, err
}

// wbGetThread is the snapshot a device loads before streaming from
// lastEventId: recent messages, approvals, the draft and spend.
func (app *application) wbGetThread(w http.ResponseWriter, r *http.Request) {
	id, user, ok := app.wbThreadFor(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	tx, err := app.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		app.serverError(w, err)
		return
	}
	defer tx.Rollback(ctx)
	thread, err := wbScanThread(tx.QueryRow(ctx, `SELECT `+wbThreadColumns+` FROM wb_threads t WHERE t.id=$1`, id))
	if err != nil {
		app.serverError(w, err)
		return
	}
	msgs, err := wbCollect(wbRows(tx.Query(ctx, `SELECT * FROM (SELECT `+wbMessageColumns+` FROM wb_messages m WHERE m.thread_id=$1 AND m.role<>'note' ORDER BY m.created_at DESC, m.id DESC LIMIT 200) x ORDER BY created_at, id`, id)), wbScanMessage)
	if err != nil {
		app.serverError(w, err)
		return
	}
	if err := app.wbFillAttachments(ctx, tx, msgs); err != nil {
		app.serverError(w, err)
		return
	}
	// A reply still streaming is stored only as deltas so far: rebuild its
	// text up to the snapshot's lastEventId, so the stream continues it exactly.
	for i := range msgs {
		if msgs[i].Status != "streaming" {
			continue
		}
		rows, err := tx.Query(ctx, `SELECT type, coalesce(payload->>'text','') FROM wb_events WHERE thread_id=$1 AND id<=$2
			AND type IN ('message.delta','message.reset') AND payload->>'id'=$3 ORDER BY id`, id, thread.LastEventID, msgs[i].ID.String())
		if err != nil {
			app.serverError(w, err)
			return
		}
		var sb strings.Builder
		for rows.Next() {
			var typ, text string
			if err := rows.Scan(&typ, &text); err != nil {
				rows.Close()
				app.serverError(w, err)
				return
			}
			if typ == "message.reset" {
				sb.Reset()
			} else {
				sb.WriteString(text)
			}
		}
		rows.Close()
		msgs[i].Text = sb.String()
	}
	approvals, err := wbCollect(wbRows(tx.Query(ctx, `SELECT `+wbApprovalColumns+` FROM wb_approvals a WHERE a.thread_id=$1 ORDER BY a.created_at DESC LIMIT 50`, id)), wbScanApproval)
	if err != nil {
		app.serverError(w, err)
		return
	}
	draft := wbDraft{}
	if err := tx.QueryRow(ctx, `SELECT text,rev,device_id,updated_at FROM wb_drafts WHERE thread_id=$1`, id).Scan(&draft.Text, &draft.Rev, &draft.DeviceID, &draft.UpdatedAt); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		app.serverError(w, err)
		return
	}
	spend, err := app.wbMonthSpend(ctx, tx, user)
	if err != nil {
		app.serverError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"thread": thread, "messages": msgs, "approvals": approvals, "draft": draft, "spend": spend})
}

// ---- Messages ---------------------------------------------------------------------

type wbPostMessageInput struct {
	ClientID      string         `json:"clientId"`
	Text          string         `json:"text"`
	Context       *wbPageContext `json:"context"`
	AttachmentIDs []uuid.UUID    `json:"attachmentIds"`
	Effort        string         `json:"effort"`
	DeviceID      string         `json:"deviceId"`
}

func (app *application) wbPostMessage(w http.ResponseWriter, r *http.Request) {
	id, user, ok := app.wbThreadFor(w, r)
	if !ok {
		return
	}
	var in wbPostMessageInput
	if err := readJSON(w, r, &in); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	msg, created, status, err := app.wbAddUserMessage(r.Context(), id, user, in)
	if err != nil {
		if status >= 500 {
			app.serverError(w, err)
		} else {
			writeError(w, status, err.Error())
		}
		return
	}
	if created {
		app.wbKick(id)
	}
	code := 201
	if !created {
		code = 200
	}
	writeJSON(w, code, map[string]any{"message": msg, "created": created})
}

// wbAddUserMessage is idempotent on clientId, so an offline outbox can replay
// a message any number of times and it lands (and runs) once.
func (app *application) wbAddUserMessage(ctx context.Context, thread, user uuid.UUID, in wbPostMessageInput) (wbMessage, bool, int, error) {
	if !wbClientIDPattern.MatchString(in.ClientID) {
		return wbMessage{}, false, 400, errors.New("clientId must be 8-64 letters, digits, - or _")
	}
	text := wbCleanText(in.Text, 20000)
	if text == "" && len(in.AttachmentIDs) == 0 {
		return wbMessage{}, false, 422, errors.New("a message needs text or an attachment")
	}
	if len(in.AttachmentIDs) > 8 {
		return wbMessage{}, false, 422, errors.New("at most 8 attachments per message")
	}
	effort, err := wbEffort(in.Effort)
	if err != nil {
		return wbMessage{}, false, 422, err
	}
	device := wbCleanText(in.DeviceID, 64)
	existing, err := wbScanMessage(app.db.QueryRow(ctx, `SELECT `+wbMessageColumns+` FROM wb_messages m WHERE m.thread_id=$1 AND m.client_id=$2`, thread, in.ClientID))
	if err == nil {
		list := []wbMessage{existing}
		err = app.wbFillAttachments(ctx, app.db, list)
		return list[0], false, 200, err
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return wbMessage{}, false, 500, err
	}
	if len(in.AttachmentIDs) > 0 {
		var n int
		if err := app.db.QueryRow(ctx, `SELECT count(*) FROM wb_attachments WHERE thread_id=$1 AND user_id=$2 AND id=ANY($3)`, thread, user, in.AttachmentIDs).Scan(&n); err != nil {
			return wbMessage{}, false, 500, err
		}
		if n != len(in.AttachmentIDs) {
			return wbMessage{}, false, 422, errors.New("unknown attachment")
		}
	}
	tx, err := app.db.Begin(ctx)
	if err != nil {
		return wbMessage{}, false, 500, err
	}
	defer tx.Rollback(ctx)
	ids := in.AttachmentIDs
	if ids == nil {
		ids = []uuid.UUID{}
	}
	msg, err := wbScanMessage(tx.QueryRow(ctx, `INSERT INTO wb_messages AS m (id,thread_id,role,text,context,attachment_ids,client_id,effort,device_id)
		VALUES ($1,$2,'user',$3,$4,$5,$6,$7,$8) ON CONFLICT (thread_id,client_id) WHERE client_id IS NOT NULL DO NOTHING RETURNING `+wbMessageColumns,
		uuid.New(), thread, text, wbCleanContext(in.Context), ids, in.ClientID, effort, device))
	if errors.Is(err, pgx.ErrNoRows) {
		// Lost a race with the same replayed message.
		tx.Rollback(ctx)
		return app.wbAddUserMessage(ctx, thread, user, in)
	} else if err != nil {
		return wbMessage{}, false, 500, err
	}
	list := []wbMessage{msg}
	if err := app.wbFillAttachments(ctx, tx, list); err != nil {
		return wbMessage{}, false, 500, err
	}
	msg = list[0]
	if _, err := app.wbAppendTx(ctx, tx, thread, "message.created", map[string]any{"message": msg}); err != nil {
		return wbMessage{}, false, 500, err
	}
	// Sending clears the synced draft on every device.
	var rev int64
	if err := tx.QueryRow(ctx, `INSERT INTO wb_drafts (thread_id,text,rev,device_id) VALUES ($1,'',1,$2)
		ON CONFLICT (thread_id) DO UPDATE SET text='',rev=wb_drafts.rev+1,device_id=$2,updated_at=now() RETURNING rev`, thread, device).Scan(&rev); err != nil {
		return wbMessage{}, false, 500, err
	}
	if _, err := app.wbAppendTx(ctx, tx, thread, "draft.updated", map[string]any{"text": "", "rev": rev, "deviceId": device}); err != nil {
		return wbMessage{}, false, 500, err
	}
	if title := wbTitleFrom(text); title != "" {
		if tag, err := tx.Exec(ctx, `UPDATE wb_threads SET title=$2 WHERE id=$1 AND title=''`, thread, title); err != nil {
			return wbMessage{}, false, 500, err
		} else if tag.RowsAffected() == 1 {
			if _, err := app.wbAppendTx(ctx, tx, thread, "thread.updated", map[string]any{"title": title}); err != nil {
				return wbMessage{}, false, 500, err
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return wbMessage{}, false, 500, err
	}
	app.wb.hub.notify(thread)
	return msg, true, 201, nil
}

func wbTitleFrom(text string) string {
	line := strings.TrimSpace(strings.SplitN(text, "\n", 2)[0])
	if line == "" {
		return ""
	}
	return wbCleanText(line, 60)
}

// ---- Drafts -----------------------------------------------------------------------

func (app *application) wbPutDraft(w http.ResponseWriter, r *http.Request) {
	id, _, ok := app.wbThreadFor(w, r)
	if !ok {
		return
	}
	var in struct {
		Text     string `json:"text"`
		DeviceID string `json:"deviceId"`
	}
	if err := readJSON(w, r, &in); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	if utf8.RuneCountInString(in.Text) > 20000 {
		writeError(w, 422, "draft is too long")
		return
	}
	device := wbCleanText(in.DeviceID, 64)
	ctx := r.Context()
	tx, err := app.db.Begin(ctx)
	if err != nil {
		app.serverError(w, err)
		return
	}
	defer tx.Rollback(ctx)
	// Lock the thread row before the draft row: wbAddUserMessage locks the
	// thread (appending an event) and then the draft, so the same order here
	// rules out a deadlock between a draft save and a send.
	if _, err := tx.Exec(ctx, `SELECT 1 FROM wb_threads WHERE id=$1 FOR UPDATE`, id); err != nil {
		app.serverError(w, err)
		return
	}
	d := wbDraft{Text: in.Text, DeviceID: device}
	if err := tx.QueryRow(ctx, `INSERT INTO wb_drafts (thread_id,text,rev,device_id) VALUES ($1,$2,1,$3)
		ON CONFLICT (thread_id) DO UPDATE SET text=$2,rev=wb_drafts.rev+1,device_id=$3,updated_at=now() RETURNING rev,updated_at`, id, in.Text, device).Scan(&d.Rev, &d.UpdatedAt); err != nil {
		app.serverError(w, err)
		return
	}
	if _, err := app.wbAppendTx(ctx, tx, id, "draft.updated", map[string]any{"text": d.Text, "rev": d.Rev, "deviceId": device}); err != nil {
		app.serverError(w, err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		app.serverError(w, err)
		return
	}
	app.wb.hub.notify(id)
	writeJSON(w, 200, d)
}

// ---- Attachments ------------------------------------------------------------------

const wbAttachmentLimit = 25 << 20

func (app *application) wbUploadAttachment(w http.ResponseWriter, r *http.Request) {
	id, user, ok := app.wbThreadFor(w, r)
	if !ok {
		return
	}
	clientID := r.Header.Get("X-Workbench-Client-Id")
	if clientID != "" && !wbClientIDPattern.MatchString(clientID) {
		writeError(w, 400, "invalid client id")
		return
	}
	ctx := r.Context()
	if clientID != "" {
		if a, err := app.wbAttachmentByClient(ctx, id, clientID); err == nil {
			writeJSON(w, 200, a)
			return
		} else if !errors.Is(err, pgx.ErrNoRows) {
			app.serverError(w, err)
			return
		}
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, wbAttachmentLimit))
	if err != nil {
		writeError(w, 413, "attachments must be 25 MB or smaller")
		return
	}
	media, err := cpValidateMedia(body, r.Header.Get("Content-Type"), nil, nil)
	if err != nil {
		writeError(w, 422, err.Error())
		return
	}
	if media.Kind == "image" {
		// Images are replayed inline to the model: keep them within its limits.
		scaled, ct, err := wbPrepareImage(body)
		if err != nil {
			writeError(w, 422, err.Error())
			return
		}
		body = scaled
		sum := sha256.Sum256(body)
		media.Body, media.ContentType, media.SHA256 = body, ct, hex.EncodeToString(sum[:])
	}
	var duration *int
	if raw := r.Header.Get("X-Workbench-Duration-Ms"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n >= 0 && n < 4*60*60*1000 {
			duration = &n
		}
	}
	attID := uuid.New()
	name := "workbench/" + user.String() + "/" + id.String() + "/" + attID.String() + "-" + media.SHA256[:16] + "." + cpMediaExtension(media.ContentType)
	if err := app.inspirationObjects().Put(ctx, name, media.ContentType, map[string]string{"userId": user.String(), "threadId": id.String(), "attachmentId": attID.String(), "sha256": media.SHA256}, body); err != nil {
		app.serverError(w, err)
		return
	}
	var client any
	if clientID != "" {
		client = clientID
	}
	a := wbAttachment{ID: attID, Kind: media.Kind, ContentType: media.ContentType, SizeBytes: int64(len(body)), DurationMS: duration}
	tag, err := app.db.Exec(ctx, `INSERT INTO wb_attachments (id,thread_id,user_id,kind,content_type,size_bytes,duration_ms,object_name,client_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT (thread_id,client_id) WHERE client_id IS NOT NULL DO NOTHING`,
		attID, id, user, a.Kind, a.ContentType, a.SizeBytes, duration, name, client)
	if err != nil {
		app.serverError(w, err)
		return
	}
	if tag.RowsAffected() == 0 {
		_ = app.inspirationObjects().Delete(ctx, name)
		existing, err := app.wbAttachmentByClient(ctx, id, clientID)
		if err != nil {
			app.serverError(w, err)
			return
		}
		writeJSON(w, 200, existing)
		return
	}
	writeJSON(w, 201, a)
}

func (app *application) wbAttachmentByClient(ctx context.Context, thread uuid.UUID, clientID string) (wbAttachment, error) {
	var a wbAttachment
	err := app.db.QueryRow(ctx, `SELECT id,kind,content_type,size_bytes,duration_ms,transcript FROM wb_attachments WHERE thread_id=$1 AND client_id=$2`, thread, clientID).
		Scan(&a.ID, &a.Kind, &a.ContentType, &a.SizeBytes, &a.DurationMS, &a.Transcript)
	return a, err
}

func (app *application) wbAttachmentMedia(w http.ResponseWriter, r *http.Request) {
	id, ok := cpPathID(w, r)
	if !ok {
		return
	}
	user, ok := app.cpUser(w, r)
	if !ok {
		return
	}
	var name string
	if err := app.db.QueryRow(r.Context(), `SELECT object_name FROM wb_attachments WHERE id=$1 AND user_id=$2`, id, user).Scan(&name); errors.Is(err, pgx.ErrNoRows) {
		writeError(w, 404, "attachment not found")
		return
	} else if err != nil {
		app.serverError(w, err)
		return
	}
	signed, err := app.inspirationObjects().SignedGet(r.Context(), name, time.Now().Add(10*time.Minute))
	if err != nil {
		app.serverError(w, err)
		return
	}
	http.Redirect(w, r, signed, http.StatusFound)
}

// ---- Approvals --------------------------------------------------------------------

func (app *application) wbDecideApproval(w http.ResponseWriter, r *http.Request) {
	id, ok := cpPathID(w, r)
	if !ok {
		return
	}
	user, ok := app.cpUser(w, r)
	if !ok {
		return
	}
	var in struct {
		Decision string `json:"decision"`
		DeviceID string `json:"deviceId"`
	}
	if err := readJSON(w, r, &in); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	if in.Decision != "approve" && in.Decision != "reject" {
		writeError(w, 422, "decision must be approve or reject")
		return
	}
	a, won, err := app.wbDecide(r.Context(), user, id, in.Decision == "approve", wbCleanText(in.DeviceID, 64))
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, 404, "approval not found")
		return
	} else if err != nil {
		app.serverError(w, err)
		return
	}
	if !won {
		writeJSON(w, 409, map[string]any{"error": "already decided", "approval": a})
		return
	}
	writeJSON(w, 200, map[string]any{"approval": a})
}

// ---- Spend ------------------------------------------------------------------------

type wbSpendSummary struct {
	Month    string  `json:"month"`
	SpentUSD float64 `json:"spentUsd"`
	CapUSD   float64 `json:"capUsd"`
	Blocked  bool    `json:"blocked"`
}

func wbMonthStart(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}

func (app *application) wbMonthSpend(ctx context.Context, q cpDB, user uuid.UUID) (wbSpendSummary, error) {
	month := wbMonthStart(time.Now())
	var micro int64
	err := q.QueryRow(ctx, `SELECT spend_micro_usd FROM wb_spend_months WHERE user_id=$1 AND month=$2`, user, month).Scan(&micro)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return wbSpendSummary{}, err
	}
	s := wbSpendSummary{Month: month.Format("2006-01"), SpentUSD: float64(micro) / 1e6, CapUSD: app.wb.cfg.MonthlyCapUSD}
	s.Blocked = s.SpentUSD >= s.CapUSD
	return s, nil
}

func (app *application) wbSpend(w http.ResponseWriter, r *http.Request) {
	user, ok := app.cpUser(w, r)
	if !ok {
		return
	}
	s, err := app.wbMonthSpend(r.Context(), app.db, user)
	if err != nil {
		app.serverError(w, err)
		return
	}
	writeJSON(w, 200, s)
}

// ---- Site status ------------------------------------------------------------------

func (app *application) wbStatus(w http.ResponseWriter, r *http.Request) {
	card, err := app.wb.github.siteStatus(r.Context())
	if err != nil {
		writeJSON(w, 200, map[string]any{"kind": "status", "error": err.Error()})
		return
	}
	writeJSON(w, 200, card)
}

func wbFormatUSD(v float64) string { return fmt.Sprintf("$%.2f", v) }
