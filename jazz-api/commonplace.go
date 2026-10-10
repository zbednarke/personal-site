package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // IANA zones for Moments, independent of the container image.
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Commonplace: a private archive of Moments. Originals (screenshots, audio,
// verbatim text, links, transcript lines) are stored as received and never
// rewritten by the server; the margin (annotations) is separate, and
// provenance (who, where, when, in which time zone) is always kept.

const cpBase = "/v1/commonplace"

var (
	cpKinds     = map[string]bool{"conversation": true, "dream": true, "idea": true, "quote": true}
	cpSources   = map[string]bool{"imessage": true, "discord": true, "voice": true, "text": true, "link": true, "other": true}
	cpRoles     = map[string]bool{"sender": true, "recipient": true, "mentioned": true}
	cpStates    = map[string]bool{"pencil": true, "ink": true, "erased": true}
	cpNoteType  = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	cpKeyPatten = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@#+-]{0,199}$`)
)

// cpDB is satisfied by both the pool and a transaction.
type cpDB interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// ---- Wire types -------------------------------------------------------------

type cpPerson struct {
	ID      uuid.UUID  `json:"id"`
	Name    string     `json:"name"`
	Aliases []string   `json:"aliases"`
	Role    string     `json:"role,omitempty"`
	Special bool       `json:"special"`
	Moments int        `json:"moments,omitempty"`
	LastAt  *time.Time `json:"lastAt,omitempty"`
}

type cpCounts struct {
	Images      int `json:"images"`
	Audio       int `json:"audio"`
	Texts       int `json:"texts"`
	Links       int `json:"links"`
	Lines       int `json:"lines"`
	Pencil      int `json:"pencil"`
	Ink         int `json:"ink"`
	MissingFile int `json:"missingFiles"`
}

type cpMoment struct {
	ID           uuid.UUID  `json:"id"`
	Kind         string     `json:"kind"`
	OccurredAt   *time.Time `json:"occurredAt"`
	EndedAt      *time.Time `json:"endedAt"`
	Timezone     string     `json:"timezone"`
	Source       string     `json:"source"`
	SourceDetail string     `json:"sourceDetail"`
	Title        string     `json:"title"`
	Why          string     `json:"why"`
	ExternalKey  string     `json:"externalKey,omitempty"`
	ImportedFrom string     `json:"importedFrom"`
	Revision     int64      `json:"revision"`
	CreatedAt    time.Time  `json:"createdAt"`
	UpdatedAt    time.Time  `json:"updatedAt"`
	People       []cpPerson `json:"people"`
	// List cards only.
	Excerpt *string    `json:"excerpt,omitempty"`
	Counts  *cpCounts  `json:"counts,omitempty"`
	Cover   *uuid.UUID `json:"coverArtifactId,omitempty"`
}

type cpArtifact struct {
	ID           uuid.UUID  `json:"id"`
	Key          string     `json:"key"`
	Position     int        `json:"position"`
	Kind         string     `json:"kind"`
	Stored       bool       `json:"stored"`
	ContentType  string     `json:"contentType,omitempty"`
	SizeBytes    int64      `json:"sizeBytes,omitempty"`
	SHA256       string     `json:"sha256,omitempty"`
	Width        *int       `json:"width,omitempty"`
	Height       *int       `json:"height,omitempty"`
	DurationMS   *int       `json:"durationMs,omitempty"`
	OriginalName string     `json:"originalName,omitempty"`
	SourceURL    string     `json:"sourceUrl,omitempty"`
	Caption      string     `json:"caption,omitempty"`
	Alt          string     `json:"alt,omitempty"`
	Text         string     `json:"text,omitempty"`
	URL          string     `json:"url,omitempty"`
	LinkTitle    string     `json:"title,omitempty"`
	SiteName     string     `json:"siteName,omitempty"`
	CapturedText string     `json:"capturedText,omitempty"`
	CapturedAt   *time.Time `json:"capturedAt,omitempty"`
}

type cpLine struct {
	ID          uuid.UUID       `json:"id"`
	Key         string          `json:"key"`
	Position    int             `json:"position"`
	Speaker     string          `json:"speaker"` // "me", a person id, or "" (unknown)
	SpeakerName string          `json:"speakerName,omitempty"`
	Text        string          `json:"text"`
	SaidAt      *time.Time      `json:"at,omitempty"`
	TimeLabel   string          `json:"timeLabel,omitempty"`
	DayLabel    string          `json:"dayLabel,omitempty"`
	Meta        json.RawMessage `json:"meta"`
	ArtifactID  *uuid.UUID      `json:"artifactId,omitempty"`
	Rect        []float64       `json:"rect,omitempty"`
	Revision    int64           `json:"revision"`
}

type cpAnnotation struct {
	ID           uuid.UUID  `json:"id"`
	Key          string     `json:"key,omitempty"`
	State        string     `json:"state"`
	Author       string     `json:"author"`
	Type         string     `json:"type"`
	Title        string     `json:"title"`
	Body         string     `json:"body"`
	LineID       *uuid.UUID `json:"lineId,omitempty"`
	ArtifactID   *uuid.UUID `json:"artifactId,omitempty"`
	Rect         []float64  `json:"rect,omitempty"`
	LinkURL      string     `json:"linkUrl,omitempty"`
	LinkMomentID *uuid.UUID `json:"linkMomentId,omitempty"`
	LinkLabel    string     `json:"linkLabel,omitempty"`
	OwnerTouched bool       `json:"ownerTouched"`
	Revision     int64      `json:"revision"`
	CreatedAt    time.Time  `json:"createdAt"`
	UpdatedAt    time.Time  `json:"updatedAt"`
}

type cpKnot struct {
	ID          uuid.UUID  `json:"id"`
	MomentID    uuid.UUID  `json:"momentId"`
	LineID      *uuid.UUID `json:"lineId,omitempty"`
	Position    int        `json:"position"`
	Label       string     `json:"label"`
	Style       string     `json:"style"`
	At          *time.Time `json:"at,omitempty"`
	Timezone    string     `json:"timezone"`
	MomentKind  string     `json:"momentKind"`
	MomentTitle string     `json:"momentTitle"`
	Excerpt     string     `json:"excerpt"`
	Speaker     string     `json:"speaker,omitempty"`
}

type cpThread struct {
	ID          uuid.UUID `json:"id"`
	Key         string    `json:"key,omitempty"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	State       string    `json:"state"`
	Revision    int64     `json:"revision"`
	Knots       []cpKnot  `json:"knots"`
	Moments     int       `json:"moments,omitempty"`
}

type cpDoorway struct {
	Kind       string     `json:"kind"` // "link" or "moment"
	URL        string     `json:"url,omitempty"`
	MomentID   *uuid.UUID `json:"momentId,omitempty"`
	Title      string     `json:"title"`
	Why        string     `json:"why"`
	State      string     `json:"state"` // pencil (suggested) or ink (kept)
	Excerpt    string     `json:"excerpt,omitempty"`
	At         *time.Time `json:"at,omitempty"`
	Timezone   string     `json:"timezone,omitempty"`
	MomentKind string     `json:"momentKind,omitempty"`
}

type cpMomentFull struct {
	Moment      cpMoment       `json:"moment"`
	Artifacts   []cpArtifact   `json:"artifacts"`
	Lines       []cpLine       `json:"lines"`
	Annotations []cpAnnotation `json:"annotations"`
	Threads     []cpThread     `json:"threads"`
	Doorways    []cpDoorway    `json:"doorways"`
}

// ---- Routes and privacy -------------------------------------------------------

func (app *application) commonplaceRoutes(mux *http.ServeMux) {
	browser := map[string]http.HandlerFunc{
		"GET " + cpBase + "/moments":                   app.cpListMoments,
		"POST " + cpBase + "/moments":                  app.cpCapture,
		"GET " + cpBase + "/moments/{id}":              app.cpGetMoment,
		"PATCH " + cpBase + "/moments/{id}":            app.cpPatchMoment,
		"DELETE " + cpBase + "/moments/{id}":           app.cpDeleteMoment,
		"POST " + cpBase + "/moments/{id}/artifacts":   app.cpUploadArtifact,
		"DELETE " + cpBase + "/artifacts/{id}":         app.cpDeleteArtifact,
		"POST " + cpBase + "/moments/{id}/lines":       app.cpCreateLine,
		"PATCH " + cpBase + "/lines/{id}":              app.cpPatchLine,
		"DELETE " + cpBase + "/lines/{id}":             app.cpDeleteLine,
		"POST " + cpBase + "/moments/{id}/annotations": app.cpCreateAnnotation,
		"PATCH " + cpBase + "/annotations/{id}":        app.cpPatchAnnotation,
		"GET " + cpBase + "/on-this-day":               app.cpOnThisDay,
		"GET " + cpBase + "/people":                    app.cpListPeople,
		"POST " + cpBase + "/people":                   app.cpCreatePerson,
		"PATCH " + cpBase + "/people/{id}":             app.cpPatchPerson,
		"GET " + cpBase + "/threads":                   app.cpListThreads,
		"POST " + cpBase + "/threads":                  app.cpCreateThread,
		"GET " + cpBase + "/threads/{id}":              app.cpGetThread,
		"PATCH " + cpBase + "/threads/{id}":            app.cpPatchThread,
		"POST " + cpBase + "/threads/{id}/knots":       app.cpCreateKnot,
		"DELETE " + cpBase + "/knots/{id}":             app.cpDeleteKnot,
		"GET " + cpBase + "/search":                    app.cpSearch,
		"GET " + cpBase + "/space":                     app.cpSpace,
		"PUT " + cpBase + "/regions/{key}":             app.cpNameRegion,
		"GET " + cpBase + "/media/{id}":                app.cpMedia,
		"POST " + cpBase + "/import/bundle":            app.cpImportBundle,
		"POST " + cpBase + "/import/discord":           app.cpImportDiscord,
	}
	for path, h := range browser {
		mux.Handle(path, app.commonplacePrivacy(app.authenticate(h)))
	}
}

// commonplacePrivacy mirrors trumpetPrivacy: private, unindexed, no referrer,
// and every mutation must be same-site with a body type that needs a CORS
// preflight. Multipart imports (a CORS-safelisted type) additionally require
// the X-Commonplace-Upload header, which forces that preflight.
func (app *application) commonplacePrivacy(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "private, no-store")
		w.Header().Set("X-Robots-Tag", "noindex, nofollow, noarchive")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if r.Method != "GET" && r.Method != "HEAD" && (!commonplaceBodyTypeAllowed(r) || r.Header.Get("Sec-Fetch-Site") == "cross-site") {
			writeError(w, 403, "same-site request with an allowed body type required")
			return
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

var cpArtifactUploadPath = regexp.MustCompile(`^/v1/commonplace/moments/[0-9a-fA-F-]{36}/artifacts$`)

func commonplaceBodyTypeAllowed(r *http.Request) bool {
	contentType := strings.ToLower(strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0]))
	switch {
	case contentType == "application/json":
		return true
	case contentType == "multipart/form-data":
		return r.Method == "POST" && strings.HasPrefix(r.URL.Path, cpBase+"/import/") && r.Header.Get("X-Commonplace-Upload") == "1"
	case r.Method == "POST" && cpArtifactUploadPath.MatchString(r.URL.Path):
		return cpMediaTypeAllowed(contentType)
	}
	return false
}

// ---- Validation helpers -------------------------------------------------------

func cpText(s string, limit int, field string) (string, error) {
	if !utf8.ValidString(s) {
		return "", fmt.Errorf("%s is not valid UTF-8", field)
	}
	if utf8.RuneCountInString(s) > limit {
		return "", fmt.Errorf("%s must be at most %d characters", field, limit)
	}
	return s, nil
}

// cpLine collapses whitespace for single-line fields (titles, labels).
func cpOneLine(s string, limit int, field string) (string, error) {
	return cpText(strings.Join(strings.Fields(s), " "), limit, field)
}

var cpZoneName = regexp.MustCompile(`^(UTC|[A-Za-z][A-Za-z_-]*(/[A-Za-z0-9][A-Za-z0-9_+-]*){1,2})$`)

// cpTimezone defaults an omitted zone to UTC (see cpRequiredTimezone).
func cpTimezone(tz string) (string, error) {
	if strings.TrimSpace(tz) == "" {
		return "UTC", nil
	}
	return cpRequiredTimezone(tz)
}

// cpRequiredTimezone accepts only real IANA names ("Area/City" or UTC); Go's
// special "Local" and other names Postgres would not know are refused.
func cpRequiredTimezone(tz string) (string, error) {
	tz = strings.TrimSpace(tz)
	if len(tz) > 64 || strings.Contains(tz, "..") || !cpZoneName.MatchString(tz) {
		return "", errors.New("timezone must be an IANA zone such as America/Los_Angeles")
	}
	if _, err := time.LoadLocation(tz); err != nil {
		return "", errors.New("timezone must be an IANA zone such as America/Los_Angeles")
	}
	return tz, nil
}

func cpTime(raw, field string) (*time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return nil, fmt.Errorf("%s must be an RFC 3339 timestamp with an offset, e.g. 2026-03-14T23:41:00-07:00", field)
	}
	if t.Year() < 1900 || t.Year() > 2200 {
		return nil, fmt.Errorf("%s is out of range", field)
	}
	return &t, nil
}

func cpRect(rect []float64, field string) ([]float64, error) {
	if rect == nil {
		return nil, nil
	}
	if len(rect) != 4 {
		return nil, fmt.Errorf("%s must be [x, y, width, height] in percent", field)
	}
	for _, v := range rect {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return nil, fmt.Errorf("%s is invalid", field)
		}
	}
	x, y, w, h := rect[0], rect[1], rect[2], rect[3]
	if x < 0 || y < 0 || w <= 0 || h <= 0 || x+w > 100.5 || y+h > 100.5 {
		return nil, fmt.Errorf("%s must lie within 0–100 percent of the image", field)
	}
	out := make([]float64, 4)
	for i, v := range rect {
		out[i] = math.Round(v*1000) / 1000
	}
	return out, nil
}

func cpRectJSON(rect []float64) any {
	if rect == nil {
		return nil
	}
	b, _ := json.Marshal(rect)
	return string(b)
}

func cpHTTPURL(raw, field string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || len(raw) > 4000 {
		return "", fmt.Errorf("%s must be an http(s) URL", field)
	}
	return u.String(), nil
}

func cpMeta(raw json.RawMessage, field string) (string, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return "{}", nil
	}
	if len(trimmed) > 8000 {
		return "", fmt.Errorf("%s is too large", field)
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(trimmed), &obj); err != nil {
		return "", fmt.Errorf("%s must be a JSON object", field)
	}
	b, _ := json.Marshal(obj)
	return string(b), nil
}

func cpPathID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, 400, "invalid id")
		return uuid.Nil, false
	}
	return id, true
}

func (app *application) cpUser(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	user, err := app.userID(r.Context())
	if err != nil {
		app.serverError(w, err)
		return uuid.Nil, false
	}
	return user, true
}

// ---- Loading ------------------------------------------------------------------

const cpMomentColumns = `m.id,m.kind,m.occurred_at,m.ended_at,m.timezone,m.source,m.source_detail,m.title,m.why,coalesce(m.external_key,''),m.imported_from,m.revision,m.created_at,m.updated_at`

func cpScanMoment(row pgx.Row, extra ...any) (cpMoment, error) {
	var m cpMoment
	targets := []any{&m.ID, &m.Kind, &m.OccurredAt, &m.EndedAt, &m.Timezone, &m.Source, &m.SourceDetail, &m.Title, &m.Why, &m.ExternalKey, &m.ImportedFrom, &m.Revision, &m.CreatedAt, &m.UpdatedAt}
	err := row.Scan(append(targets, extra...)...)
	m.People = []cpPerson{}
	return m, err
}

func cpLoadPeopleFor(ctx context.Context, q cpDB, ids []uuid.UUID) (map[uuid.UUID][]cpPerson, error) {
	out := map[uuid.UUID][]cpPerson{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := q.Query(ctx, `SELECT mp.moment_id,p.id,p.display_name,p.aliases,mp.role,p.special FROM cp_moment_people mp JOIN cp_people p ON p.id=mp.person_id
 WHERE mp.moment_id=ANY($1) ORDER BY mp.moment_id,mp.position,CASE mp.role WHEN 'sender' THEN 0 WHEN 'recipient' THEN 1 ELSE 2 END,p.display_name`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var mid uuid.UUID
		var p cpPerson
		if err := rows.Scan(&mid, &p.ID, &p.Name, &p.Aliases, &p.Role, &p.Special); err != nil {
			return nil, err
		}
		if p.Aliases == nil {
			p.Aliases = []string{}
		}
		out[mid] = append(out[mid], p)
	}
	return out, rows.Err()
}

func cpLoadMoment(ctx context.Context, q cpDB, user, id uuid.UUID) (cpMoment, error) {
	m, err := cpScanMoment(q.QueryRow(ctx, `SELECT `+cpMomentColumns+` FROM cp_moments m WHERE m.id=$1 AND m.user_id=$2`, id, user))
	if err != nil {
		return m, err
	}
	people, err := cpLoadPeopleFor(ctx, q, []uuid.UUID{id})
	if err != nil {
		return m, err
	}
	if people[id] != nil {
		m.People = people[id]
	}
	return m, nil
}

const cpArtifactColumns = `a.id,a.artifact_key,a.position,a.kind,a.object_name IS NOT NULL,coalesce(a.content_type,''),coalesce(a.size_bytes,0),coalesce(a.sha256,''),a.width,a.height,a.duration_ms,
 a.original_name,a.source_url,a.caption,a.alt,a.text_content,a.url,a.link_title,a.site_name,a.captured_text,a.captured_at`

func cpScanArtifact(row pgx.Row) (cpArtifact, error) {
	var a cpArtifact
	err := row.Scan(&a.ID, &a.Key, &a.Position, &a.Kind, &a.Stored, &a.ContentType, &a.SizeBytes, &a.SHA256, &a.Width, &a.Height, &a.DurationMS,
		&a.OriginalName, &a.SourceURL, &a.Caption, &a.Alt, &a.Text, &a.URL, &a.LinkTitle, &a.SiteName, &a.CapturedText, &a.CapturedAt)
	return a, err
}

const cpLineColumns = `l.id,l.line_key,l.position,l.speaker_is_me,l.speaker_person_id,coalesce(p.display_name,''),l.speaker_label,l.body,l.said_at,l.time_label,l.day_label,l.meta,l.artifact_id,l.rect,l.revision`

func cpScanLine(row pgx.Row) (cpLine, error) {
	var l cpLine
	var me bool
	var person *uuid.UUID
	var personName, label string
	var meta, rect []byte
	err := row.Scan(&l.ID, &l.Key, &l.Position, &me, &person, &personName, &label, &l.Text, &l.SaidAt, &l.TimeLabel, &l.DayLabel, &meta, &l.ArtifactID, &rect, &l.Revision)
	if err != nil {
		return l, err
	}
	switch {
	case me:
		l.Speaker = "me"
	case person != nil:
		l.Speaker = person.String()
		l.SpeakerName = personName
	default:
		l.SpeakerName = label
	}
	if label != "" && !me {
		l.SpeakerName = firstNonEmpty(personName, label)
	}
	l.Meta = json.RawMessage(meta)
	if len(meta) == 0 {
		l.Meta = json.RawMessage("{}")
	}
	if len(rect) > 0 {
		_ = json.Unmarshal(rect, &l.Rect)
	}
	return l, nil
}

const cpAnnotationColumns = `n.id,coalesce(n.annotation_key,''),n.state,n.author,n.note_type,n.title,n.body,n.line_id,n.artifact_id,n.rect,n.link_url,n.link_moment_id,n.link_label,n.owner_touched_at IS NOT NULL,n.revision,n.created_at,n.updated_at`

func cpScanAnnotation(row pgx.Row) (cpAnnotation, error) {
	var n cpAnnotation
	var rect []byte
	err := row.Scan(&n.ID, &n.Key, &n.State, &n.Author, &n.Type, &n.Title, &n.Body, &n.LineID, &n.ArtifactID, &rect, &n.LinkURL, &n.LinkMomentID, &n.LinkLabel, &n.OwnerTouched, &n.Revision, &n.CreatedAt, &n.UpdatedAt)
	if err == nil && len(rect) > 0 {
		_ = json.Unmarshal(rect, &n.Rect)
	}
	return n, err
}

func cpCollect[T any](rows pgx.Rows, err error, scan func(pgx.Row) (T, error)) ([]T, error) {
	out := []T{}
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		v, err := scan(rows)
		if err != nil {
			return out, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// cpExcerptSQL picks a short reading of a Moment for cards, knots and doorways.
const cpExcerptSQL = `coalesce(
 (SELECT left(l.body,280) FROM cp_lines l WHERE l.moment_id=m.id AND l.body<>'' ORDER BY l.position,l.said_at NULLS LAST LIMIT 1),
 (SELECT left(coalesce(nullif(a.text_content,''),nullif(a.link_title,''),nullif(a.captured_text,''),nullif(a.caption,''),a.url),280) FROM cp_artifacts a WHERE a.moment_id=m.id AND a.kind IN ('text','link') ORDER BY a.position LIMIT 1),
 left(m.why,280),'')`

func cpLoadThreadsForMoment(ctx context.Context, q cpDB, user, moment uuid.UUID) ([]cpThread, error) {
	rows, err := q.Query(ctx, `SELECT t.id,coalesce(t.thread_key,''),t.title,t.description,t.state,t.revision FROM cp_threads t
 WHERE t.user_id=$1 AND EXISTS(SELECT 1 FROM cp_thread_knots k WHERE k.thread_id=t.id AND k.moment_id=$2) ORDER BY t.created_at`, user, moment)
	threads, err := cpCollect(rows, err, func(row pgx.Row) (cpThread, error) {
		var t cpThread
		err := row.Scan(&t.ID, &t.Key, &t.Title, &t.Description, &t.State, &t.Revision)
		return t, err
	})
	if err != nil {
		return nil, err
	}
	for i := range threads {
		if threads[i].Knots, err = cpLoadKnots(ctx, q, threads[i].ID); err != nil {
			return nil, err
		}
	}
	return threads, nil
}

func cpLoadKnots(ctx context.Context, q cpDB, thread uuid.UUID) ([]cpKnot, error) {
	rows, err := q.Query(ctx, `SELECT k.id,k.moment_id,k.line_id,k.position,k.label,k.style,coalesce(l.said_at,m.occurred_at,m.created_at),m.timezone,m.kind,m.title,
 coalesce(left(l.body,280),`+cpExcerptSQL+`),
 CASE WHEN l.speaker_is_me THEN 'me' ELSE coalesce(p.display_name,l.speaker_label,'') END
 FROM cp_thread_knots k JOIN cp_moments m ON m.id=k.moment_id LEFT JOIN cp_lines l ON l.id=k.line_id LEFT JOIN cp_people p ON p.id=l.speaker_person_id
 WHERE k.thread_id=$1 ORDER BY coalesce(l.said_at,m.occurred_at,m.created_at),k.position,k.id`, thread)
	return cpCollect(rows, err, func(row pgx.Row) (cpKnot, error) {
		var k cpKnot
		err := row.Scan(&k.ID, &k.MomentID, &k.LineID, &k.Position, &k.Label, &k.Style, &k.At, &k.Timezone, &k.MomentKind, &k.MomentTitle, &k.Excerpt, &k.Speaker)
		return k, err
	})
}

func cpLoadDoorways(ctx context.Context, q cpDB, user, moment uuid.UUID) ([]cpDoorway, error) {
	doors := []cpDoorway{}
	seen := map[uuid.UUID]bool{moment: true}
	// 1. Link targets written in the margin (pencil until kept).
	rows, err := q.Query(ctx, `SELECT n.link_url,n.link_moment_id,n.link_label,n.title,n.state,lm.title,lm.kind,coalesce(lm.occurred_at,lm.created_at),coalesce(lm.timezone,''),
 coalesce((SELECT left(l.body,200) FROM cp_lines l WHERE l.moment_id=lm.id ORDER BY l.position LIMIT 1),'')
 FROM cp_annotations n LEFT JOIN cp_moments lm ON lm.id=n.link_moment_id AND lm.user_id=n.user_id
 WHERE n.moment_id=$1 AND n.user_id=$2 AND n.state<>'erased' AND (n.link_url<>'' OR n.link_moment_id IS NOT NULL)
 ORDER BY (n.state='ink') DESC,n.created_at`, moment, user)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var d cpDoorway
		var linkMoment *uuid.UUID
		var label, noteTitle string
		var mTitle, mKind, tz *string
		var at *time.Time
		var excerpt string
		if err := rows.Scan(&d.URL, &linkMoment, &label, &noteTitle, &d.State, &mTitle, &mKind, &at, &tz, &excerpt); err != nil {
			rows.Close()
			return nil, err
		}
		d.Why = noteTitle
		if linkMoment != nil && mKind != nil {
			if seen[*linkMoment] {
				continue
			}
			seen[*linkMoment] = true
			d.Kind, d.MomentID, d.MomentKind, d.At, d.Excerpt = "moment", linkMoment, *mKind, at, excerpt
			d.Title = firstNonEmpty(label, *mTitle, excerpt)
			if tz != nil {
				d.Timezone = *tz
			}
		} else if d.URL != "" {
			d.Kind = "link"
			d.Title = firstNonEmpty(label, d.URL)
		} else {
			continue
		}
		doors = append(doors, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// 2. Moments on the same threads, then 3. with the same people.
	type candidate struct {
		id                   uuid.UUID
		title, kind, tz, exc string
		at                   time.Time
		why                  string
	}
	collect := func(sql, why string, limit int) error {
		rows, err := q.Query(ctx, sql, moment, user, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c candidate
			if err := rows.Scan(&c.id, &c.title, &c.kind, &c.tz, &c.at, &c.exc, &c.why); err != nil {
				return err
			}
			if seen[c.id] || len(doors) >= 6 {
				continue
			}
			seen[c.id] = true
			id, at := c.id, c.at
			doors = append(doors, cpDoorway{Kind: "moment", MomentID: &id, Title: firstNonEmpty(c.title, c.exc), Why: firstNonEmpty(c.why, why), State: "pencil", Excerpt: c.exc, At: &at, Timezone: c.tz, MomentKind: c.kind})
		}
		return rows.Err()
	}
	if err := collect(`SELECT DISTINCT ON (m.id) m.id,m.title,m.kind,m.timezone,coalesce(m.occurred_at,m.created_at),`+cpExcerptSQL+`,'Also on the thread “'||t.title||'”'
 FROM cp_thread_knots k JOIN cp_thread_knots k2 ON k2.thread_id=k.thread_id JOIN cp_threads t ON t.id=k.thread_id JOIN cp_moments m ON m.id=k2.moment_id
 WHERE k.moment_id=$1 AND m.user_id=$2 AND m.id<>$1 ORDER BY m.id LIMIT $3`, "", 6); err != nil {
		return nil, err
	}
	if err := collect(`SELECT m.id,m.title,m.kind,m.timezone,coalesce(m.occurred_at,m.created_at),`+cpExcerptSQL+`,
 'Also with '||(SELECT string_agg(DISTINCT p.display_name, ', ') FROM cp_moment_people a JOIN cp_moment_people b ON b.person_id=a.person_id JOIN cp_people p ON p.id=a.person_id WHERE a.moment_id=$1 AND b.moment_id=m.id)
 FROM cp_moments m WHERE m.user_id=$2 AND m.id<>$1 AND EXISTS(SELECT 1 FROM cp_moment_people a JOIN cp_moment_people b ON b.person_id=a.person_id WHERE a.moment_id=$1 AND b.moment_id=m.id)
 ORDER BY coalesce(m.occurred_at,m.created_at) DESC LIMIT $3`, "", 3); err != nil {
		return nil, err
	}
	return doors, nil
}

func cpLoadFull(ctx context.Context, q cpDB, user, id uuid.UUID) (cpMomentFull, error) {
	var full cpMomentFull
	var err error
	if full.Moment, err = cpLoadMoment(ctx, q, user, id); err != nil {
		return full, err
	}
	rows, err := q.Query(ctx, `SELECT `+cpArtifactColumns+` FROM cp_artifacts a WHERE a.moment_id=$1 ORDER BY a.position,a.created_at`, id)
	if full.Artifacts, err = cpCollect(rows, err, cpScanArtifact); err != nil {
		return full, err
	}
	rows, err = q.Query(ctx, `SELECT `+cpLineColumns+` FROM cp_lines l LEFT JOIN cp_people p ON p.id=l.speaker_person_id WHERE l.moment_id=$1 ORDER BY l.position,l.said_at NULLS LAST,l.line_key`, id)
	if full.Lines, err = cpCollect(rows, err, cpScanLine); err != nil {
		return full, err
	}
	rows, err = q.Query(ctx, `SELECT `+cpAnnotationColumns+` FROM cp_annotations n WHERE n.moment_id=$1 ORDER BY n.created_at,n.annotation_key`, id)
	if full.Annotations, err = cpCollect(rows, err, cpScanAnnotation); err != nil {
		return full, err
	}
	if full.Threads, err = cpLoadThreadsForMoment(ctx, q, user, id); err != nil {
		return full, err
	}
	full.Doorways, err = cpLoadDoorways(ctx, q, user, id)
	return full, err
}

// ---- Moments: list, capture, get, patch, delete -------------------------------

func (app *application) cpListMoments(w http.ResponseWriter, r *http.Request) {
	user, ok := app.cpUser(w, r)
	if !ok {
		return
	}
	qs := r.URL.Query()
	where := []string{"m.user_id=$1"}
	args := []any{user}
	add := func(cond string, v any) {
		args = append(args, v)
		where = append(where, strings.ReplaceAll(cond, "?", "$"+strconv.Itoa(len(args))))
	}
	if k := qs.Get("kind"); k != "" {
		if !cpKinds[k] {
			writeError(w, 400, "unknown kind")
			return
		}
		add("m.kind=?", k)
	}
	if s := qs.Get("source"); s != "" {
		if !cpSources[s] {
			writeError(w, 400, "unknown source")
			return
		}
		add("m.source=?", s)
	}
	if p := qs.Get("person"); p != "" {
		pid, err := uuid.Parse(p)
		if err != nil {
			writeError(w, 400, "invalid person")
			return
		}
		add("EXISTS(SELECT 1 FROM cp_moment_people mp WHERE mp.moment_id=m.id AND mp.person_id=?)", pid)
	}
	if y := qs.Get("year"); y != "" {
		year, err := strconv.Atoi(y)
		if err != nil || year < 1900 || year > 2200 {
			writeError(w, 400, "invalid year")
			return
		}
		add("extract(year FROM coalesce(m.occurred_at,m.created_at) AT TIME ZONE m.timezone)=?", year)
	}
	limit, _ := strconv.Atoi(qs.Get("limit"))
	if limit <= 0 || limit > 200 {
		limit = 60
	}
	offset, _ := strconv.Atoi(qs.Get("offset"))
	if offset < 0 || offset > 100000 {
		offset = 0
	}
	args = append(args, limit+1, offset)
	sql := `SELECT ` + cpMomentColumns + `,` + cpExcerptSQL + `,
 (SELECT count(*) FROM cp_artifacts a WHERE a.moment_id=m.id AND a.kind='image')::int,
 (SELECT count(*) FROM cp_artifacts a WHERE a.moment_id=m.id AND a.kind='audio')::int,
 (SELECT count(*) FROM cp_artifacts a WHERE a.moment_id=m.id AND a.kind='text')::int,
 (SELECT count(*) FROM cp_artifacts a WHERE a.moment_id=m.id AND a.kind='link')::int,
 (SELECT count(*) FROM cp_lines l WHERE l.moment_id=m.id)::int,
 (SELECT count(*) FROM cp_annotations n WHERE n.moment_id=m.id AND n.state='pencil')::int,
 (SELECT count(*) FROM cp_annotations n WHERE n.moment_id=m.id AND n.state='ink')::int,
 (SELECT count(*) FROM cp_artifacts a WHERE a.moment_id=m.id AND a.kind IN ('image','audio') AND a.object_name IS NULL)::int,
 (SELECT a.id FROM cp_artifacts a WHERE a.moment_id=m.id AND a.kind='image' AND a.object_name IS NOT NULL ORDER BY a.position LIMIT 1)
 FROM cp_moments m WHERE ` + strings.Join(where, " AND ") + `
 ORDER BY coalesce(m.occurred_at,m.created_at) DESC,m.id DESC LIMIT $` + strconv.Itoa(len(args)-1) + ` OFFSET $` + strconv.Itoa(len(args))
	rows, err := app.db.Query(r.Context(), sql, args...)
	moments, err := cpCollect(rows, err, func(row pgx.Row) (cpMoment, error) {
		var c cpCounts
		var excerpt string
		var cover *uuid.UUID
		m, err := cpScanMoment(row, &excerpt, &c.Images, &c.Audio, &c.Texts, &c.Links, &c.Lines, &c.Pencil, &c.Ink, &c.MissingFile, &cover)
		m.Excerpt, m.Counts, m.Cover = &excerpt, &c, cover
		return m, err
	})
	if err != nil {
		app.serverError(w, err)
		return
	}
	more := len(moments) > limit
	if more {
		moments = moments[:limit]
	}
	if err := cpAttachPeople(r.Context(), app.db, moments); err != nil {
		app.serverError(w, err)
		return
	}
	var facets struct {
		Years   []int `json:"years"`
		Total   int   `json:"total"`
		Pending int   `json:"pencil"`
	}
	err = app.db.QueryRow(r.Context(), `SELECT coalesce(array_agg(DISTINCT extract(year FROM coalesce(m.occurred_at,m.created_at) AT TIME ZONE m.timezone)::int),'{}'),count(*)::int,
 (SELECT count(*) FROM cp_annotations n WHERE n.user_id=$1 AND n.state='pencil')::int FROM cp_moments m WHERE m.user_id=$1`, user).Scan(&facets.Years, &facets.Total, &facets.Pending)
	if err != nil {
		app.serverError(w, err)
		return
	}
	sort.Sort(sort.Reverse(sort.IntSlice(facets.Years)))
	writeJSON(w, 200, map[string]any{"moments": moments, "more": more, "facets": facets})
}

func cpAttachPeople(ctx context.Context, q cpDB, moments []cpMoment) error {
	ids := make([]uuid.UUID, len(moments))
	for i, m := range moments {
		ids[i] = m.ID
	}
	people, err := cpLoadPeopleFor(ctx, q, ids)
	if err != nil {
		return err
	}
	for i := range moments {
		if p := people[moments[i].ID]; p != nil {
			moments[i].People = p
		}
	}
	return nil
}

// cpOnThisDay returns Moments from earlier years on today's month and day,
// judged in each Moment's own time zone. ?today=YYYY-MM-DD is the viewer's date.
func (app *application) cpOnThisDay(w http.ResponseWriter, r *http.Request) {
	user, ok := app.cpUser(w, r)
	if !ok {
		return
	}
	today, err := time.Parse("2006-01-02", r.URL.Query().Get("today"))
	if err != nil {
		today = time.Now().UTC()
	}
	rows, err := app.db.Query(r.Context(), `SELECT `+cpMomentColumns+`,`+cpExcerptSQL+` FROM cp_moments m WHERE m.user_id=$1 AND m.occurred_at IS NOT NULL
 AND extract(month FROM m.occurred_at AT TIME ZONE m.timezone)=$2 AND extract(day FROM m.occurred_at AT TIME ZONE m.timezone)=$3
 AND extract(year FROM m.occurred_at AT TIME ZONE m.timezone)<$4 ORDER BY m.occurred_at DESC LIMIT 12`, user, int(today.Month()), today.Day(), today.Year())
	moments, err := cpCollect(rows, err, func(row pgx.Row) (cpMoment, error) {
		var excerpt string
		m, err := cpScanMoment(row, &excerpt)
		m.Excerpt = &excerpt
		return m, err
	})
	if err == nil {
		err = cpAttachPeople(r.Context(), app.db, moments)
	}
	if err != nil {
		app.serverError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"moments": moments})
}

type cpCaptureInput struct {
	ClientCaptureID string `json:"clientCaptureId"`
	Kind            string `json:"kind"`
	Text            string `json:"text"`
	URL             string `json:"url"`
	LinkTitle       string `json:"linkTitle"`
	Title           string `json:"title"`
	Why             string `json:"why"`
	OccurredAt      string `json:"occurredAt"`
	Timezone        string `json:"timezone"`
	Source          string `json:"source"`
	SourceDetail    string `json:"sourceDetail"`
}

func (app *application) cpCapture(w http.ResponseWriter, r *http.Request) {
	var in cpCaptureInput
	if err := decodeTrumpetJSON(w, r, &in); err != nil {
		writeError(w, 400, "invalid capture JSON")
		return
	}
	capture, err := uuid.Parse(in.ClientCaptureID)
	if err != nil {
		writeError(w, 400, "clientCaptureId must be a UUID")
		return
	}
	kind := firstNonEmpty(in.Kind, "conversation")
	if !cpKinds[kind] {
		writeError(w, 422, "unknown kind")
		return
	}
	link, err := cpHTTPURL(in.URL, "url")
	if err != nil {
		writeError(w, 422, err.Error())
		return
	}
	text, err := cpText(in.Text, 200000, "text")
	if err != nil {
		writeError(w, 422, err.Error())
		return
	}
	source := in.Source
	if source == "" {
		switch {
		case link != "":
			source = "link"
		case strings.TrimSpace(text) != "":
			source = "text"
		default:
			source = "other"
		}
	}
	if !cpSources[source] {
		writeError(w, 422, "unknown source")
		return
	}
	tz, err := cpTimezone(in.Timezone)
	if err != nil {
		writeError(w, 422, err.Error())
		return
	}
	occurred, err := cpTime(in.OccurredAt, "occurredAt")
	if err != nil {
		writeError(w, 422, err.Error())
		return
	}
	title, err1 := cpOneLine(in.Title, 300, "title")
	why, err2 := cpText(strings.TrimSpace(in.Why), 8000, "why")
	detail, err3 := cpOneLine(in.SourceDetail, 300, "sourceDetail")
	linkTitle, err4 := cpOneLine(in.LinkTitle, 500, "linkTitle")
	if err := errors.Join(err1, err2, err3, err4); err != nil {
		writeError(w, 422, err.Error())
		return
	}
	user, ok := app.cpUser(w, r)
	if !ok {
		return
	}
	tx, err := app.db.Begin(r.Context())
	if err != nil {
		app.serverError(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	id := uuid.New()
	tag, err := tx.Exec(r.Context(), `INSERT INTO cp_moments(id,user_id,kind,occurred_at,timezone,source,source_detail,title,why,client_capture_id,imported_from)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,'capture') ON CONFLICT (user_id,client_capture_id) WHERE client_capture_id IS NOT NULL DO NOTHING`,
		id, user, kind, occurred, tz, source, detail, title, why, capture)
	if err != nil {
		app.serverError(w, err)
		return
	}
	status := 201
	if tag.RowsAffected() == 0 {
		// Same capture id: return the original Moment, unchanged.
		if err := tx.QueryRow(r.Context(), `SELECT id FROM cp_moments WHERE user_id=$1 AND client_capture_id=$2`, user, capture).Scan(&id); err != nil {
			app.serverError(w, err)
			return
		}
		status = 200
	} else {
		position := 0
		if strings.TrimSpace(text) != "" {
			if _, err := tx.Exec(r.Context(), `INSERT INTO cp_artifacts(id,moment_id,user_id,artifact_key,position,kind,text_content) VALUES($1,$2,$3,'text-1',$4,'text',$5)`, uuid.New(), id, user, position, text); err != nil {
				app.serverError(w, err)
				return
			}
			position++
		}
		if link != "" {
			if _, err := tx.Exec(r.Context(), `INSERT INTO cp_artifacts(id,moment_id,user_id,artifact_key,position,kind,url,link_title,captured_at) VALUES($1,$2,$3,'link-1',$4,'link',$5,$6,now())`, uuid.New(), id, user, position, link, linkTitle); err != nil {
				app.serverError(w, err)
				return
			}
		}
	}
	full, err := cpLoadFull(r.Context(), tx, user, id)
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		app.serverError(w, err)
		return
	}
	writeJSON(w, status, full)
}

func (app *application) cpGetMoment(w http.ResponseWriter, r *http.Request) {
	id, ok := cpPathID(w, r)
	if !ok {
		return
	}
	user, ok := app.cpUser(w, r)
	if !ok {
		return
	}
	full, err := cpLoadFull(r.Context(), app.db, user, id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, 404, "moment not found")
		return
	}
	if err != nil {
		app.serverError(w, err)
		return
	}
	writeJSON(w, 200, full)
}

type cpPersonInput struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Role string `json:"role"`
}

type cpMomentPatch struct {
	ExpectedRevision *int64                    `json:"expectedRevision"`
	Kind             optional[string]          `json:"kind"`
	Title            optional[string]          `json:"title"`
	Why              optional[string]          `json:"why"`
	OccurredAt       optional[string]          `json:"occurredAt"`
	EndedAt          optional[string]          `json:"endedAt"`
	Timezone         optional[string]          `json:"timezone"`
	Source           optional[string]          `json:"source"`
	SourceDetail     optional[string]          `json:"sourceDetail"`
	People           optional[[]cpPersonInput] `json:"people"`
}

// cpResolvePerson finds a person by id, or by name/alias (case-insensitive),
// creating one when nobody matches.
func cpResolvePerson(ctx context.Context, q cpDB, user uuid.UUID, in cpPersonInput) (uuid.UUID, error) {
	if in.ID != "" {
		id, err := uuid.Parse(in.ID)
		if err != nil {
			return uuid.Nil, errors.New("invalid person id")
		}
		var found bool
		if err := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM cp_people WHERE id=$1 AND user_id=$2)`, id, user).Scan(&found); err != nil {
			return uuid.Nil, err
		}
		if !found {
			return uuid.Nil, errors.New("person not found")
		}
		return id, nil
	}
	name, err := cpOneLine(in.Name, 120, "person name")
	if err != nil {
		return uuid.Nil, err
	}
	if name == "" {
		return uuid.Nil, errors.New("person needs an id or a name")
	}
	var id uuid.UUID
	err = q.QueryRow(ctx, `SELECT id FROM cp_people WHERE user_id=$1 AND (lower(display_name)=lower($2) OR lower($2)=ANY(SELECT lower(x) FROM unnest(aliases) x)) ORDER BY created_at LIMIT 1`, user, name).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		id = uuid.New()
		_, err = q.Exec(ctx, `INSERT INTO cp_people(id,user_id,display_name) VALUES($1,$2,$3)`, id, user, name)
	}
	return id, err
}

