package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Web Push (VAPID). Subscriptions are bound to the signed-in owner, one per
// device. Payloads never carry message content: a notification says that
// something is waiting and links to the signed-in page.

type wbPushSub struct {
	DeviceID string
	Endpoint string
	P256dh   string
	Auth     string
	Nudges   bool
}

type wbPusher interface {
	Send(ctx context.Context, sub wbPushSub, payload []byte) (int, error)
}

type webPusher struct{ public, private, subject string }

func (p *webPusher) Send(ctx context.Context, sub wbPushSub, payload []byte) (int, error) {
	resp, err := webpush.SendNotificationWithContext(ctx, payload, &webpush.Subscription{Endpoint: sub.Endpoint, Keys: webpush.Keys{P256dh: sub.P256dh, Auth: sub.Auth}}, &webpush.Options{
		Subscriber: p.subject, VAPIDPublicKey: p.public, VAPIDPrivateKey: p.private, TTL: 3600, Urgency: webpush.UrgencyHigh,
	})
	if err != nil {
		return 0, err
	}
	resp.Body.Close()
	return resp.StatusCode, nil
}

func (app *application) wbPushKey(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"publicKey": app.wb.cfg.VAPIDPublic, "enabled": app.wb.push != nil})
}

// Only real push services may receive pushes (no internal hosts).
func wbPushEndpointAllowed(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	for _, suffix := range []string{".googleapis.com", ".push.apple.com", ".notify.windows.com", ".mozilla.com", ".mozaws.net"} {
		if strings.HasSuffix(host, suffix) {
			return true
		}
	}
	return false
}

func (app *application) wbPutPushSubscription(w http.ResponseWriter, r *http.Request) {
	device := r.PathValue("device")
	if !wbClientIDPattern.MatchString(device) {
		writeError(w, 400, "invalid device id")
		return
	}
	var in struct {
		Label        string `json:"label"`
		Nudges       bool   `json:"nudges"`
		Subscription struct {
			Endpoint       string `json:"endpoint"`
			ExpirationTime any    `json:"expirationTime"`
			Keys           struct {
				P256dh string `json:"p256dh"`
				Auth   string `json:"auth"`
			} `json:"keys"`
		} `json:"subscription"`
	}
	if err := readJSON(w, r, &in); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	s := in.Subscription
	if !wbPushEndpointAllowed(s.Endpoint) || len(s.Endpoint) > 2000 || s.Keys.P256dh == "" || len(s.Keys.P256dh) > 200 || s.Keys.Auth == "" || len(s.Keys.Auth) > 100 {
		writeError(w, 422, "invalid push subscription")
		return
	}
	user, ok := app.cpUser(w, r)
	if !ok {
		return
	}
	if _, err := app.db.Exec(r.Context(), `INSERT INTO wb_push_subscriptions (user_id,device_id,label,endpoint,p256dh,auth,nudges) VALUES ($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT (user_id,device_id) DO UPDATE SET label=$3,endpoint=$4,p256dh=$5,auth=$6,nudges=$7,failures=0,updated_at=now()`,
		user, device, wbCleanText(in.Label, 80), s.Endpoint, s.Keys.P256dh, s.Keys.Auth, in.Nudges); err != nil {
		app.serverError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"deviceId": device, "nudges": in.Nudges})
}

func (app *application) wbDeletePushSubscription(w http.ResponseWriter, r *http.Request) {
	user, ok := app.cpUser(w, r)
	if !ok {
		return
	}
	if _, err := app.db.Exec(r.Context(), `DELETE FROM wb_push_subscriptions WHERE user_id=$1 AND device_id=$2`, user, r.PathValue("device")); err != nil {
		app.serverError(w, err)
		return
	}
	w.WriteHeader(204)
}

type wbPushPayload struct {
	Kind       string `json:"kind"`
	Title      string `json:"title"`
	Body       string `json:"body"`
	URL        string `json:"url"`
	ThreadID   string `json:"threadId,omitempty"`
	ApprovalID string `json:"approvalId,omitempty"`
	Tag        string `json:"tag,omitempty"`
	// Actions offers Approve / Not now on the notification itself. Never for
	// writes to the public repository: those are reviewed on the page.
	Actions bool `json:"actions,omitempty"`
}

// wbApproveFromNotification: may this approval be answered without opening
// the page? Not GitHub writes (public, permanent), and not proposals made
// after reading outside content.
func wbApproveFromNotification(a wbApproval) bool {
	if strings.HasPrefix(a.ToolName, "github_") {
		return false
	}
	var d map[string]any
	_ = json.Unmarshal(a.Detail, &d)
	return d["afterExternal"] == nil
}

