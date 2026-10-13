package main

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ---- Head to head skips and their investigation ---------------------------
//
// A skip (pair event, action "skip", migration 037) says "these two cannot be
// compared", with optional one-tap reasons (meta.skipReasons) and an optional
// note. It is not a preference: the model ignores it, it is no edge of the
// comparison graph and the pair is not offered again (trumpets_taste_pairs.go).
//
// The daily runner lists uninvestigated skips (GET /machine/taste/skips, both
// listings' data plus the owner's reasons and notes), runs deterministic
// checks, optionally asks a model to explain, and posts one finding per skip
// (POST /machine/taste/skips/findings). The API applies only conservative,
// reversible actions, never touching price, title or status:
//
//   - exclude: a listing that is clearly bad (not a Bb trumpet, gone, a
//     duplicate) leaves Head to head, and for wrong-category or duplicate
//     listings Swipe too. A stored flag with a reason; the owner can switch it
//     off on the taste page, and an exclusion the owner switched off is never
//     re-created by a later skip.
//   - recheck: a listing that looks stale is made due for the next run.
//   - lesson: a genuinely incomparable pair records the measured gap on one
//     dimension (price ratio, condition classes, era classes). Pair selection
//     uses a dimension only once tasteLessonRecur live lessons agree; the
//     owner can switch lessons off.
//   - fix list: an unresolved data problem becomes a fixed-text fix-list
//     entry (its first line is one of tasteSkipFixLines).

var tasteSkipReasons = []string{"not_comparable", "bad_info", "stale", "duplicate", "not_bb", "other"}

var tasteSkipCauses = []string{"data_problem", "wrong_category", "stale", "duplicate", "incomparable", "unclear"}

// Fix-list first lines a finding may carry (fixReason groups on them).
var tasteSkipFixLines = []string{
	"Head to head skip: listing has no price",
	"Head to head skip: listing has no photos",
	"Head to head skip: listing not verified recently",
	"Head to head skip: listing details look wrong",
}

const (
	tasteLessonRecur         = 2   // live lessons that must agree before pair selection uses them
	tasteLessonMinPriceRatio = 1.5 // a smaller price gap is no lesson
	tasteSkipListLimit       = 25
	tasteSkipViewLimit       = 20
	// Notes saved for the same pair this long before (or after) a skip
	// belong to it.
	tasteSkipNoteBefore = 30 * time.Minute
	tasteSkipNoteAfter  = 10 * time.Minute
)

// tasteGuards is what skip investigations changed for pair selection and
// the swipe queue: excluded listings and recurring comparability lessons.
type tasteGuards struct {
	Excluded map[string]string // listing id -> "pair" or "all"
	Rules    []tasteLessonRule
}

func tasteExcludedEverywhere(id string, guards []tasteGuards) bool {
	for _, g := range guards {
		if g.Excluded[id] == "all" {
			return true
		}
	}
	return false
}

// tasteLessonRule is a recurring lesson in effect: avoid pairs at least
// Ratio apart in price (same currency), or with exactly these Classes.
type tasteLessonRule struct {
	Dimension string  `json:"dimension"`
	Ratio     float64 `json:"ratio,omitempty"`
	Classes   string  `json:"classes,omitempty"`
	Lessons   int     `json:"lessons"`
}

func (r tasteLessonRule) avoids(d *tasteData, a, b trumpetListing) bool {
	ratio, classes, ok := tasteGap(r.Dimension, a, b, tasteEra(d, a), tasteEra(d, b))
	if !ok {
		return false
	}
	if r.Dimension == "price" {
		return ratio >= r.Ratio
	}
	return classes == r.Classes
}

var (
	tasteConditionNew     = regexp.MustCompile(`\b(?:brand new|new old stock|nos|unplayed|mint|as new|like new|showroom)\b`)
	tasteConditionProject = regexp.MustCompile(`\b(?:project|needs work|needs repair|for parts|as[- ]is|restoration|damaged|heavy dents?|stuck valves?|non[- ]?playing)\b`)
	tasteConditionUsed    = regexp.MustCompile(`\b(?:excellent|very good|good|fair|used|played|pre-?owned|player'?s condition)\b`)
)

// tasteConditionClass is "new", "used", "project" or "" (not stated).
func tasteConditionClass(l trumpetListing) string {
	text := strings.ToLower(l.Details.Condition)
	if text == "" {
		text = strings.ToLower(l.Title)
	}
	switch {
	case tasteConditionProject.MatchString(text):
		return "project"
	case tasteConditionNew.MatchString(text):
		return "new"
	case tasteConditionUsed.MatchString(text):
		return "used"
	}
	return ""
}

