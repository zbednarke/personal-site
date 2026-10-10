package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// The per-thread event log. Ids come from wb_threads.last_event_id, bumped
// under the thread's row lock in the same transaction as the insert, so ids
// are gapless and commit in order. Streams always read from Postgres (the
// source of truth); in-process notifications and LISTEN/NOTIFY only wake them,
// so a lost wake-up costs latency, never an event.

type wbEvent struct {
	ID        int64           `json:"id"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt time.Time       `json:"createdAt"`
}

const wbNotifyChannel = "wb_events"

func (app *application) wbAppendTx(ctx context.Context, tx pgx.Tx, thread uuid.UUID, typ string, payload any) (int64, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}
	var id int64
	if err := tx.QueryRow(ctx, `UPDATE wb_threads SET last_event_id=last_event_id+1, updated_at=now() WHERE id=$1 RETURNING last_event_id`, thread).Scan(&id); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO wb_events (thread_id,id,type,payload) VALUES ($1,$2,$3,$4)`, thread, id, typ, body); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `SELECT pg_notify($1,$2)`, wbNotifyChannel, thread.String()); err != nil {
		return 0, err
	}
	return id, nil
}

// wbEmit appends one event in its own transaction and wakes local streams.
func (app *application) wbEmit(ctx context.Context, thread uuid.UUID, typ string, payload any) (int64, error) {
	tx, err := app.db.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	id, err := app.wbAppendTx(ctx, tx, thread, typ, payload)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	app.wb.hub.notify(thread)
	return id, nil
}

func (app *application) wbEventsAfter(ctx context.Context, thread uuid.UUID, after int64, limit int) ([]wbEvent, error) {
	return wbCollect(wbRows(app.db.Query(ctx, `SELECT id,type,payload,created_at FROM wb_events WHERE thread_id=$1 AND id>$2 ORDER BY id LIMIT $3`, thread, after, limit)), func(row pgx.Row) (wbEvent, error) {
		var e wbEvent
		var p []byte
		err := row.Scan(&e.ID, &e.Type, &p, &e.CreatedAt)
		e.Payload = p
		return e, err
	})
}

// ---- Hub ----------------------------------------------------------------------------

type wbHub struct {
	mu   sync.Mutex
	subs map[uuid.UUID]map[chan struct{}]struct{}
}

func newWBHub() *wbHub { return &wbHub{subs: map[uuid.UUID]map[chan struct{}]struct{}{}} }

func (h *wbHub) subscribe(thread uuid.UUID) (chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	h.mu.Lock()
	if h.subs[thread] == nil {
		h.subs[thread] = map[chan struct{}]struct{}{}
	}
	h.subs[thread][ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		delete(h.subs[thread], ch)
		if len(h.subs[thread]) == 0 {
			delete(h.subs, thread)
		}
		h.mu.Unlock()
	}
}

func (h *wbHub) notify(thread uuid.UUID) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs[thread] {
		select {
		case ch <- struct{}{}:
		default: // already has a pending wake-up
		}
	}
}

func (h *wbHub) notifyAll() {
	h.mu.Lock()
	threads := make([]uuid.UUID, 0, len(h.subs))
	for t := range h.subs {
		threads = append(threads, t)
	}
	h.mu.Unlock()
	for _, t := range threads {
		h.notify(t)
	}
}

// wbListen relays NOTIFY wb_events from other instances (and this one) to
// local streams. It reconnects with backoff until ctx ends.
func (app *application) wbListen(ctx context.Context) {
	backoff := time.Second
	for ctx.Err() == nil {
		err := app.wbListenOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		app.logger.Warn("workbench listener reconnecting", "error", err)
		// Anything missed while disconnected is picked up by a re-read.
		app.wb.hub.notifyAll()
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

func (app *application) wbListenOnce(ctx context.Context) error {
	pooled, err := app.db.Acquire(ctx)
	if err != nil {
		return err
	}
	// A LISTENing connection must never go back to the pool.
	conn := pooled.Hijack()
	defer conn.Close(context.Background())
	if _, err := conn.Exec(ctx, "LISTEN "+wbNotifyChannel); err != nil {
		return err
	}
	for {
		n, err := conn.WaitForNotification(ctx)
		if err != nil {
			return err
		}
		if id, err := uuid.Parse(n.Payload); err == nil {
			app.wb.hub.notify(id)
		}
	}
}

// ---- Server-sent events ----------------------------------------------------------

func wbResumeFrom(r *http.Request) (int64, error) {
	raw := strings.TrimSpace(r.Header.Get("Last-Event-ID"))
	if raw == "" {
		raw = r.URL.Query().Get("after")
	}
	if raw == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 0 {
		return 0, errors.New("Last-Event-ID must be a non-negative integer")
	}
	return n, nil
}

func (app *application) wbStream(w http.ResponseWriter, r *http.Request) {
	thread, _, ok := app.wbThreadFor(w, r)
	if !ok {
		return
	}
	after, err := wbResumeFrom(r)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, 500, "streaming unsupported")
		return
	}
	wake, cancel := app.wb.hub.subscribe(thread)
	defer cancel()
	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "private, no-store, no-transform")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
	fmt.Fprint(w, "retry: 2000\n: workbench stream\n\n")
	flusher.Flush()
	ctx := r.Context()
	heartbeat := time.NewTicker(app.wb.heartbeat)
	defer heartbeat.Stop()
	poll := time.NewTimer(app.wb.pollEvery)
	defer poll.Stop()
	for {
		events, err := app.wbEventsAfter(ctx, thread, after, 500)
		if err != nil {
			if ctx.Err() == nil {
				app.logger.Error("workbench stream read failed", "error", err)
			}
			return
		}
		for _, e := range events {
			// One frame per event; the payload JSON is a single line.
			if _, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", e.ID, e.Type, e.Payload); err != nil {
				return
			}
			after = e.ID
		}
		if len(events) == 500 {
			continue
		}
		flusher.Flush()
		if !poll.Stop() {
			select {
			case <-poll.C:
			default:
			}
		}
		poll.Reset(app.wb.pollEvery)
	wait:
		select {
		case <-ctx.Done():
			return
		case <-wake:
		case <-poll.C:
		case <-heartbeat.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
			goto wait
		}
	}
}

type wbRowsResult struct {
	rows pgx.Rows
	err  error
}

func wbRows(rows pgx.Rows, err error) wbRowsResult { return wbRowsResult{rows, err} }

func wbCollect[T any](r wbRowsResult, scan func(pgx.Row) (T, error)) ([]T, error) {
	return cpCollect(r.rows, r.err, scan)
}
