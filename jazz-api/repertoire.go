package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

var (
	tuneIDPattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,47}$`)
	tuneKeyPattern = regexp.MustCompile(`^[A-G](b|#)?m?$`)
	errUnknownTune = errors.New("unknown tune")
)

var repertoireCategoryOrder = map[string]int{"ballad": 0, "upbeat": 1, "pop": 2, "standard": 3}

// Milestone JSON keys and their columns, in display order. keysKnown is
// derived from the key list and therefore not listed here.
var tuneMilestoneColumns = []struct{ Key, Column string }{
	{"melodyByEar", "melody_by_ear"},
	{"lyrics", "lyrics"},
	{"changes", "changes"},
	{"transcription", "transcription"},
	{"improvise", "improvise"},
	{"gigReady", "gig_ready"},
}

type repertoireTune struct {
	TuneID          string            `json:"tuneId"`
	Title           string            `json:"title"`
	Category        string            `json:"category"`
	Chosen          bool              `json:"chosen"`
	Position        int               `json:"position"`
	ReferenceArtist string            `json:"referenceArtist,omitempty"`
	ReferenceTitle  string            `json:"referenceTitle,omitempty"`
	ReferenceURL    string            `json:"referenceUrl,omitempty"`
	ConcertKey      string            `json:"concertKey,omitempty"`
	KeysKnown       []string          `json:"keysKnown"`
	Milestones      map[string]string `json:"milestones"`
	Notes           string            `json:"notes"`
	Revision        int64             `json:"revision"`
	ArchivedAt      *time.Time        `json:"archivedAt,omitempty"`
	// Derived from milestones and linked practice; never stored.
	DeeplyLearned     bool   `json:"deeplyLearned"`
	PracticeStatus    string `json:"practiceStatus"`
	LastPracticedDate string `json:"lastPracticedDate,omitempty"`
	TotalPracticeMS   int64  `json:"totalPracticeMs"`
	WeekPracticeMS    int64  `json:"weekPracticeMs"`
	SessionCount      int    `json:"sessionCount"`
	TakeCount         int    `json:"takeCount"`
}

// tunePractice is the practice history derived for one tune_id.
type tunePractice struct {
	TuneID            string `json:"tuneId"`
	LastPracticedDate string `json:"lastPracticedDate,omitempty"`
	TotalPracticeMS   int64  `json:"totalPracticeMs"`
	WeekPracticeMS    int64  `json:"weekPracticeMs"`
	SessionCount      int    `json:"sessionCount"`
	TakeCount         int    `json:"takeCount"`
}

func validMilestoneStatus(value string) bool {
	return value == "not_started" || value == "learning" || value == "solid"
}

func keysKnownStatus(keys []string) string {
	switch {
	case len(keys) >= 2:
		return "solid"
	case len(keys) == 1:
		return "learning"
	}
	return "not_started"
}

func (t *repertoireTune) applyPractice(p tunePractice) {
	t.LastPracticedDate = p.LastPracticedDate
	t.TotalPracticeMS = p.TotalPracticeMS
	t.WeekPracticeMS = p.WeekPracticeMS
	t.SessionCount = p.SessionCount
	t.TakeCount = p.TakeCount
}

// deriveTuneState fills the derived fields. Practice only ever promotes a tune
// to learning; solid milestones and gig readiness are always set by hand.
func deriveTuneState(t *repertoireTune) {
	if t.KeysKnown == nil {
		t.KeysKnown = []string{}
	}
	if t.Milestones == nil {
		t.Milestones = map[string]string{}
	}
	t.Milestones["keysKnown"] = keysKnownStatus(t.KeysKnown)
	m := t.Milestones
	t.DeeplyLearned = m["melodyByEar"] == "solid" && len(t.KeysKnown) >= 2 && m["lyrics"] == "solid" && m["transcription"] == "solid"
	practiced := t.TotalPracticeMS > 0 || t.TakeCount > 0 || t.SessionCount > 0
	started := practiced
	for _, milestone := range tuneMilestoneColumns {
		if m[milestone.Key] != "not_started" {
			started = true
		}
	}
	if len(t.KeysKnown) > 0 {
		started = true
	}
	switch {
	case m["gigReady"] == "solid":
		t.PracticeStatus = "gig_ready"
	case started:
		t.PracticeStatus = "learning"
	default:
		t.PracticeStatus = "not_started"
	}
}

var diacriticFolder = transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)

// slugifyTuneTitle turns a title into a stable tune slug. Apostrophes vanish
// ('Round Midnight -> round-midnight); other separators collapse to one dash.
// It can return "" for titles without any ASCII letters or digits.
func slugifyTuneTitle(title string) string {
	folded, _, err := transform.String(diacriticFolder, title)
	if err != nil {
		folded = title
	}
	var slug strings.Builder
	dash := true
	for _, character := range strings.ToLower(folded) {
		switch {
		case character == '\'' || character == '’' || character == '‘' || character == 'ʼ':
			continue
		case (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9'):
			slug.WriteRune(character)
			dash = false
		case !dash:
			slug.WriteByte('-')
			dash = true
		}
	}
	result := strings.Trim(slug.String(), "-")
	if len(result) > 48 {
		result = strings.TrimRight(result[:48], "-")
	}
	return result
}

func randomTuneSuffix() string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	buffer := make([]byte, 6)
	_, _ = rand.Read(buffer)
	for index := range buffer {
		buffer[index] = alphabet[int(buffer[index])%len(alphabet)]
	}
	return string(buffer)
}

// normalizeTuneKey canonicalizes a concert key such as "bb", "F#M" or "E♭ minor".
func normalizeTuneKey(raw string) (string, bool) {
	value := strings.TrimSpace(strings.NewReplacer("♭", "b", "♯", "#").Replace(raw))
	if value == "" {
		return "", false
	}
	letter := strings.ToUpper(value[:1])
	rest := strings.ToLower(strings.TrimSpace(value[1:]))
	accidental := ""
	if strings.HasPrefix(rest, "b") || strings.HasPrefix(rest, "#") {
		accidental, rest = rest[:1], strings.TrimSpace(rest[1:])
	}
	switch rest {
	case "", "maj", "major":
		rest = ""
	case "m", "min", "minor", "-":
		rest = "m"
	}
	key := letter + accidental + rest
	return key, tuneKeyPattern.MatchString(key)
}