// wbPushTo sends to the owner's devices (all, or nudge-opted only), skipping
// one device. Failed endpoints are dropped after repeated failures.
func (app *application) wbPushTo(ctx context.Context, user uuid.UUID, nudgesOnly bool, skipDevice string, p wbPushPayload) int {
	if app.wb.push == nil {
		return 0
	}
	subs, err := wbCollect(wbRows(app.db.Query(ctx, `SELECT device_id,endpoint,p256dh,auth,nudges FROM wb_push_subscriptions WHERE user_id=$1 AND (NOT $2 OR nudges) AND device_id<>$3`, user, nudgesOnly, skipDevice)), func(row pgx.Row) (wbPushSub, error) {
		var s wbPushSub
		return s, row.Scan(&s.DeviceID, &s.Endpoint, &s.P256dh, &s.Auth, &s.Nudges)
	})
	if err != nil {
		app.logger.Error("workbench push lookup failed", "error", err)
		return 0
	}
	payload, _ := json.Marshal(p)
	sent := 0
	for _, s := range subs {
		sendCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		status, err := app.wb.push.Send(sendCtx, s, payload)
		cancel()
		switch {
		case err == nil && (status == 404 || status == 410):
			_, _ = app.db.Exec(ctx, `DELETE FROM wb_push_subscriptions WHERE user_id=$1 AND device_id=$2`, user, s.DeviceID)
		case err != nil || status >= 400:
			if err == nil {
				err = errors.New(http.StatusText(status))
			}
			app.logger.Warn("workbench push failed", "device", s.DeviceID, "error", err)
			_, _ = app.db.Exec(ctx, `UPDATE wb_push_subscriptions SET failures=failures+1 WHERE user_id=$1 AND device_id=$2`, user, s.DeviceID)
			_, _ = app.db.Exec(ctx, `DELETE FROM wb_push_subscriptions WHERE user_id=$1 AND device_id=$2 AND failures>=5`, user, s.DeviceID)
		default:
			sent++
		}
	}
	return sent
}

func wbThreadURL(thread uuid.UUID) string { return "/jazz/?workbench=" + thread.String() }

func (app *application) wbPushApproval(ctx context.Context, user, thread uuid.UUID, a wbApproval) {
	app.wbPushTo(ctx, user, false, "", wbPushPayload{Kind: "approval", Title: "Workbench", Body: "An approval is waiting.", URL: wbThreadURL(thread), ThreadID: thread.String(), ApprovalID: a.ID.String(), Tag: "wb-approval-" + a.ID.String(), Actions: wbApproveFromNotification(a)})
}

func (app *application) wbPushNudge(ctx context.Context, user, thread uuid.UUID, status string) {
	body := "A reply is ready."
	if status == "error" || status == "interrupted" {
		body = "A turn stopped early."
	}
	app.wbPushTo(ctx, user, true, "", wbPushPayload{Kind: "reply", Title: "Workbench", Body: body, URL: wbThreadURL(thread), ThreadID: thread.String(), Tag: "wb-reply-" + thread.String()})
}

// wbHandoff: "Open this on my other devices" pushes a deep link to every
// other signed-in device.
func (app *application) wbHandoff(w http.ResponseWriter, r *http.Request) {
	thread, user, ok := app.wbThreadFor(w, r)
	if !ok {
		return
	}
	var in struct {
		DeviceID string `json:"deviceId"`
		Path     string `json:"path"`
	}
	if err := readJSON(w, r, &in); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	path := "/jazz/"
	for _, prefix := range []string{"/jazz/", "/trumpets/", "/commonplace/"} {
		if strings.HasPrefix(in.Path, prefix) && !strings.Contains(in.Path, "//") && len(in.Path) <= 500 {
			path = in.Path
		}
	}
	u, err := url.Parse(path)
	if err != nil {
		writeError(w, 422, "invalid path")
		return
	}
	q := u.Query()
	q.Set("workbench", thread.String())
	u.RawQuery = q.Encode()
	sent := app.wbPushTo(r.Context(), user, false, wbCleanText(in.DeviceID, 64), wbPushPayload{Kind: "handoff", Title: "Workbench", Body: "Continue on this device.", URL: u.String(), ThreadID: thread.String(), Tag: "wb-handoff"})
	writeJSON(w, 200, map[string]any{"sent": sent})
}