func (app *application) cpPatchMoment(w http.ResponseWriter, r *http.Request) {
	id, ok := cpPathID(w, r)
	if !ok {
		return
	}
	var p cpMomentPatch
	if err := decodeTrumpetJSON(w, r, &p); err != nil {
		writeError(w, 400, "invalid moment JSON")
		return
	}
	if p.ExpectedRevision == nil {
		writeError(w, 400, "expectedRevision is required")
		return
	}
	user, ok := app.cpUser(w, r)
	if !ok {
		return
	}
	tx, err := app.db.Begin(r.Context())
	if err != nil {
		app.serverError(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	current, err := cpScanMoment(tx.QueryRow(r.Context(), `SELECT `+cpMomentColumns+` FROM cp_moments m WHERE m.id=$1 AND m.user_id=$2 FOR UPDATE`, id, user))
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, 404, "moment not found")
		return
	}
	if err != nil {
		app.serverError(w, err)
		return
	}
	if current.Revision != *p.ExpectedRevision {
		full, err := cpLoadFull(r.Context(), tx, user, id)
		if err != nil {
			app.serverError(w, err)
			return
		}
		writeJSON(w, 409, map[string]any{"error": "this Moment changed elsewhere", "current": full})
		return
	}
	next := current
	var errs []error
	setText := func(o optional[string], dst *string, limit int, field string, oneLine bool) {
		if !o.Set {
			return
		}
		var v string
		var err error
		if oneLine {
			v, err = cpOneLine(o.Value, limit, field)
		} else {
			v, err = cpText(strings.TrimSpace(o.Value), limit, field)
		}
		errs = append(errs, err)
		*dst = v
	}
	setText(p.Title, &next.Title, 300, "title", true)
	setText(p.Why, &next.Why, 8000, "why", false)
	setText(p.SourceDetail, &next.SourceDetail, 300, "sourceDetail", true)
	if p.Kind.Set {
		if !cpKinds[p.Kind.Value] {
			errs = append(errs, errors.New("unknown kind"))
		}
		next.Kind = p.Kind.Value
	}
	if p.Source.Set {
		if !cpSources[p.Source.Value] {
			errs = append(errs, errors.New("unknown source"))
		}
		next.Source = p.Source.Value
	}
	if p.Timezone.Set {
		next.Timezone, err = cpRequiredTimezone(p.Timezone.Value)
		errs = append(errs, err)
	}
	if p.OccurredAt.Set {
		next.OccurredAt, err = cpTime(p.OccurredAt.Value, "occurredAt")
		errs = append(errs, err)
	}
	if p.EndedAt.Set {
		next.EndedAt, err = cpTime(p.EndedAt.Value, "endedAt")
		errs = append(errs, err)
	}
	if next.OccurredAt != nil && next.EndedAt != nil && next.EndedAt.Before(*next.OccurredAt) {
		errs = append(errs, errors.New("endedAt must not be before occurredAt"))
	}
	if err := errors.Join(errs...); err != nil {
		writeError(w, 422, err.Error())
		return
	}
	if p.People.Set {
		if len(p.People.Value) > 50 {
			writeError(w, 422, "at most 50 people per Moment")
			return
		}
		if _, err := tx.Exec(r.Context(), `DELETE FROM cp_moment_people WHERE moment_id=$1`, id); err != nil {
			app.serverError(w, err)
			return
		}
		for i, person := range p.People.Value {
			role := firstNonEmpty(person.Role, "sender")
			if !cpRoles[role] {
				writeError(w, 422, "role must be sender, recipient or mentioned")
				return
			}
			pid, err := cpResolvePerson(r.Context(), tx, user, person)
			if err != nil {
				writeError(w, 422, err.Error())
				return
			}
			if _, err := tx.Exec(r.Context(), `INSERT INTO cp_moment_people(moment_id,person_id,user_id,role,position) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, id, pid, user, role, i); err != nil {
				app.serverError(w, err)
				return
			}
		}
	}
	_, err = tx.Exec(r.Context(), `UPDATE cp_moments SET kind=$3,title=$4,why=$5,occurred_at=$6,ended_at=$7,timezone=$8,source=$9,source_detail=$10,revision=revision+1,updated_at=now() WHERE id=$1 AND user_id=$2`,
		id, user, next.Kind, next.Title, next.Why, next.OccurredAt, next.EndedAt, next.Timezone, next.Source, next.SourceDetail)
	if err != nil {
		app.serverError(w, err)
		return
	}
	full, err := cpLoadFull(r.Context(), tx, user, id)
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		app.serverError(w, err)
		return
	}
	writeJSON(w, 200, full)
}

func (app *application) cpDeleteMoment(w http.ResponseWriter, r *http.Request) {
	id, ok := cpPathID(w, r)
	if !ok {
		return
	}
	user, ok := app.cpUser(w, r)
	if !ok {
		return
	}
	rows, err := app.db.Query(r.Context(), `SELECT object_name FROM cp_artifacts WHERE moment_id=$1 AND user_id=$2 AND object_name IS NOT NULL`, id, user)
	names, err := cpCollect(rows, err, func(row pgx.Row) (string, error) {
		var s string
		return s, row.Scan(&s)
	})
	if err != nil {
		app.serverError(w, err)
		return
	}
	tag, err := app.db.Exec(r.Context(), `DELETE FROM cp_moments WHERE id=$1 AND user_id=$2`, id, user)
	if err != nil {
		app.serverError(w, err)
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, 404, "moment not found")
		return
	}
	app.deleteObjectsBestEffort(r.Context(), names)
	writeJSON(w, 200, map[string]bool{"deleted": true})
}

// ---- Lines ----------------------------------------------------------------------

type cpLineInput struct {
	ExpectedRevision *int64                    `json:"expectedRevision"`
	Speaker          optional[string]          `json:"speaker"`
	SpeakerLabel     optional[string]          `json:"speakerLabel"`
	Text             optional[string]          `json:"text"`
	At               optional[string]          `json:"at"`
	TimeLabel        optional[string]          `json:"timeLabel"`
	DayLabel         optional[string]          `json:"dayLabel"`
	Meta             optional[json.RawMessage] `json:"meta"`
	ArtifactID       optional[string]          `json:"artifactId"`
	Rect             optional[[]float64]       `json:"rect"`
}

type cpLineRow struct {
	me        bool
	person    *uuid.UUID
	label     string
	body      string
	at        *time.Time
	timeLabel string
	dayLabel  string
	meta      string
	artifact  *uuid.UUID
	rect      []float64
}

func (app *application) cpApplyLineInput(ctx context.Context, q cpDB, user, moment uuid.UUID, in cpLineInput, row *cpLineRow) error {
	var errs []error
	if in.Speaker.Set {
		row.me, row.person = false, nil
		switch v := strings.TrimSpace(in.Speaker.Value); v {
		case "", "unknown":
		case "me":
			row.me = true
		default:
			pid, err := cpResolvePerson(ctx, q, user, cpPersonInput{ID: v})
			if err != nil {
				errs = append(errs, errors.New("speaker must be \"me\" or a person id"))
			} else {
				row.person = &pid
			}
		}
	}
	if in.SpeakerLabel.Set {
		v, err := cpOneLine(in.SpeakerLabel.Value, 120, "speakerLabel")
		row.label, errs = v, append(errs, err)
	}
	if in.Text.Set {
		v, err := cpText(in.Text.Value, 40000, "text")
		row.body, errs = v, append(errs, err)
	}
	if in.At.Set {
		v, err := cpTime(in.At.Value, "at")
		row.at, errs = v, append(errs, err)
	}
	if in.TimeLabel.Set {
		v, err := cpOneLine(in.TimeLabel.Value, 80, "timeLabel")
		row.timeLabel, errs = v, append(errs, err)
	}
	if in.DayLabel.Set {
		v, err := cpOneLine(in.DayLabel.Value, 120, "dayLabel")
		row.dayLabel, errs = v, append(errs, err)
	}
	if in.Meta.Set {
		v, err := cpMeta(in.Meta.Value, "meta")
		row.meta, errs = v, append(errs, err)
	}
	if in.ArtifactID.Set {
		row.artifact = nil
		if in.ArtifactID.Value != "" {
			aid, err := uuid.Parse(in.ArtifactID.Value)
			var found bool
			if err == nil {
				err = q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM cp_artifacts WHERE id=$1 AND moment_id=$2 AND kind='image')`, aid, moment).Scan(&found)
			}
			if err != nil || !found {
				errs = append(errs, errors.New("artifactId must be an image of this Moment"))
			} else {
				row.artifact = &aid
			}
		}
	}
	if in.Rect.Set {
		v, err := cpRect(in.Rect.Value, "rect")
		row.rect, errs = v, append(errs, err)
	}
	return errors.Join(errs...)
}

