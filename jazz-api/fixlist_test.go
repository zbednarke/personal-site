package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// All fixture data is fictional.

func TestFixExpectedRunDay(t *testing.T) {
	at := func(s string) time.Time { v, _ := time.Parse(time.RFC3339, s); return v }
	for _, c := range []struct{ now, want string }{
		{"2026-10-12T14:30:00Z", "2026-10-11"},
		{"2026-10-12T16:29:59Z", "2026-10-11"},
		{"2026-10-12T16:30:00Z", "2026-10-12"},
		{"2026-10-12T23:59:00Z", "2026-10-12"},
		{"2026-10-12T03:00:00Z", "2026-10-11"},
	} {
		if got := fixExpectedRunDay(at(c.now)).Format("2006-01-02"); got != c.want {
			t.Fatalf("%s: expected run day %s, want %s", c.now, got, c.want)
		}
	}
}

func TestFixReasonGroupsNumbersButKeepsHTTPCodes(t *testing.T) {
	if got := fixReason("Run time budget exhausted after 35 minutes.\nmore", true); got != "Run time budget exhausted after N minutes" {
		t.Fatal(got)
	}
	if got := fixReason("Reverb API failed: HTTP 429", true); got != "Reverb API failed: HTTP 429" {
		t.Fatal(got)
	}
	if got := fixReason("  ", false); got != "no reason given" {
		t.Fatal(got)
	}
	if got := fixReason(strings.Repeat("x", 500), false); len([]rune(got)) != 120 {
		t.Fatalf("reasons are bounded: %d", len(got))
	}
	if fixDomain("www.Shop.Example") != "shop.example" || fixDomain("not a domain") != "" || fixDomain("localhost") != "" {
		t.Fatal("domains are host names only")
	}
}

func TestFixScoreRewardsSeverityCountAndGrowth(t *testing.T) {
	if fixScore("run:missing", 1, 0) <= fixScore("api:5xx", 1, 0) {
		t.Fatal("a missing daily run outranks one 5xx")
	}
	if fixScore("checks:failed", 20, 5) <= fixScore("checks:failed", 5, 20) {
		t.Fatal("more, and growing, is worth more")
	}
	if fixScore("api:5xx", 0, 9) != 0 {
		t.Fatal("nothing to fix scores zero")
	}
}

// The push is small whatever the digest holds, and carries counts, fixed
// words, domains and routes only: never labels, notes or listing text.
func TestFixPushPayloadIsBoundedAndPrivate(t *testing.T) {
	secret := "SECRET-LISTING-NOTE"
	d := fixDigest{Version: 1, Day: "2026-10-12", Run: fixRun{Found: true, Status: "partial", Notes: []string{secret + " " + strings.Repeat("n", 300)}}}
	for i := 0; i < 400; i++ {
		d.Items = append(d.Items, fixItem{Key: "checks:x", Section: "checks", Label: secret + strings.Repeat("é", 200), Short: "failed checks on " + strings.Repeat("a", 90) + ".example", Count: int64(i + 1), Domains: []string{strings.Repeat("b", 90) + ".example"}, Score: float64(i)})
	}
	d.ToFix, d.FixNext = len(d.Items), &d.Items[399]
	d.Verdict = fixVerdict(d)
	b := fixPushPayload(d, uuid.New())
	if len(b) > fixPushMaxBytes {
		t.Fatalf("push payload is %d bytes", len(b))
	}
	if strings.Contains(string(b), secret) {
		t.Fatal("labels and notes stay out of pushes")
	}
	var p wbPushPayload
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatal(err)
	}
	if p.Title != "Fix list – 2026-10-12" || p.URL != fixListPage || p.Kind != "fixlist" || p.Quiet {
		t.Fatalf("payload: %+v", p)
	}
	if len([]rune(p.Body)) > fixBodyMax || !strings.HasPrefix(p.Body, "400 to fix") || !strings.Contains(p.Body, "Run partial") {
		t.Fatalf("body: %q", p.Body)
	}
	clear := fixDigest{Day: "2026-10-13", AllClear: true, Run: fixRun{Found: true, Status: "succeeded"}}
	clear.Verdict = fixVerdict(clear)
	p = wbPushPayload{}
	_ = json.Unmarshal(fixPushPayload(clear, uuid.Nil), &p)
	if !p.Quiet || p.Body != "All clear: the daily run succeeded and nothing failed." || p.ThreadID != "" {
		t.Fatalf("an all-clear day still arrives, quietly: %+v", p)
	}
}

