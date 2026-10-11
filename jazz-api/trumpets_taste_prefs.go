package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Owner-editable preference statements ("cool horn, not dark horn"), shown and
// edited in the "What I've learned" panel. Structured liked/disliked makers and
// traits become the taste model's prior; the statements and price note are
// passed to search clients as written. Every save is a new version.

const tastePreferencePrior = 0.5 // prior mean (log-odds) for a stated like/dislike

type tastePreferences struct {
	Revision          int        `json:"revision"`
	Statements        []string   `json:"statements"`
	LikedMakers       []string   `json:"likedMakers"`
	DislikedMakers    []string   `json:"dislikedMakers"`
	LikedTraits       []string   `json:"likedTraits"`
	DislikedTraits    []string   `json:"dislikedTraits"`
	PriceGuidance     string     `json:"priceGuidance"`
	LikedTraitKeys    []string   `json:"likedTraitKeys"`
	DislikedTraitKeys []string   `json:"dislikedTraitKeys"`
	UpdatedAt         *time.Time `json:"updatedAt"`
}

type tastePreferencesInput struct {
	Statements       []string `json:"statements"`
	LikedMakers      []string `json:"likedMakers"`
	DislikedMakers   []string `json:"dislikedMakers"`
	LikedTraits      []string `json:"likedTraits"`
	DislikedTraits   []string `json:"dislikedTraits"`
	PriceGuidance    string   `json:"priceGuidance"`
	ExpectedRevision *int     `json:"expectedRevision"`
}

func emptyTastePreferences() *tastePreferences {
	return &tastePreferences{Statements: []string{}, LikedMakers: []string{}, DislikedMakers: []string{}, LikedTraits: []string{}, DislikedTraits: []string{}, LikedTraitKeys: []string{}, DislikedTraitKeys: []string{}}
}

// tasteTraitKey maps a trait the owner typed ("raw brass", "upswept bell",
// "too pricey") to a model feature, or "" when it names no known feature.
func tasteTraitKey(text string) string {
	t := strings.ToLower(strings.Join(strings.Fields(text), " "))
	if key, ok := tasteChipFeatures[t]; ok {
		return key
	}
	for _, list := range [][]struct{ key, label, re string }{tasteFinishes, tasteTraits} {
		for _, f := range list {
			if tasteCompiled[f.key].MatchString(t) || strings.EqualFold(f.label, t) {
				return f.key
			}
		}
	}
	switch {
	case strings.Contains(t, "vintage") || strings.Contains(t, "antique"):
		return "era:vintage"
	case strings.Contains(t, "modern"):
		return "era:modern"
	case strings.Contains(t, "value") || strings.Contains(t, "bargain") || strings.Contains(t, "cheap"):
		return "price:below"
	case strings.Contains(t, "pricey") || strings.Contains(t, "expensive") || strings.Contains(t, "overpriced"):
		return "price:above"
	}
	return ""
}

func cleanTasteList(values []string, limit, max int, what string) ([]string, error) {
	out := []string{}
	seen := map[string]bool{}
	if len(values) > limit {
		return nil, errors.New("too many " + what)
	}
	for _, v := range values {
		v = strings.Join(strings.Fields(strings.ToValidUTF8(v, "")), " ")
		if v == "" {
			continue
		}
		if utf8.RuneCountInString(v) > max {
			return nil, errors.New(what + " must be short")
		}
		if key := strings.ToLower(v); !seen[key] {
			seen[key] = true
			out = append(out, v)
		}
	}
	return out, nil
}

func normalizeTastePreferences(in tastePreferencesInput) (*tastePreferences, error) {
	p := emptyTastePreferences()
	var err error
	if p.Statements, err = cleanTasteList(in.Statements, 30, 200, "statements"); err != nil {
		return nil, err
	}
	for _, f := range []struct {
		in  []string
		out *[]string
		max int
		w   string
	}{{in.LikedMakers, &p.LikedMakers, 80, "makers"}, {in.DislikedMakers, &p.DislikedMakers, 80, "makers"}, {in.LikedTraits, &p.LikedTraits, 60, "traits"}, {in.DislikedTraits, &p.DislikedTraits, 60, "traits"}} {
		if *f.out, err = cleanTasteList(f.in, 40, f.max, f.w); err != nil {
			return nil, err
		}
	}
	p.PriceGuidance = strings.TrimSpace(strings.ToValidUTF8(in.PriceGuidance, ""))
	if utf8.RuneCountInString(p.PriceGuidance) > 500 {
		return nil, errors.New("price guidance must be 500 characters or fewer")
	}
	p.resolve()
	return p, nil
}

