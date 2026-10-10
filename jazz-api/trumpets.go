package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type hornDetails struct {
	Year            *int     `json:"year,omitempty"`
	Condition       string   `json:"condition,omitempty"`
	Finish          string   `json:"finish,omitempty"`
	Bore            string   `json:"bore,omitempty"`
	Bell            string   `json:"bell,omitempty"`
	NotableFeatures []string `json:"notableFeatures,omitempty"`
	Provenance      string   `json:"provenance,omitempty"`
}
type trumpetCandidate struct {
	VerificationState string      `json:"verificationState,omitempty"`
	ID                string      `json:"id,omitempty"`
	HornID            string      `json:"hornId,omitempty"`
	Maker             string      `json:"maker"`
	Model             string      `json:"model"`
	SerialNumber      string      `json:"serialNumber"`
	Details           hornDetails `json:"details"`
	Title             string      `json:"title"`
	Description       string      `json:"description"`
	URL               string      `json:"url"`
	Source            string      `json:"source"`
	SourceListingID   string      `json:"sourceListingId"`
	Seller            string      `json:"seller"`
	Location          string      `json:"location"`
	Price             *float64    `json:"price"`
	Currency          string      `json:"currency"`
	Shipping          *float64    `json:"shipping"`
	PostedAt          *time.Time  `json:"postedAt"`
	Status            string      `json:"status"`
	DiscoveryType     string      `json:"discoveryType"`
	Images            []string    `json:"images"`
	SearchScore       float64     `json:"searchScore"`
	SearchRationale   string      `json:"searchRationale"`
	Tags              []string    `json:"tags"`
	Evidence          string      `json:"evidence"`
	SeedKey           string      `json:"-"`
}
type trumpetFeedback struct {
	Rating             *int     `json:"rating"`
	InterestState      string   `json:"interestState"`
	Notes              string   `json:"notes"`
	Favorite           bool     `json:"favorite"`
	FavoredAttributes  []string `json:"favoredAttributes"`
	DislikedAttributes []string `json:"dislikedAttributes"`
}
type trumpetListing struct {
	trumpetCandidate
	FirstSeen       time.Time         `json:"firstSeen"`
	LastChecked     *time.Time        `json:"lastChecked"`
	ChangedAt       time.Time         `json:"changedAt"`
	Acquired        bool              `json:"acquired"`
	Feedback        trumpetFeedback   `json:"feedback"`
	PriceHistory    []json.RawMessage `json:"priceHistory"`
	StatusHistory   []json.RawMessage `json:"statusHistory"`
	PossibleRelists []string          `json:"possibleRelists"`
	// Live horn-inspiration entries linked to this instrument.
	InspirationCount int `json:"inspirationCount"`
}
type trumpetSourceCheck struct {
	Source         string `json:"source"`
	Status         string `json:"status"`
	Candidates     int    `json:"candidates"`
	Note           string `json:"note"`
	Domain         string `json:"domain,omitempty"`
	Query          string `json:"query,omitempty"`
	Geography      string `json:"geography,omitempty"`
	Specialty      string `json:"specialty,omitempty"`
	PagesOpened    int    `json:"pagesOpened"`
	VerifiedOffers int    `json:"verifiedOffers"`
	StaleResults   int    `json:"staleResults"`
}
type trumpetRunInput struct {
	ExternalID string               `json:"externalId"`
	StartedAt  *time.Time           `json:"startedAt,omitempty"`
	Kind       string               `json:"kind"`
	Status     string               `json:"status"`
	Error      string               `json:"error"`
	Sources    []trumpetSourceCheck `json:"sources"`
	Listings   []trumpetCandidate   `json:"listings"`
}