func (app *application) cpCreateLine(w http.ResponseWriter, r *http.Request) {
	moment, ok := cpPathID(w, r)
	if !ok {
		return
	}
	var in cpLineInput
	if err := decodeTrumpetJSON(w, r, &in); err != nil {
		writeError(w, 400, "invalid line JSON")
		return
	}
	user, ok := app.cpUser(w, r)
	if !ok {
		return
	}
	if !app.cpMomentExists(w, r, user, moment) {
		return
	}
	row := cpLineRow{meta: "{}"}
	if err := app.cpApplyLineInput(r.Context(), app.db, user, moment, in, &row); err != nil {
		writeError(w, 422, err.Error())
		return
	}
	id := uuid.New()
	// Lines added in the app use the owner- prefix so bundle re-imports keep them.
	_, err := app.db.Exec(r.Context(), `INSERT INTO cp_lines(id,moment_id,user_id,line_key,position,speaker_is_me,speaker_person_id,speaker_label,body,said_at,time_label,day_label,meta,artifact_id,rect)
 VALUES($1,$2,$3,$4,(SELECT coalesce(max(position)+1,0) FROM cp_lines WHERE moment_id=$2),$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`,
		id, moment, user, "owner-"+id.String()[:8], row.me, row.person, row.label, row.body, row.at, row.timeLabel, row.dayLabel, row.meta, row.artifact, cpRectJSON(row.rect))
	if err != nil {
		app.serverError(w, err)
		return
	}
	line, err := cpScanLine(app.db.QueryRow(r.Context(), `SELECT `+cpLineColumns+` FROM cp_lines l LEFT JOIN cp_people p ON p.id=l.speaker_person_id WHERE l.id=$1`, id))
	if err != nil {
		app.serverError(w, err)
		return
	}
	writeJSON(w, 201, map[string]any{"line": line})
}

