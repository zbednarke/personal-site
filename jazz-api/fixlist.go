package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	webpush "github.com/SherClockHolmes/webpush-go"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// The daily fix list: what failed in the last 24 hours (trumpet runs, search
// sources, listing checks, marketplace budgets, Magic Film jobs, the API's
// own 5xx and slow responses), each compared with the 24 hours before, read
// from Postgres only. It is sent once per UTC day as a Workbench Web Push and
// as a message in the owner's "Fix list" thread, and shown in full at
// /jazz/#fixlist.
//
// Triggers (any of them; the first to claim the day's row sends it):
//   - the trumpet runner, at the end of each run: POST /v1/trumpets/machine/fixlist
//     (machine token; from 13:00 UTC, after the 13:17 daily run starts);
//   - the API's and Workbench's own requests after 15:00 UTC (a fallback for a
//     run that never finished), and Workbench's minute sweep while it is awake.
// No Cloud Scheduler: the runner is the daily clock, and any later traffic
// catches a day the runner missed (that digest then flags the missing run).
//
// Privacy: pushes carry fixed words, counts, domains and route patterns only;
// never listing notes, titles, profile text or free-form error text.

const (
	fixListThreadTitle = "Fix list"
	fixListPage        = "/jazz/#fixlist"
	// The daily run starts at 13:17 UTC; after 14:30 a day without one is missing.
	fixRunDeadlineHour, fixRunDeadlineMinute = 14, 30
	fixMachineFromHour                       = 13
	fixFallbackFromHour                      = 15
	// A claim not delivered within this long may be retried.
	fixClaimLease = 15 * time.Minute
	// Web Push payloads must fit in about 4 KB once encrypted.
	fixPushMaxBytes = 3000
	fixTitleMax     = 60
	fixBodyMax      = 240
)

// fixListOwnerSubject is whose fix list this is: FIX_LIST_OWNER_SUBJECT, or
// the Workbench owner, or the trumpet machine owner (the same person).
func fixListOwnerSubject() string {
	for _, k := range []string{"FIX_LIST_OWNER_SUBJECT", "WORKBENCH_OWNER_SUBJECT", "TRUMPETS_MACHINE_USER"} {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" && len(v) <= 128 {
			return v
		}
	}
	return ""
}

// pusherFromEnv: the API service sends the fix list itself when it has the
// Workbench VAPID keys (the same secret), so delivery never waits for the
// scale-to-zero Workbench service to wake.
func pusherFromEnv() wbPusher {
	pub, priv := strings.TrimSpace(os.Getenv("WORKBENCH_VAPID_PUBLIC_KEY")), strings.TrimSpace(os.Getenv("WORKBENCH_VAPID_PRIVATE_KEY"))
	if pub == "" || priv == "" {
		return nil
	}
	return &webPusher{public: pub, private: priv, subject: envOr("WORKBENCH_VAPID_SUBJECT", "mailto:workbench@zachbednarke.com")}
}

// pushService is Workbench's pusher in the Workbench service, else the API's.
func (app *application) pushService() wbPusher {
	if app.wb != nil && app.wb.push != nil {
		return app.wb.push
	}
	return app.push
}

// ---- The digest -------------------------------------------------------------------

// fixItem is one counted thing to fix (or, with Info, worth knowing).
type fixItem struct {
	Key      string   `json:"key"`
	Section  string   `json:"section"`
	Label    string   `json:"label"`
	Short    string   `json:"short"` // push-safe: fixed words, domains, routes
	Count    int64    `json:"count"`
	Previous int64    `json:"previous"`
	Domains  []string `json:"domains,omitempty"`
	Info     bool     `json:"info,omitempty"`
	Score    float64  `json:"score"`
}

type fixRun struct {
	ExpectedDay string     `json:"expectedDay"`
	Found       bool       `json:"found"`
	Missing     bool       `json:"missing"`
	Status      string     `json:"status,omitempty"`
	StartedAt   *time.Time `json:"startedAt,omitempty"`
	CompletedAt *time.Time `json:"completedAt,omitempty"`
	Notes       []string   `json:"notes,omitempty"`
	Runs        int64      `json:"runs"`
	Previous    int64      `json:"previousRuns"`
}

type fixDigest struct {
	Version     int       `json:"version"`
	Day         string    `json:"day"`
	GeneratedAt time.Time `json:"generatedAt"`
	WindowStart time.Time `json:"windowStart"`
	Verdict     string    `json:"verdict"`
	AllClear    bool      `json:"allClear"`
	ToFix       int       `json:"toFix"`
	FixNext     *fixItem  `json:"fixNext,omitempty"`
	Run         fixRun    `json:"run"`
	Items       []fixItem `json:"items"`
}

var fixSectionOrder = map[string]int{"run": 0, "api": 1, "checks": 2, "sources": 3, "market": 4, "films": 5}

// Base value of fixing each kind of item, before its count and trend.
var fixBase = map[string]float64{
	"run:missing": 100, "run:failed": 90, "api:5xx": 70, "films:stuck": 55, "films:failed": 60, "run:partial": 45,
	"checks:unverifiable": 40, "checks:failed": 30, "sources:failed": 30, "api:slow": 20, "market": 15, "sources:skipped": 10,
}

func fixScore(kind string, count, previous int64) float64 {
	if count <= 0 {
		return 0
	}
	s := fixBase[kind] * (1 + math.Log2(1+float64(count))/4)
	if count > previous {
		s += 10
	}
	if previous == 0 {
		s += 5
	}
	return math.Round(s*10) / 10
}

var fixDomainPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{0,98}[a-z0-9]$`)

// fixDomain keeps a host name only; anything else reads "a source".
func fixDomain(d string) string {
	d = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(d), "www."))
	if fixDomainPattern.MatchString(d) && strings.Contains(d, ".") {
		return d
	}
	return ""
}

var fixNumber = regexp.MustCompile(`(HTTP )?\d+`)

// Reverb's note lists skipped items as "(skipped: 3 budget, ...)".
var fixBudgetSkip = regexp.MustCompile(`\d+ budget`)

// fixReason turns a note into a grouping key: one line, numbers as N (HTTP
// status codes kept), at most 120 characters.
func fixReason(s string, numbers bool) string {
	s = strings.TrimSpace(strings.SplitN(s, "\n", 2)[0])
	if numbers {
		s = fixNumber.ReplaceAllStringFunc(s, func(m string) string {
			if strings.HasPrefix(m, "HTTP ") {
				return m
			}
			return "N"
		})
	}
	s = strings.TrimRight(s, ". ")
	if s == "" {
		s = "no reason given"
	}
	return wbCleanText(s, 120)
}

func fixDay(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// fixExpectedRunDay is the UTC day whose daily run should exist by now:
// today after 14:30 UTC, otherwise yesterday.
func fixExpectedRunDay(now time.Time) time.Time {
	day := fixDay(now)
	if now.UTC().Before(day.Add(fixRunDeadlineHour*time.Hour + fixRunDeadlineMinute*time.Minute)) {
		return day.AddDate(0, 0, -1)
	}
	return day
}

type fixGroup struct {
	cur, prev int64
	domains   map[string]bool
}

func (g *fixGroup) add(current bool, domain string) {
	if current {
		g.cur++
		if domain != "" {
			if g.domains == nil {
				g.domains = map[string]bool{}
			}
			g.domains[domain] = true
		}
	} else {
		g.prev++
	}
}

func fixDomainList(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for d := range m {
		out = append(out, d)
	}
	sort.Strings(out)
	if len(out) > 8 {
		out = out[:8]
	}
	return out
}

func fixPlural(n int64, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// computeFixDigest reads the two 24-hour windows ending at now. The previous
// digest (if any) supplies yesterday's failed-check counts: a listing keeps
// only its latest failed attempt, so the raw rows undercount the earlier day.
func (app *application) computeFixDigest(ctx context.Context, user uuid.UUID, now time.Time) (fixDigest, error) {
	now = now.UTC()
	ws, ps := now.Add(-24*time.Hour), now.Add(-48*time.Hour)
	d := fixDigest{Version: 1, Day: now.Format("2006-01-02"), GeneratedAt: now, WindowStart: ws, Items: []fixItem{}}
	add := func(it fixItem, kind string) {
		if !it.Info {
			it.Score = fixScore(kind, it.Count, it.Previous)
		}
		if it.Count > 0 || it.Previous > 0 {
			d.Items = append(d.Items, it)
		}
	}

	// Trumpet runs: the daily run (externalId adaptive-watch-*) and all runs.
	type runRow struct {
		status, errText string
		started         time.Time
		completed       *time.Time
		daily           bool
	}
	runs, err := wbCollect(wbRows(app.db.Query(ctx, `SELECT status,error,started_at,completed_at,external_id LIKE 'adaptive-watch-%'
		FROM trumpet_runs WHERE user_id=$1 AND started_at >= $2 AND started_at <= $3 ORDER BY started_at DESC LIMIT 200`, user, fixDay(ps).AddDate(0, 0, -1), now)), func(row pgx.Row) (runRow, error) {
		var r runRow
		return r, row.Scan(&r.status, &r.errText, &r.started, &r.completed, &r.daily)
	})
	if err != nil {
		return d, err
	}
	dailyOn := func(day time.Time) *runRow {
		for i := range runs {
			if runs[i].daily && !runs[i].started.Before(day) && runs[i].started.Before(day.AddDate(0, 0, 1)) {
				return &runs[i]
			}
		}
		return nil
	}
	// Today's run once it exists (the runner's trigger comes right after it),
	// otherwise the run that should exist by now.
	expected := fixExpectedRunDay(now)
	if dailyOn(fixDay(now)) != nil {
		expected = fixDay(now)
	}
	d.Run.ExpectedDay = expected.Format("2006-01-02")
	statusCount := map[string]*fixGroup{"failed": {}, "partial": {}}
	for _, r := range runs {
		if r.started.Before(ps) {
			continue
		}
		cur := !r.started.Before(ws)
		if cur {
			d.Run.Runs++
		} else {
			d.Run.Previous++
		}
		if g := statusCount[r.status]; g != nil {
			g.add(cur, "")
		}
	}
	if r := dailyOn(expected); r != nil {
		started := r.started
		d.Run.Found, d.Run.Status, d.Run.StartedAt, d.Run.CompletedAt = true, r.status, &started, r.completed
		for _, part := range strings.Split(r.errText, "; ") {
			if part = wbCleanText(part, 200); part != "" && len(d.Run.Notes) < 12 {
				d.Run.Notes = append(d.Run.Notes, part)
			}
		}
	} else {
		d.Run.Missing = true
	}
	var prevMissing int64
	if dailyOn(expected.AddDate(0, 0, -1)) == nil {
		prevMissing = 1
	}
	if d.Run.Missing {
		add(fixItem{Key: "run:missing", Section: "run", Label: "No daily trumpet run on " + d.Run.ExpectedDay, Short: "daily run missing", Count: 1, Previous: prevMissing}, "run:missing")
	} else if prevMissing > 0 {
		add(fixItem{Key: "run:missing", Section: "run", Label: "Daily trumpet run missing", Short: "daily run missing", Count: 0, Previous: prevMissing}, "run:missing")
	}
	add(fixItem{Key: "run:failed", Section: "run", Label: "Trumpet runs that failed", Short: "trumpet run failed", Count: statusCount["failed"].cur, Previous: statusCount["failed"].prev}, "run:failed")
	add(fixItem{Key: "run:partial", Section: "run", Label: "Trumpet runs that were partial", Short: "trumpet run partial", Count: statusCount["partial"].cur, Previous: statusCount["partial"].prev}, "run:partial")

	// Failed or skipped search sources, grouped by reason.
	type srcRow struct {
		status, source, domain, note string
		cur                          bool
	}
	srcs, err := wbCollect(wbRows(app.db.Query(ctx, `SELECT s.status,s.source,s.domain,s.note,r.started_at >= $3
		FROM trumpet_run_sources s JOIN trumpet_runs r ON r.id=s.run_id
		WHERE r.user_id=$1 AND r.started_at >= $2 AND r.started_at <= $4 AND s.status IN ('failed','skipped') LIMIT 5000`, user, ps, ws, now)), func(row pgx.Row) (srcRow, error) {
		var s srcRow
		return s, row.Scan(&s.status, &s.source, &s.domain, &s.note, &s.cur)
	})
	if err != nil {
		return d, err
	}
	type srcKey struct{ status, reason string }
	srcGroups, srcOrder := map[srcKey]*fixGroup{}, []srcKey{}
	for _, s := range srcs {
		k := srcKey{s.status, fixReason(s.note, true)}
		if srcGroups[k] == nil {
			srcGroups[k] = &fixGroup{}
			srcOrder = append(srcOrder, k)
		}
		name := fixDomain(s.domain)
		if name == "" {
			name = wbCleanText(s.source, 60)
		}
		srcGroups[k].add(s.cur, name)
	}
	for _, k := range srcOrder {
		g := srcGroups[k]
		verb := "Failed"
		if k.status == "skipped" {
			verb = "Skipped"
		}
		// A lead nobody opened yet is how discovery works, not a fault.
		info := k.status == "skipped" && strings.Contains(strings.ToLower(k.reason), "no live page accessed")
		add(fixItem{Key: "sources:" + k.status + ":" + k.reason, Section: "sources", Label: verb + ": " + k.reason, Short: fmt.Sprintf("%d search %s %s", g.cur, fixPlural(g.cur, "source", "sources"), strings.ToLower(verb)),
			Count: g.cur, Previous: g.prev, Domains: fixDomainList(g.domains), Info: info}, "sources:"+k.status)
	}

	// Failed checks by domain and reason (still failing: no success since).
	var prevDigest *fixDigest
	var raw []byte
	if err := app.db.QueryRow(ctx, `SELECT digest FROM fix_list_digests WHERE user_id=$1 AND day=$2 AND delivered_at IS NOT NULL`, user, fixDay(now).AddDate(0, 0, -1)).Scan(&raw); err == nil {
		var p fixDigest
		if json.Unmarshal(raw, &p) == nil && p.Version == 1 {
			prevDigest = &p
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return d, err
	}
	type chkRow struct {
		domain, reason, verification string
		cur                          bool
	}
	chks, err := wbCollect(wbRows(app.db.Query(ctx, `SELECT split_part(last_check_error,' / ',1),
		CASE WHEN strpos(last_check_error,' / ')>0 THEN substr(last_check_error,strpos(last_check_error,' / ')+3) ELSE last_check_error END,
		verification_state, last_check_attempt >= $3
		FROM trumpet_listings WHERE user_id=$1 AND last_check_attempt >= $2 AND last_check_attempt <= $4 AND last_check_error<>''
		AND (last_checked IS NULL OR last_checked < last_check_attempt) LIMIT 5000`, user, ps, ws, now)), func(row pgx.Row) (chkRow, error) {
		var c chkRow
		return c, row.Scan(&c.domain, &c.reason, &c.verification, &c.cur)
	})
	if err != nil {
		return d, err
	}
	type chkKey struct{ domain, reason string }
	chkGroups, chkOrder := map[chkKey]*fixGroup{}, []chkKey{}
	leads := &fixGroup{}
	verifyCap := &fixGroup{}
	for _, c := range chks {
		domain := fixDomain(c.domain)
		reason := fixReason(c.reason, false)
		if strings.Contains(c.reason, "search-provider check budget reached") {
			verifyCap.add(c.cur, domain)
		}
		if c.verification == "candidate" {
			leads.add(c.cur, domain)
			continue
		}
		k := chkKey{domain, reason}
		if chkGroups[k] == nil {
			chkGroups[k] = &fixGroup{}
			chkOrder = append(chkOrder, k)
		}
		chkGroups[k].add(c.cur, domain)
	}
	prevByKey := map[string]int64{}
	if prevDigest != nil {
		for _, it := range prevDigest.Items {
			prevByKey[it.Key] = it.Count
		}
	}
	for _, k := range chkOrder {
		g := chkGroups[k]
		key := "checks:" + k.domain + ":" + k.reason
		prev := g.prev
		if prevDigest != nil {
			prev = prevByKey[key]
		}
		where := k.domain
		if where == "" {
			where = "a source"
		}
		add(fixItem{Key: key, Section: "checks", Label: where + ": " + k.reason, Short: "failed checks on " + where, Count: g.cur, Previous: prev, Domains: fixDomainList(g.domains)}, "checks:failed")
	}
	if prevDigest != nil {
		// Yesterday's groups that no longer fail show as fixed (count 0).
		seen := map[string]bool{}
		for _, it := range d.Items {
			seen[it.Key] = true
		}
		for _, it := range prevDigest.Items {
			if it.Section == "checks" && strings.HasPrefix(it.Key, "checks:") && !strings.HasPrefix(it.Key, "checks:unverifiable") && !strings.HasPrefix(it.Key, "checks:leads") && !seen[it.Key] && it.Count > 0 {
				add(fixItem{Key: it.Key, Section: "checks", Label: it.Label, Short: it.Short, Count: 0, Previous: it.Count}, "checks:failed")
			}
		}
	}
	leadPrev := leads.prev
	if prevDigest != nil {
		leadPrev = prevByKey["checks:leads"]
	}
	add(fixItem{Key: "checks:leads", Section: "checks", Label: "Lead checks that failed (unverified leads, retried later)", Short: "lead checks failed", Count: leads.cur, Previous: leadPrev, Domains: fixDomainList(leads.domains), Info: true}, "")
	var unvCur, unvPrev int64
	if err := app.db.QueryRow(ctx, `SELECT count(*) FILTER (WHERE occurred_at >= $3), count(*) FILTER (WHERE occurred_at < $3)
		FROM trumpet_events WHERE user_id=$1 AND new_status='unverifiable' AND occurred_at >= $2 AND occurred_at <= $4`, user, ps, ws, now).Scan(&unvCur, &unvPrev); err != nil {
		return d, err
	}
	add(fixItem{Key: "checks:unverifiable", Section: "checks", Label: "Listings that became unverifiable (failed checks on consecutive days)", Short: "listings became unverifiable", Count: unvCur, Previous: unvPrev}, "checks:unverifiable")

	// Marketplace notes where a budget or cap skipped work.
	type market struct{ key, label, short string }
	marketRules := []struct {
		m     market
		match func(source, note string) bool
	}{
		{market{"market:reverb-budget", "Reverb: request budget reached before every search or item", "Reverb request budget reached"}, func(src, note string) bool {
			return src == "Reverb API" && (strings.Contains(note, "Request budget reached") || fixBudgetSkip.MatchString(note))
		}},
		{market{"market:ebay-time", "eBay: run time budget reached before discovery finished", "eBay time budget reached"}, func(src, note string) bool {
			return strings.HasPrefix(src, "eBay") && strings.Contains(note, "time budget")
		}},
		{market{"market:ebay-no-provider", "eBay: discovery skipped (no search provider key)", "eBay discovery skipped"}, func(src, note string) bool {
			return strings.HasPrefix(src, "eBay") && strings.Contains(note, "not configured")
		}},
	}
	marketGroups := map[string]*fixGroup{}
	notes, err := wbCollect(wbRows(app.db.Query(ctx, `SELECT s.source,s.note,r.started_at >= $3 FROM trumpet_run_sources s JOIN trumpet_runs r ON r.id=s.run_id
		WHERE r.user_id=$1 AND r.started_at >= $2 AND r.started_at <= $4 AND (s.source='Reverb API' OR s.source LIKE 'eBay%') LIMIT 2000`, user, ps, ws, now)), func(row pgx.Row) (srcRow, error) {
		var s srcRow
		return s, row.Scan(&s.source, &s.note, &s.cur)
	})
	if err != nil {
		return d, err
	}
	for _, n := range notes {
		for _, rule := range marketRules {
			if rule.match(n.source, n.note) {
				if marketGroups[rule.m.key] == nil {
					marketGroups[rule.m.key] = &fixGroup{}
				}
				marketGroups[rule.m.key].add(n.cur, "")
			}
		}
	}
	runRules := []market{
		{"market:page-budget", "Discovery page budget (240) reached; the rest stayed unverified leads", "page budget reached"},
		{"market:search-time", "Search time budget exhausted", "search time budget exhausted"},
	}
	for _, r := range runs {
		if r.started.Before(ps) {
			continue
		}
		cur := !r.started.Before(ws)
		for _, m := range runRules {
			needle := map[string]string{"market:page-budget": "Discovery page budget", "market:search-time": "Search time budget exhausted"}[m.key]
			if strings.Contains(r.errText, needle) {
				if marketGroups[m.key] == nil {
					marketGroups[m.key] = &fixGroup{}
				}
				marketGroups[m.key].add(cur, "")
			}
		}
	}
	for _, rule := range marketRules {
		if g := marketGroups[rule.m.key]; g != nil {
			add(fixItem{Key: rule.m.key, Section: "market", Label: rule.m.label + " (runs)", Short: rule.m.short, Count: g.cur, Previous: g.prev}, "market")
		}
	}
	for _, m := range runRules {
		if g := marketGroups[m.key]; g != nil {
			add(fixItem{Key: m.key, Section: "market", Label: m.label + " (runs)", Short: m.short, Count: g.cur, Previous: g.prev}, "market")
		}
	}
	capPrev := verifyCap.prev
	if prevDigest != nil {
		capPrev = prevByKey["market:openai-verify-cap"]
	}
	add(fixItem{Key: "market:openai-verify-cap", Section: "market", Label: "OpenAI verify cap reached: blocked listings left unchecked", Short: "OpenAI verify cap reached", Count: verifyCap.cur, Previous: capPrev, Domains: fixDomainList(verifyCap.domains)}, "market")

	// Magic Film jobs that failed, by error; and jobs stuck mid-way.
	type filmRow struct {
		errText string
		cur     bool
	}
	films, err := wbCollect(wbRows(app.db.Query(ctx, `SELECT error, COALESCE(finished_at,updated_at) >= $3 FROM magic_film_jobs
		WHERE user_id=$1 AND status='failed' AND COALESCE(finished_at,updated_at) >= $2 AND COALESCE(finished_at,updated_at) <= $4 LIMIT 1000`, user, ps, ws, now)), func(row pgx.Row) (filmRow, error) {
		var f filmRow
		return f, row.Scan(&f.errText, &f.cur)
	})
	if err != nil {
		return d, err
	}
	filmGroups, filmOrder := map[string]*fixGroup{}, []string{}
	for _, f := range films {
		k := fixReason(f.errText, true)
		if filmGroups[k] == nil {
			filmGroups[k] = &fixGroup{}
			filmOrder = append(filmOrder, k)
		}
		filmGroups[k].add(f.cur, "")
	}
	for _, k := range filmOrder {
		g := filmGroups[k]
		add(fixItem{Key: "films:failed:" + k, Section: "films", Label: "Magic Film failed: " + k, Short: "Magic Film failed", Count: g.cur, Previous: g.prev}, "films:failed")
	}
	var stuck int64
	if err := app.db.QueryRow(ctx, `SELECT count(*) FROM magic_film_jobs WHERE user_id=$1 AND status IN ('queued','starting','preparing','editing','rendering') AND updated_at < $2::timestamptz - interval '1 hour'`, user, now).Scan(&stuck); err != nil {
		return d, err
	}
	add(fixItem{Key: "films:stuck", Section: "films", Label: "Magic Film jobs stuck for over an hour", Short: "Magic Film stuck", Count: stuck}, "films:stuck")

	// The API's own 5xx and slow responses (both services, every route).
	type apiRow struct {
		service, route           string
		e5, e5p, slow, slowp, rq int64
	}
	apis, err := wbCollect(wbRows(app.db.Query(ctx, `SELECT service,route,
		COALESCE(sum(errors_5xx) FILTER (WHERE hour >= $2),0)::bigint, COALESCE(sum(errors_5xx) FILTER (WHERE hour < $2),0)::bigint,
		COALESCE(sum(slow) FILTER (WHERE hour >= $2),0)::bigint, COALESCE(sum(slow) FILTER (WHERE hour < $2),0)::bigint,
		COALESCE(sum(requests) FILTER (WHERE hour >= $2),0)::bigint
		FROM api_route_counters WHERE hour >= $1 AND hour <= $3 GROUP BY service,route HAVING sum(errors_5xx)>0 OR sum(slow)>0 ORDER BY service,route LIMIT 200`,
		ps.Truncate(time.Hour), ws.Truncate(time.Hour), now)), func(row pgx.Row) (apiRow, error) {
		var a apiRow
		return a, row.Scan(&a.service, &a.route, &a.e5, &a.e5p, &a.slow, &a.slowp, &a.rq)
	})
	if err != nil {
		return d, err
	}
	for _, a := range apis {
		where := wbCleanText(a.route, 120) + " (" + wbCleanText(a.service, 40) + ")"
		add(fixItem{Key: "api:5xx:" + a.service + ":" + a.route, Section: "api", Label: fmt.Sprintf("5xx on %s, of %d requests", where, a.rq), Short: "5xx on " + where, Count: a.e5, Previous: a.e5p}, "api:5xx")
		add(fixItem{Key: "api:slow:" + a.service + ":" + a.route, Section: "api", Label: "10 s or slower: " + where, Short: "slow " + where, Count: a.slow, Previous: a.slowp}, "api:slow")
	}

	// Order: section, then value; pick the single most valuable item.
	sort.SliceStable(d.Items, func(i, j int) bool {
		a, b := d.Items[i], d.Items[j]
		if fixSectionOrder[a.Section] != fixSectionOrder[b.Section] {
			return fixSectionOrder[a.Section] < fixSectionOrder[b.Section]
		}
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		return a.Key < b.Key
	})
	for i := range d.Items {
		it := d.Items[i]
		if it.Info || it.Count <= 0 {
			continue
		}
		d.ToFix++
		if d.FixNext == nil || it.Score > d.FixNext.Score {
			next := it
			d.FixNext = &next
		}
	}
	d.AllClear = d.ToFix == 0
	d.Verdict = fixVerdict(d)
	return d, nil
}

func fixVerdict(d fixDigest) string {
	if d.AllClear {
		if d.Run.Found && d.Run.Status == "succeeded" {
			return "All clear: the daily run succeeded and nothing failed."
		}
		return "All clear: nothing failed."
	}
	return wbCleanText(fmt.Sprintf("%d to fix · next: %s", d.ToFix, d.FixNext.Short), 120)
}

// fixPushBody is a short summary from counts and fixed words only.
func fixPushBody(d fixDigest) string {
	if d.AllClear {
		return wbCleanText(d.Verdict, fixBodyMax)
	}
	sums := map[string]int64{}
	domains := map[string]map[string]bool{}
	for _, it := range d.Items {
		if it.Info || it.Count <= 0 {
			continue
		}
		sums[it.Section] += it.Count
		if domains[it.Section] == nil {
			domains[it.Section] = map[string]bool{}
		}
		for _, dm := range it.Domains {
			domains[it.Section][dm] = true
		}
	}
	parts := []string{}
	switch {
	case d.Run.Missing:
		parts = append(parts, "Run missing")
	case d.Run.Status != "" && d.Run.Status != "succeeded":
		parts = append(parts, "Run "+d.Run.Status)
	}
	if n := sums["api"]; n > 0 {
		parts = append(parts, fmt.Sprintf("API %d", n))
	}
	if n := sums["checks"]; n > 0 {
		parts = append(parts, fmt.Sprintf("Checks %d on %d %s", n, len(domains["checks"]), fixPlural(int64(len(domains["checks"])), "domain", "domains")))
	}
	if n := sums["sources"]; n > 0 {
		parts = append(parts, fmt.Sprintf("Sources %d", n))
	}
	if n := sums["market"]; n > 0 {
		parts = append(parts, fmt.Sprintf("Budgets %d", n))
	}
	if n := sums["films"]; n > 0 {
		parts = append(parts, fmt.Sprintf("Films %d", n))
	}
	return wbCleanText(d.Verdict+"\n"+strings.Join(parts, " · "), fixBodyMax)
}

// fixPushPayload is what the service worker shows; bounded well under the
// Web Push limit whatever the digest holds.
func fixPushPayload(d fixDigest, thread uuid.UUID) []byte {
	p := wbPushPayload{Kind: "fixlist", Title: wbCleanText("Fix list – "+d.Day, fixTitleMax), Body: fixPushBody(d), URL: fixListPage, Tag: "fixlist", Quiet: d.AllClear}
	if thread != uuid.Nil {
		p.ThreadID = thread.String()
	}
	b, _ := json.Marshal(p)
	for len(b) > fixPushMaxBytes && utf8.RuneCountInString(p.Body) > 20 {
		p.Body = wbCleanText(p.Body, utf8.RuneCountInString(p.Body)/2)
		b, _ = json.Marshal(p)
	}
	return b
}

func fixMD(s string) string {
	return strings.NewReplacer("`", "'", "*", "", "[", "(", "]", ")", "\n", " ", "#", "").Replace(s)
}