func (app *application) trumpetRoutes(mux *http.ServeMux) {
	browser := map[string]http.HandlerFunc{
		"GET /v1/trumpets/listings":                  app.trumpetBoard,
		"GET /v1/trumpets/profile":                   app.trumpetProfile,
		"PUT /v1/trumpets/listings/{id}/feedback":    app.saveTrumpetFeedback,
		"DELETE /v1/trumpets/listings/{id}/feedback": app.deleteTrumpetFeedback,
		"POST /v1/trumpets/seed":                     app.seedTrumpets,
		// Horn inspiration board (same privacy headers and authentication).
		"GET /v1/trumpets/inspiration":                          app.listInspirations,
		"POST /v1/trumpets/inspiration":                         app.createInspiration,
		"POST /v1/trumpets/inspiration/seed":                    app.seedInspiration,
		"POST /v1/trumpets/inspiration/{id}/enrich":             app.enrichInspiration,
		"PATCH /v1/trumpets/inspiration/{id}":                   app.patchInspiration,
		"DELETE /v1/trumpets/inspiration/{id}":                  app.deleteInspiration,
		"POST /v1/trumpets/inspiration/{id}/images":             app.uploadInspirationImage,
		"DELETE /v1/trumpets/inspiration/{id}/images/{imageId}": app.deleteInspirationImage,
		"GET /v1/trumpets/inspiration/images/{imageId}":         app.inspirationImageRedirect,
	}
	for path, h := range browser {
		mux.Handle(path, app.trumpetPrivacy(app.authenticate(h)))
	}
	machine := map[string]http.HandlerFunc{
		"GET /v1/trumpets/machine/profile":  app.trumpetProfile,
		"GET /v1/trumpets/machine/listings": app.trumpetBoard,
		"GET /v1/trumpets/machine/due":      app.trumpetDue,
		"POST /v1/trumpets/machine/runs":    app.ingestTrumpetRun,
	}
	for path, h := range machine {
		mux.Handle(path, app.trumpetPrivacy(app.trumpetMachineAuth(h)))
	}
}
func (app *application) trumpetPrivacy(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "private, no-store")
		w.Header().Set("X-Robots-Tag", "noindex, nofollow, noarchive")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if r.Method != "GET" && (!trumpetBodyTypeAllowed(r) || r.Header.Get("Sec-Fetch-Site") == "cross-site") {
			writeError(w, 403, "JSON same-site request required")
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

// Mutations must be JSON, except raw inspiration image uploads, whose image
// content types (like JSON) are not CORS-safelisted and so still need preflight.
func trumpetBodyTypeAllowed(r *http.Request) bool {
	contentType := strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0])
	if contentType == "application/json" {
		return true
	}
	if r.Method == "POST" && strings.HasPrefix(r.URL.Path, "/v1/trumpets/inspiration/") && strings.HasSuffix(r.URL.Path, "/images") {
		_, ok := inspirationImageTypes[strings.ToLower(contentType)]
		return ok
	}
	return false
}
func (app *application) trumpetMachineAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		expected, err := hex.DecodeString(os.Getenv("TRUMPETS_MACHINE_TOKEN_SHA256"))
		subject := strings.TrimSpace(os.Getenv("TRUMPETS_MACHINE_USER"))
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		hash := sha256.Sum256([]byte(token))
		if err != nil || len(expected) != 32 || subject == "" || len(subject) > 128 || len(token) < 32 || !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") || subtle.ConstantTimeCompare(expected, hash[:]) != 1 {
			writeError(w, 401, "unauthorized")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userSubjectKey, subject)))
	})
}
func decodeTrumpetJSON(w http.ResponseWriter, r *http.Request, out any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return err
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return errors.New("expected one JSON value")
	}
	return nil
}
func canonicalTrumpetURL(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", nil
	}
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil {
		return "", errors.New("invalid listing URL")
	}
	u.Scheme = "https"
	u.Host = strings.ToLower(u.Host)
	u.Host = strings.TrimPrefix(u.Host, "www.")
	u.Fragment = ""
	u.Path = strings.TrimRight(u.Path, "/")
	q := u.Query()
	for k := range q {
		lower := strings.ToLower(k)
		if strings.HasPrefix(lower, "utm_") || containsString([]string{"fbclid", "gclid", "ref", "referrer", "srsltid", "itmmeta", "itmprp", "hash", "_trksid", "_trkparms"}, lower) {
			q.Del(k)
		}
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}
func containsString(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}
func identityPart(value string) string {
	return strings.Join(strings.Fields(strings.ToLower(value)), " ")
}
func hornIdentity(maker, serial string) string {
	if strings.TrimSpace(serial) == "" {
		return ""
	}
	return identityPart(maker) + ":" + strings.ReplaceAll(identityPart(serial), " ", "")
}
func normalizeTrumpet(c *trumpetCandidate) error {
	c.Maker = strings.TrimSpace(c.Maker)
	c.Model = strings.TrimSpace(c.Model)
	c.Source = strings.TrimSpace(c.Source)
	if c.Maker == "" || c.Model == "" || c.Source == "" || c.Title == "" {
		return errors.New("maker, model, source and title required")
	}
	if len(c.Description) > 50000 || len(c.Title) > 1000 || len(c.SearchRationale) > 10000 || len(c.Source) > 200 || len(c.SerialNumber) > 200 {
		return errors.New("listing text too long")
	}
	canonical, err := canonicalTrumpetURL(c.URL)
	if err != nil {
		return err
	}
	c.URL = canonical
	if c.URL == "" && c.SeedKey == "" && c.ID == "" {
		return errors.New("listing URL required")
	}
	if c.Status == "" {
		c.Status = "active"
	}
	if c.VerificationState == "" {
		c.VerificationState = "verified"
		if c.SeedKey != "" {
			c.VerificationState = "historical"
		}
	}
	if !containsString([]string{"verified", "candidate", "historical"}, c.VerificationState) {
		return errors.New("invalid verification state")
	}
	if c.VerificationState == "candidate" {
		if c.URL == "" {
			return errors.New("candidate URL required")
		}
		if c.Status != "stale" || c.Price != nil || c.Shipping != nil || c.PostedAt != nil || c.SerialNumber != "" {
			return errors.New("unverified candidates cannot assert availability, money, dates or serials")
		}
	}
	if !containsString([]string{"active", "sold", "removed", "stale", "acquired"}, c.Status) {
		return errors.New("invalid status")
	}
	if c.DiscoveryType == "" {
		c.DiscoveryType = "newly discovered"
	}
	if !containsString([]string{"new listing", "newly discovered", "rediscovered"}, c.DiscoveryType) {
		return errors.New("discovery type for ingestion must be new listing, newly discovered or rediscovered; changes are detected")
	}
	if c.DiscoveryType == "new listing" && (c.PostedAt == nil || c.PostedAt.Before(time.Now().Add(-24*time.Hour)) || c.PostedAt.After(time.Now())) {
		c.DiscoveryType = "newly discovered"
	}
	if c.Currency == "" {
		c.Currency = "USD"
	}
	c.Currency = strings.ToUpper(c.Currency)
	if len(c.Currency) != 3 || c.Currency[0] < 'A' || c.Currency[0] > 'Z' || c.Currency[1] < 'A' || c.Currency[1] > 'Z' || c.Currency[2] < 'A' || c.Currency[2] > 'Z' {
		return errors.New("ISO currency required")
	}
	for _, n := range []*float64{c.Price, c.Shipping} {
		if n != nil && (math.IsNaN(*n) || math.IsInf(*n, 0) || *n < 0 || *n > 999999999999.99) {
			return errors.New("invalid amount")
		}
		if n != nil {
			*n = math.Round(*n*100) / 100
		}
	}
	if math.IsNaN(c.SearchScore) || math.IsInf(c.SearchScore, 0) || c.SearchScore < 0 || c.SearchScore > 100 {
		return errors.New("search score must be 0–100")
	}
	if len(c.Images) > 20 || len(c.Tags) > 40 {
		return errors.New("too many images or tags")
	}
	for _, v := range c.Images {
		u, e := url.Parse(v)
		if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
			return errors.New("images require HTTPS URLs")
		}
	}
	if c.Images == nil {
		c.Images = []string{}
	}
	if c.Tags == nil {
		c.Tags = []string{}
	}
	return nil
}
func validateFeedback(f *trumpetFeedback) error {
	if f.Rating != nil && (*f.Rating < 1 || *f.Rating > 5) {
		return errors.New("rating must be 1–5 or null")
	}
	if !containsString([]string{"pass", "watch", "interested", "contact", "buy", "acquired"}, f.InterestState) {
		return errors.New("invalid interest state")
	}
	if len(f.Notes) > 20000 || len(f.FavoredAttributes) > 40 || len(f.DislikedAttributes) > 40 {
		return errors.New("feedback too long")
	}
	for _, values := range [][]string{f.FavoredAttributes, f.DislikedAttributes} {
		for _, v := range values {
			if len(v) > 100 || strings.TrimSpace(v) == "" {
				return errors.New("invalid attribute")
			}
		}
	}
	if f.FavoredAttributes == nil {
		f.FavoredAttributes = []string{}
	}
	if f.DislikedAttributes == nil {
		f.DislikedAttributes = []string{}
	}
	return nil
}
func nullText(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// Serialize mutations for one owner. Serial numbers are strong identities; fuzzy
// matches are returned as suggestions rather than silently merging distinct horns.
func lockTrumpetOwner(ctx context.Context, tx pgx.Tx, user uuid.UUID) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, user.String())
	return err
}
func upsertTrumpet(ctx context.Context, tx pgx.Tx, user uuid.UUID, c trumpetCandidate, run *uuid.UUID) (uuid.UUID, error) {
	if err := normalizeTrumpet(&c); err != nil {
		return uuid.Nil, err
	}
	var id, horn uuid.UUID
	var oldPrice *float64
	var oldStatus, oldCurrency, oldVerification, oldDescription string
	var acquired bool
	err := tx.QueryRow(ctx, `SELECT l.id,l.horn_id,l.price,l.status,l.currency,h.acquired,l.verification_state,l.description FROM trumpet_listings l JOIN trumpet_horns h ON h.id=l.horn_id
 WHERE l.user_id=$1 AND (($2::uuid IS NOT NULL AND l.id=$2) OR ($2::uuid IS NULL AND (($3::text IS NOT NULL AND l.canonical_url=$3) OR ($4::text IS NOT NULL AND l.source=$5 AND l.source_listing_id=$4) OR ($6::text IS NOT NULL AND l.seed_key=$6))))
 ORDER BY l.first_seen LIMIT 1 FOR UPDATE OF l`, user, nullText(c.ID), nullText(c.URL), nullText(c.SourceListingID), c.Source, nullText(c.SeedKey)).Scan(&id, &horn, &oldPrice, &oldStatus, &oldCurrency, &acquired, &oldVerification, &oldDescription)
	fresh := errors.Is(err, pgx.ErrNoRows)
	if err != nil && !fresh {
		return uuid.Nil, err
	}
	if fresh && c.ID != "" {
		return uuid.Nil, errors.New("listing id not owned or not found")
	}
	// Search hints never overwrite a previously verified offer or its market facts.
	if !fresh && c.VerificationState == "candidate" && oldVerification != "candidate" {
		return id, nil
	}
	details, _ := json.Marshal(c.Details)
	var previousSnapshot json.RawMessage
	if !fresh {
		e := tx.QueryRow(ctx, `SELECT snapshot FROM trumpet_observations WHERE listing_id=$1 ORDER BY checked_at DESC,id DESC LIMIT 1`, id).Scan(&previousSnapshot)
		if e != nil && !errors.Is(e, pgx.ErrNoRows) {
			return uuid.Nil, e
		}
		if c.SerialNumber != "" && c.VerificationState == "verified" && oldVerification == "candidate" {
			var linked uuid.UUID
			e := tx.QueryRow(ctx, `SELECT id FROM trumpet_horns WHERE user_id=$1 AND identity_key=$2 AND id<>$3`, user, hornIdentity(c.Maker, c.SerialNumber), horn).Scan(&linked)
			if e == nil {
				if err = mergeTrumpetHorn(ctx, tx, user, horn, linked); err != nil {
					return uuid.Nil, err
				}
				horn = linked
				if err = tx.QueryRow(ctx, `SELECT acquired FROM trumpet_horns WHERE id=$1`, horn).Scan(&acquired); err != nil {
					return uuid.Nil, err
				}
			} else if !errors.Is(e, pgx.ErrNoRows) {
				return uuid.Nil, e
			}
		}
	}
	if fresh {
		identity := hornIdentity(c.Maker, c.SerialNumber)
		if c.HornID == "" && c.VerificationState != "candidate" {
			var seeded uuid.UUID
			e := tx.QueryRow(ctx, `SELECT h.id FROM trumpet_horns h JOIN trumpet_listings l ON l.horn_id=h.id
    WHERE h.user_id=$1 AND l.seed_key IS NOT NULL AND lower(h.maker)=lower($2) AND lower(h.model)=lower($3)
    AND (h.serial_number='' OR $4='' OR h.serial_number=$4)
    AND (NOT EXISTS(SELECT 1 FROM trumpet_listings live WHERE live.horn_id=h.id AND live.canonical_url IS NOT NULL) OR (h.serial_number<>'' AND h.serial_number=$4)) LIMIT 1`, user, c.Maker, c.Model, c.SerialNumber).Scan(&seeded)
			if e == nil {
				c.HornID = seeded.String()
			} else if !errors.Is(e, pgx.ErrNoRows) {
				return uuid.Nil, e
			}
		}
		if c.HornID == "" && isAcquiredTaylor(c) {
			var known uuid.UUID
			e := tx.QueryRow(ctx, `SELECT h.id FROM trumpet_horns h JOIN trumpet_listings l ON l.horn_id=h.id WHERE h.user_id=$1 AND h.acquired AND l.seed_key='Taylor / Chicago 46 II / Harrelson-modified' LIMIT 1`, user).Scan(&known)
			if e == nil {
				c.HornID = known.String()
			} else if !errors.Is(e, pgx.ErrNoRows) {
				return uuid.Nil, e
			}
		}
		if c.HornID != "" { // Explicitly link an independently verified relist.
			err = tx.QueryRow(ctx, `SELECT id,acquired FROM trumpet_horns WHERE id=$1 AND user_id=$2`, c.HornID, user).Scan(&horn, &acquired)
			if err != nil {
				return uuid.Nil, errors.New("horn id not owned or not found")
			}
		} else {
			horn = uuid.New()
			err = tx.QueryRow(ctx, `INSERT INTO trumpet_horns(id,user_id,maker,model,serial_number,identity_key,details,acquired)
    VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT(user_id,identity_key) DO UPDATE SET identity_key=EXCLUDED.identity_key RETURNING id,acquired`, horn, user, c.Maker, c.Model, c.SerialNumber, nullText(identity), details, c.Status == "acquired").Scan(&horn, &acquired)
			if err != nil {
				return uuid.Nil, err
			}
		}
		id = uuid.New()
	} else if run != nil {
		var already bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM trumpet_observations WHERE listing_id=$1 AND run_id=$2)`, id, run).Scan(&already); err != nil {
			return uuid.Nil, err
		}
		if already {
			return id, nil
		}
	}
	if acquired || isAcquiredTaylor(c) {
		c.Status = "acquired"
		acquired = true
	}
	images, _ := json.Marshal(c.Images)
	tags, _ := json.Marshal(c.Tags)
	if fresh {
		_, err = tx.Exec(ctx, `INSERT INTO trumpet_listings(id,user_id,horn_id,canonical_url,source,source_listing_id,seed_key,title,description,seller,location,price,currency,shipping,posted_at,status,discovery_type,search_score,search_rationale,images,tags,last_checked)
   VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,CASE WHEN $7::text IS NULL THEN now() ELSE NULL END)`, id, user, horn, nullText(c.URL), c.Source, nullText(c.SourceListingID), nullText(c.SeedKey), c.Title, c.Description, c.Seller, c.Location, c.Price, c.Currency, c.Shipping, c.PostedAt, c.Status, c.DiscoveryType, c.SearchScore, c.SearchRationale, images, tags)
		if err != nil {
			return uuid.Nil, err
		}
		// Seeds are incomplete historical references, not fresh search results.
		if c.SeedKey == "" {
			meaningful := !acquired && c.VerificationState != "candidate" && c.Status == "active" && c.SearchScore >= 65
			var crossposted bool
			if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM trumpet_listings WHERE horn_id=$1 AND id<>$2 AND verification_state='verified' AND status='active' AND (currency<>$3 OR price IS NULL OR $4::numeric IS NULL OR price<=$4::numeric*1.03))`, horn, id, c.Currency, c.Price).Scan(&crossposted); err != nil {
				return uuid.Nil, err
			}
			if err = trumpetEvent(ctx, tx, user, id, c.DiscoveryType, nil, c.Price, c.Currency, "", c.Status, meaningful && !crossposted); err != nil {
				return uuid.Nil, err
			}
		}
	} else {
		discovery := ""
		if oldPrice != nil && c.Price != nil && oldCurrency == c.Currency && *c.Price < *oldPrice {
			discovery = "price drop"
		}
		if oldStatus != c.Status {
			discovery = "status change"
			if oldStatus != "active" && c.Status == "active" {
				discovery = "rediscovered"
				if oldVerification == "candidate" {
					discovery = "newly discovered"
				}
			}
		}
		_, err = tx.Exec(ctx, `UPDATE trumpet_listings SET title=$3,description=$4,seller=$5,location=$6,price=$7,currency=$8,shipping=$9,
   posted_at=COALESCE($10,posted_at),status=$11,search_score=$12,search_rationale=$13,images=$14,tags=$15,last_checked=now(),
   changed_at=CASE WHEN price IS DISTINCT FROM $7::numeric OR currency<>$8 OR status<>$11 OR description<>$4 THEN now() ELSE changed_at END,
   discovery_type=CASE WHEN $16<>'' THEN $16 ELSE discovery_type END WHERE id=$1 AND user_id=$2`, id, user, c.Title, c.Description, c.Seller, c.Location, c.Price, c.Currency, c.Shipping, c.PostedAt, c.Status, c.SearchScore, c.SearchRationale, images, tags, discovery)
		if err != nil {
			return uuid.Nil, err
		}
		if oldCurrency != c.Currency || !samePrice(oldPrice, c.Price) {
			kind := "price change"
			if oldCurrency == c.Currency && oldPrice != nil && c.Price != nil && *c.Price < *oldPrice {
				kind = "price drop"
			}
			if err = trumpetEvent(ctx, tx, user, id, kind, oldPrice, c.Price, c.Currency, oldStatus, c.Status, !acquired && c.VerificationState != "candidate" && significantTrumpetPrice(oldPrice, c.Price, oldCurrency, c.Currency)); err != nil {
				return uuid.Nil, err
			}
		}
		if oldStatus != c.Status {
			kind := "status change"
			if oldStatus != "active" && c.Status == "active" {
				kind = "rediscovered"
				if oldVerification == "candidate" {
					kind = "newly discovered"
				}
			}
			meaningful := !acquired && c.VerificationState != "candidate"
			if oldVerification == "candidate" && c.Status == "active" {
				var crossposted bool
				if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM trumpet_listings WHERE horn_id=$1 AND id<>$2 AND verification_state='verified' AND status='active' AND (currency<>$3 OR price IS NULL OR $4::numeric IS NULL OR price<=$4::numeric*1.03))`, horn, id, c.Currency, c.Price).Scan(&crossposted); err != nil {
					return uuid.Nil, err
				}
				meaningful = meaningful && !crossposted && c.SearchScore >= 65
			}
			if err = trumpetEvent(ctx, tx, user, id, kind, oldPrice, c.Price, c.Currency, oldStatus, c.Status, meaningful); err != nil {
				return uuid.Nil, err
			}
		}
	}
	if !fresh {
		_, err = tx.Exec(ctx, `UPDATE trumpet_listings SET canonical_url=COALESCE($3,canonical_url),source=$4,source_listing_id=COALESCE($5,source_listing_id) WHERE id=$1 AND user_id=$2`, id, user, nullText(c.URL), c.Source, nullText(c.SourceListingID))
		if err != nil {
			return uuid.Nil, err
		}
	}
	_, err = tx.Exec(ctx, `UPDATE trumpet_horns SET details=details||$3::jsonb,serial_number=CASE WHEN $4<>'' THEN $4 ELSE serial_number END,
   identity_key=COALESCE($5,identity_key) WHERE id=$1 AND user_id=$2`, horn, user, details, c.SerialNumber, nullText(hornIdentity(c.Maker, c.SerialNumber)))
	if err != nil {
		return uuid.Nil, err
	}
	if c.Status == "acquired" {
		if err = markTrumpetAcquired(ctx, tx, user, horn); err != nil {
			return uuid.Nil, err
		}
	}
	if oldVerification == "candidate" && c.VerificationState == "verified" && !acquired {
		_, err = tx.Exec(ctx, `UPDATE trumpet_horns SET maker=$3,model=$4 WHERE id=$1 AND user_id=$2`, horn, user, c.Maker, c.Model)
		if err != nil {
			return uuid.Nil, err
		}
	}
	if c.VerificationState == "candidate" {
		_, err = tx.Exec(ctx, `UPDATE trumpet_listings SET verification_state=$3,last_checked=NULL WHERE id=$1 AND user_id=$2`, id, user, c.VerificationState)
		return id, err
	}
	_, err = tx.Exec(ctx, `UPDATE trumpet_listings SET verification_state=$3 WHERE id=$1 AND user_id=$2`, id, user, c.VerificationState)
	if err != nil {
		return uuid.Nil, err
	}
	if c.SeedKey == "" {
		snapshot, _ := json.Marshal(map[string]any{"title": c.Title, "description": c.Description, "details": c.Details, "seller": c.Seller, "location": c.Location, "images": c.Images})
		if !fresh && oldVerification == "verified" {
			changes := trumpetSnapshotChanges(previousSnapshot, snapshot, oldDescription, c.Description)
			if changes != "" {
				_, err = tx.Exec(ctx, `INSERT INTO trumpet_events(listing_id,user_id,kind,old_status,new_status,meaningful,detail) VALUES($1,$2,'details change',$3,$4,$5,$6)`, id, user, oldStatus, c.Status, !acquired, changes)
				if err != nil {
					return uuid.Nil, err
				}
			}
		}
		_, err = tx.Exec(ctx, `INSERT INTO trumpet_observations(listing_id,user_id,run_id,price,currency,shipping,status,evidence,snapshot) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT(listing_id,run_id) DO NOTHING`, id, user, run, c.Price, c.Currency, c.Shipping, c.Status, c.Evidence, snapshot)
	}
	return id, err
}
func samePrice(a, b *float64) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
}
func trumpetEvent(ctx context.Context, tx pgx.Tx, user, id uuid.UUID, kind string, oldPrice, newPrice *float64, currency, oldStatus, newStatus string, meaningful bool) error {
	_, err := tx.Exec(ctx, `INSERT INTO trumpet_events(listing_id,user_id,kind,old_price,new_price,currency,old_status,new_status,meaningful) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, id, user, kind, oldPrice, newPrice, currency, nullText(oldStatus), newStatus, meaningful)
	return err
}