// tasteEra is the listing's era feature ("vintage", "classic", "modern") or "".
func tasteEra(d *tasteData, l trumpetListing) string {
	fs, ok := []tasteFeature(nil), false
	if d != nil {
		fs, ok = d.features[l.ID]
	}
	if !ok {
		fs = tasteFeatures(l, tasteMarket{}, nil)
	}
	for _, f := range fs {
		if f.Kind == "era" {
			return strings.TrimPrefix(f.Key, "era:")
		}
	}
	return ""
}

// tasteGap measures how far apart two listings are on one dimension: the
// price ratio (>= 1, same currency only) or the sorted pair of differing
// classes. ok is false when the gap cannot be measured.
func tasteGap(dimension string, a, b trumpetListing, eraA, eraB string) (float64, string, bool) {
	classes := func(x, y string) (float64, string, bool) {
		if x == "" || y == "" || x == y {
			return 0, "", false
		}
		if x > y {
			x, y = y, x
		}
		return 0, x + "|" + y, true
	}
	switch dimension {
	case "price":
		if a.Price == nil || b.Price == nil || *a.Price <= 0 || *b.Price <= 0 || a.Currency == "" || a.Currency != b.Currency {
			return 0, "", false
		}
		hi, lo := math.Max(*a.Price, *b.Price), math.Min(*a.Price, *b.Price)
		return math.Round(100*hi/lo) / 100, "", true
	case "condition":
		return classes(tasteConditionClass(a), tasteConditionClass(b))
	case "era":
		return classes(eraA, eraB)
	}
	return 0, "", false
}

type tasteLessonRow struct {
	ID        int64     `json:"id"`
	EventID   int64     `json:"eventId"`
	Dimension string    `json:"dimension"`
	Ratio     *float64  `json:"ratio,omitempty"`
	Classes   string    `json:"classes,omitempty"`
	Active    bool      `json:"active"`
	Live      bool      `json:"live"`     // its skip still stands (not undone)
	InEffect  bool      `json:"inEffect"` // part of a rule pair selection uses
	CreatedAt time.Time `json:"createdAt"`
}

// tasteLessonRules: the lessons in effect. Only active lessons of live skips
// count; a dimension (for condition and era: a pair of classes) needs
// tasteLessonRecur of them. A price rule avoids gaps at least as large as the
// smallest agreeing lesson's. Marks the lessons that are in effect.
func tasteLessonRules(lessons []tasteLessonRow) []tasteLessonRule {
	out := []tasteLessonRule{}
	price := []int{}
	classes := map[string][]int{}
	order := []string{}
	for i, l := range lessons {
		if !l.Active || !l.Live {
			continue
		}
		switch {
		case l.Dimension == "price" && l.Ratio != nil && *l.Ratio >= tasteLessonMinPriceRatio:
			price = append(price, i)
		case (l.Dimension == "condition" || l.Dimension == "era") && l.Classes != "":
			k := l.Dimension + "/" + l.Classes
			if classes[k] == nil {
				order = append(order, k)
			}
			classes[k] = append(classes[k], i)
		}
	}
	if len(price) >= tasteLessonRecur {
		least := math.Inf(1)
		for _, i := range price {
			least = math.Min(least, *lessons[i].Ratio)
			lessons[i].InEffect = true
		}
		out = append(out, tasteLessonRule{Dimension: "price", Ratio: least, Lessons: len(price)})
	}
	for _, k := range order {
		if len(classes[k]) < tasteLessonRecur {
			continue
		}
		for _, i := range classes[k] {
			lessons[i].InEffect = true
		}
		parts := strings.SplitN(k, "/", 2)
		out = append(out, tasteLessonRule{Dimension: parts[0], Classes: parts[1], Lessons: len(classes[k])})
	}
	return out
}

// ---- Owner view ------------------------------------------------------------

type tasteSkipAction struct {
	Kind        string `json:"kind"` // exclude, recheck, lesson, fix, kept, note
	ListingID   string `json:"listingId,omitempty"`
	ExclusionID int64  `json:"exclusionId,omitempty"`
	LessonID    int64  `json:"lessonId,omitempty"`
	Text        string `json:"text"`
}