// tuneKeyIdentity is equal for enharmonic spellings of the same key (A# and Bb).
func tuneKeyIdentity(key string) int {
	pitch := map[byte]int{'C': 0, 'D': 2, 'E': 4, 'F': 5, 'G': 7, 'A': 9, 'B': 11}[key[0]]
	minor := 0
	for _, character := range key[1:] {
		switch character {
		case 'b':
			pitch--
		case '#':
			pitch++
		case 'm':
			minor = 1
		}
	}
	return ((pitch+12)%12)*2 + minor
}

// normalizeTuneKeys validates and deduplicates a key list (first spelling wins).
func normalizeTuneKeys(input []string) ([]string, error) {
	seen := map[int]bool{}
	result := []string{}
	for _, raw := range input {
		key, ok := normalizeTuneKey(raw)
		if !ok {
			return nil, errors.New("keys must look like C, Bb, F# or Gm")
		}
		identity := tuneKeyIdentity(key)
		if seen[identity] {
			continue
		}
		seen[identity] = true
		result = append(result, key)
	}
	if len(result) > 12 {
		return nil, errors.New("at most 12 keys are allowed")
	}
	return result, nil
}

type tuneEvent struct {
	Type    string         `json:"eventType"`
	Payload map[string]any `json:"payload"`
}

// diffTune returns one history event per changed field.
func diffTune(before, after repertoireTune) []tuneEvent {
	events := []tuneEvent{}
	field := func(name string, from, to any) {
		if from != to {
			events = append(events, tuneEvent{"tune.updated", map[string]any{"field": name, "from": from, "to": to}})
		}
	}
	field("title", before.Title, after.Title)
	field("category", before.Category, after.Category)
	field("chosen", before.Chosen, after.Chosen)
	field("position", before.Position, after.Position)
	field("referenceArtist", before.ReferenceArtist, after.ReferenceArtist)
	field("referenceTitle", before.ReferenceTitle, after.ReferenceTitle)
	field("referenceUrl", before.ReferenceURL, after.ReferenceURL)
	field("concertKey", before.ConcertKey, after.ConcertKey)
	if before.Notes != after.Notes {
		// Notes can be long and autosave often; record that they changed, not every draft.
		events = append(events, tuneEvent{"tune.updated", map[string]any{"field": "notes", "fromLength": utf8.RuneCountInString(before.Notes), "toLength": utf8.RuneCountInString(after.Notes)}})
	}
	for _, milestone := range tuneMilestoneColumns {
		from, to := before.Milestones[milestone.Key], after.Milestones[milestone.Key]
		if from != to {
			events = append(events, tuneEvent{"milestone.changed", map[string]any{"field": milestone.Key, "from": from, "to": to}})
		}
	}
	had := map[string]bool{}
	for _, key := range before.KeysKnown {
		had[key] = true
	}
	has := map[string]bool{}
	for _, key := range after.KeysKnown {
		has[key] = true
		if !had[key] {
			events = append(events, tuneEvent{"key.added", map[string]any{"key": key}})
		}
	}
	for _, key := range before.KeysKnown {
		if !has[key] {
			events = append(events, tuneEvent{"key.removed", map[string]any{"key": key}})
		}
	}
	if (before.ArchivedAt == nil) != (after.ArchivedAt == nil) {
		if after.ArchivedAt != nil {
			events = append(events, tuneEvent{"tune.archived", map[string]any{}})
		} else {
			events = append(events, tuneEvent{"tune.restored", map[string]any{}})
		}
	}
	return events
}

type dbQuerier interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

const tuneColumns = `tune_id,title,category,chosen,position,COALESCE(reference_artist,''),COALESCE(reference_title,''),COALESCE(reference_url,''),
	COALESCE(concert_key,''),keys_known,melody_by_ear,lyrics,changes,transcription,improvise,gig_ready,COALESCE(notes,''),revision,archived_at`

func scanTune(row pgx.Row) (repertoireTune, error) {
	var t repertoireTune
	statuses := make([]string, len(tuneMilestoneColumns))
	if err := row.Scan(&t.TuneID, &t.Title, &t.Category, &t.Chosen, &t.Position, &t.ReferenceArtist, &t.ReferenceTitle, &t.ReferenceURL,
		&t.ConcertKey, &t.KeysKnown, &statuses[0], &statuses[1], &statuses[2], &statuses[3], &statuses[4], &statuses[5],
		&t.Notes, &t.Revision, &t.ArchivedAt); err != nil {
		return t, err
	}
	t.Milestones = map[string]string{}
	for index, milestone := range tuneMilestoneColumns {
		t.Milestones[milestone.Key] = statuses[index]
	}
	deriveTuneState(&t)
	return t, nil
}

// loadTunePractice derives practice history per tune_id from linked blocks
// (reconciled with their takes, like the archive), tagged takes outside any
// block, and the take count. Nothing is copied into repertoire_tunes.
func loadTunePractice(ctx context.Context, db dbQuerier, userID uuid.UUID, weekStart, onlyTune string) (map[string]tunePractice, error) {
	rows, err := db.Query(ctx, `
		WITH linked AS (
			SELECT pb.tune_id, pb.practice_date AS day,
			       LEAST(GREATEST(pb.elapsed_ms::bigint, COALESCE((
			           SELECT SUM(r.duration_ms) FROM recordings r
			           WHERE r.practice_block_id=pb.id AND r.user_id=pb.user_id AND r.status IN ('uploading','ready')),0)), $3)::bigint AS ms
			FROM practice_blocks pb
			WHERE pb.user_id=$1 AND pb.tune_id IS NOT NULL AND ($4='' OR pb.tune_id=$4)
		),
		loose AS (
			SELECT r.tune_id, timezone('America/Los_Angeles', r.recorded_at)::date AS day, COALESCE(r.duration_ms,0)::bigint AS ms
			FROM recordings r
			WHERE r.user_id=$1 AND r.practice_block_id IS NULL AND COALESCE(r.tune_id,'')<>'' AND r.status IN ('uploading','ready')
			  AND ($4='' OR r.tune_id=$4)
		),
		takes AS (
			SELECT COALESCE(NULLIF(r.tune_id,''), pb.tune_id) AS tune_id,
			       COALESCE(pb.practice_date, timezone('America/Los_Angeles', r.recorded_at)::date) AS day
			FROM recordings r LEFT JOIN practice_blocks pb ON pb.id=r.practice_block_id AND pb.user_id=r.user_id
			WHERE r.user_id=$1 AND r.status IN ('uploading','ready') AND COALESCE(NULLIF(r.tune_id,''), pb.tune_id) IS NOT NULL
			  AND ($4='' OR COALESCE(NULLIF(r.tune_id,''), pb.tune_id)=$4)
		),
		activity AS (
			SELECT tune_id, day, ms FROM linked WHERE ms > 0
			UNION ALL SELECT tune_id, day, ms FROM loose
			UNION ALL SELECT tune_id, day, 0 FROM takes
		)
		SELECT a.tune_id, MAX(a.day)::text, COALESCE(SUM(a.ms),0)::bigint,
		       COALESCE(SUM(a.ms) FILTER (WHERE a.day >= $2::date),0)::bigint,
		       COUNT(DISTINCT a.day)::int,
		       (SELECT COUNT(*)::int FROM takes t WHERE t.tune_id=a.tune_id)
		FROM activity a GROUP BY a.tune_id`, userID, weekStart, maxBlockElapsedMS, onlyTune)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]tunePractice{}
	for rows.Next() {
		var p tunePractice
		if err := rows.Scan(&p.TuneID, &p.LastPracticedDate, &p.TotalPracticeMS, &p.WeekPracticeMS, &p.SessionCount, &p.TakeCount); err != nil {
			return nil, err
		}
		result[p.TuneID] = p
	}
	return result, rows.Err()
}