func (app *application) loadTrumpets(ctx context.Context, user uuid.UUID) ([]trumpetListing, error) {
	rows, err := app.db.Query(ctx, `SELECT l.id::text,l.horn_id::text,h.maker,h.model,h.serial_number,h.details,l.title,l.description,
 COALESCE(l.canonical_url,''),l.source,COALESCE(l.source_listing_id,''),l.seller,l.location,l.price,l.currency,l.shipping,l.posted_at,
 l.status,l.discovery_type,l.search_score,l.search_rationale,l.images,l.tags,l.first_seen,l.last_checked,l.changed_at,h.acquired,l.verification_state,
 COALESCE((SELECT jsonb_build_object('rating',f.rating,'interestState',f.interest_state,'notes',f.notes,'favorite',f.favorite,
 'favoredAttributes',f.favored_attributes,'dislikedAttributes',f.disliked_attributes) FROM trumpet_feedback f WHERE f.horn_id=h.id),'{}'),
 COALESCE((SELECT jsonb_agg(jsonb_build_object('price',o.price,'currency',o.currency,'shipping',o.shipping,'checkedAt',o.checked_at,'status',o.status,'evidence',o.evidence,'snapshot',o.snapshot) ORDER BY o.checked_at) FROM trumpet_observations o WHERE o.listing_id=l.id),'[]'),
 COALESCE((SELECT jsonb_agg(jsonb_build_object('kind',e.kind,'oldStatus',e.old_status,'newStatus',e.new_status,'oldPrice',e.old_price,'newPrice',e.new_price,'currency',e.currency,'occurredAt',e.occurred_at,'detail',e.detail) ORDER BY e.occurred_at) FROM trumpet_events e WHERE e.listing_id=l.id),'[]'),
 (SELECT count(*) FROM horn_inspirations hi WHERE hi.horn_id=h.id AND hi.user_id=l.user_id AND hi.archived_at IS NULL)
 FROM trumpet_listings l JOIN trumpet_horns h ON h.id=l.horn_id WHERE l.user_id=$1 ORDER BY l.first_seen DESC`, user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []trumpetListing{}
	for rows.Next() {
		var l trumpetListing
		var details, feedback, images, tags, prices, statuses []byte
		if err = rows.Scan(&l.ID, &l.HornID, &l.Maker, &l.Model, &l.SerialNumber, &details, &l.Title, &l.Description, &l.URL, &l.Source, &l.SourceListingID, &l.Seller, &l.Location, &l.Price, &l.Currency, &l.Shipping, &l.PostedAt, &l.Status, &l.DiscoveryType, &l.SearchScore, &l.SearchRationale, &images, &tags, &l.FirstSeen, &l.LastChecked, &l.ChangedAt, &l.Acquired, &l.VerificationState, &feedback, &prices, &statuses, &l.InspirationCount); err != nil {
			return nil, err
		}
		for _, pair := range []struct {
			data   []byte
			target any
		}{{details, &l.Details}, {feedback, &l.Feedback}, {images, &l.Images}, {tags, &l.Tags}, {prices, &l.PriceHistory}, {statuses, &l.StatusHistory}} {
			if err = json.Unmarshal(pair.data, pair.target); err != nil {
				return nil, err
			}
		}
		if l.Feedback.InterestState == "" {
			l.Feedback.InterestState = "watch"
		}
		if l.Acquired {
			l.Feedback.InterestState = "acquired"
		}
		l.PossibleRelists = []string{}
		out = append(out, l)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		for j := range out {
			if i != j && out[i].HornID != out[j].HornID && likelySameHorn(out[i], out[j]) {
				out[i].PossibleRelists = append(out[i].PossibleRelists, out[j].ID)
			}
		}
	}
	return out, nil
}
func likelySameHorn(a, b trumpetListing) bool {
	if identityPart(a.Maker) != identityPart(b.Maker) || identityPart(a.Model) != identityPart(b.Model) {
		return false
	}
	// Conflicting serials are distinct instruments; missing serials require a
	// distinctive provenance or identical photo URL, never model alone.
	if a.SerialNumber != "" && b.SerialNumber != "" {
		return hornIdentity(a.Maker, a.SerialNumber) == hornIdentity(b.Maker, b.SerialNumber)
	}
	if len(a.Details.Provenance) > 20 && identityPart(a.Details.Provenance) == identityPart(b.Details.Provenance) {
		return true
	}
	for _, im := range a.Images {
		if containsString(b.Images, im) {
			return true
		}
	}
	return false
}
func (app *application) trumpetBoard(w http.ResponseWriter, r *http.Request) {
	user, err := app.userID(r.Context())
	if err != nil {
		app.serverError(w, err)
		return
	}
	listings, err := app.loadTrumpets(r.Context(), user)
	if err != nil {
		app.serverError(w, err)
		return
	}
	var latest, lastSuccess json.RawMessage
	err = app.db.QueryRow(r.Context(), `SELECT jsonb_build_object('id',r.id,'kind',r.kind,'status',r.status,'startedAt',r.started_at,'completedAt',r.completed_at,'error',r.error,
 'sources',COALESCE((SELECT jsonb_agg(jsonb_build_object('source',s.source,'status',s.status,'candidates',s.candidates,'note',s.note,'domain',s.domain,'query',s.query,'pagesOpened',s.pages_opened,'verifiedOffers',s.verified_offers,'staleResults',s.stale_results) ORDER BY s.source) FROM trumpet_run_sources s WHERE s.run_id=r.id),'[]'))
 FROM trumpet_runs r WHERE user_id=$1 ORDER BY started_at DESC LIMIT 1`, user).Scan(&latest)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		app.serverError(w, err)
		return
	}
	err = app.db.QueryRow(r.Context(), `SELECT jsonb_object_agg(kind,completed_at) FROM (SELECT DISTINCT ON(kind) kind,completed_at FROM trumpet_runs WHERE user_id=$1 AND status='succeeded' ORDER BY kind,completed_at DESC) s`, user).Scan(&lastSuccess)
	if err != nil {
		app.serverError(w, err)
		return
	}
	rows, err := app.db.Query(r.Context(), `SELECT jsonb_build_object('id',e.id,'listingId',e.listing_id,'kind',e.kind,'oldPrice',e.old_price,'newPrice',e.new_price,'currency',e.currency,'oldStatus',e.old_status,'newStatus',e.new_status,'occurredAt',e.occurred_at,'detail',e.detail)
 FROM trumpet_events e JOIN trumpet_listings l ON l.id=e.listing_id JOIN trumpet_horns h ON h.id=l.horn_id
 WHERE e.user_id=$1 AND e.meaningful AND NOT h.acquired AND e.occurred_at >= date_trunc('day',now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC' ORDER BY e.occurred_at DESC`, user)
	if err != nil {
		app.serverError(w, err)
		return
	}
	defer rows.Close()
	events := []json.RawMessage{}
	for rows.Next() {
		var event json.RawMessage
		if err = rows.Scan(&event); err != nil {
			app.serverError(w, err)
			return
		}
		events = append(events, event)
	}
	if err = rows.Err(); err != nil {
		app.serverError(w, err)
		return
	}
	universe, e := app.trumpetSourceUniverse(r.Context(), user)
	if e != nil {
		app.serverError(w, e)
		return
	}
	alerts := selectTrumpetAlerts(listings, events)
	writeJSON(w, 200, map[string]any{"sourceUniverse": universe, "alerts": alerts, "alertLimit": 8, "listings": listings, "events": events, "latestRun": latest, "lastSuccess": lastSuccess, "serverTime": time.Now().UTC(), "dayBoundary": "UTC", "sourceCatalog": trumpetSourceCatalog})
}
func (app *application) trumpetDue(w http.ResponseWriter, r *http.Request) {
	user, err := app.userID(r.Context())
	if err != nil {
		app.serverError(w, err)
		return
	}
	listings, err := app.loadTrumpets(r.Context(), user)
	if err != nil {
		app.serverError(w, err)
		return
	}
	due := []trumpetListing{}
	boundary := time.Now().UTC().Truncate(24 * time.Hour)
	for _, l := range listings {
		if !l.Acquired && !(l.VerificationState == "candidate" && l.Feedback.InterestState == "pass") && (l.Status == "active" || l.Status == "stale") && (l.LastChecked == nil || l.LastChecked.Before(boundary)) {
			due = append(due, l)
		}
	}
	writeJSON(w, 200, map[string]any{"listings": due, "dayBoundary": "UTC"})
}
func (app *application) saveTrumpetFeedback(w http.ResponseWriter, r *http.Request) {
	var f trumpetFeedback
	if err := decodeTrumpetJSON(w, r, &f); err != nil {
		writeError(w, 400, "invalid feedback JSON")
		return
	}
	if err := validateFeedback(&f); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, 400, "invalid listing id")
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
	var horn uuid.UUID
	err = tx.QueryRow(r.Context(), `SELECT horn_id FROM trumpet_listings WHERE id=$1 AND user_id=$2`, id, user).Scan(&horn)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, 404, "listing not found")
		return
	}
	if err != nil {
		app.serverError(w, err)
		return
	}
	favored, _ := json.Marshal(f.FavoredAttributes)
	disliked, _ := json.Marshal(f.DislikedAttributes)
	_, err = tx.Exec(r.Context(), `INSERT INTO trumpet_feedback(horn_id,user_id,rating,interest_state,notes,favorite,favored_attributes,disliked_attributes)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT(horn_id) DO UPDATE SET rating=EXCLUDED.rating,interest_state=EXCLUDED.interest_state,notes=EXCLUDED.notes,favorite=EXCLUDED.favorite,favored_attributes=EXCLUDED.favored_attributes,disliked_attributes=EXCLUDED.disliked_attributes,updated_at=now()`, horn, user, f.Rating, f.InterestState, f.Notes, f.Favorite, favored, disliked)
	if err != nil {
		app.serverError(w, err)
		return
	}
	if f.InterestState == "acquired" {
		err = markTrumpetAcquired(r.Context(), tx, user, horn)
	}
	if err != nil {
		app.serverError(w, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		app.serverError(w, err)
		return
	}
	writeJSON(w, 200, f)
}