func fixDelta(it fixItem) string {
	switch delta := it.Count - it.Previous; {
	case it.Previous == 0 && it.Count > 0:
		return "new"
	case delta > 0:
		return fmt.Sprintf("+%d", delta)
	case delta < 0:
		return fmt.Sprintf("−%d", -delta)
	}
	return "same"
}

// fixMessage is the thread message: the verdict, Fix next and the items.
func fixMessage(d fixDigest) string {
	var b strings.Builder
	fmt.Fprintf(&b, "**Fix list – %s** · %s\n\n", d.Day, fixMD(d.Verdict))
	if d.FixNext != nil {
		fmt.Fprintf(&b, "Fix next: **%s** (%d, %s)\n\n", fixMD(d.FixNext.Label), d.FixNext.Count, fixDelta(*d.FixNext))
	}
	switch {
	case d.Run.Missing:
		fmt.Fprintf(&b, "- Daily trumpet run: missing for %s\n", d.Run.ExpectedDay)
	case d.Run.Found:
		fmt.Fprintf(&b, "- Daily trumpet run: %s (%s)\n", d.Run.Status, d.Run.StartedAt.UTC().Format("2006-01-02 15:04 UTC"))
	}
	shown := 0
	for _, it := range d.Items {
		if it.Info || it.Count <= 0 || it.Section == "run" && it.Key == "run:missing" {
			continue
		}
		if shown == 12 {
			fmt.Fprintf(&b, "- …and %d more on the page\n", d.ToFix-shown)
			break
		}
		fmt.Fprintf(&b, "- %s: %d (%s)\n", fixMD(it.Label), it.Count, fixDelta(it))
		shown++
	}
	fmt.Fprintf(&b, "\n[Open the full fix list](%s). Reply here to ask me to fix an item.", fixListPage)
	return b.String()
}

