package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"cloud.google.com/go/storage"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Horn inspiration board: a fast personal capture of links and images that
// inspire, separate from verified market listings. Notes never leave the
// server except through the private search profile; only canonical URLs are
// fetched for previews.

type inspirationHorn struct {
	Maker        string `json:"maker"`
	Model        string `json:"model"`
	Acquired     bool   `json:"acquired"`
	ActiveOffers int    `json:"activeOffers"`
}

type inspirationImage struct {
	ID          uuid.UUID `json:"id"`
	Role        string    `json:"role"`
	Width       *int      `json:"width"`
	Height      *int      `json:"height"`
	ContentType string    `json:"contentType"`
	Position    int       `json:"position"`
}

type hornInspiration struct {
	ID              uuid.UUID          `json:"id"`
	ClientCaptureID uuid.UUID          `json:"clientCaptureId"`
	Kind            string             `json:"kind"`
	SourceURL       string             `json:"sourceUrl,omitempty"`
	CanonicalURL    string             `json:"canonicalUrl,omitempty"`
	Provider        string             `json:"provider"`
	ProviderMediaID string             `json:"providerMediaId,omitempty"`
	MediaFormat     string             `json:"mediaFormat,omitempty"`
	Title           string             `json:"title,omitempty"`
	AuthorName      string             `json:"authorName,omitempty"`
	SiteName        string             `json:"siteName,omitempty"`
	Description     string             `json:"description,omitempty"`
	MetadataStatus  string             `json:"metadataStatus"`
	MetadataError   string             `json:"metadataError,omitempty"`
	Maker           string             `json:"maker,omitempty"`
	Model           string             `json:"model,omitempty"`
	Tags            []string           `json:"tags"`
	Why             string             `json:"why"`
	Priority        string             `json:"priority"`
	PriceSeen       *float64           `json:"priceSeen"`
	PriceCurrency   string             `json:"priceCurrency"`
	PriceSeenOn     string             `json:"priceSeenOn,omitempty"`
	PriceAuto       bool               `json:"priceAuto"`
	HornID          *uuid.UUID         `json:"hornId,omitempty"`
	HornSummary     *inspirationHorn   `json:"horn,omitempty"`
	Pinned          bool               `json:"pinned"`
	Images          []inspirationImage `json:"images"`
	Embed           *inspirationEmbed  `json:"embed,omitempty"`
	Revision        int64              `json:"revision"`
	ArchivedAt      *time.Time         `json:"archivedAt,omitempty"`
	CreatedAt       time.Time          `json:"createdAt"`
	UpdatedAt       time.Time          `json:"updatedAt"`
}

var inspirationPriorities = []string{"inspiration", "someday", "want", "hunting"}

// Static tag suggestions (mirrored in inspiration-model.js); free text is allowed.
var inspirationTagSuggestions = []string{
	"raw brass", "lacquer", "silver", "gold plate", "black nickel", "mixed metals", "engraving", "patina", "satin",
	"one-piece bell", "two-piece bell", "upswept bell", "flip bell", "rose brass bell", "sterling bell", "large bell", "small bell",
	"reverse leadpipe", "standard leadpipe", "heavy leadpipe",
	"heavyweight", "lightweight", "vintage", "one-off", "artist model", "unusual engineering", "mbs technology",
	"heavy caps", "bottom sprung",
	"ml bore", "large bore",
}

const inspirationSeedCaptureID = "00000000-0000-4000-8000-0000000000a1"

// Overridable only by tests; production always uses the public endpoints.
var (
	youtubeOEmbedEndpoint = "https://www.youtube.com/oembed?url="
	tiktokOEmbedEndpoint  = "https://www.tiktok.com/oembed?url="
	youtubeThumbnailBase  = "https://i.ytimg.com/vi/"
)

// ---- Object storage -----------------------------------------------------------

// objectStore isolates the private bucket so tests need no GCS.
type objectStore interface {
	Put(ctx context.Context, name, contentType string, metadata map[string]string, body []byte) error
	Delete(ctx context.Context, name string) error
	SignedGet(ctx context.Context, name string, expires time.Time) (string, error)
}

type gcsObjectStore struct{ app *application }

func (s gcsObjectStore) Put(ctx context.Context, name, contentType string, metadata map[string]string, body []byte) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	w := s.app.storage.Bucket(s.app.cfg.Bucket).Object(name).NewWriter(ctx)
	w.ContentType = contentType
	w.Metadata = metadata
	if _, err := w.Write(body); err != nil {
		cancel()
		_ = w.Close()
		return err
	}
	return w.Close()
}

func (s gcsObjectStore) Delete(ctx context.Context, name string) error {
	err := s.app.storage.Bucket(s.app.cfg.Bucket).Object(name).Delete(ctx)
	if errors.Is(err, storage.ErrObjectNotExist) {
		return nil
	}
	return err
}

func (s gcsObjectStore) SignedGet(ctx context.Context, name string, expires time.Time) (string, error) {
	return s.app.signedObjectURL(ctx, name, expires, nil)
}

func (app *application) inspirationObjects() objectStore {
	if app.objects != nil {
		return app.objects
	}
	return gcsObjectStore{app: app}
}

func (app *application) inspirationFetchClient() *http.Client {
	if app.inspirationHTTP != nil {
		return app.inspirationHTTP
	}
	return defaultInspirationHTTP
}

var defaultInspirationHTTP = newSafeHTTPClient(nil, false)

func decodeImageConfig(body []byte) (int, int, error) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(body))
	return cfg.Width, cfg.Height, err
}

// ---- Validation ---------------------------------------------------------------