type tasteSkipCheck struct {
	Code   string `json:"code"`
	Side   string `json:"side,omitempty"`
	Detail string `json:"detail,omitempty"`
}

type tasteSkipFinding struct {
	ID          int64             `json:"id"`
	EventID     int64             `json:"eventId"`
	Cause       string            `json:"cause"`
	Dimension   string            `json:"dimension,omitempty"`
	Side        string            `json:"side,omitempty"`
	LLMCause    string            `json:"llmCause,omitempty"`
	LLMValid    bool              `json:"llmValid"`
	Mode        string            `json:"mode,omitempty"`
	Explanation string            `json:"explanation"`
	Checks      []tasteSkipCheck  `json:"checks"`
	Actions     []tasteSkipAction `json:"actions"`
	FixText     string            `json:"fixText,omitempty"`
	CreatedAt   time.Time         `json:"createdAt"`
}

type tasteExclusion struct {
	ID        int64     `json:"id"`
	ListingID string    `json:"listingId"`
	FindingID *int64    `json:"findingId,omitempty"`
	Scope     string    `json:"scope"`
	Reason    string    `json:"reason"`
	Detail    string    `json:"detail,omitempty"`
	Active    bool      `json:"active"`
	ChangedBy string    `json:"changedBy"`
	CreatedAt time.Time `json:"createdAt"`
	ChangedAt time.Time `json:"changedAt"`
}

type tasteSkipItem struct {
	EventID   int64             `json:"eventId"`
	CreatedAt time.Time         `json:"createdAt"`
	A         string            `json:"a"`
	B         string            `json:"b"`
	Reasons   []string          `json:"reasons"`
	Note      string            `json:"note,omitempty"`
	Finding   *tasteSkipFinding `json:"finding"`
}

type tasteSkipView struct {
	Items      []tasteSkipItem   `json:"items"`
	Total      int               `json:"total"`   // live skips
	Pending    int               `json:"pending"` // live skips not yet investigated
	Exclusions []tasteExclusion  `json:"exclusions"`
	Lessons    []tasteLessonRow  `json:"lessons"`
	Rules      []tasteLessonRule `json:"rules"`
}

const tasteFindingColumns = `id,event_id,cause,dimension,side,llm_cause,llm_valid,mode,explanation,checks,actions,fix_text,created_at`

func scanTasteFinding(row pgx.Row) (tasteSkipFinding, error) {
	var f tasteSkipFinding
	var checks, actions []byte
	err := row.Scan(&f.ID, &f.EventID, &f.Cause, &f.Dimension, &f.Side, &f.LLMCause, &f.LLMValid, &f.Mode, &f.Explanation, &checks, &actions, &f.FixText, &f.CreatedAt)
	if err == nil {
		f.Checks, f.Actions = []tasteSkipCheck{}, []tasteSkipAction{}
		_ = json.Unmarshal(checks, &f.Checks)
		_ = json.Unmarshal(actions, &f.Actions)
	}
	return f, err
}

func (app *application) loadTasteExclusions(ctx context.Context, user uuid.UUID) ([]tasteExclusion, error) {
	return wbCollect(wbRows(app.db.Query(ctx, `SELECT id,listing_id::text,finding_id,scope,reason,detail,active,changed_by,created_at,changed_at
		FROM trumpet_taste_exclusions WHERE user_id=$1 ORDER BY id`, user)), func(row pgx.Row) (tasteExclusion, error) {
		var x tasteExclusion
		return x, row.Scan(&x.ID, &x.ListingID, &x.FindingID, &x.Scope, &x.Reason, &x.Detail, &x.Active, &x.ChangedBy, &x.CreatedAt, &x.ChangedAt)
	})
}

func (app *application) loadTasteLessons(ctx context.Context, user uuid.UUID, live map[int64]bool) ([]tasteLessonRow, error) {
	rows, err := wbCollect(wbRows(app.db.Query(ctx, `SELECT l.id,f.event_id,l.dimension,l.ratio,l.classes,l.active,l.created_at
		FROM trumpet_taste_lessons l JOIN trumpet_taste_skip_findings f ON f.id=l.finding_id WHERE l.user_id=$1 ORDER BY l.id`, user)), func(row pgx.Row) (tasteLessonRow, error) {
		var l tasteLessonRow
		return l, row.Scan(&l.ID, &l.EventID, &l.Dimension, &l.Ratio, &l.Classes, &l.Active, &l.CreatedAt)
	})
	for i := range rows {
		rows[i].Live = live[rows[i].EventID]
	}
	return rows, err
}