// fixNote is the agent's context for a later turn in the thread: compact.
func fixNote(d fixDigest) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Daily fix list for %s (last 24 h vs the previous 24 h, from the database). %s\n", d.Day, d.Verdict)
	if d.Run.Found {
		fmt.Fprintf(&b, "Daily trumpet run: %s.", d.Run.Status)
		if len(d.Run.Notes) > 0 {
			fmt.Fprintf(&b, " Notes: %s", strings.Join(d.Run.Notes, "; "))
		}
		b.WriteString("\n")
	}
	for _, it := range d.Items {
		if it.Count <= 0 && it.Previous <= 0 {
			continue
		}
		fmt.Fprintf(&b, "- [%s] %s: %d (previous %d)", it.Section, it.Label, it.Count, it.Previous)
		if len(it.Domains) > 0 {
			fmt.Fprintf(&b, " domains: %s", strings.Join(it.Domains, ", "))
		}
		b.WriteString("\n")
	}
	return wbCleanText(b.String(), 8000)
}

// ---- Delivery ---------------------------------------------------------------------

type fixListResult struct {
	Day      string     `json:"day"`
	Sent     bool       `json:"sent"`
	Replayed bool       `json:"replayed,omitempty"`
	Reason   string     `json:"reason,omitempty"`
	Verdict  string     `json:"verdict,omitempty"`
	AllClear bool       `json:"allClear,omitempty"`
	Pushed   int        `json:"pushed"`
	ThreadID *uuid.UUID `json:"threadId,omitempty"`
}