var trumpetPriorityMakers = []string{"Taylor", "AR Resonance", "Harrelson", "Monette", "Adams", "Blackburn", "Van Laar", "Schilke Handcraft", "Inderbinen", "Lawler", "LOTUS", "BAC", "Eclipse", "Del Quadro", "Calicchio", "Benge", "GERDT"}
var trumpetPositiveTraits = []string{"raw brass", "aged brass", "patina", "engraving", "full gold plate", "mixed metals", "black hardware", "unusual bell geometry", "weird engineering", "one-off builds", "artist models", "provenance", "rare configurations", "unusually good value"}
var trumpetSourceCatalog = []string{"Reverb", "HornTrader", "Austin Custom Brass", "Thompson Music", "J. Landress Brass", "Dillon Music", "Trent Austin", "Baltimore Brass", "Rich Ita", "Brass Ark", "Horn Stash", "Brass Exchange", "Mighty Quinn", "Ferguson Music", "Gamonbrass", "Dawkes", "Windblowers", "Phil Parker", "Trevor Jones", "John Packer", "TC Gakki / Japanese shops", "European specialist dealers", "eBay", "Marktplaats", "Auction houses", "Regional music shops", "Maker / demo inventory", "Credible private listings"}

// trumpetSearchProfile builds the private search profile. Optional
// inspirations ({maker, model, tags, priority, why}; no URLs or images) tell
// the search what the owner is drawn to.
func trumpetSearchProfile(listings []trumpetListing, inspirations ...[]map[string]any) map[string]any {
	feed := []map[string]any{}
	for _, list := range inspirations {
		feed = append(feed, list...)
	}
	high := []trumpetListing{}
	low := []trumpetListing{}
	notes := []map[string]any{}
	excluded := []map[string]any{}
	favored := map[string]int{}
	disliked := map[string]int{}
	seen := map[string]bool{}
	for _, l := range listings {
		if seen[l.HornID] {
			continue
		}
		seen[l.HornID] = true
		f := l.Feedback
		if f.Rating != nil {
			if *f.Rating >= 4 {
				high = append(high, l)
			}
			if *f.Rating <= 2 {
				low = append(low, l)
			}
		}
		if f.Favorite && (f.Rating == nil || *f.Rating < 4) {
			high = append(high, l)
		}
		if f.Notes != "" {
			notes = append(notes, map[string]any{"hornId": l.HornID, "maker": l.Maker, "model": l.Model, "notes": f.Notes})
		}
		for _, v := range f.FavoredAttributes {
			favored[identityPart(v)]++
		}
		for _, v := range f.DislikedAttributes {
			disliked[identityPart(v)]++
		}
		if f.Rating != nil && *f.Rating >= 4 {
			for _, v := range l.Tags {
				favored[identityPart(v)]++
			}
		}
		if f.Rating != nil && *f.Rating <= 2 {
			for _, v := range l.Tags {
				disliked[identityPart(v)]++
			}
		}
		if l.Acquired || l.Status == "acquired" {
			excluded = append(excluded, map[string]any{"hornId": l.HornID, "maker": l.Maker, "model": l.Model, "serialNumber": l.SerialNumber, "provenance": l.Details.Provenance})
		}
	}
	return map[string]any{"version": 2, "notePreferences": trumpetNotePreferences(listings), "instrument": "professional Bb trumpet", "priorityMakers": trumpetPriorityMakers, "positiveTraits": trumpetPositiveTraits,
		"instructions":        []string{"No strict warm/dark sound filter.", "Ordinary production Bach/Yamaha only when the specific horn is exceptional.", "Cover diverse specialist, regional, international and private sources; deliberately discover new sources.", "Exclude acquired instruments from alerts, including serial-confirmed relists; use them for comparison/provenance/setup/resale only.", "Use ratings and notes as ranking evidence; preserve all tracked offers, including sold records."},
		"highlyRatedExamples": high, "negativelyRatedExamples": low, "notes": notes, "favoredAttributes": favored, "dislikedAttributes": disliked, "acquiredExclusions": excluded, "sourceCatalog": trumpetSourceCatalog, "inspirations": feed}
}
func (app *application) trumpetProfile(w http.ResponseWriter, r *http.Request) {
	user, err := app.userID(r.Context())
	if err != nil {
		app.serverError(w, err)
		return
	}
	listings, err := app.loadTrumpets(r.Context(), user)
	if err != nil {
		app.serverError(w, err)
		return
	}
	inspirations, err := app.inspirationProfile(r.Context(), user)
	if err != nil {
		app.serverError(w, err)
		return
	}
	profile := trumpetSearchProfile(listings, inspirations)
	universe, e := app.trumpetSourceUniverse(r.Context(), user)
	if e != nil {
		app.serverError(w, e)
		return
	}
	profile["sourceUniverse"] = universe
	profile["watchPolicy"] = map[string]any{"minimumDistinctDomains": 36, "newSourceQueries": 4, "explorationFraction": 0.25, "alertLimit": 8, "dailyRevalidation": true}
	writeJSON(w, 200, profile)
}
func validateTrumpetRun(input *trumpetRunInput) error {
	if strings.TrimSpace(input.ExternalID) == "" || len(input.ExternalID) > 200 || len(input.Error) > 10000 {
		return errors.New("bounded externalId required")
	}
	if !containsString([]string{"search", "recheck", "combined"}, input.Kind) || !containsString([]string{"succeeded", "partial", "failed"}, input.Status) {
		return errors.New("invalid run kind/status")
	}
	if len(input.Listings) > 1000 || len(input.Sources) > 200 {
		return errors.New("run too large")
	}
	if input.Status == "succeeded" && len(input.Sources) == 0 {
		return errors.New("successful run requires source coverage")
	}
	if input.StartedAt != nil && (input.StartedAt.After(time.Now().Add(time.Minute)) || input.StartedAt.Before(time.Now().Add(-48*time.Hour))) {
		return errors.New("run start must be within the past 48 hours")
	}
	seen := map[string]bool{}
	for _, s := range input.Sources {
		if s.Source == "" || len(s.Source) > 200 || seen[s.Source] || !containsString([]string{"checked", "failed", "skipped"}, s.Status) || s.Candidates < 0 || len(s.Note) > 5000 {
			return errors.New("invalid or duplicate source coverage")
		}
		if err := validateTrumpetSource(s); err != nil {
			return err
		}
		seen[s.Source] = true
		if input.Status == "succeeded" && s.Status == "failed" {
			return errors.New("failed sources require partial/failed run")
		}
	}
	for i := range input.Listings {
		if err := normalizeTrumpet(&input.Listings[i]); err != nil {
			return fmt.Errorf("listing %d: %w", i, err)
		}
	}
	return nil
}
func (app *application) ingestTrumpetRun(w http.ResponseWriter, r *http.Request) {
	var input trumpetRunInput
	if err := decodeTrumpetJSON(w, r, &input); err != nil {
		writeError(w, 400, "invalid run JSON")
		return
	}
	if err := validateTrumpetRun(&input); err != nil {
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
	run := uuid.New()
	var existing uuid.UUID
	var existingStatus string
	err = tx.QueryRow(r.Context(), `SELECT id,status FROM trumpet_runs WHERE user_id=$1 AND external_id=$2`, user, input.ExternalID).Scan(&existing, &existingStatus)
	if err == nil {
		writeJSON(w, 200, map[string]any{"runId": existing, "replayed": true, "status": existingStatus})
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		app.serverError(w, err)
		return
	}
	_, err = tx.Exec(r.Context(), `INSERT INTO trumpet_runs(id,user_id,external_id,kind,started_at) VALUES($1,$2,$3,$4,COALESCE($5,now()))`, run, user, input.ExternalID, input.Kind, input.StartedAt)
	if err != nil {
		app.serverError(w, err)
		return
	}
	ids := []uuid.UUID{}
	for _, c := range input.Listings {
		id, e := upsertTrumpet(r.Context(), tx, user, c, &run)
		if e != nil {
			writeError(w, 400, "listing identity conflict or invalid owner; run rolled back")
			return
		}
		ids = append(ids, id)
	}
	for _, s := range input.Sources {
		if _, err = tx.Exec(r.Context(), `INSERT INTO trumpet_run_sources(run_id,source,status,candidates,note,domain,query,pages_opened,verified_offers,stale_results) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, run, s.Source, s.Status, s.Candidates, s.Note, s.Domain, s.Query, s.PagesOpened, s.VerifiedOffers, s.StaleResults); err != nil {
			app.serverError(w, err)
			return
		}
	}
	if err = accumulateTrumpetSources(r.Context(), tx, user, input.Sources); err != nil {
		app.serverError(w, err)
		return
	}
	remaining := 0
	if input.Kind != "search" {
		err = tx.QueryRow(r.Context(), `SELECT count(*) FROM trumpet_listings l JOIN trumpet_horns h ON h.id=l.horn_id WHERE l.user_id=$1 AND NOT h.acquired AND l.status='active' AND (l.last_checked IS NULL OR l.last_checked < date_trunc('day',now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC')`, user).Scan(&remaining)
		if err != nil {
			app.serverError(w, err)
			return
		}
		if remaining > 0 && input.Status == "succeeded" {
			input.Status = "partial"
			input.Error = fmt.Sprintf("%d active listings still require daily revalidation. %s", remaining, input.Error)
		}
	}
	_, err = tx.Exec(r.Context(), `UPDATE trumpet_runs SET completed_at=now(),status=$2,error=$3 WHERE id=$1`, run, input.Status, input.Error)
	if err != nil {
		app.serverError(w, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		app.serverError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"runId": run, "listingIds": ids, "status": input.Status, "remainingActive": remaining})
}

func isAcquiredTaylor(c trumpetCandidate) bool {
	text := identityPart(c.Model + " " + c.Title + " " + c.Details.Provenance)
	return identityPart(c.Maker) == "taylor" && strings.Contains(text, "chicago") && strings.Contains(text, "46") && strings.Contains(text, "ii") && strings.Contains(text, "harrelson")
}

func (app *application) deleteTrumpetFeedback(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, 400, "invalid listing id")
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
	var horn uuid.UUID
	err = tx.QueryRow(r.Context(), `SELECT horn_id FROM trumpet_listings WHERE id=$1 AND user_id=$2`, id, user).Scan(&horn)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, 404, "listing not found")
		return
	}
	if err != nil {
		app.serverError(w, err)
		return
	}
	if _, err = tx.Exec(r.Context(), `DELETE FROM trumpet_feedback WHERE horn_id=$1 AND user_id=$2`, horn, user); err != nil {
		app.serverError(w, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		app.serverError(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"deleted": true})
}

func markTrumpetAcquired(ctx context.Context, tx pgx.Tx, user, horn uuid.UUID) error {
	if _, err := tx.Exec(ctx, `UPDATE trumpet_horns SET acquired=true WHERE id=$1 AND user_id=$2`, horn, user); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO trumpet_events(listing_id,user_id,kind,old_status,new_status,meaningful)
 SELECT id,user_id,'status change',status,'acquired',false FROM trumpet_listings WHERE horn_id=$1 AND user_id=$2 AND status<>'acquired'`, horn, user); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE trumpet_listings SET status='acquired',discovery_type='status change',changed_at=now() WHERE horn_id=$1 AND user_id=$2 AND status<>'acquired'`, horn, user)
	return err
}