func (app *application) cpPatchLine(w http.ResponseWriter, r *http.Request) {
	id, ok := cpPathID(w, r)
	if !ok {
		return
	}
	var in cpLineInput
	if err := decodeTrumpetJSON(w, r, &in); err != nil {
		writeError(w, 400, "invalid line JSON")
		return
	}
	if in.ExpectedRevision == nil {
		writeError(w, 400, "expectedRevision is required")
		return
	}
	user, ok := app.cpUser(w, r)
	if !ok {
		return
	}
	tx, err := app.db.Begin(r.Context())
	if err != nil {
		app.serverError(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	var row cpLineRow
	var moment uuid.UUID
	var revision int64
	var rect []byte
	err = tx.QueryRow(r.Context(), `SELECT moment_id,revision,speaker_is_me,speaker_person_id,speaker_label,body,said_at,time_label,day_label,meta::text,artifact_id,rect FROM cp_lines WHERE id=$1 AND user_id=$2 FOR UPDATE`, id, user).
		Scan(&moment, &revision, &row.me, &row.person, &row.label, &row.body, &row.at, &row.timeLabel, &row.dayLabel, &row.meta, &row.artifact, &rect)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, 404, "line not found")
		return
	}
	if err != nil {
		app.serverError(w, err)
		return
	}
	if len(rect) > 0 {
		_ = json.Unmarshal(rect, &row.rect)
	}
	if revision != *in.ExpectedRevision {
		writeError(w, 409, "this line changed elsewhere")
		return
	}
	if err := app.cpApplyLineInput(r.Context(), tx, user, moment, in, &row); err != nil {
		writeError(w, 422, err.Error())
		return
	}
	_, err = tx.Exec(r.Context(), `UPDATE cp_lines SET speaker_is_me=$2,speaker_person_id=$3,speaker_label=$4,body=$5,said_at=$6,time_label=$7,day_label=$8,meta=$9,artifact_id=$10,rect=$11,owner_touched_at=now(),revision=revision+1,updated_at=now() WHERE id=$1`,
		id, row.me, row.person, row.label, row.body, row.at, row.timeLabel, row.dayLabel, row.meta, row.artifact, cpRectJSON(row.rect))
	if err != nil {
		app.serverError(w, err)
		return
	}
	line, err := cpScanLine(tx.QueryRow(r.Context(), `SELECT `+cpLineColumns+` FROM cp_lines l LEFT JOIN cp_people p ON p.id=l.speaker_person_id WHERE l.id=$1`, id))
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		app.serverError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"line": line})
}