// tasteLiveSkips: skip events that were not undone, oldest first.
func tasteLiveSkips(events []tasteEvent) []tasteEvent {
	out := []tasteEvent{}
	for _, e := range events {
		if e.Kind == "pair" && e.Action == "skip" && !e.undone {
			out = append(out, e)
		}
	}
	return out
}

// loadTasteSkipState reads exclusions, lessons and findings: the guards pair
// selection uses and the "Skipped pairs" view the page shows.
func (app *application) loadTasteSkipState(ctx context.Context, user uuid.UUID, d *tasteData, events []tasteEvent) (tasteGuards, tasteSkipView, error) {
	guards := tasteGuards{Excluded: map[string]string{}}
	view := tasteSkipView{Items: []tasteSkipItem{}}
	skips := tasteLiveSkips(events)
	live := map[int64]bool{}
	for _, e := range skips {
		live[e.ID] = true
	}
	var err error
	if view.Exclusions, err = app.loadTasteExclusions(ctx, user); err != nil {
		return guards, view, err
	}
	for _, x := range view.Exclusions {
		if x.Active && guards.Excluded[x.ListingID] != "all" {
			guards.Excluded[x.ListingID] = x.Scope
		}
	}
	if view.Lessons, err = app.loadTasteLessons(ctx, user, live); err != nil {
		return guards, view, err
	}
	guards.Rules = tasteLessonRules(view.Lessons)
	view.Rules = guards.Rules
	findings, err := wbCollect(wbRows(app.db.Query(ctx, `SELECT `+tasteFindingColumns+` FROM trumpet_taste_skip_findings WHERE user_id=$1 ORDER BY id DESC LIMIT 500`, user)), scanTasteFinding)
	if err != nil {
		return guards, view, err
	}
	byEvent := map[int64]*tasteSkipFinding{}
	for i := range findings {
		byEvent[findings[i].EventID] = &findings[i]
	}
	view.Total = len(skips)
	for i := len(skips) - 1; i >= 0; i-- {
		e := skips[i]
		f := byEvent[e.ID]
		if f == nil {
			view.Pending++
		}
		if len(view.Items) < tasteSkipViewLimit {
			reasons := e.Meta.SkipReasons
			if reasons == nil {
				reasons = []string{}
			}
			view.Items = append(view.Items, tasteSkipItem{EventID: e.ID, CreatedAt: e.CreatedAt, A: e.ListingID, B: e.OtherListingID, Reasons: reasons, Note: e.Note, Finding: f})
		}
	}
	return guards, view, nil
}

// ---- Bounding (the runner's #94 rules, on the API side) -------------------

// tasteSkipClean drops invalid UTF-8, NUL and other control characters (a
// newline is kept only when multiline), trims, and cuts to at most limit
// bytes on a character boundary.
func tasteSkipClean(s string, limit int, multiline bool) string {
	s = strings.ToValidUTF8(s, "")
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '\n' && multiline:
			return r
		case r == '\n' || r == '\t' || r == '\r':
			return ' '
		case unicode.IsControl(r) || r == utf8.RuneError:
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if len(s) > limit {
		cut := limit
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		s = strings.TrimSpace(s[:cut])
	}
	return s
}

// ---- Machine API -----------------------------------------------------------

type tasteSkipListing struct {
	ID                string     `json:"id"`
	HornID            string     `json:"hornId"`
	Title             string     `json:"title"`
	Maker             string     `json:"maker"`
	Model             string     `json:"model"`
	Price             *float64   `json:"price"`
	Currency          string     `json:"currency"`
	Status            string     `json:"status"`
	VerificationState string     `json:"verificationState"`
	VerifiedVia       string     `json:"verifiedVia,omitempty"`
	FirstSeen         time.Time  `json:"firstSeen"`
	LastChecked       *time.Time `json:"lastChecked"`
	LastCheckAttempt  *time.Time `json:"lastCheckAttempt"`
	LastCheckError    string     `json:"lastCheckError,omitempty"`
	CheckFailures     int        `json:"checkFailures"`
	Images            int        `json:"images"`
	URL               string     `json:"url"`
	Source            string     `json:"source"`
	Seller            string     `json:"seller"`
	Condition         string     `json:"condition,omitempty"`
	ConditionClass    string     `json:"conditionClass,omitempty"`
	Era               string     `json:"era,omitempty"`
	Year              *int       `json:"year,omitempty"`
	Description       string     `json:"description"`
	Acquired          bool       `json:"acquired"`
}