// resolve fills the trait feature keys the model and the runner use.
func (p *tastePreferences) resolve() {
	keys := func(traits []string) []string {
		out := []string{}
		seen := map[string]bool{}
		for _, t := range traits {
			if k := tasteTraitKey(t); k != "" && !seen[k] {
				seen[k] = true
				out = append(out, k)
			}
		}
		sort.Strings(out)
		return out
	}
	p.LikedTraitKeys, p.DislikedTraitKeys = keys(p.LikedTraits), keys(p.DislikedTraits)
}

// priorMeans: stated likes start at +0.5 log-odds, dislikes at -0.5; the
// reactions then move them like any other weight.
func (p *tastePreferences) priorMeans() map[string]float64 {
	out := map[string]float64{}
	if p == nil {
		return out
	}
	for _, m := range p.LikedMakers {
		out["maker:"+identityPart(m)] += tastePreferencePrior
	}
	for _, m := range p.DislikedMakers {
		out["maker:"+identityPart(m)] -= tastePreferencePrior
	}
	for _, k := range p.LikedTraitKeys {
		out[k] += tastePreferencePrior
	}
	for _, k := range p.DislikedTraitKeys {
		out[k] -= tastePreferencePrior
	}
	for k, v := range out {
		if v == 0 {
			delete(out, k)
		}
	}
	return out
}

func (p *tastePreferences) feature(key string) tasteFeature {
	kind := strings.SplitN(key, ":", 2)[0]
	if kind == "maker" {
		for _, m := range append(append([]string{}, p.LikedMakers...), p.DislikedMakers...) {
			if "maker:"+identityPart(m) == key {
				return tasteFeature{key, kind, m}
			}
		}
	}
	return tasteFeature{key, kind, tasteFeatureLabel(key)}
}

func (app *application) loadTastePreferences(ctx context.Context, user uuid.UUID) (*tastePreferences, error) {
	p := emptyTastePreferences()
	var statements, likedM, dislikedM, likedT, dislikedT []byte
	var at time.Time
	err := app.db.QueryRow(ctx, `SELECT revision,statements,liked_makers,disliked_makers,liked_traits,disliked_traits,price_guidance,created_at
 FROM trumpet_taste_preferences WHERE user_id=$1 ORDER BY revision DESC LIMIT 1`, user).Scan(&p.Revision, &statements, &likedM, &dislikedM, &likedT, &dislikedT, &p.PriceGuidance, &at)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, nil
	}
	if err != nil {
		return nil, err
	}
	for _, pair := range []struct {
		data   []byte
		target *[]string
	}{{statements, &p.Statements}, {likedM, &p.LikedMakers}, {dislikedM, &p.DislikedMakers}, {likedT, &p.LikedTraits}, {dislikedT, &p.DislikedTraits}} {
		if err := json.Unmarshal(pair.data, pair.target); err != nil {
			return nil, err
		}
	}
	p.UpdatedAt = &at
	p.resolve()
	return p, nil
}

func (app *application) getTastePreferences(w http.ResponseWriter, r *http.Request) {
	user, err := app.userID(r.Context())
	if err != nil {
		app.serverError(w, err)
		return
	}
	p, err := app.loadTastePreferences(r.Context(), user)
	if err != nil {
		app.serverError(w, err)
		return
	}
	writeJSON(w, 200, p)
}

// putTastePreferences saves a new version (full replacement). With
// expectedRevision, a stale editor gets 409 instead of overwriting.
func (app *application) putTastePreferences(w http.ResponseWriter, r *http.Request) {
	var in tastePreferencesInput
	if err := decodeTrumpetJSON(w, r, &in); err != nil {
		writeError(w, 400, "invalid preferences JSON")
		return
	}
	p, err := normalizeTastePreferences(in)
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
	var current int
	if err = tx.QueryRow(r.Context(), `SELECT COALESCE(max(revision),0) FROM trumpet_taste_preferences WHERE user_id=$1`, user).Scan(&current); err != nil {
		app.serverError(w, err)
		return
	}
	if in.ExpectedRevision != nil && *in.ExpectedRevision != current {
		writeError(w, 409, "preferences changed elsewhere; reload and try again")
		return
	}
	enc := func(v []string) []byte { b, _ := json.Marshal(v); return b }
	var at time.Time
	err = tx.QueryRow(r.Context(), `INSERT INTO trumpet_taste_preferences(user_id,revision,statements,liked_makers,disliked_makers,liked_traits,disliked_traits,price_guidance)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8) RETURNING created_at`, user, current+1, enc(p.Statements), enc(p.LikedMakers), enc(p.DislikedMakers), enc(p.LikedTraits), enc(p.DislikedTraits), p.PriceGuidance).Scan(&at)
	if err != nil {
		app.serverError(w, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		app.serverError(w, err)
		return
	}
	p.Revision, p.UpdatedAt = current+1, &at
	writeJSON(w, 200, p)
}
