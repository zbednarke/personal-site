package main

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// API self-observation for the daily fix list: per route pattern, how many
// requests each service answered with a 5xx and how many took 10 s or more.
// Counting is a map update under a mutex; the counts go to Postgres about
// once a minute in one statement (api_route_counters, per UTC hour), from a
// goroutine, so a request never waits for it. Workbench event streams are
// long-lived by design and are left out.

const routeSlowAfter = 10 * time.Second

type routeCounterKey struct {
	hour  int64 // Unix seconds at the start of the UTC hour
	route string
}

type routeCounts struct{ requests, errors, slow int64 }

type routeCounters struct {
	service string
	every   time.Duration
	mu      sync.Mutex
	counts  map[routeCounterKey]*routeCounts
	last    time.Time
	busy    atomic.Bool
	pruned  string
}

func newRouteCounters(service string) *routeCounters {
	return &routeCounters{service: service, every: time.Minute, counts: map[routeCounterKey]*routeCounts{}, last: time.Now()}
}

// routeService names the Cloud Run service (K_SERVICE), so the API and
// Workbench, which share this code, are counted apart.
func routeService(workbench bool) string {
	if s := strings.TrimSpace(os.Getenv("K_SERVICE")); s != "" && len(s) <= 64 {
		return s
	}
	if workbench {
		return "workbench"
	}
	return "jazz-api"
}

// record counts one request and says whether a flush is due.
func (c *routeCounters) record(route string, status int, took time.Duration, now time.Time) bool {
	if route == "" {
		route = "(unmatched)"
	}
	if len(route) > 200 {
		route = route[:200]
	}
	key := routeCounterKey{hour: now.UTC().Truncate(time.Hour).Unix(), route: route}
	c.mu.Lock()
	defer c.mu.Unlock()
	n := c.counts[key]
	if n == nil {
		n = &routeCounts{}
		c.counts[key] = n
	}
	n.requests++
	if status >= 500 {
		n.errors++
	}
	if took >= routeSlowAfter {
		n.slow++
	}
	return now.Sub(c.last) >= c.every
}

// take empties the in-memory counts.
func (c *routeCounters) take(now time.Time) map[routeCounterKey]*routeCounts {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := c.counts
	c.counts = map[routeCounterKey]*routeCounts{}
	c.last = now
	return out
}

// putBack returns counts a failed flush could not store.
func (c *routeCounters) putBack(counts map[routeCounterKey]*routeCounts) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, v := range counts {
		n := c.counts[k]
		if n == nil {
			c.counts[k] = v
			continue
		}
		n.requests += v.requests
		n.errors += v.errors
		n.slow += v.slow
	}
}

// flushRouteCounters adds the in-memory counts to api_route_counters in one
// statement, and prunes old hours once a day.
func (app *application) flushRouteCounters(ctx context.Context) error {
	c := app.counters
	if c == nil || app.db == nil {
		return nil
	}
	now := time.Now()
	counts := c.take(now)
	if len(counts) == 0 {
		return nil
	}
	hours, services, routes := make([]time.Time, 0, len(counts)), make([]string, 0, len(counts)), make([]string, 0, len(counts))
	requests, errs, slow := make([]int64, 0, len(counts)), make([]int64, 0, len(counts)), make([]int64, 0, len(counts))
	for k, v := range counts {
		hours = append(hours, time.Unix(k.hour, 0).UTC())
		services = append(services, c.service)
		routes = append(routes, k.route)
		requests, errs, slow = append(requests, v.requests), append(errs, v.errors), append(slow, v.slow)
	}
	_, err := app.db.Exec(ctx, `INSERT INTO api_route_counters (hour,service,route,requests,errors_5xx,slow)
		SELECT * FROM unnest($1::timestamptz[],$2::text[],$3::text[],$4::bigint[],$5::bigint[],$6::bigint[])
		ON CONFLICT (hour,service,route) DO UPDATE SET requests=api_route_counters.requests+EXCLUDED.requests,
		errors_5xx=api_route_counters.errors_5xx+EXCLUDED.errors_5xx, slow=api_route_counters.slow+EXCLUDED.slow, updated_at=now()`,
		hours, services, routes, requests, errs, slow)
	if err != nil {
		c.putBack(counts)
		return err
	}
	day := now.UTC().Format("2006-01-02")
	c.mu.Lock()
	prune := c.pruned != day
	c.pruned = day
	c.mu.Unlock()
	if prune {
		_, _ = app.db.Exec(ctx, `DELETE FROM api_route_counters WHERE hour < now() - interval '35 days'`)
	}
	return nil
}

// flushRouteCountersSoon flushes in the background unless one is running.
func (app *application) flushRouteCountersSoon() {
	c := app.counters
	if c == nil || !c.busy.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer c.busy.Store(false)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := app.flushRouteCounters(ctx); err != nil {
			app.logger.Warn("route counters flush failed", "error", err)
		}
	}()
}

// wbIsEventStream: the Workbench SSE stream (open for minutes on purpose).
func wbIsEventStream(r *http.Request) bool {
	return r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, wbBase+"/threads/") && strings.HasSuffix(r.URL.Path, "/events")
}

// statusRecorder remembers the status code; it passes Flush, Hijack and
// ReadFrom through (and Unwrap for http.ResponseController).
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		if s.status == 0 {
			s.status = http.StatusOK
		}
		f.Flush()
	}
}

func (s *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := s.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, errors.New("hijacking is not supported")
}

func (s *statusRecorder) ReadFrom(src io.Reader) (int64, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	if rf, ok := s.ResponseWriter.(io.ReaderFrom); ok {
		return rf.ReadFrom(src)
	}
	return io.Copy(s.ResponseWriter, src)
}

func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

func (s *statusRecorder) code() int {
	if s.status == 0 {
		return http.StatusOK
	}
	return s.status
}