func (app *application) cpDeleteLine(w http.ResponseWriter, r *http.Request) {
	id, ok := cpPathID(w, r)
	if !ok {
		return
	}
	user, ok := app.cpUser(w, r)
	if !ok {
		return
	}
	tag, err := app.db.Exec(r.Context(), `DELETE FROM cp_lines WHERE id=$1 AND user_id=$2`, id, user)
	if err != nil {
		app.serverError(w, err)
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, 404, "line not found")
		return
	}
	writeJSON(w, 200, map[string]bool{"deleted": true})
}

func (app *application) cpMomentExists(w http.ResponseWriter, r *http.Request, user, moment uuid.UUID) bool {
	var found bool
	if err := app.db.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM cp_moments WHERE id=$1 AND user_id=$2)`, moment, user).Scan(&found); err != nil {
		app.serverError(w, err)
		return false
	}
	if !found {
		writeError(w, 404, "moment not found")
	}
	return found
}

// ---- Annotations (the margin) ------------------------------------------------------

type cpAnnotationInput struct {
	ExpectedRevision *int64              `json:"expectedRevision"`
	State            optional[string]    `json:"state"`
	Type             optional[string]    `json:"type"`
	Title            optional[string]    `json:"title"`
	Body             optional[string]    `json:"body"`
	LineID           optional[string]    `json:"lineId"`
	ArtifactID       optional[string]    `json:"artifactId"`
	Rect             optional[[]float64] `json:"rect"`
	LinkURL          optional[string]    `json:"linkUrl"`
	LinkMomentID     optional[string]    `json:"linkMomentId"`
	LinkLabel        optional[string]    `json:"linkLabel"`
}

type cpAnnotationRow struct {
	state, typ, title, body string
	line, artifact          *uuid.UUID
	rect                    []float64
	linkURL, linkLabel      string
	linkMoment              *uuid.UUID
}

func cpApplyAnnotationInput(ctx context.Context, q cpDB, user, moment uuid.UUID, in cpAnnotationInput, row *cpAnnotationRow) error {
	var errs []error
	if in.State.Set {
		if !cpStates[in.State.Value] {
			errs = append(errs, errors.New("state must be pencil, ink or erased"))
		}
		row.state = in.State.Value
	}
	if in.Type.Set {
		if !cpNoteType.MatchString(in.Type.Value) {
			errs = append(errs, errors.New("type must be a short lowercase word"))
		}
		row.typ = in.Type.Value
	}
	if in.Title.Set {
		v, err := cpText(strings.TrimSpace(in.Title.Value), 500, "title")
		row.title, errs = v, append(errs, err)
	}
	if in.Body.Set {
		v, err := cpText(strings.TrimSpace(in.Body.Value), 8000, "body")
		row.body, errs = v, append(errs, err)
	}
	ref := func(o optional[string], sql, msg string) *uuid.UUID {
		if !o.Set || o.Value == "" {
			return nil
		}
		id, err := uuid.Parse(o.Value)
		var found bool
		if err == nil {
			err = q.QueryRow(ctx, sql, id, moment, user).Scan(&found)
		}
		if err != nil || !found {
			errs = append(errs, errors.New(msg))
			return nil
		}
		return &id
	}
	if in.LineID.Set {
		row.line = ref(in.LineID, `SELECT EXISTS(SELECT 1 FROM cp_lines WHERE id=$1 AND moment_id=$2 AND user_id=$3)`, "lineId must be a line of this Moment")
	}
	if in.ArtifactID.Set {
		row.artifact = ref(in.ArtifactID, `SELECT EXISTS(SELECT 1 FROM cp_artifacts WHERE id=$1 AND moment_id=$2 AND user_id=$3)`, "artifactId must be an artifact of this Moment")
	}
	if in.Rect.Set {
		v, err := cpRect(in.Rect.Value, "rect")
		row.rect, errs = v, append(errs, err)
	}
	if row.rect != nil && row.artifact == nil {
		errs = append(errs, errors.New("a rect anchor needs an artifactId"))
	}
	if in.LinkURL.Set {
		v, err := cpHTTPURL(in.LinkURL.Value, "linkUrl")
		row.linkURL, errs = v, append(errs, err)
	}
	if in.LinkMomentID.Set {
		row.linkMoment = ref(in.LinkMomentID, `SELECT EXISTS(SELECT 1 FROM cp_moments WHERE id=$1 AND $2::uuid IS NOT NULL AND user_id=$3)`, "linkMomentId must be one of your Moments")
	}
	if in.LinkLabel.Set {
		v, err := cpOneLine(in.LinkLabel.Value, 200, "linkLabel")
		row.linkLabel, errs = v, append(errs, err)
	}
	return errors.Join(errs...)
}

func (app *application) cpCreateAnnotation(w http.ResponseWriter, r *http.Request) {
	moment, ok := cpPathID(w, r)
	if !ok {
		return
	}
	var in cpAnnotationInput
	if err := decodeTrumpetJSON(w, r, &in); err != nil {
		writeError(w, 400, "invalid annotation JSON")
		return
	}
	user, ok := app.cpUser(w, r)
	if !ok {
		return
	}
	if !app.cpMomentExists(w, r, user, moment) {
		return
	}
	// The owner writes in ink.
	row := cpAnnotationRow{state: "ink", typ: "note"}
	if err := cpApplyAnnotationInput(r.Context(), app.db, user, moment, in, &row); err != nil {
		writeError(w, 422, err.Error())
		return
	}
	if strings.TrimSpace(row.title) == "" && strings.TrimSpace(row.body) == "" {
		writeError(w, 422, "a note needs words")
		return
	}
	id := uuid.New()
	_, err := app.db.Exec(r.Context(), `INSERT INTO cp_annotations(id,moment_id,user_id,state,author,note_type,title,body,line_id,artifact_id,rect,link_url,link_moment_id,link_label,owner_touched_at)
 VALUES($1,$2,$3,$4,'owner',$5,$6,$7,$8,$9,$10,$11,$12,$13,now())`,
		id, moment, user, row.state, row.typ, row.title, row.body, row.line, row.artifact, cpRectJSON(row.rect), row.linkURL, row.linkMoment, row.linkLabel)
	if err != nil {
		app.serverError(w, err)
		return
	}
	n, err := cpScanAnnotation(app.db.QueryRow(r.Context(), `SELECT `+cpAnnotationColumns+` FROM cp_annotations n WHERE n.id=$1`, id))
	if err != nil {
		app.serverError(w, err)
		return
	}
	writeJSON(w, 201, map[string]any{"annotation": n})
}

// cpPatchAnnotation keeps (ink), erases, restores (undo) or edits a note.
// Editing the words of a pencil note inks it: they are now the owner's words.
func (app *application) cpPatchAnnotation(w http.ResponseWriter, r *http.Request) {
	id, ok := cpPathID(w, r)
	if !ok {
		return
	}
	var in cpAnnotationInput
	if err := decodeTrumpetJSON(w, r, &in); err != nil {
		writeError(w, 400, "invalid annotation JSON")
		return
	}
	if in.ExpectedRevision == nil {
		writeError(w, 400, "expectedRevision is required")
		return
	}
	user, ok := app.cpUser(w, r)
	if !ok {
		return
	}
	tx, err := app.db.Begin(r.Context())
	if err != nil {
		app.serverError(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	current, err := cpScanAnnotation(tx.QueryRow(r.Context(), `SELECT `+cpAnnotationColumns+` FROM cp_annotations n WHERE n.id=$1 AND n.user_id=$2 FOR UPDATE`, id, user))
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, 404, "note not found")
		return
	}
	if err != nil {
		app.serverError(w, err)
		return
	}
	if current.Revision != *in.ExpectedRevision {
		writeJSON(w, 409, map[string]any{"error": "this note changed elsewhere", "annotation": current})
		return
	}
	var moment uuid.UUID
	if err := tx.QueryRow(r.Context(), `SELECT moment_id FROM cp_annotations WHERE id=$1`, id).Scan(&moment); err != nil {
		app.serverError(w, err)
		return
	}
	row := cpAnnotationRow{state: current.State, typ: current.Type, title: current.Title, body: current.Body, line: current.LineID, artifact: current.ArtifactID,
		rect: current.Rect, linkURL: current.LinkURL, linkMoment: current.LinkMomentID, linkLabel: current.LinkLabel}
	if err := cpApplyAnnotationInput(r.Context(), tx, user, moment, in, &row); err != nil {
		writeError(w, 422, err.Error())
		return
	}
	wordsChanged := (in.Title.Set && row.title != current.Title) || (in.Body.Set && row.body != current.Body)
	if wordsChanged && !in.State.Set {
		row.state = "ink"
	}
	_, err = tx.Exec(r.Context(), `UPDATE cp_annotations SET state=$2,note_type=$3,title=$4,body=$5,line_id=$6,artifact_id=$7,rect=$8,link_url=$9,link_moment_id=$10,link_label=$11,
 owner_touched_at=now(),revision=revision+1,updated_at=now() WHERE id=$1`,
		id, row.state, row.typ, row.title, row.body, row.line, row.artifact, cpRectJSON(row.rect), row.linkURL, row.linkMoment, row.linkLabel)
	if err != nil {
		app.serverError(w, err)
		return
	}
	n, err := cpScanAnnotation(tx.QueryRow(r.Context(), `SELECT `+cpAnnotationColumns+` FROM cp_annotations n WHERE n.id=$1`, id))
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		app.serverError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"annotation": n})
}

// ---- People ----------------------------------------------------------------------

func cpCleanAliases(in []string) ([]string, error) {
	out := []string{}
	seen := map[string]bool{}
	for _, a := range in {
		a = strings.Join(strings.Fields(a), " ")
		if a == "" || seen[strings.ToLower(a)] {
			continue
		}
		if utf8.RuneCountInString(a) > 120 {
			return nil, errors.New("aliases must be at most 120 characters")
		}
		seen[strings.ToLower(a)] = true
		out = append(out, a)
	}
	if len(out) > 40 {
		return nil, errors.New("at most 40 aliases")
	}
	return out, nil
}

func (app *application) cpListPeople(w http.ResponseWriter, r *http.Request) {
	user, ok := app.cpUser(w, r)
	if !ok {
		return
	}
	rows, err := app.db.Query(r.Context(), `SELECT p.id,p.display_name,p.aliases,p.special,
 (SELECT count(DISTINCT mp.moment_id) FROM cp_moment_people mp WHERE mp.person_id=p.id)::int,
 (SELECT max(coalesce(m.occurred_at,m.created_at)) FROM cp_moment_people mp JOIN cp_moments m ON m.id=mp.moment_id WHERE mp.person_id=p.id)
 FROM cp_people p WHERE p.user_id=$1 ORDER BY 5 DESC,p.display_name`, user)
	people, err := cpCollect(rows, err, func(row pgx.Row) (cpPerson, error) {
		var p cpPerson
		err := row.Scan(&p.ID, &p.Name, &p.Aliases, &p.Special, &p.Moments, &p.LastAt)
		if p.Aliases == nil {
			p.Aliases = []string{}
		}
		return p, err
	})
	if err != nil {
		app.serverError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"people": people})
}

type cpPersonPatch struct {
	Name    optional[string]   `json:"name"`
	Aliases optional[[]string] `json:"aliases"`
	Special optional[bool]     `json:"special"`
}

func (app *application) cpCreatePerson(w http.ResponseWriter, r *http.Request) {
	var in cpPersonPatch
	if err := decodeTrumpetJSON(w, r, &in); err != nil {
		writeError(w, 400, "invalid person JSON")
		return
	}
	name, err := cpOneLine(in.Name.Value, 120, "name")
	if err != nil || name == "" {
		writeError(w, 422, "a person needs a name of at most 120 characters")
		return
	}
	aliases, err := cpCleanAliases(in.Aliases.Value)
	if err != nil {
		writeError(w, 422, err.Error())
		return
	}
	user, ok := app.cpUser(w, r)
	if !ok {
		return
	}
	p := cpPerson{ID: uuid.New(), Name: name, Aliases: aliases, Special: in.Special.Value}
	if _, err := app.db.Exec(r.Context(), `INSERT INTO cp_people(id,user_id,display_name,aliases,special) VALUES($1,$2,$3,$4,$5)`, p.ID, user, name, aliases, p.Special); err != nil {
		app.serverError(w, err)
		return
	}
	writeJSON(w, 201, map[string]any{"person": p})
}

func (app *application) cpPatchPerson(w http.ResponseWriter, r *http.Request) {
	id, ok := cpPathID(w, r)
	if !ok {
		return
	}
	var in cpPersonPatch
	if err := decodeTrumpetJSON(w, r, &in); err != nil {
		writeError(w, 400, "invalid person JSON")
		return
	}
	user, ok := app.cpUser(w, r)
	if !ok {
		return
	}
	var p cpPerson
	if err := app.db.QueryRow(r.Context(), `SELECT id,display_name,aliases,special FROM cp_people WHERE id=$1 AND user_id=$2`, id, user).Scan(&p.ID, &p.Name, &p.Aliases, &p.Special); errors.Is(err, pgx.ErrNoRows) {
		writeError(w, 404, "person not found")
		return
	} else if err != nil {
		app.serverError(w, err)
		return
	}
	if in.Name.Set {
		name, err := cpOneLine(in.Name.Value, 120, "name")
		if err != nil || name == "" {
			writeError(w, 422, "a person needs a name of at most 120 characters")
			return
		}
		p.Name = name
	}
	if in.Aliases.Set {
		aliases, err := cpCleanAliases(in.Aliases.Value)
		if err != nil {
			writeError(w, 422, err.Error())
			return
		}
		p.Aliases = aliases
	}
	if in.Special.Set {
		p.Special = in.Special.Value
	}
	if _, err := app.db.Exec(r.Context(), `UPDATE cp_people SET display_name=$3,aliases=$4,special=$5,updated_at=now() WHERE id=$1 AND user_id=$2`, id, user, p.Name, p.Aliases, p.Special); err != nil {
		app.serverError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"person": p})
}

// ---- Threads ------------------------------------------------------------------------

func (app *application) cpListThreads(w http.ResponseWriter, r *http.Request) {
	user, ok := app.cpUser(w, r)
	if !ok {
		return
	}
	rows, err := app.db.Query(r.Context(), `SELECT t.id,coalesce(t.thread_key,''),t.title,t.description,t.state,t.revision,
 (SELECT count(DISTINCT k.moment_id) FROM cp_thread_knots k WHERE k.thread_id=t.id)::int FROM cp_threads t WHERE t.user_id=$1 ORDER BY t.updated_at DESC`, user)
	threads, err := cpCollect(rows, err, func(row pgx.Row) (cpThread, error) {
		var t cpThread
		err := row.Scan(&t.ID, &t.Key, &t.Title, &t.Description, &t.State, &t.Revision, &t.Moments)
		t.Knots = []cpKnot{}
		return t, err
	})
	if err != nil {
		app.serverError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"threads": threads})
}

type cpThreadInput struct {
	ExpectedRevision *int64           `json:"expectedRevision"`
	Title            optional[string] `json:"title"`
	Description      optional[string] `json:"description"`
}

func (app *application) cpCreateThread(w http.ResponseWriter, r *http.Request) {
	var in cpThreadInput
	if err := decodeTrumpetJSON(w, r, &in); err != nil {
		writeError(w, 400, "invalid thread JSON")
		return
	}
	title, err1 := cpOneLine(in.Title.Value, 300, "title")
	desc, err2 := cpText(strings.TrimSpace(in.Description.Value), 4000, "description")
	if err := errors.Join(err1, err2); err != nil {
		writeError(w, 422, err.Error())
		return
	}
	user, ok := app.cpUser(w, r)
	if !ok {
		return
	}
	t := cpThread{ID: uuid.New(), Title: title, Description: desc, State: "ink", Revision: 1, Knots: []cpKnot{}}
	if _, err := app.db.Exec(r.Context(), `INSERT INTO cp_threads(id,user_id,title,description) VALUES($1,$2,$3,$4)`, t.ID, user, title, desc); err != nil {
		app.serverError(w, err)
		return
	}
	writeJSON(w, 201, map[string]any{"thread": t})
}

func (app *application) cpLoadThread(ctx context.Context, user, id uuid.UUID) (cpThread, error) {
	var t cpThread
	err := app.db.QueryRow(ctx, `SELECT id,coalesce(thread_key,''),title,description,state,revision FROM cp_threads WHERE id=$1 AND user_id=$2`, id, user).
		Scan(&t.ID, &t.Key, &t.Title, &t.Description, &t.State, &t.Revision)
	if err == nil {
		t.Knots, err = cpLoadKnots(ctx, app.db, id)
	}
	return t, err
}

func (app *application) cpGetThread(w http.ResponseWriter, r *http.Request) {
	id, ok := cpPathID(w, r)
	if !ok {
		return
	}
	user, ok := app.cpUser(w, r)
	if !ok {
		return
	}
	t, err := app.cpLoadThread(r.Context(), user, id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, 404, "thread not found")
		return
	}
	if err != nil {
		app.serverError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"thread": t})
}

func (app *application) cpPatchThread(w http.ResponseWriter, r *http.Request) {
	id, ok := cpPathID(w, r)
	if !ok {
		return
	}
	var in cpThreadInput
	if err := decodeTrumpetJSON(w, r, &in); err != nil || in.ExpectedRevision == nil {
		writeError(w, 400, "invalid thread JSON (expectedRevision is required)")
		return
	}
	user, ok := app.cpUser(w, r)
	if !ok {
		return
	}
	t, err := app.cpLoadThread(r.Context(), user, id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, 404, "thread not found")
		return
	}
	if err != nil {
		app.serverError(w, err)
		return
	}
	if t.Revision != *in.ExpectedRevision {
		writeJSON(w, 409, map[string]any{"error": "this thread changed elsewhere", "thread": t})
		return
	}
	if in.Title.Set {
		if t.Title, err = cpOneLine(in.Title.Value, 300, "title"); err != nil {
			writeError(w, 422, err.Error())
			return
		}
	}
	if in.Description.Set {
		if t.Description, err = cpText(strings.TrimSpace(in.Description.Value), 4000, "description"); err != nil {
			writeError(w, 422, err.Error())
			return
		}
	}
	tag, err := app.db.Exec(r.Context(), `UPDATE cp_threads SET title=$4,description=$5,state='ink',revision=revision+1,updated_at=now() WHERE id=$1 AND user_id=$2 AND revision=$3`, id, user, t.Revision, t.Title, t.Description)
	if err != nil {
		app.serverError(w, err)
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, 409, "this thread changed elsewhere")
		return
	}
	t, err = app.cpLoadThread(r.Context(), user, id)
	if err != nil {
		app.serverError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"thread": t})
}

type cpKnotInput struct {
	MomentID string `json:"momentId"`
	LineID   string `json:"lineId"`
	Label    string `json:"label"`
	Style    string `json:"style"`
}

func (app *application) cpCreateKnot(w http.ResponseWriter, r *http.Request) {
	thread, ok := cpPathID(w, r)
	if !ok {
		return
	}
	var in cpKnotInput
	if err := decodeTrumpetJSON(w, r, &in); err != nil {
		writeError(w, 400, "invalid knot JSON")
		return
	}
	style := firstNonEmpty(in.Style, "plain")
	label, err := cpOneLine(in.Label, 200, "label")
	moment, err2 := uuid.Parse(in.MomentID)
	if err != nil || err2 != nil || (style != "fire" && style != "plain") {
		writeError(w, 422, "a knot needs a momentId, a label of at most 200 characters and style fire or plain")
		return
	}
	user, ok := app.cpUser(w, r)
	if !ok {
		return
	}
	var line *uuid.UUID
	if in.LineID != "" {
		lid, err := uuid.Parse(in.LineID)
		var found bool
		if err == nil {
			err = app.db.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM cp_lines WHERE id=$1 AND moment_id=$2 AND user_id=$3)`, lid, moment, user).Scan(&found)
		}
		if err != nil || !found {
			writeError(w, 422, "lineId must be a line of that Moment")
			return
		}
		line = &lid
	}
	tag, err := app.db.Exec(r.Context(), `INSERT INTO cp_thread_knots(id,thread_id,user_id,moment_id,line_id,position,label,style)
 SELECT $1,t.id,$3,m.id,$5,(SELECT coalesce(max(position)+1,0) FROM cp_thread_knots WHERE thread_id=t.id),$6,$7
 FROM cp_threads t, cp_moments m WHERE t.id=$2 AND t.user_id=$3 AND m.id=$4 AND m.user_id=$3`, uuid.New(), thread, user, moment, line, label, style)
	if err != nil {
		app.serverError(w, err)
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, 404, "thread or moment not found")
		return
	}
	_, _ = app.db.Exec(r.Context(), `UPDATE cp_threads SET updated_at=now() WHERE id=$1`, thread)
	t, err := app.cpLoadThread(r.Context(), user, thread)
	if err != nil {
		app.serverError(w, err)
		return
	}
	writeJSON(w, 201, map[string]any{"thread": t})
}