func insertSeedTune(ctx context.Context, tx pgx.Tx, userID uuid.UUID, seed repertoireSeed, position int) (bool, error) {
	tag, err := tx.Exec(ctx, `
		INSERT INTO repertoire_tunes (user_id,tune_id,title,category,chosen,position,reference_artist,reference_title)
		VALUES ($1,$2,$3,$4,$5,$6,NULLIF($7,''),NULLIF($8,'')) ON CONFLICT (user_id,tune_id) DO NOTHING`,
		userID, seed.TuneID, seed.Title, seed.Category, seed.Chosen, position, seed.ReferenceArtist, seed.ReferenceTitle)
	if err != nil || tag.RowsAffected() == 0 {
		return false, err
	}
	return true, insertTuneEvent(ctx, tx, userID, seed.TuneID, nil, "tune.seeded", map[string]any{"title": seed.Title, "category": seed.Category}, nil)
}

func insertTuneEvent(ctx context.Context, tx pgx.Tx, userID uuid.UUID, tuneID string, mutationID *uuid.UUID, eventType string, payload map[string]any, blockID *uuid.UUID) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO repertoire_events (user_id,tune_id,client_mutation_id,event_type,payload,practice_block_id) VALUES ($1,$2,$3,$4,$5,$6)`,
		userID, tuneID, mutationID, eventType, encoded, blockID)
	return err
}

// ensureRepertoireSeeded runs the one-time seed and legacy import. Claiming the
// settings row serializes concurrent first loads; once it exists the seed never
// runs again, so archived or swapped tunes are never resurrected.
func ensureRepertoireSeeded(ctx context.Context, tx pgx.Tx, userID uuid.UUID) error {
	tag, err := tx.Exec(ctx, `INSERT INTO repertoire_settings (user_id) VALUES ($1) ON CONFLICT DO NOTHING`, userID)
	if err != nil || tag.RowsAffected() == 0 {
		return err
	}
	positions := map[string]int{}
	insert := func(seed repertoireSeed) error {
		_, err := insertSeedTune(ctx, tx, userID, seed, positions[seed.Category])
		positions[seed.Category]++
		return err
	}
	for _, seed := range repertoireSeeds {
		if err := insert(seed); err != nil {
			return err
		}
	}

	legacy := map[string]int{}
	var raw []byte
	err = tx.QueryRow(ctx, `SELECT COALESCE(data->'repertoire','{}'::jsonb) FROM campaign_state WHERE user_id=$1`, userID).Scan(&raw)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &legacy)
	}
	practiced := map[string]bool{}
	rows, err := tx.Query(ctx, `
		SELECT tune_id FROM practice_blocks WHERE user_id=$1 AND tune_id IS NOT NULL
		UNION SELECT tune_id FROM recordings WHERE user_id=$1 AND COALESCE(tune_id,'')<>'' AND status<>'deleted'`, userID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var tuneID string
		if err := rows.Scan(&tuneID); err != nil {
			rows.Close()
			return err
		}
		practiced[tuneID] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	known := map[string]bool{}
	for _, seed := range repertoireSeeds {
		known[seed.TuneID] = true
	}
	for _, seed := range legacyOnlyTunes {
		if legacy[seed.TuneID] > 0 || practiced[seed.TuneID] {
			if err := insert(seed); err != nil {
				return err
			}
			known[seed.TuneID] = true
		}
	}
	legacyIDs := make([]string, 0, len(legacy))
	for tuneID := range legacy {
		legacyIDs = append(legacyIDs, tuneID)
	}
	sort.Strings(legacyIDs)
	for _, tuneID := range legacyIDs {
		stage := legacy[tuneID]
		if !known[tuneID] || stage < 0 || stage > 6 {
			continue
		}
		milestones := legacyStageMilestones(stage)
		value := func(key string) *string {
			if status, ok := milestones[key]; ok {
				return &status
			}
			return nil
		}
		if _, err := tx.Exec(ctx, `
			UPDATE repertoire_tunes SET legacy_stage=$3,melody_by_ear=COALESCE($4,melody_by_ear),changes=COALESCE($5,changes),
			       improvise=COALESCE($6,improvise),gig_ready=COALESCE($7,gig_ready)
			WHERE user_id=$1 AND tune_id=$2`, userID, tuneID, stage, value("melodyByEar"), value("changes"), value("improvise"), value("gigReady")); err != nil {
			return err
		}
		if err := insertTuneEvent(ctx, tx, userID, tuneID, nil, "tune.legacy_imported", map[string]any{"stage": stage}, nil); err != nil {
			return err
		}
	}
	if practiced[preludeToAKiss.TuneID] {
		if err := insert(preludeToAKiss); err != nil {
			return err
		}
	}
	return nil
}

func repertoirePrivacy(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "private, no-store")
		w.Header().Set("X-Robots-Tag", "noindex, nofollow, noarchive")
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

func (app *application) repertoireRoutes(mux *http.ServeMux) {
	routes := map[string]http.HandlerFunc{
		"GET /v1/repertoire":                        app.getRepertoire,
		"PATCH /v1/repertoire/settings":             app.updateRepertoireSettings,
		"POST /v1/repertoire/tunes":                 app.createRepertoireTune,
		"PATCH /v1/repertoire/tunes/{tuneId}":       app.updateRepertoireTune,
		"GET /v1/repertoire/tunes/{tuneId}/history": app.repertoireTuneHistory,
	}
	for path, handler := range routes {
		mux.Handle(path, repertoirePrivacy(app.authenticate(handler)))
	}
}

// repertoireToday is the caller's local date (the browser sends it) or the
// studio's Pacific date, matching the archive's day boundaries.
func (app *application) repertoireToday(r *http.Request) (string, error) {
	if today := r.URL.Query().Get("today"); today != "" {
		if _, err := parseArchiveDate(today); err != nil {
			return "", err
		}
		return today, nil
	}
	var today string
	err := app.db.QueryRow(r.Context(), `SELECT timezone('America/Los_Angeles', now())::date::text`).Scan(&today)
	return today, err
}

// mondayOf matches startOfWeek() in app.js: weeks start on Monday.
func mondayOf(date string) string {
	parsed, err := time.Parse("2006-01-02", date)
	if err != nil {
		return date
	}
	offset := (int(parsed.Weekday()) + 6) % 7
	return parsed.AddDate(0, 0, -offset).Format("2006-01-02")
}

type repertoireArchivedSummary struct {
	TuneID string `json:"tuneId"`
	Title  string `json:"title"`
}

type repertoireResponse struct {
	Tunes    []repertoireTune `json:"tunes"`
	Archived any              `json:"archived"`
	Unlinked []tunePractice   `json:"unlinked"`
	Settings struct {
		SetTargetDate string `json:"setTargetDate"`
	} `json:"settings"`
	Week struct {
		Start string `json:"start"`
		Today string `json:"today"`
		// All practice and jazz-track (non-trumpet) practice this week, for the 50/50 split.
		PracticeMS     int64 `json:"practiceMs"`
		JazzPracticeMS int64 `json:"jazzPracticeMs"`
	} `json:"week"`
}

func (app *application) seedRepertoire(ctx context.Context, userID uuid.UUID) error {
	tx, err := app.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := ensureRepertoireSeeded(ctx, tx, userID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (app *application) getRepertoire(w http.ResponseWriter, r *http.Request) {
	today, err := app.repertoireToday(r)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "today is invalid")
		return
	}
	userID, err := app.userID(r.Context())
	if err != nil {
		app.serverError(w, err)
		return
	}
	if err := app.seedRepertoire(r.Context(), userID); err != nil {
		app.serverError(w, err)
		return
	}
	response, err := app.loadRepertoire(r.Context(), userID, today, r.URL.Query().Get("includeArchived") == "1")
	if err != nil {
		app.serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (app *application) loadRepertoire(ctx context.Context, userID uuid.UUID, today string, includeArchived bool) (repertoireResponse, error) {
	var response repertoireResponse
	response.Week.Today = today
	response.Week.Start = mondayOf(today)
	if err := app.db.QueryRow(ctx, `SELECT set_target_date::text FROM repertoire_settings WHERE user_id=$1`, userID).Scan(&response.Settings.SetTargetDate); err != nil {
		return response, err
	}
	if err := app.db.QueryRow(ctx, `
		SELECT COALESCE(SUM(ms),0)::bigint, COALESCE(SUM(ms) FILTER (WHERE track <> 'trumpet'),0)::bigint FROM (
			SELECT pb.track, LEAST(GREATEST(pb.elapsed_ms::bigint, COALESCE((
			    SELECT SUM(r.duration_ms) FROM recordings r
			    WHERE r.practice_block_id=pb.id AND r.user_id=pb.user_id AND r.status IN ('uploading','ready')),0)), $3)::bigint AS ms
			FROM practice_blocks pb
			WHERE pb.user_id=$1 AND pb.practice_date >= $2::date AND pb.practice_date < $2::date + 7
		) week`, userID, response.Week.Start, maxBlockElapsedMS).Scan(&response.Week.PracticeMS, &response.Week.JazzPracticeMS); err != nil {
		return response, err
	}
	practice, err := loadTunePractice(ctx, app.db, userID, response.Week.Start, "")
	if err != nil {
		return response, err
	}
	rows, err := app.db.Query(ctx, `SELECT `+tuneColumns+` FROM repertoire_tunes WHERE user_id=$1
		ORDER BY CASE category WHEN 'ballad' THEN 0 WHEN 'upbeat' THEN 1 WHEN 'pop' THEN 2 ELSE 3 END, position, title`, userID)
	if err != nil {
		return response, err
	}
	defer rows.Close()
	response.Tunes = []repertoireTune{}
	archivedFull := []repertoireTune{}
	archivedSummary := []repertoireArchivedSummary{}
	known := map[string]bool{}
	for rows.Next() {
		tune, err := scanTune(rows)
		if err != nil {
			return response, err
		}
		known[tune.TuneID] = true
		tune.applyPractice(practice[tune.TuneID])
		deriveTuneState(&tune)
		if tune.ArchivedAt == nil {
			response.Tunes = append(response.Tunes, tune)
		} else {
			archivedFull = append(archivedFull, tune)
			archivedSummary = append(archivedSummary, repertoireArchivedSummary{tune.TuneID, tune.Title})
		}
	}
	if err := rows.Err(); err != nil {
		return response, err
	}
	response.Archived = archivedSummary
	if includeArchived {
		response.Archived = archivedFull
	}
	response.Unlinked = []tunePractice{}
	for tuneID, p := range practice {
		if !known[tuneID] {
			response.Unlinked = append(response.Unlinked, p)
		}
	}
	sort.Slice(response.Unlinked, func(i, j int) bool {
		if response.Unlinked[i].LastPracticedDate != response.Unlinked[j].LastPracticedDate {
			return response.Unlinked[i].LastPracticedDate > response.Unlinked[j].LastPracticedDate
		}
		return response.Unlinked[i].TuneID < response.Unlinked[j].TuneID
	})
	return response, nil
}

func (app *application) loadRepertoireTune(ctx context.Context, db dbQuerier, userID uuid.UUID, tuneID, weekStart string) (repertoireTune, error) {
	tune, err := scanTune(db.QueryRow(ctx, `SELECT `+tuneColumns+` FROM repertoire_tunes WHERE user_id=$1 AND tune_id=$2`, userID, tuneID))
	if err != nil {
		return tune, err
	}
	practice, err := loadTunePractice(ctx, db, userID, weekStart, tuneID)
	if err != nil {
		return tune, err
	}
	tune.applyPractice(practice[tuneID])
	deriveTuneState(&tune)
	return tune, nil
}

type updateRepertoireSettingsRequest struct {
	SetTargetDate string `json:"setTargetDate"`
}

func (app *application) updateRepertoireSettings(w http.ResponseWriter, r *http.Request) {
	var input updateRepertoireSettingsRequest
	if err := readJSON(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, err := parseArchiveDate(input.SetTargetDate); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "set target date is invalid")
		return
	}
	userID, err := app.userID(r.Context())
	if err != nil {
		app.serverError(w, err)
		return
	}
	if err := app.seedRepertoire(r.Context(), userID); err != nil {
		app.serverError(w, err)
		return
	}
	if _, err := app.db.Exec(r.Context(), `UPDATE repertoire_settings SET set_target_date=$2 WHERE user_id=$1`, userID, input.SetTargetDate); err != nil {
		app.serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"setTargetDate": input.SetTargetDate})
}

type tuneFields struct {
	Title           string
	Category        string
	ReferenceArtist string
	ReferenceTitle  string
	ReferenceURL    string
	ConcertKey      string
	Notes           string
}

// validateTuneFields trims and validates the editable text fields of a tune.
func validateTuneFields(fields *tuneFields) error {
	fields.Title = strings.TrimSpace(fields.Title)
	fields.Category = strings.TrimSpace(fields.Category)
	fields.ReferenceArtist = strings.TrimSpace(fields.ReferenceArtist)
	fields.ReferenceTitle = strings.TrimSpace(fields.ReferenceTitle)
	fields.ReferenceURL = strings.TrimSpace(fields.ReferenceURL)
	if count := utf8.RuneCountInString(fields.Title); count < 1 || count > 160 {
		return errors.New("tune title must be 1 to 160 characters")
	}
	if _, ok := repertoireCategoryOrder[fields.Category]; !ok {
		return errors.New("tune category is invalid")
	}
	if utf8.RuneCountInString(fields.ReferenceArtist) > 160 || utf8.RuneCountInString(fields.ReferenceTitle) > 160 {
		return errors.New("reference is too long")
	}
	if fields.ReferenceURL != "" && (len(fields.ReferenceURL) > 500 || !strings.HasPrefix(fields.ReferenceURL, "https://")) {
		return errors.New("reference link must be an https:// URL")
	}
	if strings.TrimSpace(fields.ConcertKey) == "" {
		fields.ConcertKey = ""
	} else if key, ok := normalizeTuneKey(fields.ConcertKey); ok {
		fields.ConcertKey = key
	} else {
		return errors.New("concert key must look like C, Bb, F# or Gm")
	}
	if utf8.RuneCountInString(fields.Notes) > 8000 {
		return errors.New("tune notes are too long")
	}
	return nil
}

type createTuneRequest struct {
	TuneID           string `json:"tuneId"`
	Title            string `json:"title"`
	Category         string `json:"category"`
	Chosen           *bool  `json:"chosen"`
	ReferenceArtist  string `json:"referenceArtist"`
	ReferenceTitle   string `json:"referenceTitle"`
	ReferenceURL     string `json:"referenceUrl"`
	ConcertKey       string `json:"concertKey"`
	Notes            string `json:"notes"`
	ClientMutationID string `json:"clientMutationId"`
}

func (app *application) createRepertoireTune(w http.ResponseWriter, r *http.Request) {
	var input createTuneRequest
	if err := readJSON(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	mutationID, err := uuid.Parse(input.ClientMutationID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid mutation")
		return
	}
	fields := tuneFields{input.Title, input.Category, input.ReferenceArtist, input.ReferenceTitle, input.ReferenceURL, input.ConcertKey, input.Notes}
	if err := validateTuneFields(&fields); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	explicit := strings.TrimSpace(input.TuneID)
	if explicit != "" && !tuneIDPattern.MatchString(explicit) {
		writeError(w, http.StatusUnprocessableEntity, "tune id is invalid")
		return
	}
	chosen := input.Chosen == nil || *input.Chosen
	today, err := app.repertoireToday(r)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "today is invalid")
		return
	}
	userID, err := app.userID(r.Context())
	if err != nil {
		app.serverError(w, err)
		return
	}
	tx, err := app.db.Begin(r.Context())
	if err != nil {
		app.serverError(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	if err := ensureRepertoireSeeded(r.Context(), tx, userID); err != nil {
		app.serverError(w, err)
		return
	}
	// Serialize creates per user so concurrent slug choices cannot collide.
	if _, err := tx.Exec(r.Context(), `SELECT 1 FROM repertoire_settings WHERE user_id=$1 FOR UPDATE`, userID); err != nil {
		app.serverError(w, err)
		return
	}
	var previous string
	err = tx.QueryRow(r.Context(), `SELECT tune_id FROM repertoire_events WHERE user_id=$1 AND client_mutation_id=$2`, userID, mutationID).Scan(&previous)
	if err == nil {
		tune, loadErr := app.loadRepertoireTune(r.Context(), tx, userID, previous, mondayOf(today))
		if loadErr != nil {
			app.serverError(w, loadErr)
			return
		}
		writeJSON(w, http.StatusOK, tune)
		return
	} else if !errors.Is(err, pgx.ErrNoRows) {
		app.serverError(w, err)
		return
	}
	existing := func(tuneID string) (bool, bool, string, error) {
		var archived bool
		var title string
		err := tx.QueryRow(r.Context(), `SELECT archived_at IS NOT NULL,title FROM repertoire_tunes WHERE user_id=$1 AND tune_id=$2`, userID, tuneID).Scan(&archived, &title)
		if errors.Is(err, pgx.ErrNoRows) {
			return false, false, "", nil
		}
		return err == nil, archived, title, err
	}
	conflict := func(tuneID, title string, archived bool) {
		message := "a tune with this id already exists"
		if archived {
			message = "an archived tune with this id exists; restore it instead"
		}
		writeJSON(w, http.StatusConflict, map[string]any{"error": message, "tuneId": tuneID, "title": title, "archived": archived})
	}
	tuneID := explicit
	if tuneID != "" {
		found, archived, title, err := existing(tuneID)
		if err != nil {
			app.serverError(w, err)
			return
		}
		if found {
			conflict(tuneID, title, archived)
			return
		}
	} else {
		base := slugifyTuneTitle(fields.Title)
		if base == "" {
			base = "tune-" + randomTuneSuffix()
		}
		found, archived, title, err := existing(base)
		if err != nil {
			app.serverError(w, err)
			return
		}
		if found && archived {
			conflict(base, title, true)
			return
		}
		tuneID = base
		for suffix := 2; found; suffix++ {
			tail := "-" + strconv.Itoa(suffix)
			tuneID = strings.TrimRight(base[:min(len(base), 48-len(tail))], "-") + tail
			if found, _, _, err = existing(tuneID); err != nil {
				app.serverError(w, err)
				return
			}
		}
	}
	_, err = tx.Exec(r.Context(), `
		INSERT INTO repertoire_tunes (user_id,tune_id,title,category,chosen,position,reference_artist,reference_title,reference_url,concert_key,notes)
		VALUES ($1,$2,$3,$4,$5,(SELECT LEAST(COALESCE(MAX(position)+1,0),999) FROM repertoire_tunes WHERE user_id=$1 AND category=$4),
		        NULLIF($6,''),NULLIF($7,''),NULLIF($8,''),NULLIF($9,''),NULLIF($10,''))`,
		userID, tuneID, fields.Title, fields.Category, chosen, fields.ReferenceArtist, fields.ReferenceTitle, fields.ReferenceURL, fields.ConcertKey, fields.Notes)
	if err == nil {
		err = insertTuneEvent(r.Context(), tx, userID, tuneID, &mutationID, "tune.created", map[string]any{"title": fields.Title, "category": fields.Category}, nil)
	}
	if err != nil {
		app.serverError(w, err)
		return
	}
	tune, err := app.loadRepertoireTune(r.Context(), tx, userID, tuneID, mondayOf(today))
	if err != nil {
		app.serverError(w, err)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		app.serverError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, tune)
}

type tuneMilestonesPatch struct {
	MelodyByEar   *string `json:"melodyByEar"`
	Lyrics        *string `json:"lyrics"`
	Changes       *string `json:"changes"`
	Transcription *string `json:"transcription"`
	Improvise     *string `json:"improvise"`
	GigReady      *string `json:"gigReady"`
}

func (patch tuneMilestonesPatch) values() map[string]*string {
	return map[string]*string{"melodyByEar": patch.MelodyByEar, "lyrics": patch.Lyrics, "changes": patch.Changes,
		"transcription": patch.Transcription, "improvise": patch.Improvise, "gigReady": patch.GigReady}
}

type patchTuneRequest struct {
	ExpectedRevision *int64               `json:"expectedRevision"`
	ClientMutationID string               `json:"clientMutationId"`
	PracticeBlockID  string               `json:"practiceBlockId"`
	Title            *string              `json:"title"`
	Category         *string              `json:"category"`
	Chosen           *bool                `json:"chosen"`
	Position         *int                 `json:"position"`
	ReferenceArtist  *string              `json:"referenceArtist"`
	ReferenceTitle   *string              `json:"referenceTitle"`
	ReferenceURL     *string              `json:"referenceUrl"`
	ConcertKey       *string              `json:"concertKey"`
	Notes            *string              `json:"notes"`
	KeysKnown        *[]string            `json:"keysKnown"`
	Milestones       *tuneMilestonesPatch `json:"milestones"`
	Archived         *bool                `json:"archived"`
}

// applyTunePatch returns the tune with the requested changes, validated.
func applyTunePatch(current repertoireTune, input patchTuneRequest, now time.Time) (repertoireTune, error) {
	next := current
	next.Milestones = map[string]string{}
	for key, value := range current.Milestones {
		next.Milestones[key] = value
	}
	next.KeysKnown = append([]string{}, current.KeysKnown...)
	set := func(target *string, value *string) {
		if value != nil {
			*target = *value
		}
	}
	fields := tuneFields{next.Title, next.Category, next.ReferenceArtist, next.ReferenceTitle, next.ReferenceURL, next.ConcertKey, next.Notes}
	set(&fields.Title, input.Title)
	set(&fields.Category, input.Category)
	set(&fields.ReferenceArtist, input.ReferenceArtist)
	set(&fields.ReferenceTitle, input.ReferenceTitle)
	set(&fields.ReferenceURL, input.ReferenceURL)
	set(&fields.ConcertKey, input.ConcertKey)
	set(&fields.Notes, input.Notes)
	if err := validateTuneFields(&fields); err != nil {
		return current, err
	}
	next.Title, next.Category, next.ReferenceArtist, next.ReferenceTitle = fields.Title, fields.Category, fields.ReferenceArtist, fields.ReferenceTitle
	next.ReferenceURL, next.ConcertKey, next.Notes = fields.ReferenceURL, fields.ConcertKey, fields.Notes
	if input.Chosen != nil {
		next.Chosen = *input.Chosen
	}
	if input.Position != nil {
		if *input.Position < 0 || *input.Position > 999 {
			return current, errors.New("tune position is invalid")
		}
		next.Position = *input.Position
	}
	if input.KeysKnown != nil {
		keys, err := normalizeTuneKeys(*input.KeysKnown)
		if err != nil {
			return current, err
		}
		next.KeysKnown = keys
	}
	if input.Milestones != nil {
		for key, value := range input.Milestones.values() {
			if value == nil {
				continue
			}
			if !validMilestoneStatus(*value) {
				return current, errors.New("milestone status is invalid")
			}
			next.Milestones[key] = *value
		}
	}
	if input.Archived != nil {
		if *input.Archived && next.ArchivedAt == nil {
			next.ArchivedAt = &now
		} else if !*input.Archived {
			next.ArchivedAt = nil
		}
	}
	deriveTuneState(&next)
	return next, nil
}

func (app *application) updateRepertoireTune(w http.ResponseWriter, r *http.Request) {
	tuneID := r.PathValue("tuneId")
	if !tuneIDPattern.MatchString(tuneID) {
		writeError(w, http.StatusBadRequest, "invalid tune id")
		return
	}
	var input patchTuneRequest
	if err := readJSON(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	mutationID, err := uuid.Parse(input.ClientMutationID)
	if err != nil || input.ExpectedRevision == nil {
		writeError(w, http.StatusBadRequest, "expectedRevision and clientMutationId are required")
		return
	}
	today, err := app.repertoireToday(r)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "today is invalid")
		return
	}
	weekStart := mondayOf(today)
	userID, err := app.userID(r.Context())
	if err != nil {
		app.serverError(w, err)
		return
	}
	var blockID *uuid.UUID
	if input.PracticeBlockID != "" {
		parsed, parseErr := uuid.Parse(input.PracticeBlockID)
		var owned bool
		if parseErr == nil {
			parseErr = app.db.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM practice_blocks WHERE id=$1 AND user_id=$2)`, parsed, userID).Scan(&owned)
		}
		if parseErr != nil || !owned {
			writeError(w, http.StatusUnprocessableEntity, "practice block is invalid")
			return
		}
		blockID = &parsed
	}
	tx, err := app.db.Begin(r.Context())
	if err != nil {
		app.serverError(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	current, err := scanTune(tx.QueryRow(r.Context(), `SELECT `+tuneColumns+` FROM repertoire_tunes WHERE user_id=$1 AND tune_id=$2 FOR UPDATE`, userID, tuneID))
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "tune not found")
		return
	}
	if err != nil {
		app.serverError(w, err)
		return
	}
	respond := func(status int, message string) {
		tune, err := app.loadRepertoireTune(r.Context(), tx, userID, tuneID, weekStart)
		if err != nil {
			app.serverError(w, err)
			return
		}
		if err := tx.Commit(r.Context()); err != nil {
			app.serverError(w, err)
			return
		}
		if message != "" {
			writeJSON(w, status, map[string]any{"error": message, "tune": tune})
			return
		}
		writeJSON(w, status, tune)
	}
	var duplicate bool
	if err := tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM repertoire_events WHERE user_id=$1 AND client_mutation_id=$2)`, userID, mutationID).Scan(&duplicate); err != nil {
		app.serverError(w, err)
		return
	}
	if duplicate {
		respond(http.StatusOK, "")
		return
	}
	if *input.ExpectedRevision != current.Revision {
		respond(http.StatusConflict, "This tune changed on another device.")
		return
	}
	next, err := applyTunePatch(current, input, time.Now())
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	events := diffTune(current, next)
	if len(events) == 0 {
		respond(http.StatusOK, "")
		return
	}
	statuses := make([]string, len(tuneMilestoneColumns))
	for index, milestone := range tuneMilestoneColumns {
		statuses[index] = next.Milestones[milestone.Key]
	}
	_, err = tx.Exec(r.Context(), `
		UPDATE repertoire_tunes SET title=$3,category=$4,chosen=$5,position=$6,reference_artist=NULLIF($7,''),reference_title=NULLIF($8,''),
		       reference_url=NULLIF($9,''),concert_key=NULLIF($10,''),keys_known=$11,melody_by_ear=$12,lyrics=$13,changes=$14,
		       transcription=$15,improvise=$16,gig_ready=$17,notes=NULLIF($18,''),archived_at=$19,revision=revision+1,updated_at=now()
		WHERE user_id=$1 AND tune_id=$2`,
		userID, tuneID, next.Title, next.Category, next.Chosen, next.Position, next.ReferenceArtist, next.ReferenceTitle, next.ReferenceURL,
		next.ConcertKey, next.KeysKnown, statuses[0], statuses[1], statuses[2], statuses[3], statuses[4], statuses[5], next.Notes, next.ArchivedAt)
	if err != nil {
		app.serverError(w, err)
		return
	}
	for index, event := range events {
		// The mutation id marks the first event; the unique index makes retries idempotent.
		var eventMutation *uuid.UUID
		if index == 0 {
			eventMutation = &mutationID
		}
		if err := insertTuneEvent(r.Context(), tx, userID, tuneID, eventMutation, event.Type, event.Payload, blockID); err != nil {
			app.serverError(w, err)
			return
		}
	}
	respond(http.StatusOK, "")
}

// validateBlockTunes checks tune links on new block definitions. Lenient mode
// (curriculum initialization) drops unknown links so a stale data.js can never
// keep the day from loading; otherwise an unknown or archived tune is rejected.
func validateBlockTunes(ctx context.Context, db dbQuerier, userID uuid.UUID, blocks []blockDefinition, lenient bool) error {
	requested := []string{}
	for index := range blocks {
		blocks[index].TuneID = strings.TrimSpace(blocks[index].TuneID)
		if blocks[index].TuneID != "" {
			requested = append(requested, blocks[index].TuneID)
		}
	}
	if len(requested) == 0 {
		return nil
	}
	active, err := activeTuneIDs(ctx, db, userID, requested)
	if err != nil {
		return err
	}
	for index := range blocks {
		if blocks[index].TuneID == "" || active[blocks[index].TuneID] {
			continue
		}
		if !lenient {
			return errUnknownTune
		}
		blocks[index].TuneID = ""
	}
	return nil
}

func activeTuneIDs(ctx context.Context, db dbQuerier, userID uuid.UUID, tuneIDs []string) (map[string]bool, error) {
	rows, err := db.Query(ctx, `SELECT tune_id FROM repertoire_tunes WHERE user_id=$1 AND archived_at IS NULL AND tune_id = ANY($2)`, userID, tuneIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	active := map[string]bool{}
	for rows.Next() {
		var tuneID string
		if err := rows.Scan(&tuneID); err != nil {
			return nil, err
		}
		active[tuneID] = true
	}
	return active, rows.Err()
}

type tuneHistoryBlock struct {
	ID            uuid.UUID `json:"id"`
	Title         string    `json:"title"`
	Notes         string    `json:"notes"`
	ElapsedMS     int       `json:"elapsedMs"`
	TargetMinutes int       `json:"targetMinutes"`
	Status        string    `json:"status"`
	Removed       bool      `json:"removed"`
	Category      string    `json:"category"`
	Track         string    `json:"track"`
}

type tuneHistoryRecording struct {
	ID                 uuid.UUID `json:"id"`
	Status             string    `json:"status"`
	ContentType        string    `json:"contentType"`
	MediaKind          string    `json:"mediaKind"`
	DurationMS         int       `json:"durationMs"`
	RecordedAt         time.Time `json:"recordedAt"`
	TakeNumber         int       `json:"takeNumber,omitempty"`
	Notes              string    `json:"notes,omitempty"`
	TuneID             string    `json:"tuneId,omitempty"`
	PracticeBlockID    string    `json:"practiceBlockId,omitempty"`
	PracticeBlockTitle string    `json:"practiceBlockTitle,omitempty"`
	PracticeDate       string    `json:"practiceDate"`
}

type tuneHistoryDay struct {
	PracticeDate string                 `json:"practiceDate"`
	PracticeMS   int64                  `json:"practiceMs"`
	Minutes      int                    `json:"minutes"`
	Blocks       []tuneHistoryBlock     `json:"blocks"`
	Recordings   []tuneHistoryRecording `json:"recordings"`
}

type tuneHistoryEvent struct {
	ID              int64           `json:"id"`
	EventType       string          `json:"eventType"`
	Payload         json.RawMessage `json:"payload"`
	PracticeBlockID *uuid.UUID      `json:"practiceBlockId,omitempty"`
	PracticeDate    string          `json:"practiceDate,omitempty"`
	OccurredAt      time.Time       `json:"occurredAt"`
}

func (app *application) repertoireTuneHistory(w http.ResponseWriter, r *http.Request) {
	tuneID := r.PathValue("tuneId")
	if !tuneIDPattern.MatchString(tuneID) {
		writeError(w, http.StatusBadRequest, "invalid tune id")
		return
	}
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 200 {
			writeError(w, http.StatusUnprocessableEntity, "limit is invalid")
			return
		}
		limit = parsed
	}
	before := r.URL.Query().Get("before")
	if before != "" {
		if _, err := parseArchiveDate(before); err != nil {
			writeError(w, http.StatusUnprocessableEntity, "before is invalid")
			return
		}
	}
	userID, err := app.userID(r.Context())
	if err != nil {
		app.serverError(w, err)
		return
	}
	days, nextBefore, err := app.loadTuneHistoryDays(r.Context(), userID, tuneID, before, limit)
	if err != nil {
		app.serverError(w, err)
		return
	}
	events, err := app.loadTuneEvents(r.Context(), userID, tuneID)
	if err != nil {
		app.serverError(w, err)
		return
	}
	drills, err := app.loadGuideToneSummary(r.Context(), userID, tuneID)
	if err != nil {
		app.serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tuneId": tuneID, "days": days, "nextBefore": nextBefore, "events": events, "drills": drills})
}

// loadTuneHistoryDays groups a tune's linked blocks and takes by practice day.
// Removed sections still count: removal keeps practice history.
func (app *application) loadTuneHistoryDays(ctx context.Context, userID uuid.UUID, tuneID, before string, limit int) ([]tuneHistoryDay, string, error) {
	recordingRows, err := app.db.Query(ctx, `
		SELECT r.id,r.status,r.content_type,COALESCE(r.media_kind,'audio'),COALESCE(r.duration_ms,0),r.recorded_at,COALESCE(r.take_number,0),
		       COALESCE(r.notes,''),COALESCE(r.tune_id,''),COALESCE(r.practice_block_id::text,''),COALESCE(pb.title,''),
		       COALESCE(pb.practice_date, timezone('America/Los_Angeles', r.recorded_at)::date)::text
		FROM recordings r LEFT JOIN practice_blocks pb ON pb.id=r.practice_block_id AND pb.user_id=r.user_id
		WHERE r.user_id=$1 AND r.status IN ('ready','uploading') AND (r.tune_id=$2 OR pb.tune_id=$2)
		ORDER BY r.recorded_at,r.id`, userID, tuneID)
	if err != nil {
		return nil, "", err
	}
	recordings := []tuneHistoryRecording{}
	for recordingRows.Next() {
		var item tuneHistoryRecording
		if err := recordingRows.Scan(&item.ID, &item.Status, &item.ContentType, &item.MediaKind, &item.DurationMS, &item.RecordedAt, &item.TakeNumber,
			&item.Notes, &item.TuneID, &item.PracticeBlockID, &item.PracticeBlockTitle, &item.PracticeDate); err != nil {
			recordingRows.Close()
			return nil, "", err
		}
		recordings = append(recordings, item)
	}
	recordingRows.Close()
	if err := recordingRows.Err(); err != nil {
		return nil, "", err
	}
	blockRows, err := app.db.Query(ctx, `
		SELECT id,practice_date::text,title,COALESCE(notes,''),elapsed_ms,target_minutes,status,removed_at IS NOT NULL,category,track,updated_at,completed_at
		FROM practice_blocks WHERE user_id=$1 AND tune_id=$2 ORDER BY practice_date DESC,position,id`, userID, tuneID)
	if err != nil {
		return nil, "", err
	}
	byDay := map[string]*tuneHistoryDay{}
	day := func(date string) *tuneHistoryDay {
		if byDay[date] == nil {
			byDay[date] = &tuneHistoryDay{PracticeDate: date, Blocks: []tuneHistoryBlock{}, Recordings: []tuneHistoryRecording{}}
		}
		return byDay[date]
	}
	claimed := map[string]bool{}
	for blockRows.Next() {
		var block practiceBlock
		var removed bool
		if err := blockRows.Scan(&block.ID, &block.PracticeDate, &block.Title, &block.Notes, &block.ElapsedMS, &block.TargetMinutes, &block.Status,
			&removed, &block.Category, &block.Track, &block.UpdatedAt, &block.CompletedAt); err != nil {
			blockRows.Close()
			return nil, "", err
		}
		for _, recording := range recordings {
			if recording.PracticeBlockID == block.ID.String() {
				block.Recordings = append(block.Recordings, blockRecordingSummary{Status: recording.Status, DurationMS: recording.DurationMS})
			}
		}
		reconcileBlockPracticeTime(&block)
		claimed[block.ID.String()] = true
		if block.ElapsedMS == 0 && block.Notes == "" && len(block.Recordings) == 0 {
			continue
		}
		entry := day(block.PracticeDate)
		entry.PracticeMS += int64(block.ElapsedMS)
		entry.Blocks = append(entry.Blocks, tuneHistoryBlock{block.ID, block.Title, block.Notes, block.ElapsedMS, block.TargetMinutes, block.Status, removed, block.Category, block.Track})
	}
	blockRows.Close()
	if err := blockRows.Err(); err != nil {
		return nil, "", err
	}
	for _, recording := range recordings {
		entry := day(recording.PracticeDate)
		entry.Recordings = append(entry.Recordings, recording)
		if recording.PracticeBlockID == "" {
			entry.PracticeMS += int64(recording.DurationMS)
		}
	}
	dates := make([]string, 0, len(byDay))
	for date := range byDay {
		if before == "" || date < before {
			dates = append(dates, date)
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(dates)))
	nextBefore := ""
	if len(dates) > limit {
		dates = dates[:limit]
		nextBefore = dates[len(dates)-1]
	}
	result := make([]tuneHistoryDay, 0, len(dates))
	for _, date := range dates {
		entry := byDay[date]
		entry.Minutes = int((entry.PracticeMS + 30000) / 60000)
		result = append(result, *entry)
	}
	return result, nextBefore, nil
}

func (app *application) loadTuneEvents(ctx context.Context, userID uuid.UUID, tuneID string) ([]tuneHistoryEvent, error) {
	rows, err := app.db.Query(ctx, `
		SELECT e.id,e.event_type,e.payload,e.practice_block_id,COALESCE(pb.practice_date::text,''),e.occurred_at
		FROM repertoire_events e LEFT JOIN practice_blocks pb ON pb.id=e.practice_block_id AND pb.user_id=e.user_id
		WHERE e.user_id=$1 AND e.tune_id=$2 ORDER BY e.occurred_at DESC,e.id DESC LIMIT 200`, userID, tuneID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := []tuneHistoryEvent{}
	for rows.Next() {
		var event tuneHistoryEvent
		if err := rows.Scan(&event.ID, &event.EventType, &event.Payload, &event.PracticeBlockID, &event.PracticeDate, &event.OccurredAt); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}