func newTasteSkipListing(d *tasteData, l trumpetListing) tasteSkipListing {
	return tasteSkipListing{ID: l.ID, HornID: l.HornID, Title: l.Title, Maker: l.Maker, Model: l.Model, Price: l.Price, Currency: l.Currency, Status: l.Status,
		VerificationState: l.VerificationState, VerifiedVia: l.VerifiedVia, FirstSeen: l.FirstSeen, LastChecked: l.LastChecked, LastCheckAttempt: l.LastCheckAttempt,
		LastCheckError: tasteSkipClean(l.LastCheckError, 300, false), CheckFailures: l.CheckFailures, Images: len(l.Images), URL: l.URL, Source: l.Source, Seller: l.Seller,
		Condition: tasteSkipClean(l.Details.Condition, 200, false), ConditionClass: tasteConditionClass(l), Era: tasteEra(d, l), Year: l.Details.Year,
		Description: tasteSkipClean(l.Description, 800, false), Acquired: l.Acquired}
}

type tasteSkipNote struct {
	Note     string `json:"note,omitempty"`
	HasAudio bool   `json:"hasAudio"`
}

type tasteSkipForRunner struct {
	EventID   int64            `json:"eventId"`
	CreatedAt time.Time        `json:"createdAt"`
	Reasons   []string         `json:"reasons"`
	Note      string           `json:"note,omitempty"`
	Notes     []tasteSkipNote  `json:"notes"` // notes saved for the same pair around the skip
	A         tasteSkipListing `json:"a"`
	B         tasteSkipListing `json:"b"`
}

// machineTasteSkips lists live skips that have no finding yet (oldest first,
// at most tasteSkipListLimit) with both listings' data.
func (app *application) machineTasteSkips(w http.ResponseWriter, r *http.Request) {
	user, err := app.userID(r.Context())
	if err != nil {
		app.serverError(w, err)
		return
	}
	ctx := r.Context()
	events, err := app.loadTasteEvents(ctx, user)
	if err != nil {
		app.serverError(w, err)
		return
	}
	listings, err := app.loadTrumpetsForTaste(ctx, user)
	if err != nil {
		app.serverError(w, err)
		return
	}
	done, err := wbCollect(wbRows(app.db.Query(ctx, `SELECT event_id FROM trumpet_taste_skip_findings WHERE user_id=$1`, user)), func(row pgx.Row) (int64, error) {
		var id int64
		return id, row.Scan(&id)
	})
	if err != nil {
		app.serverError(w, err)
		return
	}
	investigated := map[int64]bool{}
	for _, id := range done {
		investigated[id] = true
	}
	d := newTasteData(listings, nil)
	pending := 0
	out := []tasteSkipForRunner{}
	for _, e := range tasteLiveSkips(events) {
		a, okA := d.byListing[e.ListingID]
		b, okB := d.byListing[e.OtherListingID]
		if investigated[e.ID] || !okA || !okB {
			continue
		}
		pending++
		if len(out) == tasteSkipListLimit {
			continue
		}
		reasons := e.Meta.SkipReasons
		if reasons == nil {
			reasons = []string{}
		}
		item := tasteSkipForRunner{EventID: e.ID, CreatedAt: e.CreatedAt, Reasons: reasons, Note: tasteSkipClean(e.Note, 1000, false), Notes: []tasteSkipNote{}, A: newTasteSkipListing(d, a), B: newTasteSkipListing(d, b)}
		for _, n := range events {
			samePair := (n.ListingID == e.ListingID && n.OtherListingID == e.OtherListingID) || (n.ListingID == e.OtherListingID && n.OtherListingID == e.ListingID)
			if n.Kind == "note" && !n.undone && samePair && !n.CreatedAt.Before(e.CreatedAt.Add(-tasteSkipNoteBefore)) && !n.CreatedAt.After(e.CreatedAt.Add(tasteSkipNoteAfter)) && len(item.Notes) < 5 {
				item.Notes = append(item.Notes, tasteSkipNote{Note: tasteSkipClean(n.Note, 1000, false), HasAudio: n.HasAudio})
			}
		}
		out = append(out, item)
	}
	writeJSON(w, 200, map[string]any{"skips": out, "pending": pending, "limit": tasteSkipListLimit, "reasons": tasteSkipReasons, "causes": tasteSkipCauses, "fixLines": tasteSkipFixLines})
}