func (app *application) cpDeleteKnot(w http.ResponseWriter, r *http.Request) {
	id, ok := cpPathID(w, r)
	if !ok {
		return
	}
	user, ok := app.cpUser(w, r)
	if !ok {
		return
	}
	tag, err := app.db.Exec(r.Context(), `DELETE FROM cp_thread_knots WHERE id=$1 AND user_id=$2`, id, user)
	if err != nil {
		app.serverError(w, err)
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, 404, "knot not found")
		return
	}
	writeJSON(w, 200, map[string]bool{"deleted": true})
}

// ---- Search ---------------------------------------------------------------------------

var cpSearchToken = regexp.MustCompile(`[\p{L}\p{N}]+`)

// cpTSQuery turns free text into a prefix-matching AND query. Tokens contain
// only letters and digits, so nothing the user types reaches tsquery syntax.
func cpTSQuery(q string) string {
	tokens := cpSearchToken.FindAllString(strings.ToLower(q), 12)
	parts := make([]string, 0, len(tokens))
	for _, t := range tokens {
		if r := []rune(t); len(r) > 64 {
			t = string(r[:64])
		}
		parts = append(parts, "'"+t+"':*")
	}
	return strings.Join(parts, " & ")
}

type cpSearchHit struct {
	Kind     string     `json:"kind"` // line, note, artifact, moment, person
	AnchorID *uuid.UUID `json:"anchorId,omitempty"`
	Snippet  string     `json:"snippet"`
	Rank     float64    `json:"rank"`
	State    string     `json:"state,omitempty"`
}