// fixListDeliver sends the day's fix list once: the first caller claims the
// (owner, UTC day) row; everyone else gets the stored outcome. A claim that
// was never delivered (a crash) can be taken again after fixClaimLease.
func (app *application) fixListDeliver(ctx context.Context, user uuid.UUID, now time.Time, trigger string) (fixListResult, error) {
	day := fixDay(now)
	res := fixListResult{Day: day.Format("2006-01-02")}
	var claimed bool
	err := app.db.QueryRow(ctx, `INSERT INTO fix_list_digests (user_id,day,trigger) VALUES ($1,$2,$3)
		ON CONFLICT (user_id,day) DO UPDATE SET claimed_at=now(), trigger=EXCLUDED.trigger
		WHERE fix_list_digests.delivered_at IS NULL AND fix_list_digests.claimed_at < now() - $4::interval
		RETURNING true`, user, day, wbCleanText(trigger, 40), fmt.Sprintf("%d seconds", int(fixClaimLease.Seconds()))).Scan(&claimed)
	if errors.Is(err, pgx.ErrNoRows) {
		var delivered bool
		var thread *uuid.UUID
		if err := app.db.QueryRow(ctx, `SELECT delivered_at IS NOT NULL, verdict, all_clear, pushed, thread_id FROM fix_list_digests WHERE user_id=$1 AND day=$2`, user, day).
			Scan(&delivered, &res.Verdict, &res.AllClear, &res.Pushed, &thread); err != nil {
			return res, err
		}
		res.Replayed, res.ThreadID = true, thread
		res.Reason = "already sent today"
		if !delivered {
			res.Reason = "being sent"
		}
		return res, nil
	}
	if err != nil {
		return res, err
	}
	release := func() {
		_, _ = app.db.Exec(context.WithoutCancel(ctx), `DELETE FROM fix_list_digests WHERE user_id=$1 AND day=$2 AND delivered_at IS NULL`, user, day)
	}
	_ = app.flushRouteCounters(ctx)
	d, err := app.computeFixDigest(ctx, user, now)
	if err != nil {
		release()
		return res, err
	}
	thread, err := app.fixListPost(ctx, user, d)
	if err != nil {
		release()
		return res, err
	}
	// Quiet days still arrive (so the owner knows it is alive), without sound
	// and at low urgency.
	pushCtx := fixPushContext(ctx, d)
	sent, pending := 0, app.pushService() == nil
	if !pending {
		sent = app.wbPushRaw(pushCtx, user, false, "", fixPushPayload(d, thread))
	}
	body, _ := json.Marshal(d)
	if _, err := app.db.Exec(context.WithoutCancel(ctx), `UPDATE fix_list_digests SET delivered_at=now(), verdict=$3, all_clear=$4, digest=$5, thread_id=$6, pushed=$7, push_pending=$8 WHERE user_id=$1 AND day=$2`,
		user, day, wbCleanText(d.Verdict, 300), d.AllClear, body, thread, sent, pending); err != nil {
		return res, err
	}
	res.Sent, res.Verdict, res.AllClear, res.Pushed, res.ThreadID = true, d.Verdict, d.AllClear, sent, &thread
	return res, nil
}