func normalizeInspirationTags(tags []string) ([]string, error) {
	out := []string{}
	seen := map[string]bool{}
	for _, t := range tags {
		t = strings.ToLower(strings.Join(strings.Fields(t), " "))
		if t == "" {
			continue
		}
		if len([]rune(t)) > 40 {
			return nil, errors.New("tags must be 1–40 characters")
		}
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	if len(out) > 20 {
		return nil, errors.New("at most 20 tags")
	}
	return out, nil
}

func cleanInspirationText(s string, limit int, field string) (string, error) {
	s = strings.Join(strings.Fields(s), " ")
	if len([]rune(s)) > limit {
		return "", fmt.Errorf("%s must be at most %d characters", field, limit)
	}
	return s, nil
}

func cleanInspirationWhy(s string) (string, error) {
	s = strings.TrimSpace(s)
	if len([]rune(s)) > 4000 {
		return "", errors.New("why must be at most 4000 characters")
	}
	return s, nil
}

// optional distinguishes an absent JSON field from an explicit null.
type optional[T any] struct {
	Set, Null bool
	Value     T
}

func (o *optional[T]) UnmarshalJSON(b []byte) error {
	o.Set = true
	if string(bytes.TrimSpace(b)) == "null" {
		o.Null = true
		return nil
	}
	return json.Unmarshal(b, &o.Value)
}

type inspirationPatch struct {
	ExpectedRevision *int64             `json:"expectedRevision"`
	URL              optional[string]   `json:"url"`
	Title            optional[string]   `json:"title"`
	Maker            optional[string]   `json:"maker"`
	Model            optional[string]   `json:"model"`
	Tags             optional[[]string] `json:"tags"`
	Why              optional[string]   `json:"why"`
	Priority         optional[string]   `json:"priority"`
	PriceSeen        optional[float64]  `json:"priceSeen"`
	PriceCurrency    optional[string]   `json:"priceCurrency"`
	PriceSeenOn      optional[string]   `json:"priceSeenOn"`
	HornID           optional[string]   `json:"hornId"`
	Pinned           optional[bool]     `json:"pinned"`
	Archived         optional[bool]     `json:"archived"`
	ImageOrder       optional[[]string] `json:"imageOrder"`

	// Derived by validateInspirationPatch.
	canonical, provider, mediaID, format string
	hornID                               *uuid.UUID
	imageOrder                           []uuid.UUID
}

// validateInspirationPatch normalizes a patch in place; it is pure so the
// rules (enums, lengths, currency, dates, price bounds) are unit-tested.
func validateInspirationPatch(p *inspirationPatch, today time.Time) error {
	var err error
	if p.ExpectedRevision == nil || *p.ExpectedRevision < 1 {
		return errors.New("expectedRevision required")
	}
	if p.URL.Set {
		if p.URL.Null || strings.TrimSpace(p.URL.Value) == "" {
			p.URL.Null, p.URL.Value = true, ""
		} else {
			p.URL.Value = strings.TrimSpace(p.URL.Value)
			if p.canonical, p.provider, p.mediaID, p.format, err = canonicalInspirationURL(p.URL.Value); err != nil {
				return err
			}
			p.URL.Value = cleanSourceURL(p.URL.Value)
		}
	}
	for _, f := range []struct {
		o     *optional[string]
		limit int
		name  string
	}{{&p.Title, 300, "title"}, {&p.Maker, 80, "maker"}, {&p.Model, 120, "model"}} {
		if f.o.Set && !f.o.Null {
			if f.o.Value, err = cleanInspirationText(f.o.Value, f.limit, f.name); err != nil {
				return err
			}
		}
	}
	if p.Tags.Set {
		if p.Tags.Value, err = normalizeInspirationTags(p.Tags.Value); err != nil {
			return err
		}
	}
	if p.Why.Set && !p.Why.Null {
		if p.Why.Value, err = cleanInspirationWhy(p.Why.Value); err != nil {
			return err
		}
	}
	if p.Priority.Set && !containsString(inspirationPriorities, p.Priority.Value) {
		return errors.New("priority must be inspiration, someday, want or hunting")
	}
	if p.PriceSeen.Set && !p.PriceSeen.Null {
		v := p.PriceSeen.Value
		if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > 1000000 {
			return errors.New("price must be between 0 and 1,000,000")
		}
		p.PriceSeen.Value = math.Round(v*100) / 100
	}
	if p.PriceCurrency.Set {
		p.PriceCurrency.Value = strings.ToUpper(strings.TrimSpace(p.PriceCurrency.Value))
		if p.PriceCurrency.Null || !currencyPattern.MatchString(p.PriceCurrency.Value) {
			return errors.New("ISO currency required")
		}
	}
	if p.PriceSeenOn.Set && !p.PriceSeenOn.Null && strings.TrimSpace(p.PriceSeenOn.Value) != "" {
		d, e := time.Parse("2006-01-02", strings.TrimSpace(p.PriceSeenOn.Value))
		if e != nil {
			return errors.New("priceSeenOn must be YYYY-MM-DD")
		}
		// One day of slack for local dates ahead of UTC.
		if d.After(today.UTC().Truncate(24 * time.Hour).Add(24 * time.Hour)) {
			return errors.New("priceSeenOn cannot be in the future")
		}
		p.PriceSeenOn.Value = d.Format("2006-01-02")
	} else if p.PriceSeenOn.Set {
		p.PriceSeenOn.Null, p.PriceSeenOn.Value = true, ""
	}
	if p.HornID.Set && !p.HornID.Null {
		id, e := uuid.Parse(p.HornID.Value)
		if e != nil {
			return errors.New("invalid horn id")
		}
		p.hornID = &id
	}
	if p.ImageOrder.Set {
		if len(p.ImageOrder.Value) > inspirationMaxImages {
			return errors.New("too many images")
		}
		for _, raw := range p.ImageOrder.Value {
			id, e := uuid.Parse(raw)
			if e != nil {
				return errors.New("invalid image id")
			}
			p.imageOrder = append(p.imageOrder, id)
		}
	}
	return nil
}

type inspirationCreate struct {
	ClientCaptureID string   `json:"clientCaptureId"`
	URL             string   `json:"url"`
	Kind            string   `json:"kind"`
	Why             string   `json:"why"`
	Priority        string   `json:"priority"`
	Maker           string   `json:"maker"`
	Model           string   `json:"model"`
	Tags            []string `json:"tags"`
}

func validateInspirationCreate(in *inspirationCreate) (uuid.UUID, string, string, string, string, error) {
	capture, err := uuid.Parse(in.ClientCaptureID)
	if err != nil {
		return uuid.Nil, "", "", "", "", errors.New("clientCaptureId must be a UUID")
	}
	in.URL = strings.TrimSpace(in.URL)
	if in.Kind == "" {
		in.Kind = "image"
		if in.URL != "" {
			in.Kind = "link"
		}
	}
	if in.Kind != "link" && in.Kind != "image" {
		return uuid.Nil, "", "", "", "", errors.New("kind must be link or image")
	}
	if in.Kind == "link" && in.URL == "" {
		return uuid.Nil, "", "", "", "", errors.New("link required")
	}
	canonical, provider, mediaID, format := "", "upload", "", "image"
	if in.URL != "" {
		if canonical, provider, mediaID, format, err = canonicalInspirationURL(in.URL); err != nil {
			return uuid.Nil, "", "", "", "", err
		}
		in.URL = cleanSourceURL(in.URL)
	}
	if in.Priority == "" {
		in.Priority = "someday"
	}
	if !containsString(inspirationPriorities, in.Priority) {
		return uuid.Nil, "", "", "", "", errors.New("invalid priority")
	}
	if in.Why, err = cleanInspirationWhy(in.Why); err != nil {
		return uuid.Nil, "", "", "", "", err
	}
	if in.Maker, err = cleanInspirationText(in.Maker, 80, "maker"); err != nil {
		return uuid.Nil, "", "", "", "", err
	}
	if in.Model, err = cleanInspirationText(in.Model, 120, "model"); err != nil {
		return uuid.Nil, "", "", "", "", err
	}
	if in.Tags, err = normalizeInspirationTags(in.Tags); err != nil {
		return uuid.Nil, "", "", "", "", err
	}
	return capture, canonical, provider, mediaID, format, nil
}

// ---- Loading ------------------------------------------------------------------

type inspirationQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

const inspirationSelect = `SELECT i.id,i.client_capture_id,i.kind,COALESCE(i.source_url,''),COALESCE(i.canonical_url,''),i.provider,COALESCE(i.provider_media_id,''),COALESCE(i.media_format,''),
 COALESCE(i.title,''),COALESCE(i.author_name,''),COALESCE(i.site_name,''),COALESCE(i.description,''),i.metadata_status,i.metadata_error,
 COALESCE(i.maker,''),COALESCE(i.model,''),i.tags,i.why,i.priority,i.price_seen::float8,i.price_currency,COALESCE(to_char(i.price_seen_on,'YYYY-MM-DD'),''),i.price_auto,
 i.horn_id,i.pinned,i.revision,i.archived_at,i.created_at,i.updated_at,
 COALESCE((SELECT jsonb_agg(jsonb_build_object('id',m.id,'role',m.role,'width',m.width,'height',m.height,'contentType',m.content_type,'position',m.position) ORDER BY m.role DESC,m.position,m.created_at) FROM horn_inspiration_images m WHERE m.inspiration_id=i.id),'[]'),
 (SELECT jsonb_build_object('maker',h.maker,'model',h.model,'acquired',h.acquired,'activeOffers',(SELECT count(*) FROM trumpet_listings l WHERE l.horn_id=h.id AND l.status='active' AND l.verification_state='verified')) FROM trumpet_horns h WHERE h.id=i.horn_id AND h.user_id=i.user_id)
 FROM horn_inspirations i`

func scanInspiration(row pgx.Row) (hornInspiration, error) {
	var x hornInspiration
	var tags, images, horn []byte
	err := row.Scan(&x.ID, &x.ClientCaptureID, &x.Kind, &x.SourceURL, &x.CanonicalURL, &x.Provider, &x.ProviderMediaID, &x.MediaFormat,
		&x.Title, &x.AuthorName, &x.SiteName, &x.Description, &x.MetadataStatus, &x.MetadataError,
		&x.Maker, &x.Model, &tags, &x.Why, &x.Priority, &x.PriceSeen, &x.PriceCurrency, &x.PriceSeenOn, &x.PriceAuto,
		&x.HornID, &x.Pinned, &x.Revision, &x.ArchivedAt, &x.CreatedAt, &x.UpdatedAt, &images, &horn)
	if err != nil {
		return x, err
	}
	if err = json.Unmarshal(tags, &x.Tags); err != nil {
		return x, err
	}
	if err = json.Unmarshal(images, &x.Images); err != nil {
		return x, err
	}
	if len(horn) > 0 {
		x.HornSummary = &inspirationHorn{}
		if err = json.Unmarshal(horn, x.HornSummary); err != nil {
			return x, err
		}
	}
	if x.Tags == nil {
		x.Tags = []string{}
	}
	x.Embed = inspirationEmbedFor(x.Provider, x.ProviderMediaID, x.MediaFormat)
	return x, nil
}

func loadInspiration(ctx context.Context, q inspirationQuerier, user, id uuid.UUID) (hornInspiration, error) {
	return scanInspiration(q.QueryRow(ctx, inspirationSelect+` WHERE i.id=$1 AND i.user_id=$2`, id, user))
}

func loadInspirations(ctx context.Context, q inspirationQuerier, user uuid.UUID, archived bool) ([]hornInspiration, error) {
	rows, err := q.Query(ctx, inspirationSelect+` WHERE i.user_id=$1 AND (i.archived_at IS NOT NULL)=$2 ORDER BY i.pinned DESC,i.created_at DESC,i.id`, user, archived)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []hornInspiration{}
	for rows.Next() {
		x, err := scanInspiration(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

func mergeVocab(lists ...[]string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, list := range lists {
		for _, v := range list {
			v = strings.TrimSpace(v)
			if v != "" && !seen[strings.ToLower(v)] {
				seen[strings.ToLower(v)] = true
				out = append(out, v)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return strings.ToLower(out[i]) < strings.ToLower(out[j]) })
	return out
}

func (app *application) listInspirations(w http.ResponseWriter, r *http.Request) {
	user, err := app.userID(r.Context())
	if err != nil {
		app.serverError(w, err)
		return
	}
	items, err := loadInspirations(r.Context(), app.db, user, r.URL.Query().Get("archived") == "1")
	if err != nil {
		app.serverError(w, err)
		return
	}
	var makers, tags []string
	if err = app.db.QueryRow(r.Context(), `SELECT COALESCE(array_agg(DISTINCT maker) FILTER (WHERE COALESCE(maker,'')<>''),'{}'),
 COALESCE((SELECT array_agg(DISTINCT t) FROM horn_inspirations x, jsonb_array_elements_text(x.tags) t WHERE x.user_id=$1),'{}')
 FROM horn_inspirations WHERE user_id=$1`, user).Scan(&makers, &tags); err != nil {
		app.serverError(w, err)
		return
	}
	var live int
	if err = app.db.QueryRow(r.Context(), `SELECT count(*) FROM horn_inspirations WHERE user_id=$1 AND archived_at IS NULL`, user).Scan(&live); err != nil {
		app.serverError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"inspirations": items, "liveCount": live, "vocab": map[string][]string{
		"makers": mergeVocab(makers, trumpetPriorityMakers),
		"tags":   mergeVocab(tags, inspirationTagSuggestions, trumpetPositiveTraits),
	}})
}

func (app *application) inspirationProfile(ctx context.Context, user uuid.UUID) ([]map[string]any, error) {
	rows, err := app.db.Query(ctx, `SELECT COALESCE(maker,''),COALESCE(model,''),tags,priority,why FROM horn_inspirations
 WHERE user_id=$1 AND archived_at IS NULL ORDER BY pinned DESC,created_at DESC LIMIT 100`, user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var maker, model, priority, why string
		var raw []byte
		if err = rows.Scan(&maker, &model, &raw, &priority, &why); err != nil {
			return nil, err
		}
		tags := []string{}
		_ = json.Unmarshal(raw, &tags)
		out = append(out, map[string]any{"maker": maker, "model": model, "tags": tags, "priority": priority, "why": why})
	}
	return out, rows.Err()
}

// ---- Create / seed --------------------------------------------------------------

func isUniqueViolation(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "23505"
}

// findInspirationDuplicate prefers a live match; otherwise the newest archived one.
func findInspirationDuplicate(ctx context.Context, q inspirationQuerier, user uuid.UUID, canonical, provider, mediaID string, except uuid.UUID) (uuid.UUID, bool, error) {
	var id uuid.UUID
	var archived bool
	err := q.QueryRow(ctx, `SELECT id,archived_at IS NOT NULL FROM horn_inspirations WHERE user_id=$1 AND id<>$5
 AND (canonical_url=$2 OR ($4<>'' AND provider=$3 AND provider_media_id=$4))
 ORDER BY (archived_at IS NULL) DESC,archived_at DESC NULLS FIRST LIMIT 1`, user, canonical, provider, mediaID, except).Scan(&id, &archived)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, false, nil
	}
	return id, archived, err
}

func (app *application) writeInspirationDuplicate(w http.ResponseWriter, r *http.Request, q inspirationQuerier, user, id uuid.UUID, archived bool) {
	existing, err := loadInspiration(r.Context(), q, user, id)
	if err != nil {
		app.serverError(w, err)
		return
	}
	body := map[string]any{"duplicate": true, "inspiration": existing, "error": "already on your board"}
	if archived {
		body["archived"] = true
	}
	writeJSON(w, 409, body)
}

func (app *application) createInspiration(w http.ResponseWriter, r *http.Request) {
	var in inspirationCreate
	if err := decodeTrumpetJSON(w, r, &in); err != nil {
		writeError(w, 400, "invalid inspiration JSON")
		return
	}
	capture, canonical, provider, mediaID, format, err := validateInspirationCreate(&in)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	user, err := app.userID(r.Context())
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
	if err = lockTrumpetOwner(r.Context(), tx, user); err != nil {
		app.serverError(w, err)
		return
	}
	var existing uuid.UUID
	err = tx.QueryRow(r.Context(), `SELECT id FROM horn_inspirations WHERE user_id=$1 AND client_capture_id=$2`, user, capture).Scan(&existing)
	if err == nil {
		row, e := loadInspiration(r.Context(), tx, user, existing)
		if e != nil {
			app.serverError(w, e)
			return
		}
		writeJSON(w, 200, row)
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		app.serverError(w, err)
		return
	}
	if canonical != "" {
		dup, archived, e := findInspirationDuplicate(r.Context(), tx, user, canonical, provider, mediaID, uuid.Nil)
		if e != nil {
			app.serverError(w, e)
			return
		}
		if dup != uuid.Nil {
			app.writeInspirationDuplicate(w, r, tx, user, dup, archived)
			return
		}
	}
	status := "skipped"
	if in.Kind == "link" {
		status = "pending"
	}
	id := uuid.New()
	tags, _ := json.Marshal(in.Tags)
	_, err = tx.Exec(r.Context(), `INSERT INTO horn_inspirations(id,user_id,client_capture_id,kind,source_url,canonical_url,provider,provider_media_id,media_format,metadata_status,maker,model,tags,why,priority)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`, id, user, capture, in.Kind, nullText(in.URL), nullText(canonical), provider, nullText(mediaID), nullText(format), status, nullText(in.Maker), nullText(in.Model), tags, in.Why, in.Priority)
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, 409, "already on your board")
			return
		}
		app.serverError(w, err)
		return
	}
	row, err := loadInspiration(r.Context(), tx, user, id)
	if err != nil {
		app.serverError(w, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		app.serverError(w, err)
		return
	}
	writeJSON(w, 201, row)
}

func (app *application) seedInspiration(w http.ResponseWriter, r *http.Request) {
	var input struct{}
	if err := decodeTrumpetJSON(w, r, &input); err != nil {
		writeError(w, 400, "expected empty JSON object")
		return
	}
	user, err := app.userID(r.Context())
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
	if err = lockTrumpetOwner(r.Context(), tx, user); err != nil {
		app.serverError(w, err)
		return
	}
	tag, err := tx.Exec(r.Context(), `INSERT INTO horn_inspiration_seeds(user_id,seed_key) VALUES($1,$2) ON CONFLICT DO NOTHING`, user, inspirationSeedCaptureID)
	if err != nil {
		app.serverError(w, err)
		return
	}
	added := 0
	if tag.RowsAffected() == 1 {
		const source = "https://youtube.com/shorts/qHetQ-t4Wi0"
		canonical, provider, mediaID, format, _ := canonicalInspirationURL(source)
		dup, _, e := findInspirationDuplicate(r.Context(), tx, user, canonical, provider, mediaID, uuid.Nil)
		if e != nil {
			app.serverError(w, e)
			return
		}
		if dup == uuid.Nil {
			// Model is filled only from the fetched title during enrichment.
			_, err = tx.Exec(r.Context(), `INSERT INTO horn_inspirations(id,user_id,client_capture_id,kind,source_url,canonical_url,provider,provider_media_id,media_format,metadata_status,maker,why,priority)
 VALUES($1,$2,$3,'link',$4,$5,$6,$7,$8,'pending','Harrelson',$9,'someday') ON CONFLICT DO NOTHING`, uuid.New(), user, uuid.MustParse(inspirationSeedCaptureID), source, canonical, provider, mediaID, format, "Some day I may want a Harrelson like this one.")
			if err != nil {
				app.serverError(w, err)
				return
			}
			added = 1
		}
	}
	if err = tx.Commit(r.Context()); err != nil {
		app.serverError(w, err)
		return
	}
	writeJSON(w, 200, map[string]int{"added": added})
}

// ---- Patch / delete -------------------------------------------------------------

func (app *application) patchInspiration(w http.ResponseWriter, r *http.Request) {
	var p inspirationPatch
	if err := decodeTrumpetJSON(w, r, &p); err != nil {
		writeError(w, 400, "invalid inspiration patch JSON")
		return
	}
	if err := validateInspirationPatch(&p, time.Now()); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, 400, "invalid inspiration id")
		return
	}
	user, err := app.userID(r.Context())
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
	if err = lockTrumpetOwner(r.Context(), tx, user); err != nil {
		app.serverError(w, err)
		return
	}
	current, err := loadInspiration(r.Context(), tx, user, id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, 404, "inspiration not found")
		return
	}
	if err != nil {
		app.serverError(w, err)
		return
	}
	if current.Revision != *p.ExpectedRevision {
		writeJSON(w, 409, map[string]any{"error": "updated on another device", "conflict": true, "inspiration": current})
		return
	}
	sets := []string{}
	args := []any{id, user}
	set := func(column string, value any) {
		args = append(args, value)
		sets = append(sets, fmt.Sprintf("%s=$%d", column, len(args)))
	}
	if p.URL.Set {
		if p.URL.Null {
			if current.Kind == "link" {
				writeError(w, 400, "a link entry needs its link")
				return
			}
			set("source_url", nil)
			set("canonical_url", nil)
			set("provider", "upload")
			set("provider_media_id", nil)
			set("media_format", "image")
			set("metadata_status", "skipped")
		} else if p.canonical != current.CanonicalURL || p.URL.Value != current.SourceURL {
			dup, archived, e := findInspirationDuplicate(r.Context(), tx, user, p.canonical, p.provider, p.mediaID, id)
			if e != nil {
				app.serverError(w, e)
				return
			}
			if dup != uuid.Nil && (!archived || current.ArchivedAt != nil) {
				app.writeInspirationDuplicate(w, r, tx, user, dup, archived)
				return
			}
			set("source_url", p.URL.Value)
			set("canonical_url", p.canonical)
			set("provider", p.provider)
			set("provider_media_id", nullText(p.mediaID))
			set("media_format", p.format)
			set("metadata_status", "pending")
			set("metadata_error", "")
			if !p.Title.Set {
				set("title", nil)
			}
			set("author_name", nil)
			set("site_name", nil)
			set("description", nil)
		}
	}
	if p.Title.Set {
		set("title", nullText(p.Title.Value))
	}
	if p.Maker.Set {
		set("maker", nullText(p.Maker.Value))
	}
	if p.Model.Set {
		set("model", nullText(p.Model.Value))
	}
	if p.Tags.Set {
		tags, _ := json.Marshal(p.Tags.Value)
		set("tags", tags)
	}
	if p.Why.Set {
		set("why", p.Why.Value)
	}
	if p.Priority.Set {
		set("priority", p.Priority.Value)
	}
	if p.PriceSeen.Set {
		if p.PriceSeen.Null {
			set("price_seen", nil)
		} else {
			set("price_seen", p.PriceSeen.Value)
			if !p.PriceSeenOn.Set && current.PriceSeenOn == "" {
				set("price_seen_on", time.Now().UTC().Truncate(24*time.Hour))
			}
		}
		set("price_auto", false)
	}
	if p.PriceCurrency.Set {
		set("price_currency", p.PriceCurrency.Value)
	}
	if p.PriceSeenOn.Set {
		set("price_seen_on", dateValue(p.PriceSeenOn.Value))
	}
	if p.HornID.Set {
		if p.hornID != nil {
			var owned bool
			if err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM trumpet_horns WHERE id=$1 AND user_id=$2)`, *p.hornID, user).Scan(&owned); err != nil {
				app.serverError(w, err)
				return
			}
			if !owned {
				writeError(w, 400, "horn not found")
				return
			}
			set("horn_id", *p.hornID)
		} else {
			set("horn_id", nil)
		}
	}
	if p.Pinned.Set {
		set("pinned", p.Pinned.Value)
	}
	if p.Archived.Set {
		if p.Archived.Value {
			if current.ArchivedAt == nil {
				set("archived_at", time.Now().UTC())
			}
		} else if current.ArchivedAt != nil {
			canonical := current.CanonicalURL
			provider, mediaID := current.Provider, current.ProviderMediaID
			if p.canonical != "" {
				canonical, provider, mediaID = p.canonical, p.provider, p.mediaID
			}
			if canonical != "" {
				var live uuid.UUID
				e := tx.QueryRow(r.Context(), `SELECT id FROM horn_inspirations WHERE user_id=$1 AND id<>$2 AND archived_at IS NULL AND (canonical_url=$3 OR ($5<>'' AND provider=$4 AND provider_media_id=$5)) LIMIT 1`, user, id, canonical, provider, mediaID).Scan(&live)
				if e == nil {
					app.writeInspirationDuplicate(w, r, tx, user, live, false)
					return
				}
				if !errors.Is(e, pgx.ErrNoRows) {
					app.serverError(w, e)
					return
				}
			}
			set("archived_at", nil)
		}
	}
	if p.ImageOrder.Set {
		for i, imageID := range p.imageOrder {
			tag, e := tx.Exec(r.Context(), `UPDATE horn_inspiration_images SET position=$4 WHERE id=$1 AND inspiration_id=$2 AND user_id=$3`, imageID, id, user, i)
			if e != nil {
				app.serverError(w, e)
				return
			}
			if tag.RowsAffected() != 1 {
				writeError(w, 400, "image not found on this entry")
				return
			}
		}
	}
	sets = append(sets, "revision=revision+1", "updated_at=now()")
	if _, err = tx.Exec(r.Context(), `UPDATE horn_inspirations SET `+strings.Join(sets, ",")+` WHERE id=$1 AND user_id=$2`, args...); err != nil {
		if isUniqueViolation(err) {
			writeError(w, 409, "already on your board")
			return
		}
		app.serverError(w, err)
		return
	}
	row, err := loadInspiration(r.Context(), tx, user, id)
	if err != nil {
		app.serverError(w, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		app.serverError(w, err)
		return
	}
	writeJSON(w, 200, row)
}

// deleteObjectsBestEffort never blocks a delete on storage; orphans are logged
// and may be swept manually (see docs/trumpets/README.md).
func (app *application) deleteObjectsBestEffort(ctx context.Context, names []string) {
	store := app.inspirationObjects()
	for _, name := range names {
		c, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		if err := store.Delete(c, name); err != nil {
			app.logger.Error("inspiration object delete failed; orphaned object", "object", name, "error", err)
		}
		cancel()
	}
}

func (app *application) deleteInspiration(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, 400, "invalid inspiration id")
		return
	}
	user, err := app.userID(r.Context())
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
	rows, err := tx.Query(r.Context(), `SELECT object_name FROM horn_inspiration_images WHERE inspiration_id=$1 AND user_id=$2`, id, user)
	if err != nil {
		app.serverError(w, err)
		return
	}
	names := []string{}
	for rows.Next() {
		var n string
		if err = rows.Scan(&n); err != nil {
			rows.Close()
			app.serverError(w, err)
			return
		}
		names = append(names, n)
	}
	rows.Close()
	tag, err := tx.Exec(r.Context(), `DELETE FROM horn_inspirations WHERE id=$1 AND user_id=$2`, id, user)
	if err != nil {
		app.serverError(w, err)
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, 404, "inspiration not found")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		app.serverError(w, err)
		return
	}
	app.deleteObjectsBestEffort(r.Context(), names)
	w.WriteHeader(http.StatusNoContent)
}

// ---- Images ---------------------------------------------------------------------

func inspirationObjectName(user, inspiration, imageID uuid.UUID, contentType string) string {
	return "inspiration/" + user.String() + "/" + inspiration.String() + "/" + imageID.String() + "." + inspirationImageTypes[contentType]
}

func optionalDimension(raw string) *int {
	v, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || v <= 0 || v > inspirationMaxPixels {
		return nil
	}
	return &v
}

func (app *application) uploadInspirationImage(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, 400, "invalid inspiration id")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, inspirationUploadLimit))
	if err != nil {
		writeError(w, 413, "image must be 10 MB or smaller")
		return
	}
	img, err := sniffInspirationImage(body, r.Header.Get("Content-Type"))
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	if img.ContentType == "image/webp" {
		img.Width, img.Height = optionalDimension(r.Header.Get("X-Image-Width")), optionalDimension(r.Header.Get("X-Image-Height"))
	}
	user, err := app.userID(r.Context())
	if err != nil {
		app.serverError(w, err)
		return
	}
	var count, uploads int
	err = app.db.QueryRow(r.Context(), `SELECT count(m.id),count(m.id) FILTER (WHERE m.role='upload') FROM horn_inspirations i LEFT JOIN horn_inspiration_images m ON m.inspiration_id=i.id WHERE i.id=$1 AND i.user_id=$2 GROUP BY i.id`, id, user).Scan(&count, &uploads)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, 404, "inspiration not found")
		return
	}
	if err != nil {
		app.serverError(w, err)
		return
	}
	if count >= inspirationMaxImages {
		writeError(w, 400, "at most 20 images per entry")
		return
	}
	position := uploads
	if raw := r.Header.Get("X-Image-Position"); raw != "" {
		if v, e := strconv.Atoi(raw); e == nil && v >= 0 && v < inspirationMaxImages {
			position = v
		}
	}
	if position >= inspirationMaxImages {
		position = inspirationMaxImages - 1
	}
	imageID := uuid.New()
	sum := sha256.Sum256(body)
	name := inspirationObjectName(user, id, imageID, img.ContentType)
	if err = app.inspirationObjects().Put(r.Context(), name, img.ContentType, map[string]string{"userId": user.String(), "inspirationId": id.String(), "imageId": imageID.String(), "role": "upload"}, body); err != nil {
		app.serverError(w, err)
		return
	}
	_, err = app.db.Exec(r.Context(), `INSERT INTO horn_inspiration_images(id,inspiration_id,user_id,role,object_name,content_type,size_bytes,width,height,sha256,position)
 SELECT $1,$2,$3,'upload',$4,$5,$6,$7,$8,$9,$10 WHERE EXISTS(SELECT 1 FROM horn_inspirations WHERE id=$2 AND user_id=$3)`, imageID, id, user, name, img.ContentType, len(body), img.Width, img.Height, hex.EncodeToString(sum[:]), position)
	if err != nil {
		app.deleteObjectsBestEffort(r.Context(), []string{name})
		app.serverError(w, err)
		return
	}
	_, _ = app.db.Exec(r.Context(), `UPDATE horn_inspirations SET updated_at=now() WHERE id=$1 AND user_id=$2`, id, user)
	writeJSON(w, 201, map[string]any{"image": inspirationImage{ID: imageID, Role: "upload", Width: img.Width, Height: img.Height, ContentType: img.ContentType, Position: position}})
}

func (app *application) deleteInspirationImage(w http.ResponseWriter, r *http.Request) {
	id, err1 := uuid.Parse(r.PathValue("id"))
	imageID, err2 := uuid.Parse(r.PathValue("imageId"))
	if err1 != nil || err2 != nil {
		writeError(w, 400, "invalid id")
		return
	}
	user, err := app.userID(r.Context())
	if err != nil {
		app.serverError(w, err)
		return
	}
	var name string
	err = app.db.QueryRow(r.Context(), `DELETE FROM horn_inspiration_images WHERE id=$1 AND inspiration_id=$2 AND user_id=$3 RETURNING object_name`, imageID, id, user).Scan(&name)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, 404, "image not found")
		return
	}
	if err != nil {
		app.serverError(w, err)
		return
	}
	app.deleteObjectsBestEffort(r.Context(), []string{name})
	writeJSON(w, 200, map[string]bool{"deleted": true})
}

// inspirationImageRedirect keeps the bucket private: authenticated viewers get
// a 302 to a ten-minute signed URL; no image URL is permanent.
func (app *application) inspirationImageRedirect(w http.ResponseWriter, r *http.Request) {
	imageID, err := uuid.Parse(r.PathValue("imageId"))
	if err != nil {
		writeError(w, 400, "invalid image id")
		return
	}
	user, err := app.userID(r.Context())
	if err != nil {
		app.serverError(w, err)
		return
	}
	var name string
	err = app.db.QueryRow(r.Context(), `SELECT object_name FROM horn_inspiration_images WHERE id=$1 AND user_id=$2`, imageID, user).Scan(&name)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, 404, "image not found")
		return
	}
	if err != nil {
		app.serverError(w, err)
		return
	}
	signed, err := app.inspirationObjects().SignedGet(r.Context(), name, time.Now().Add(10*time.Minute))
	if err != nil {
		app.serverError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "private, max-age=300")
	http.Redirect(w, r, signed, http.StatusFound)
}

// ---- Enrichment -------------------------------------------------------------------

type enrichOutcome struct {
	Title, AuthorName, SiteName, Description string
	ImageURL                                 string
	Price                                    *float64
	Currency                                 string
	Status, Error                            string
}

func shortFetchError(err error) string {
	if errors.Is(err, errBlockedAddress) {
		return "blocked address"
	}
	msg := err.Error()
	if errors.Is(err, context.DeadlineExceeded) || strings.Contains(msg, "Client.Timeout") || strings.Contains(msg, "deadline exceeded") {
		return "timed out"
	}
	if strings.Contains(msg, "blocked port") {
		return "blocked port"
	}
	if strings.HasPrefix(msg, "HTTP ") || msg == "response too large" || msg == "too many redirects" {
		return msg
	}
	if strings.Contains(msg, "too many redirects") || strings.Contains(msg, "stopped after") {
		return "too many redirects"
	}
	if strings.Contains(msg, "no such host") {
		return "host not found"
	}
	return truncateRunes("fetch failed", 200)
}

// enrichLink fetches preview metadata for a canonical URL. Only the canonical
// URL leaves the server; notes are never sent.
func enrichLink(ctx context.Context, client *http.Client, canonical, provider, mediaID, format string) enrichOutcome {
	var out enrichOutcome
	switch {
	case provider == "youtube" && mediaID != "":
		out.SiteName = "YouTube"
		out.ImageURL = youtubeThumbnailBase + mediaID + "/hqdefault.jpg"
		o, err := fetchOEmbed(ctx, client, youtubeOEmbedEndpoint, canonical)
		if err != nil {
			out.Status, out.Error = "partial", "oEmbed: "+shortFetchError(err)
			if errors.Is(err, errBlockedAddress) {
				out.Status, out.Error = "failed", "blocked address"
			}
			return out
		}
		out.Title, out.AuthorName, out.Status = o.Title, o.AuthorName, "fetched"
		if u := resolveHTTPURL(nil, o.ThumbnailURL); u != "" {
			out.ImageURL = u
		}
		return out
	case provider == "tiktok":
		if o, err := fetchOEmbed(ctx, client, tiktokOEmbedEndpoint, canonical); err == nil && o.Title != "" {
			out.Title, out.AuthorName, out.SiteName, out.Status = o.Title, o.AuthorName, "TikTok", "fetched"
			out.ImageURL = resolveHTTPURL(nil, o.ThumbnailURL)
			return out
		}
		fallthrough
	case provider == "instagram" || provider == "facebook":
		body, _, final, err := fetchLimited(ctx, client, canonical, inspirationHTMLLimit, "text/html,application/xhtml+xml")
		generic := genericSocialTitle(provider, format)
		if err != nil {
			out.Title, out.Status, out.Error = generic, "partial", shortFetchError(err)
			if errors.Is(err, errBlockedAddress) {
				out.Title, out.Status = "", "failed"
			}
			return out
		}
		m := parsePageMetadata(body, final)
		if isLoginWall(m) {
			out.Title, out.Status, out.Error = generic, "partial", "login wall; add a screenshot for a preview"
			return out
		}
		out.Title, out.SiteName, out.Description, out.ImageURL, out.Status = m.Title, m.SiteName, m.Description, m.Image, "fetched"
		return out
	}
	body, _, final, err := fetchLimited(ctx, client, canonical, inspirationHTMLLimit, "text/html,application/xhtml+xml")
	if err != nil {
		out.Status, out.Error = "failed", shortFetchError(err)
		return out
	}
	m := parsePageMetadata(body, final)
	out.Title, out.SiteName, out.Description, out.ImageURL = m.Title, m.SiteName, m.Description, m.Image
	out.Price, out.Currency = m.Price, m.Currency
	out.Status = "fetched"
	if out.Title == "" {
		out.Status, out.Error = "partial", "no title found"
	}
	return out
}

func fetchInspirationThumbnail(ctx context.Context, client *http.Client, raw string) ([]byte, sniffedImage, error) {
	body, _, _, err := fetchLimited(ctx, client, raw, inspirationImageLimit, "image/avif,image/webp,image/png,image/jpeg,image/gif;q=0.9,*/*;q=0.1")
	if err != nil {
		return nil, sniffedImage{}, err
	}
	img, err := sniffInspirationImage(body, "")
	return body, img, err
}

var inspirationEnriching sync.Map

func (app *application) enrichInspiration(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, 400, "invalid inspiration id")
		return
	}
	user, err := app.userID(r.Context())
	if err != nil {
		app.serverError(w, err)
		return
	}
	current, err := loadInspiration(r.Context(), app.db, user, id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, 404, "inspiration not found")
		return
	}
	if err != nil {
		app.serverError(w, err)
		return
	}
	force := r.URL.Query().Get("force") == "1"
	if current.CanonicalURL == "" || (current.MetadataStatus == "fetched" && !force) {
		writeJSON(w, 200, current)
		return
	}
	// One enrichment per entry at a time; a concurrent caller gets the current row.
	if _, busy := inspirationEnriching.LoadOrStore(id, true); busy {
		writeJSON(w, 200, current)
		return
	}
	defer inspirationEnriching.Delete(id)
	ctx, cancel := context.WithTimeout(r.Context(), inspirationTotalLimit)
	defer cancel()
	client := app.inspirationFetchClient()
	out := enrichLink(ctx, client, current.CanonicalURL, current.Provider, current.ProviderMediaID, current.MediaFormat)
	hasThumb := false
	for _, im := range current.Images {
		if im.Role == "thumbnail" {
			hasThumb = true
		}
	}
	if out.ImageURL != "" && !hasThumb && len(current.Images) < inspirationMaxImages {
		body, img, e := fetchInspirationThumbnail(ctx, client, out.ImageURL)
		if e == nil {
			e = app.storeInspirationThumbnail(r.Context(), user, id, out.ImageURL, body, img)
		}
		if e != nil {
			if out.Status == "fetched" {
				out.Status, out.Error = "partial", "thumbnail unavailable"
			} else if out.Status == "partial" && current.Provider == "youtube" {
				out.Status = "failed"
			}
		}
	} else if out.ImageURL == "" && out.Status == "partial" && current.Provider == "youtube" {
		out.Status = "failed"
	}
	out.Error = truncateRunes(out.Error, 200)
	today := time.Now().UTC().Truncate(24 * time.Hour)
	_, err = app.db.Exec(r.Context(), `UPDATE horn_inspirations SET
 title=CASE WHEN COALESCE(title,'')='' AND $3<>'' THEN $3 ELSE title END,
 author_name=COALESCE(NULLIF($4,''),author_name), site_name=COALESCE(NULLIF($5,''),site_name), description=COALESCE(NULLIF($6,''),description),
 price_auto=CASE WHEN price_seen IS NULL AND $7::numeric IS NOT NULL THEN true ELSE price_auto END,
 price_currency=CASE WHEN price_seen IS NULL AND $7::numeric IS NOT NULL THEN $8 ELSE price_currency END,
 price_seen_on=CASE WHEN price_seen IS NULL AND $7::numeric IS NOT NULL THEN $9::date ELSE price_seen_on END,
 price_seen=CASE WHEN price_seen IS NULL AND $7::numeric IS NOT NULL THEN $7::numeric ELSE price_seen END,
 metadata_status=$10, metadata_error=$11, metadata_fetched_at=now(), updated_at=now()
 WHERE id=$1 AND user_id=$2 AND canonical_url=$12`, id, user, out.Title, out.AuthorName, out.SiteName, out.Description, out.Price, firstNonEmpty(out.Currency, "USD"), today, out.Status, out.Error, current.CanonicalURL)
	if err != nil {
		app.serverError(w, err)
		return
	}
	// The seed's model comes only from its fetched title ("1st Harrelson Muse with MBS Technology").
	if current.ClientCaptureID.String() == inspirationSeedCaptureID && out.Title != "" {
		lower := strings.ToLower(out.Title)
		if strings.Contains(lower, "muse") {
			_, _ = app.db.Exec(r.Context(), `UPDATE horn_inspirations SET model='MUSE' WHERE id=$1 AND user_id=$2 AND COALESCE(model,'')='' AND lower(COALESCE(maker,''))='harrelson'`, id, user)
		}
		if strings.Contains(lower, "mbs technology") {
			_, _ = app.db.Exec(r.Context(), `UPDATE horn_inspirations SET tags=tags||'["mbs technology"]'::jsonb WHERE id=$1 AND user_id=$2 AND NOT tags ? 'mbs technology' AND jsonb_array_length(tags)<20`, id, user)
		}
	}
	row, err := loadInspiration(r.Context(), app.db, user, id)
	if err != nil {
		app.serverError(w, err)
		return
	}
	writeJSON(w, 200, row)
}

func dateValue(s string) any {
	d, err := time.Parse("2006-01-02", s)
	if err != nil {
		return nil
	}
	return d
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func (app *application) storeInspirationThumbnail(ctx context.Context, user, id uuid.UUID, source string, body []byte, img sniffedImage) error {
	imageID := uuid.New()
	sum := sha256.Sum256(body)
	name := inspirationObjectName(user, id, imageID, img.ContentType)
	if err := app.inspirationObjects().Put(ctx, name, img.ContentType, map[string]string{"userId": user.String(), "inspirationId": id.String(), "imageId": imageID.String(), "role": "thumbnail"}, body); err != nil {
		return err
	}
	tag, err := app.db.Exec(ctx, `INSERT INTO horn_inspiration_images(id,inspiration_id,user_id,role,object_name,content_type,size_bytes,width,height,sha256,source_url,position)
 SELECT $1,$2,$3,'thumbnail',$4,$5,$6,$7,$8,$9,$10,0 WHERE EXISTS(SELECT 1 FROM horn_inspirations WHERE id=$2 AND user_id=$3)
 AND NOT EXISTS(SELECT 1 FROM horn_inspiration_images WHERE inspiration_id=$2 AND role='thumbnail')`, imageID, id, user, name, img.ContentType, len(body), img.Width, img.Height, hex.EncodeToString(sum[:]), truncateRunes(source, 2000))
	if err != nil || tag.RowsAffected() == 0 {
		app.deleteObjectsBestEffort(ctx, []string{name})
	}
	return err
}