type cpSearchResult struct {
	Moment cpMoment      `json:"moment"`
	Hits   []cpSearchHit `json:"hits"`
	Rank   float64       `json:"rank"`
}

// Snippet highlight markers (Unicode private use) are split by the browser,
// which escapes everything else; the server never returns HTML.
const cpHeadlineOptions = "StartSel=, StopSel=, MaxWords=26, MinWords=10, ShortWord=2, MaxFragments=2, FragmentDelimiter=\" … \""

func (app *application) cpSearch(w http.ResponseWriter, r *http.Request) {
	user, ok := app.cpUser(w, r)
	if !ok {
		return
	}
	raw := strings.TrimSpace(r.URL.Query().Get("q"))
	if utf8.RuneCountInString(raw) > 200 {
		writeError(w, 400, "search is limited to 200 characters")
		return
	}
	tsq := cpTSQuery(raw)
	if tsq == "" {
		writeJSON(w, 200, map[string]any{"query": raw, "results": []cpSearchResult{}})
		return
	}
	rows, err := app.db.Query(r.Context(), `WITH q AS (SELECT to_tsquery('english',$2) AS q),
 hits AS (
  SELECT 'line' AS kind,l.moment_id,l.id AS anchor,ts_rank(l.search,q.q)*1.2 AS rank,l.body AS doc,'' AS state FROM cp_lines l,q WHERE l.user_id=$1 AND l.search @@ q.q
  UNION ALL
  SELECT 'note',n.moment_id,n.id,ts_rank(n.search,q.q),n.title||E'\n'||n.body,n.state FROM cp_annotations n,q WHERE n.user_id=$1 AND n.state<>'erased' AND n.search @@ q.q
  UNION ALL
  SELECT 'artifact',a.moment_id,a.id,ts_rank(a.search,q.q),concat_ws(E'\n',nullif(a.link_title,''),nullif(a.caption,''),nullif(a.text_content,''),nullif(a.captured_text,'')),'' FROM cp_artifacts a,q WHERE a.user_id=$1 AND a.search @@ q.q
  UNION ALL
  SELECT 'moment',m.id,NULL,ts_rank(m.search,q.q)*1.5,concat_ws(E'\n',nullif(m.title,''),nullif(m.why,''),nullif(m.source_detail,'')),'' FROM cp_moments m,q WHERE m.user_id=$1 AND m.search @@ q.q
  UNION ALL
  SELECT DISTINCT ON (mp.moment_id,p.id) 'person',mp.moment_id,NULL::uuid,1.0::real,p.display_name,'' FROM cp_people p JOIN cp_moment_people mp ON mp.person_id=p.id,q
   WHERE p.user_id=$1 AND to_tsvector('simple',p.display_name||' '||array_to_string(p.aliases,' ')) @@ to_tsquery('simple',$2)
 ),
 top AS (SELECT * FROM hits ORDER BY rank DESC LIMIT 120)
 SELECT top.kind,top.moment_id,top.anchor,top.rank,ts_headline('english',top.doc,q.q,$3),top.state FROM top,q ORDER BY top.rank DESC`, user, tsq, cpHeadlineOptions)
	type rawHit struct {
		moment uuid.UUID
		hit    cpSearchHit
	}
	hits, err := cpCollect(rows, err, func(row pgx.Row) (rawHit, error) {
		var h rawHit
		var rank float32
		err := row.Scan(&h.hit.Kind, &h.moment, &h.hit.AnchorID, &rank, &h.hit.Snippet, &h.hit.State)
		h.hit.Rank = math.Round(float64(rank)*10000) / 10000
		return h, err
	})
	if err != nil {
		app.serverError(w, err)
		return
	}
	order := []uuid.UUID{}
	grouped := map[uuid.UUID]*cpSearchResult{}
	for _, h := range hits {
		g := grouped[h.moment]
		if g == nil {
			g = &cpSearchResult{Hits: []cpSearchHit{}}
			grouped[h.moment] = g
			order = append(order, h.moment)
		}
		g.Rank += h.hit.Rank
		if len(g.Hits) < 4 {
			g.Hits = append(g.Hits, h.hit)
		}
	}
	if len(order) > 40 {
		order = order[:40]
	}
	results := []cpSearchResult{}
	if len(order) > 0 {
		rows, err := app.db.Query(r.Context(), `SELECT `+cpMomentColumns+`,`+cpExcerptSQL+` FROM cp_moments m WHERE m.user_id=$1 AND m.id=ANY($2)`, user, order)
		moments, err := cpCollect(rows, err, func(row pgx.Row) (cpMoment, error) {
			var excerpt string
			m, err := cpScanMoment(row, &excerpt)
			m.Excerpt = &excerpt
			return m, err
		})
		if err == nil {
			err = cpAttachPeople(r.Context(), app.db, moments)
		}
		if err != nil {
			app.serverError(w, err)
			return
		}
		for _, m := range moments {
			g := grouped[m.ID]
			g.Moment = m
			results = append(results, *g)
		}
		sort.SliceStable(results, func(i, j int) bool { return results[i].Rank > results[j].Rank })
	}
	writeJSON(w, 200, map[string]any{"query": raw, "results": results})
}