// fixPushContext: quiet days still arrive (so the owner knows it is alive),
// without sound and at low urgency; both keep for 12 hours on a phone that
// is offline.
func fixPushContext(ctx context.Context, d fixDigest) context.Context {
	urgency := webpush.UrgencyNormal
	if d.AllClear {
		urgency = webpush.UrgencyLow
	}
	return context.WithValue(ctx, wbPushOptionsKey{}, wbPushOptions{Urgency: urgency, TTL: 12 * 3600})
}

// fixListPendingPushes sends today's push for a fix list delivered by a
// process without the VAPID keys (the API before its keys are set): the
// Workbench sweep, which has them, calls it every minute while awake.
func (app *application) fixListPendingPushes(ctx context.Context, now time.Time) {
	if app.pushService() == nil || app.db == nil {
		return
	}
	type pending struct {
		user   uuid.UUID
		raw    []byte
		thread *uuid.UUID
	}
	rows, err := wbCollect(wbRows(app.db.Query(ctx, `UPDATE fix_list_digests SET push_pending=false WHERE push_pending AND day=$1 RETURNING user_id,digest,thread_id`, fixDay(now))), func(row pgx.Row) (pending, error) {
		var p pending
		return p, row.Scan(&p.user, &p.raw, &p.thread)
	})
	if err != nil {
		app.logger.Warn("fix list pending pushes failed", "error", err)
		return
	}
	for _, p := range rows {
		var d fixDigest
		if json.Unmarshal(p.raw, &d) != nil {
			continue
		}
		thread := uuid.Nil
		if p.thread != nil {
			thread = *p.thread
		}
		sent := app.wbPushRaw(fixPushContext(ctx, d), p.user, false, "", fixPushPayload(d, thread))
		_, _ = app.db.Exec(ctx, `UPDATE fix_list_digests SET pushed=pushed+$3 WHERE user_id=$1 AND day=$2`, p.user, fixDay(now), sent)
	}
}