func TestFixMessageEscapesMarkdownAndLinksThePage(t *testing.T) {
	d := fixDigest{Day: "2026-10-12", Verdict: "1 to fix", ToFix: 1, Items: []fixItem{{Key: "k", Section: "checks", Label: "shop.example: [x](javascript:alert(1)) **bold** `code`", Count: 2, Previous: 1}}}
	d.FixNext = &d.Items[0]
	msg := fixMessage(d)
	if strings.Contains(msg, "](javascript") || strings.Contains(msg, "`code`") {
		t.Fatalf("labels cannot inject Markdown: %s", msg)
	}
	if !strings.Contains(msg, "[Open the full fix list](/jazz/#fixlist)") || !strings.Contains(msg, "(+1)") {
		t.Fatal(msg)
	}
}

func TestRouteCountersCountFiveHundredsSlowAndPanicsNotStreams(t *testing.T) {
	c := newRouteCounters("jazz-api")
	now := time.Date(2026, 10, 12, 9, 30, 0, 0, time.UTC)
	c.record("GET /v1/a", 200, time.Second, now)
	c.record("GET /v1/a", 502, 12*time.Second, now)
	c.record("", 404, 0, now)
	got := c.take(now)
	hour := now.Truncate(time.Hour).Unix()
	if a := got[routeCounterKey{hour, "GET /v1/a"}]; a == nil || a.requests != 2 || a.errors != 1 || a.slow != 1 {
		t.Fatalf("counts: %+v", a)
	}
	if got[routeCounterKey{hour, "(unmatched)"}] == nil {
		t.Fatal("unmatched requests are counted together")
	}
	if len(c.take(now)) != 0 {
		t.Fatal("take empties the counts")
	}

	app := &application{logger: discardLogger(), counters: newRouteCounters("workbench")}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/boom/{id}", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) })
	mux.HandleFunc("GET /v1/panic", func(w http.ResponseWriter, r *http.Request) { panic("fixture") })
	mux.HandleFunc("GET "+wbBase+"/threads/{id}/events", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := w.(http.Flusher); !ok {
			t.Error("streams keep their Flusher")
		}
		w.WriteHeader(500)
	})
	mux.HandleFunc("GET /v1/flush", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := w.(http.Flusher); !ok {
			t.Error("the recorder passes Flush through")
		}
		http.NewResponseController(w).Flush()
	})
	h := app.recoverPanic(app.logRequests(mux))
	for _, p := range []string{"/v1/boom/1", "/v1/boom/2", "/v1/panic", wbBase + "/threads/x/events", "/v1/flush"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", p, nil))
	}
	counts := app.counters.take(time.Now())
	byRoute := map[string]*routeCounts{}
	for k, v := range counts {
		byRoute[k.route] = v
	}
	if b := byRoute["GET /v1/boom/{id}"]; b == nil || b.errors != 2 || b.requests != 2 {
		t.Fatalf("5xx per route pattern: %+v", byRoute)
	}
	if p := byRoute["GET /v1/panic"]; p == nil || p.errors != 1 {
		t.Fatalf("a panic counts as a 500: %+v", byRoute)
	}
	if _, ok := byRoute["GET "+wbBase+"/threads/{id}/events"]; ok {
		t.Fatal("event streams are not counted")
	}
	if f := byRoute["GET /v1/flush"]; f == nil || f.errors != 0 {
		t.Fatalf("flushed responses count as 200: %+v", byRoute)
	}
}