type tasteSkipExcludeInput struct {
	ListingID string `json:"listingId"`
	Scope     string `json:"scope"`
	Reason    string `json:"reason"`
	Detail    string `json:"detail"`
}

type tasteSkipFindingInput struct {
	EventID     int64                   `json:"eventId"`
	Cause       string                  `json:"cause"`
	Dimension   string                  `json:"dimension"`
	Side        string                  `json:"side"`
	LLMCause    string                  `json:"llmCause"`
	LLMValid    bool                    `json:"llmValid"`
	Mode        string                  `json:"mode"`
	Explanation string                  `json:"explanation"`
	Checks      []tasteSkipCheck        `json:"checks"`
	Exclude     []tasteSkipExcludeInput `json:"exclude"`
	Recheck     []string                `json:"recheck"`
	Lesson      bool                    `json:"lesson"`
	FixText     string                  `json:"fixText"`
}

var tasteSkipCodePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}$`)

// validateTasteSkipFinding checks enums and sizes and bounds every string
// (NUL and control characters stripped, byte limits).
func validateTasteSkipFinding(in *tasteSkipFindingInput) error {
	if in.EventID <= 0 {
		return errors.New("eventId required")
	}
	if !containsString(tasteSkipCauses, in.Cause) {
		return errors.New("unknown cause")
	}
	if in.LLMCause != "" && !containsString(tasteSkipCauses, in.LLMCause) {
		return errors.New("unknown llmCause")
	}
	if !containsString([]string{"", "price", "condition", "era", "other"}, in.Dimension) {
		return errors.New("dimension must be price, condition, era or other")
	}
	if !containsString([]string{"", "a", "b", "both"}, in.Side) {
		return errors.New("side must be a, b or both")
	}
	if len(in.Checks) > 20 || len(in.Exclude) > 2 || len(in.Recheck) > 2 {
		return errors.New("finding too large")
	}
	in.Mode = tasteSkipClean(in.Mode, 20, false)
	in.Explanation = tasteSkipClean(in.Explanation, 1000, false)
	for i := range in.Checks {
		c := &in.Checks[i]
		if !tasteSkipCodePattern.MatchString(c.Code) || !containsString([]string{"", "a", "b", "both"}, c.Side) {
			return errors.New("invalid check")
		}
		c.Detail = tasteSkipClean(c.Detail, 200, false)
	}
	for i := range in.Exclude {
		x := &in.Exclude[i]
		if !containsString([]string{"pair", "all"}, x.Scope) || !containsString([]string{"wrong_category", "stale", "duplicate"}, x.Reason) {
			return errors.New("invalid exclusion")
		}
		x.Detail = tasteSkipClean(x.Detail, 300, false)
	}
	fix := tasteSkipClean(in.FixText, 500, true)
	if fix != "" && !containsString(tasteSkipFixLines, strings.TrimSpace(strings.SplitN(fix, "\n", 2)[0])) {
		fix = "" // only fixed-text entries reach the fix list
	}
	if parts := strings.SplitN(fix, "\n", 2); len(parts) == 2 {
		fix = parts[0] + "\n" + strings.ReplaceAll(parts[1], "\n", " ")
	}
	in.FixText = fix
	return nil
}

var errTasteSkipGone = errors.New("skip not found or undone")

// saveTasteSkipFinding stores one finding and applies its actions in one
// transaction. replayed: the skip already had a finding (returned as is).
func (app *application) saveTasteSkipFinding(ctx context.Context, user uuid.UUID, in tasteSkipFindingInput) (tasteSkipFinding, bool, error) {
	tx, err := app.db.Begin(ctx)
	if err != nil {
		return tasteSkipFinding{}, false, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	if err = lockTrumpetOwner(ctx, tx, user); err != nil {
		return tasteSkipFinding{}, false, err
	}
	var a, b string
	err = tx.QueryRow(ctx, `SELECT listing_id::text,other_listing_id::text FROM trumpet_taste_events e WHERE e.id=$1 AND e.user_id=$2 AND e.kind='pair' AND e.action='skip'
		AND NOT EXISTS(SELECT 1 FROM trumpet_taste_events u WHERE u.undoes=e.id)`, in.EventID, user).Scan(&a, &b)
	if errors.Is(err, pgx.ErrNoRows) {
		return tasteSkipFinding{}, false, errTasteSkipGone
	}
	if err != nil {
		return tasteSkipFinding{}, false, err
	}
	if existing, err := scanTasteFinding(tx.QueryRow(ctx, `SELECT `+tasteFindingColumns+` FROM trumpet_taste_skip_findings WHERE event_id=$1 AND user_id=$2`, in.EventID, user)); err == nil {
		return existing, true, nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return tasteSkipFinding{}, false, err
	}
	checks, _ := json.Marshal(in.Checks)
	if in.Checks == nil {
		checks = []byte("[]")
	}
	var findingID int64
	if err = tx.QueryRow(ctx, `INSERT INTO trumpet_taste_skip_findings(user_id,event_id,cause,dimension,side,llm_cause,llm_valid,mode,explanation,checks,fix_text)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) RETURNING id`, user, in.EventID, in.Cause, in.Dimension, in.Side, in.LLMCause, in.LLMValid, in.Mode, in.Explanation, checks, in.FixText).Scan(&findingID); err != nil {
		return tasteSkipFinding{}, false, err
	}
	actions := []tasteSkipAction{}
	inPair := func(id string) bool { return id != "" && (id == a || id == b) }
	// Exclusions: only for a clearly bad listing of this pair, when the
	// finding's cause is that very reason.
	for _, x := range in.Exclude {
		if !inPair(x.ListingID) || x.Reason != in.Cause {
			actions = append(actions, tasteSkipAction{Kind: "note", ListingID: x.ListingID, Text: "exclusion not applied (it must name a listing of this pair and match the cause)"})
			continue
		}
		var restored, already bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM trumpet_taste_exclusions WHERE user_id=$1 AND listing_id=$2 AND changed_by='owner' AND NOT active),
			EXISTS(SELECT 1 FROM trumpet_taste_exclusions WHERE user_id=$1 AND listing_id=$2 AND active AND (scope='all' OR scope=$3))`, user, x.ListingID, x.Scope).Scan(&restored, &already); err != nil {
			return tasteSkipFinding{}, false, err
		}
		switch {
		case restored:
			actions = append(actions, tasteSkipAction{Kind: "kept", ListingID: x.ListingID, Text: "You put this listing back earlier, so it was not excluded again"})
		case already:
			actions = append(actions, tasteSkipAction{Kind: "kept", ListingID: x.ListingID, Text: "Already excluded"})
		default:
			var id int64
			if err = tx.QueryRow(ctx, `INSERT INTO trumpet_taste_exclusions(user_id,listing_id,finding_id,scope,reason,detail) VALUES($1,$2,$3,$4,$5,$6) RETURNING id`,
				user, x.ListingID, findingID, x.Scope, x.Reason, x.Detail).Scan(&id); err != nil {
				return tasteSkipFinding{}, false, err
			}
			where := "Head to head"
			if x.Scope == "all" {
				where = "Head to head and Swipe"
			}
			actions = append(actions, tasteSkipAction{Kind: "exclude", ListingID: x.ListingID, ExclusionID: id, Text: "Excluded from " + where + " (" + strings.ReplaceAll(x.Reason, "_", " ") + ")"})
		}
	}
	for _, id := range in.Recheck {
		if !inPair(id) {
			continue
		}
		if _, err = tx.Exec(ctx, `UPDATE trumpet_listings SET recheck_requested_at=now() WHERE id=$1 AND user_id=$2`, id, user); err != nil {
			return tasteSkipFinding{}, false, err
		}
		actions = append(actions, tasteSkipAction{Kind: "recheck", ListingID: id, Text: "Recheck queued for the next run"})
	}
	if in.Lesson && in.Cause == "incomparable" && containsString([]string{"price", "condition", "era"}, in.Dimension) {
		// The gap is measured here from the stored listings, never taken
		// from the runner or a model.
		listings, err := app.loadTrumpetsForTaste(ctx, user)
		if err != nil {
			return tasteSkipFinding{}, false, err
		}
		var la, lb *trumpetListing
		for i := range listings {
			switch listings[i].ID {
			case a:
				la = &listings[i]
			case b:
				lb = &listings[i]
			}
		}
		ratio, classes, ok := 0.0, "", false
		if la != nil && lb != nil {
			ratio, classes, ok = tasteGap(in.Dimension, *la, *lb, tasteEra(nil, *la), tasteEra(nil, *lb))
		}
		if ok && in.Dimension == "price" && ratio < tasteLessonMinPriceRatio {
			ok = false
		}
		if ok {
			var ratioArg any
			text := ""
			if in.Dimension == "price" {
				ratioArg = ratio
				text = "Lesson recorded: prices " + strconv.FormatFloat(ratio, 'f', -1, 64) + "× apart"
			} else {
				text = "Lesson recorded: " + in.Dimension + " " + strings.ReplaceAll(classes, "|", " vs ")
			}
			var id int64
			if err = tx.QueryRow(ctx, `INSERT INTO trumpet_taste_lessons(user_id,finding_id,dimension,ratio,classes) VALUES($1,$2,$3,$4,$5) RETURNING id`, user, findingID, in.Dimension, ratioArg, classes).Scan(&id); err != nil {
				return tasteSkipFinding{}, false, err
			}
			actions = append(actions, tasteSkipAction{Kind: "lesson", LessonID: id, Text: text + " (used once it recurs)"})
		} else {
			actions = append(actions, tasteSkipAction{Kind: "note", Text: "No lesson: the " + in.Dimension + " gap could not be measured from the listings"})
		}
	}
	if in.FixText != "" {
		actions = append(actions, tasteSkipAction{Kind: "fix", Text: "Added to the daily fix list"})
	}
	raw, _ := json.Marshal(actions)
	if _, err = tx.Exec(ctx, `UPDATE trumpet_taste_skip_findings SET actions=$1 WHERE id=$2`, raw, findingID); err != nil {
		return tasteSkipFinding{}, false, err
	}
	f, err := scanTasteFinding(tx.QueryRow(ctx, `SELECT `+tasteFindingColumns+` FROM trumpet_taste_skip_findings WHERE id=$1`, findingID))
	if err != nil {
		return f, false, err
	}
	return f, false, tx.Commit(ctx)
}