// fixListPost writes the digest into the "Fix list" thread: a visible
// assistant message (idempotent on its client id) and a server note the
// agent reads with the owner's next message there. Older unread fix-list
// notes are superseded so the context stays small.
func (app *application) fixListPost(ctx context.Context, user uuid.UUID, d fixDigest) (uuid.UUID, error) {
	tx, err := app.db.Begin(ctx)
	if err != nil {
		return uuid.Nil, err
	}
	defer tx.Rollback(ctx)
	var thread uuid.UUID
	err = tx.QueryRow(ctx, `SELECT id FROM wb_threads WHERE user_id=$1 AND title=$2 AND archived_at IS NULL ORDER BY created_at LIMIT 1`, user, fixListThreadTitle).Scan(&thread)
	if errors.Is(err, pgx.ErrNoRows) {
		thread = uuid.New()
		_, err = tx.Exec(ctx, `INSERT INTO wb_threads (id,user_id,title) VALUES ($1,$2,$3)`, thread, user, fixListThreadTitle)
	}
	if err != nil {
		return uuid.Nil, err
	}
	clientID := "fixlist-" + d.Day
	msg, err := wbScanMessage(tx.QueryRow(ctx, `INSERT INTO wb_messages AS m (id,thread_id,role,text,client_id,status,handled_at)
		VALUES ($1,$2,'assistant',$3,$4,'done',now()) ON CONFLICT (thread_id,client_id) WHERE client_id IS NOT NULL DO NOTHING RETURNING `+wbMessageColumns,
		uuid.New(), thread, fixMessage(d), clientID))
	if errors.Is(err, pgx.ErrNoRows) {
		return thread, nil // already posted (a retried delivery)
	}
	if err != nil {
		return uuid.Nil, err
	}
	if _, err := app.wbAppendTx(ctx, tx, thread, "message.created", map[string]any{"message": msg}); err != nil {
		return uuid.Nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE wb_messages SET handled_at=now() WHERE thread_id=$1 AND role='note' AND handled_at IS NULL AND context ? 'fixlist'`, thread); err != nil {
		return uuid.Nil, err
	}
	noteCtx, _ := json.Marshal(map[string]string{"fixlist": d.Day})
	if _, err := tx.Exec(ctx, `INSERT INTO wb_messages (id,thread_id,role,text,context) VALUES ($1,$2,'note',$3,$4)`, uuid.New(), thread, fixNote(d), noteCtx); err != nil {
		return uuid.Nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return uuid.Nil, err
	}
	if app.wb != nil {
		app.wb.hub.notify(thread)
	}
	return thread, nil
}

// ---- Triggers ---------------------------------------------------------------------

// fixListAuto is the in-process state of the automatic triggers: enabled
// in production (an owner is configured), off in tests unless set.
type fixListAuto struct {
	enabled bool
	mu      sync.Mutex
	busy    bool
	doneDay string
	retryAt time.Time
	// now and wait are test hooks.
	now  func() time.Time
	wait chan fixListResult
}

func (app *application) fixFallbackTrigger() string {
	if app.wbOnly {
		return "workbench-fallback"
	}
	return "api-fallback"
}

// fixListMaybe starts a background delivery after 15:00 UTC unless today's
// is known to be sent. It never blocks the caller.
func (app *application) fixListMaybe(now time.Time, trigger string) {
	f := &app.fix
	if !f.enabled || app.db == nil {
		return
	}
	if f.now != nil {
		now = f.now()
	}
	now = now.UTC()
	if now.Hour() < fixFallbackFromHour {
		return
	}
	day := now.Format("2006-01-02")
	f.mu.Lock()
	if f.busy || f.doneDay == day || now.Before(f.retryAt) {
		f.mu.Unlock()
		return
	}
	f.busy = true
	f.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		res, err := app.fixListForOwner(ctx, now, trigger)
		f.mu.Lock()
		f.busy = false
		switch {
		case err != nil:
			f.retryAt = now.Add(5 * time.Minute)
			app.logger.Warn("fix list fallback failed", "trigger", trigger, "error", err)
		case res.Sent || res.Reason == "already sent today":
			f.doneDay = day
		default:
			f.retryAt = now.Add(5 * time.Minute)
		}
		f.mu.Unlock()
		if res.Sent {
			app.logger.Info("fix list sent", "trigger", trigger, "day", res.Day, "pushed", res.Pushed, "all_clear", res.AllClear)
		}
		if f.wait != nil {
			f.wait <- res
		}
	}()
}

func (app *application) fixListForOwner(ctx context.Context, now time.Time, trigger string) (fixListResult, error) {
	subject := fixListOwnerSubject()
	if subject == "" {
		return fixListResult{}, errors.New("no fix list owner configured")
	}
	user, err := app.userID(context.WithValue(ctx, userSubjectKey, subject))
	if err != nil {
		return fixListResult{}, err
	}
	return app.fixListDeliver(ctx, user, now, trigger)
}

func (app *application) flushRouteCountersOnExit() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := app.flushRouteCounters(ctx); err != nil {
		app.logger.Warn("route counters flush on exit failed", "error", err)
	}
}

// ---- Routes -----------------------------------------------------------------------

func (app *application) fixListMachineRoutes(mux *http.ServeMux) {
	mux.Handle("POST /v1/trumpets/machine/fixlist", app.trumpetPrivacy(app.trumpetMachineAuth(http.HandlerFunc(app.machineFixList))))
}

func (app *application) fixListWorkbenchRoutes(mux *http.ServeMux) {
	mux.Handle("GET "+wbBase+"/fixlist", app.workbenchPrivacy(app.authenticate(app.workbenchOwner(app.wbFixList))))
}

// machineFixList: the trumpet runner's end-of-run trigger. Idempotent per
// UTC day; before 13:00 UTC it sends nothing (a manual early run must not
// use up the day before the daily run has happened).
func (app *application) machineFixList(w http.ResponseWriter, r *http.Request) {
	var in struct{}
	if err := decodeTrumpetJSON(w, r, &in); err != nil {
		writeError(w, 400, "invalid JSON")
		return
	}
	now := time.Now().UTC()
	if app.fix.now != nil {
		now = app.fix.now().UTC()
	}
	if now.Hour() < fixMachineFromHour {
		writeJSON(w, 200, fixListResult{Day: now.Format("2006-01-02"), Reason: "before 13:00 UTC; sent after the daily run"})
		return
	}
	user, err := app.userID(r.Context())
	if err != nil {
		app.serverError(w, err)
		return
	}
	res, err := app.fixListDeliver(r.Context(), user, now, "runner")
	if err != nil {
		app.serverError(w, err)
		return
	}
	writeJSON(w, 200, res)
}

// wbFixList serves the page: the latest delivered digest (or ?day=), the
// last two weeks' verdicts, and a live digest when none was sent yet.
func (app *application) wbFixList(w http.ResponseWriter, r *http.Request) {
	user, ok := app.cpUser(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	day := r.URL.Query().Get("day")
	if day != "" && !datePattern.MatchString(day) {
		writeError(w, 400, "day must be YYYY-MM-DD")
		return
	}
	var raw []byte
	var delivered *time.Time
	var thread *uuid.UUID
	var gotDay time.Time
	err := app.db.QueryRow(ctx, `SELECT day,digest,delivered_at,thread_id FROM fix_list_digests WHERE user_id=$1 AND delivered_at IS NOT NULL AND ($2='' OR day=$2::date) ORDER BY day DESC LIMIT 1`, user, day).
		Scan(&gotDay, &raw, &delivered, &thread)
	out := map[string]any{"live": false}
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		if day != "" {
			writeError(w, 404, "no fix list for that day")
			return
		}
		now := time.Now()
		if app.fix.now != nil {
			now = app.fix.now()
		}
		_ = app.flushRouteCounters(ctx)
		d, err := app.computeFixDigest(ctx, user, now)
		if err != nil {
			app.serverError(w, err)
			return
		}
		out["digest"], out["live"] = d, true
	case err != nil:
		app.serverError(w, err)
		return
	default:
		out["digest"], out["deliveredAt"] = json.RawMessage(raw), delivered
		if thread != nil {
			out["threadId"] = thread
		}
	}
	type past struct {
		Day      string `json:"day"`
		Verdict  string `json:"verdict"`
		AllClear bool   `json:"allClear"`
	}
	history, err := wbCollect(wbRows(app.db.Query(ctx, `SELECT to_char(day,'YYYY-MM-DD'),verdict,all_clear FROM fix_list_digests WHERE user_id=$1 AND delivered_at IS NOT NULL ORDER BY day DESC LIMIT 14`, user)), func(row pgx.Row) (past, error) {
		var p past
		return p, row.Scan(&p.Day, &p.Verdict, &p.AllClear)
	})
	if err != nil {
		app.serverError(w, err)
		return
	}
	out["history"] = history
	writeJSON(w, 200, out)
}