func (app *application) machineTasteSkipFinding(w http.ResponseWriter, r *http.Request) {
	var in tasteSkipFindingInput
	if err := decodeTrumpetJSON(w, r, &in); err != nil {
		writeError(w, 400, "invalid finding JSON")
		return
	}
	if err := validateTasteSkipFinding(&in); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	user, err := app.userID(r.Context())
	if err != nil {
		app.serverError(w, err)
		return
	}
	f, replayed, err := app.saveTasteSkipFinding(r.Context(), user, in)
	switch {
	case errors.Is(err, errTasteSkipGone):
		writeError(w, 404, err.Error())
		return
	case err != nil:
		app.serverError(w, err)
		return
	}
	code := 201
	if replayed {
		code = 200
	}
	writeJSON(w, code, map[string]any{"finding": f, "replayed": replayed})
}

// ---- Owner toggles -----------------------------------------------------------

// putTasteGuard switches an exclusion or a lesson on or off ({"active":
// bool}) and answers with the refreshed taste state.
func (app *application) putTasteGuard(table string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Active *bool `json:"active"`
		}
		if err := decodeTrumpetJSON(w, r, &in); err != nil || in.Active == nil {
			writeError(w, 400, `body must be {"active": true|false}`)
			return
		}
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || id <= 0 {
			writeError(w, 400, "invalid id")
			return
		}
		user, err := app.userID(r.Context())
		if err != nil {
			app.serverError(w, err)
			return
		}
		query := `UPDATE trumpet_taste_lessons SET active=$1,changed_at=now() WHERE id=$2 AND user_id=$3`
		if table == "exclusions" {
			query = `UPDATE trumpet_taste_exclusions SET active=$1,changed_at=now(),changed_by='owner' WHERE id=$2 AND user_id=$3`
		}
		tag, err := app.db.Exec(r.Context(), query, *in.Active, id, user)
		if err != nil {
			app.serverError(w, err)
			return
		}
		if tag.RowsAffected() == 0 {
			writeError(w, 404, "not found")
			return
		}
		out, err := app.tasteResponse(context.WithoutCancel(r.Context()), user)
		if err != nil {
			app.serverError(w, err)
			return
		}
		writeJSON(w, 200, out)
	}
}

// tasteRecheckDue: a recheck a skip investigation asked for that no check
// has answered yet. A request lapses after tasteRecheckLapse, so a page that
// can no longer be checked is not due forever.
const tasteRecheckLapse = 7 * 24 * time.Hour

func tasteRecheckDue(l trumpetListing) bool {
	return l.RecheckRequestedAt != nil && !l.Acquired && time.Since(*l.RecheckRequestedAt) < tasteRecheckLapse &&
		(l.LastChecked == nil || l.LastChecked.Before(*l.RecheckRequestedAt))
}
