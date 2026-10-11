package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Taste training: Swipe and Head-to-head reactions are append-only events
// (migration 032). A regularized Bradley-Terry / logistic model over listing
// features is refit from those events when its inputs change (cached per owner,
// see trumpets_taste_cache.go), steering the learned listing score, Today's
// alerts (once the owner has reacted) and the daily runner's search budget.

// One-tap chips. Trait chips assert a feature the owner sees in the horn
// (added to that horn's feature set); reason chips are recorded for analysis.
var tasteChips = []string{"raw brass", "gold plate", "patina", "engraving", "odd bell", "vintage", "one-off", "great value", "too pricey", "wrong vibe"}
var tasteChipFeatures = map[string]string{
	"raw brass": "finish:raw brass", "gold plate": "finish:gold plate", "patina": "finish:patina",
	"engraving": "trait:engraving", "odd bell": "bell:unusual", "vintage": "era:vintage",
	"one-off": "trait:one-off", "great value": "price:below", "too pricey": "price:above",
}

const (
	tastePriorPrecision     = 1.0  // L2 / Gaussian prior on feature weights (sd 1).
	tasteInterceptPrecision = 0.25 // Swipe base rate; cancels out of pairs.
	tasteHalfLifeDays       = 365.0
	tasteBlendPoints        = 40.0 // Largest learned adjustment to the rule score.
	tasteConfidenceHalf     = 8.0  // Effective observations at 50% confidence.
	tasteReseeAfter         = 14 * 24 * time.Hour
	tasteQueueLimit         = 30
	tastePairLimit          = 10
	tasteMinSupport         = 2  // distinct horns; fewer and the feature keeps its prior
	tasteNewtonIterations   = 10 // warm-started from the previous fit
	// The learned blend phases in over the owner's first taste reactions
	// (swipes and head-to-head picks), so deploying with only existing ratings
	// leaves Today's alerts exactly as they were.
	tasteReactionsForFullBlend = 10.0
)

type tasteFeature struct {
	Key   string `json:"feature"`
	Kind  string `json:"kind"`
	Label string `json:"label"`
}

type tasteEvent struct {
	ID             int64     `json:"id"`
	ClientID       string    `json:"clientId"`
	Kind           string    `json:"kind"`
	Mode           string    `json:"mode"`
	ListingID      string    `json:"listingId,omitempty"`
	OtherListingID string    `json:"otherListingId,omitempty"`
	Action         string    `json:"action,omitempty"`
	Tag            string    `json:"tag,omitempty"`
	Note           string    `json:"note,omitempty"`
	Undoes         *int64    `json:"undoes,omitempty"`
	HasAudio       bool      `json:"hasAudio"`
	AudioMs        *int      `json:"audioDurationMs,omitempty"`
	CreatedAt      time.Time `json:"createdAt"`
	undone         bool
}

type tasteWeight struct {
	tasteFeature
	Weight     float64 `json:"weight"`
	SD         float64 `json:"sd"`
	N          float64 `json:"n"`
	Confidence float64 `json:"confidence"`
}
type tasteScore struct {
	Taste   float64 `json:"taste"`   // 0–100, 50 = neutral
	Blended float64 `json:"blended"` // rule score adjusted by the learned taste
	SD      float64 `json:"sd"`      // uncertainty of the learned affinity
	// Share of the learned adjustment in Blended (0 before any reaction); the
	// alert ranking scales its separate rating/favorite bonuses by 1 - share.
	LearnedShare float64 `json:"learnedShare"`
}
type tasteQueueItem struct {
	ListingID string  `json:"listingId"`
	Reason    string  `json:"reason"`
	Taste     float64 `json:"taste"`
	SD        float64 `json:"sd"`
}
type tastePair struct {
	A  string  `json:"a"`
	B  string  `json:"b"`
	P  float64 `json:"p"`
	SD float64 `json:"sd"`
}
type tasteChange struct {
	tasteFeature
	Before float64 `json:"before"`
	After  float64 `json:"after"`
}

// tasteModel is a fitted posterior: mean weights and (Laplace) covariance over
// the features that appear in training data; other features keep the prior.
type tasteModel struct {
	index       map[string]int // feature key -> parameter (0 is the swipe intercept)
	features    map[string]tasteFeature
	theta       []float64
	cov         [][]float64
	counts      map[string]float64
	prior       map[string]float64
	preferences *tastePreferences
	effective   float64
	swipes      int
	pairs       int
	ratings     int
	updatedAt   *time.Time
}

type tasteObs struct {
	keys  []string
	vals  []float64
	horns []string // physical horn each key came from (for feature support)
	y, w  float64
	bias  bool
}

// ---- Features -------------------------------------------------------------

var (
	tasteFinishes = []struct{ key, label, re string }{
		{"finish:raw brass", "Raw brass", `raw brass|unlacquered|unlaquered|bare brass`},
		{"finish:raw nickel", "Raw nickel", `raw nickel`},
		{"finish:gold plate", "Gold plate", `gold[- ]plat|24k|gold finish|full gold`},
		{"finish:silver plate", "Silver plate", `silver[- ]plat`},
		{"finish:lacquer", "Lacquer", `\blacquer(?:ed)?\b`},
		{"finish:patina", "Patina / aged", `patina|aged brass|antiqued|relic`},
		{"finish:mixed metals", "Mixed metals", `mixed metals?|two[- ]tone`},
		{"finish:black", "Black finish", `black (?:hardware|finish|nickel|chrome|plat)`},
		{"finish:copper", "Copper", `\bcopper\b`},
	}
	tasteTraits = []struct{ key, label, re string }{
		{"bell:upswept", "Upswept bell", `up-?swept|dizzy`},
		{"bell:unusual", "Unusual bell", `unusual bell|odd bell|bell geometry|bell flare|large bell|oversized bell`},
		{"leadpipe:reverse", "Reverse leadpipe", `reversed? lead ?pipe`},
		{"leadpipe:interchangeable", "Interchangeable leadpipes", `(?:removable|interchangeable|multiple|two|three) lead ?pipes?`},
		{"trait:engraving", "Engraving", `engrav`},
		{"trait:one-off", "One-off / custom", `one[- ]off|prototype|bespoke|custom|hand[- ]?made|handcraft`},
		{"trait:provenance", "Provenance", `provenance|artist|owned by|played by|signature model|documentation`},
		{"trait:heavy", "Heavyweight", `heavy ?weight|\bheavy\b`},
		{"trait:engineering", "Unusual engineering", `engineering|weird|unusual construction|mbs`},
	}
	tasteCompiled = map[string]*regexp.Regexp{}
	tasteYear     = regexp.MustCompile(`\b(18[89][0-9]|19[0-9]{2}|20[0-2][0-9])\b`)
	tasteToken    = regexp.MustCompile(`[a-z0-9]+`)
)

func init() {
	for _, list := range [][]struct{ key, label, re string }{tasteFinishes, tasteTraits} {
		for _, f := range list {
			tasteCompiled[f.key] = regexp.MustCompile(f.re)
		}
	}
}

var tasteGenericModelWords = map[string]bool{"bb": true, "b": true, "trumpet": true, "trumpets": true, "professional": true, "used": true, "the": true, "a": true, "custom": true, "vintage": true, "new": true, "model": true, "pro": true}

// tasteSourceKey groups an offer's source by its domain (eBay country sites
// are one family), falling back to the reported source name.
func tasteSourceKey(l trumpetListing) (string, string) {
	if u, err := url.Parse(l.URL); err == nil && u.Hostname() != "" {
		host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
		if strings.HasPrefix(host, "ebay.") {
			return "ebay", "eBay"
		}
		return host, host
	}
	return identityPart(l.Source), l.Source
}

// tasteMarket holds median asking prices by currency, and by maker+currency,
// over distinct verified horns.
type tasteMarket map[string][]float64

func newTasteMarket(listings []trumpetListing) tasteMarket {
	m := tasteMarket{}
	seen := map[string]bool{}
	for _, l := range listings {
		if l.VerificationState != "verified" || l.Price == nil || *l.Price <= 0 || seen[l.HornID+l.Currency] {
			continue
		}
		seen[l.HornID+l.Currency] = true
		m[l.Currency] = append(m[l.Currency], *l.Price)
		k := identityPart(l.Maker) + "|" + l.Currency
		m[k] = append(m[k], *l.Price)
	}
	return m
}
func (m tasteMarket) median(key string, exclude float64) (float64, bool) {
	values := []float64{}
	excluded := false
	for _, v := range m[key] {
		if !excluded && v == exclude {
			excluded = true
			continue
		}
		values = append(values, v)
	}
	if len(values) < 3 {
		return 0, false
	}
	sort.Float64s(values)
	return (values[(len(values)-1)/2] + values[len(values)/2]) / 2, true
}

// tasteFeatures extracts the listing's features: maker, model family, era,
// finish/metal, bell/leadpipe traits, provenance/one-off flags, price relative
// to the market, and source. ownerTags are chips the owner tapped for the horn.
func tasteFeatures(l trumpetListing, market tasteMarket, ownerTags []string) []tasteFeature {
	out := []tasteFeature{}
	seen := map[string]bool{}
	add := func(key, kind, label string) {
		if !seen[key] {
			seen[key] = true
			out = append(out, tasteFeature{key, kind, label})
		}
	}
	maker := identityPart(l.Maker)
	if maker != "" {
		add("maker:"+maker, "maker", strings.TrimSpace(l.Maker))
		for _, token := range tasteToken.FindAllString(strings.ToLower(l.Model), -1) {
			if tasteGenericModelWords[token] || len(token) < 2 || strings.Contains(maker, token) {
				continue
			}
			add("family:"+maker+"/"+token, "family", strings.TrimSpace(l.Maker)+" "+strings.ToUpper(token[:1])+token[1:])
			break
		}
	}
	text := strings.ToLower(strings.Join(append([]string{l.Title, l.Model, l.Description, l.Details.Finish, l.Details.Bell, l.Details.Bore, l.Details.Provenance, strings.Join(l.Details.NotableFeatures, " ")}, l.Tags...), " "))
	for _, list := range [][]struct{ key, label, re string }{tasteFinishes, tasteTraits} {
		for _, f := range list {
			if tasteCompiled[f.key].MatchString(text) {
				add(f.key, strings.SplitN(f.key, ":", 2)[0], f.label)
			}
		}
	}
	year := 0
	if l.Details.Year != nil {
		year = *l.Details.Year
	} else if m := tasteYear.FindString(l.Title + " " + l.Model + " " + l.Details.Provenance); m != "" {
		year, _ = strconv.Atoi(m)
	}
	switch {
	case year > 0 && year < 1970:
		add("era:vintage", "era", "Vintage (before 1970)")
	case year >= 1970 && year < 2000:
		add("era:classic", "era", "1970s–1990s")
	case year >= 2000:
		add("era:modern", "era", "Modern (2000+)")
	case strings.Contains(text, "vintage") || strings.Contains(text, "antique"):
		add("era:vintage", "era", "Vintage (before 1970)")
	}
	if l.Price != nil && *l.Price > 0 {
		median, ok := market.median(maker+"|"+l.Currency, *l.Price)
		if !ok {
			median, ok = market.median(l.Currency, *l.Price)
		}
		if ok && median > 0 {
			switch ratio := *l.Price / median; {
			case ratio <= 0.85:
				add("price:below", "price", "Below market")
			case ratio >= 1.25:
				add("price:above", "price", "Above market")
			default:
				add("price:near", "price", "Near market")
			}
		}
	}
	if key, label := tasteSourceKey(l); key != "" {
		add("source:"+key, "source", label)
	}
	for _, tag := range ownerTags {
		key, ok := tasteChipFeatures[tag]
		if !ok {
			continue
		}
		if strings.HasPrefix(key, "price:") {
			kept := out[:0]
			for _, f := range out {
				if f.Kind != "price" {
					kept = append(kept, f)
				}
			}
			out = kept
			delete(seen, "price:below")
			delete(seen, "price:near")
			delete(seen, "price:above")
		}
		parts := strings.SplitN(key, ":", 2)
		label := tasteFeatureLabel(key)
		add(key, parts[0], label)
	}
	return out
}

func tasteFeatureLabel(key string) string {
	for _, list := range [][]struct{ key, label, re string }{tasteFinishes, tasteTraits} {
		for _, f := range list {
			if f.key == key {
				return f.label
			}
		}
	}
	switch key {
	case "era:vintage":
		return "Vintage (before 1970)"
	case "price:below":
		return "Below market"
	case "price:above":
		return "Above market"
	}
	return key
}

// ---- Fitting --------------------------------------------------------------

func sigmoid(z float64) float64 { return 1 / (1 + math.Exp(-z)) }

// fitTaste fits a regularized logistic (swipes, ratings) / Bradley-Terry
// (pairs) model by Newton's method and keeps the Laplace covariance.
// prior holds the owner's stated preferences as prior means (0 otherwise).
// Features seen on fewer than tasteMinSupport distinct horns stay at their
// prior (pruned from the fit): a feature unique to one horn cannot be told
// apart from that horn, and pruning bounds the matrix size. warm, when given,
// starts Newton from a previous fit's weights ("" is the intercept).
func fitTaste(obs []tasteObs, features map[string]tasteFeature, prior map[string]float64, warm ...map[string]float64) *tasteModel {
	m := &tasteModel{index: map[string]int{}, features: features, counts: map[string]float64{}, prior: prior}
	horns := map[string]map[string]bool{}
	for _, o := range obs {
		for i, k := range o.keys {
			if horns[k] == nil {
				horns[k] = map[string]bool{}
			}
			horn := "?"
			if i < len(o.horns) {
				horn = o.horns[i]
			}
			horns[k][horn] = true
		}
	}
	support := map[string]int{}
	for k, set := range horns {
		support[k] = len(set)
	}
	keys := make([]string, 0, len(support)+len(prior))
	for k := range prior {
		keys = append(keys, k)
	}
	for k, n := range support {
		if _, stated := prior[k]; !stated && n >= tasteMinSupport {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		m.index[k] = len(m.index) + 1
	}
	type sparse struct {
		idx []int
		val []float64
		y   float64
		w   float64
	}
	data := make([]sparse, 0, len(obs))
	for _, o := range obs {
		m.effective += o.w
		s := sparse{y: o.y, w: o.w}
		if o.bias {
			s.idx, s.val = append(s.idx, 0), append(s.val, 1)
		}
		for i, k := range o.keys {
			if p, ok := m.index[k]; ok {
				s.idx, s.val = append(s.idx, p), append(s.val, o.vals[i])
				m.counts[k] += o.w
			}
		}
		if len(s.idx) > 0 {
			data = append(data, s)
		}
	}
	d := len(m.index) + 1
	m.theta = make([]float64, d)
	mean := make([]float64, d)
	for k, v := range prior {
		mean[m.index[k]] = v
	}
	copy(m.theta, mean)
	for _, start := range warm {
		if v, ok := start[""]; ok {
			m.theta[0] = v
		}
		for k, i := range m.index {
			if v, ok := start[k]; ok {
				m.theta[i] = v
			}
		}
	}
	precision := func(i int) float64 {
		if i == 0 {
			return tasteInterceptPrecision
		}
		return tastePriorPrecision
	}
	a := make([][]float64, d)
	for i := range a {
		a[i] = make([]float64, d)
	}
	g := make([]float64, d)
	hessian := func() {
		for i := range a {
			for j := range a[i] {
				a[i][j] = 0
			}
			a[i][i] = precision(i)
			g[i] = -precision(i) * (m.theta[i] - mean[i])
		}
		for _, o := range data {
			z := 0.0
			for i, p := range o.idx {
				z += m.theta[p] * o.val[i]
			}
			p := sigmoid(z)
			r, s := o.w*(o.y-p), o.w*p*(1-p)
			for i, pi := range o.idx {
				g[pi] += r * o.val[i]
				for j, pj := range o.idx {
					a[pi][pj] += s * o.val[i] * o.val[j]
				}
			}
		}
	}
	for iter := 0; iter < tasteNewtonIterations; iter++ {
		hessian()
		step, ok := choleskySolve(a, g)
		if !ok {
			break
		}
		largest := 0.0
		for i := range step {
			// Damped steps keep separable data (one-sided swipes) stable.
			if math.Abs(step[i]) > 4 {
				step[i] = math.Copysign(4, step[i])
			}
			m.theta[i] += step[i]
			largest = math.Max(largest, math.Abs(step[i]))
		}
		if largest < 1e-6 {
			break
		}
	}
	hessian()
	m.cov = choleskyInverse(a)
	return m
}

// thetaMap exports the fitted weights for warm-starting the next fit.
func (m *tasteModel) thetaMap() map[string]float64 {
	out := map[string]float64{"": m.theta[0]}
	for k, i := range m.index {
		out[k] = m.theta[i]
	}
	return out
}

func cholesky(a [][]float64) ([][]float64, bool) {
	n := len(a)
	l := make([][]float64, n)
	for i := range l {
		l[i] = make([]float64, n)
	}
	for i := 0; i < n; i++ {
		for j := 0; j <= i; j++ {
			sum := a[i][j]
			for k := 0; k < j; k++ {
				sum -= l[i][k] * l[j][k]
			}
			if i == j {
				if sum <= 0 {
					return nil, false
				}
				l[i][i] = math.Sqrt(sum)
			} else {
				l[i][j] = sum / l[j][j]
			}
		}
	}
	return l, true
}
func choleskySolveWith(l [][]float64, b []float64) []float64 {
	n := len(b)
	y := make([]float64, n)
	for i := 0; i < n; i++ {
		sum := b[i]
		for k := 0; k < i; k++ {
			sum -= l[i][k] * y[k]
		}
		y[i] = sum / l[i][i]
	}
	x := make([]float64, n)
	for i := n - 1; i >= 0; i-- {
		sum := y[i]
		for k := i + 1; k < n; k++ {
			sum -= l[k][i] * x[k]
		}
		x[i] = sum / l[i][i]
	}
	return x
}
func choleskySolve(a [][]float64, b []float64) ([]float64, bool) {
	l, ok := cholesky(a)
	if !ok {
		return nil, false
	}
	return choleskySolveWith(l, b), true
}
func choleskyInverse(a [][]float64) [][]float64 {
	n := len(a)
	l, ok := cholesky(a)
	inv := make([][]float64, n)
	for i := range inv {
		inv[i] = make([]float64, n)
	}
	if !ok {
		for i := range inv {
			inv[i][i] = 1 / tastePriorPrecision
		}
		return inv
	}
	for j := 0; j < n; j++ {
		e := make([]float64, n)
		e[j] = 1
		col := choleskySolveWith(l, e)
		for i := range col {
			inv[i][j] = col[i]
		}
	}
	return inv
}

// weight returns a feature's posterior mean and sd (the prior for features
// no reaction has touched yet).
func (m *tasteModel) weight(key string) (float64, float64) {
	if i, ok := m.index[key]; ok {
		return m.theta[i], math.Sqrt(math.Max(m.cov[i][i], 0))
	}
	return 0, math.Sqrt(1 / tastePriorPrecision)
}

// affinity is the learned log-odds for a feature set (no intercept), with its
// posterior variance.
func (m *tasteModel) affinity(keys []string) (float64, float64) {
	z, v := 0.0, 0.0
	for _, a := range keys {
		w, sd := m.weight(a)
		z += w
		ia, okA := m.index[a]
		if !okA {
			v += sd * sd
			continue
		}
		for _, b := range keys {
			if ib, okB := m.index[b]; okB {
				v += m.cov[ia][ib]
			}
		}
	}
	return z, v
}

// covariance of the affinities of two feature sets.
func (m *tasteModel) covariance(a, b []string) float64 {
	c := 0.0
	for _, x := range a {
		ix, okX := m.index[x]
		for _, y := range b {
			if x == y && !okX {
				c += 1 / tastePriorPrecision
				continue
			}
			if iy, okY := m.index[y]; okX && okY {
				c += m.cov[ix][iy]
			}
		}
	}
	return c
}

// confidence grows with the data behind the fit (reactions and ratings);
// stated preferences count as one observation each, at most eight.
func (m *tasteModel) confidence() float64 {
	n := m.effective + math.Min(8, float64(len(m.prior)))
	return n / (n + tasteConfidenceHalf)
}

// learnedShare phases the blend in over the first taste reactions: 0 with no
// swipes or picks (ratings and stated preferences alone never move alerts),
// 1 from tasteReactionsForFullBlend reactions on.
func (m *tasteModel) learnedShare() float64 {
	return math.Min(1, float64(m.swipes+m.pairs)/tasteReactionsForFullBlend)
}

// score blends the rule score with the learned affinity. Until the owner has
// reacted it is exactly the rule score, so Today's 65 threshold and its alerts
// are unchanged by deploying the model.
func (m *tasteModel) score(rule float64, keys []string) tasteScore {
	z, v := m.affinity(keys)
	share := m.learnedShare()
	blended := rule + tasteBlendPoints*m.confidence()*share*(2*sigmoid(z)-1)
	return tasteScore{Taste: math.Round(1000*sigmoid(z)) / 10, Blended: math.Round(10*math.Max(0, math.Min(100, blended))) / 10, SD: math.Round(1000*math.Sqrt(v)) / 1000, LearnedShare: share}
}

func (m *tasteModel) weights() []tasteWeight {
	out := []tasteWeight{}
	for key := range m.index {
		w, sd := m.weight(key)
		out = append(out, tasteWeight{tasteFeature: m.features[key], Weight: math.Round(1000*w) / 1000, SD: math.Round(1000*sd) / 1000, N: math.Round(100*m.counts[key]) / 100, Confidence: math.Round(1000*math.Max(0, 1-sd)) / 1000})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Weight != out[j].Weight {
			return out[i].Weight > out[j].Weight
		}
		return out[i].Key < out[j].Key
	})
	return out
}

// ---- Training data --------------------------------------------------------

type tasteHorn struct {
	primary  trumpetListing
	listings []trumpetListing
}

// tasteData groups offers by physical horn and resolves the owner's current
// tags per horn (the latest add/remove per chip wins).
type tasteData struct {
	byListing map[string]trumpetListing
	horns     map[string]*tasteHorn
	order     []string
	tags      map[string][]string // horn -> current chips
	market    tasteMarket
	features  map[string][]tasteFeature // listing -> features
	all       map[string]tasteFeature
}

func newTasteData(listings []trumpetListing, events []tasteEvent) *tasteData {
	d := &tasteData{byListing: map[string]trumpetListing{}, horns: map[string]*tasteHorn{}, tags: map[string][]string{}, market: newTasteMarket(listings), features: map[string][]tasteFeature{}, all: map[string]tasteFeature{}}
	rank := func(l trumpetListing) int {
		r := 0
		if l.Status == "active" {
			r += 4
		}
		if l.VerificationState == "verified" {
			r += 2
		}
		if len(l.Images) > 0 {
			r++
		}
		return r
	}
	for _, l := range listings {
		d.byListing[l.ID] = l
		h, ok := d.horns[l.HornID]
		if !ok {
			h = &tasteHorn{primary: l}
			d.horns[l.HornID] = h
			d.order = append(d.order, l.HornID)
		} else if rank(l) > rank(h.primary) {
			h.primary = l
		}
		h.listings = append(h.listings, l)
	}
	state := map[string]map[string]bool{}
	for _, e := range events {
		if e.Kind != "tag" || e.undone {
			continue
		}
		l, ok := d.byListing[e.ListingID]
		if !ok {
			continue
		}
		if state[l.HornID] == nil {
			state[l.HornID] = map[string]bool{}
		}
		state[l.HornID][e.Tag] = e.Action == "add"
	}
	for horn, tags := range state {
		for _, chip := range tasteChips {
			if tags[chip] {
				d.tags[horn] = append(d.tags[horn], chip)
			}
		}
	}
	for _, l := range listings {
		fs := tasteFeatures(l, d.market, d.tags[l.HornID])
		d.features[l.ID] = fs
		for _, f := range fs {
			d.all[f.Key] = f
		}
	}
	return d
}
func (d *tasteData) keys(listingID string) []string {
	out := []string{}
	for _, f := range d.features[listingID] {
		out = append(out, f.Key)
	}
	return out
}

// observations turns reactions (and existing ratings) into model data. Events
// before `before` only are used when before is non-nil (for recent changes).
func (d *tasteData) observations(events []tasteEvent, now time.Time, before *time.Time) []tasteObs {
	obs := []tasteObs{}
	decay := func(at time.Time) float64 {
		return math.Pow(0.5, now.Sub(at).Hours()/24/tasteHalfLifeDays)
	}
	single := func(id string, y, w float64) {
		keys := d.keys(id)
		vals := make([]float64, len(keys))
		horns := make([]string, len(keys))
		for i := range vals {
			vals[i], horns[i] = 1, d.byListing[id].HornID
		}
		obs = append(obs, tasteObs{keys: keys, vals: vals, horns: horns, y: y, w: w, bias: true})
	}
	for _, e := range events {
		if e.undone || (before != nil && !e.CreatedAt.Before(*before)) {
			continue
		}
		if _, ok := d.byListing[e.ListingID]; !ok {
			continue
		}
		w := decay(e.CreatedAt)
		switch e.Kind {
		case "swipe":
			switch e.Action {
			case "right":
				single(e.ListingID, 1, w)
			case "up":
				single(e.ListingID, 1, 2*w)
			case "left":
				single(e.ListingID, 0, w)
			}
		case "pair":
			if _, ok := d.byListing[e.OtherListingID]; !ok {
				continue
			}
			switch e.Action {
			case "a", "b":
				winner, loser := e.ListingID, e.OtherListingID
				if e.Action == "b" {
					winner, loser = loser, winner
				}
				diff := map[string]float64{}
				for _, k := range d.keys(winner) {
					diff[k]++
				}
				for _, k := range d.keys(loser) {
					diff[k]--
				}
				o := tasteObs{y: 1, w: w}
				keys := make([]string, 0, len(diff))
				for k, v := range diff {
					if v != 0 {
						keys = append(keys, k)
					}
				}
				sort.Strings(keys)
				for _, k := range keys {
					horn := d.byListing[winner].HornID
					if diff[k] < 0 {
						horn = d.byListing[loser].HornID
					}
					o.keys, o.vals, o.horns = append(o.keys, k), append(o.vals, diff[k]), append(o.horns, horn)
				}
				if len(o.keys) > 0 {
					obs = append(obs, o)
				}
			case "both":
				single(e.ListingID, 1, w/2)
				single(e.OtherListingID, 1, w/2)
			case "neither":
				single(e.ListingID, 0, w/2)
				single(e.OtherListingID, 0, w/2)
			}
		}
	}
	// Existing ratings, favorites and passes are profile evidence too.
	for _, horn := range d.order {
		h := d.horns[horn]
		f := h.primary.Feedback
		switch {
		case f.Rating != nil && *f.Rating >= 4:
			single(h.primary.ID, 1, 0.5)
		case f.Rating != nil && *f.Rating <= 2:
			single(h.primary.ID, 0, 0.5)
		case f.Favorite:
			single(h.primary.ID, 1, 0.5)
		case f.InterestState == "pass":
			single(h.primary.ID, 0, 0.5)
		}
	}
	return obs
}

func buildTasteModel(listings []trumpetListing, events []tasteEvent, now time.Time, prefs ...*tastePreferences) (*tasteModel, *tasteData) {
	return buildTasteModelWarm(listings, events, now, nil, prefs...)
}

func buildTasteModelWarm(listings []trumpetListing, events []tasteEvent, now time.Time, warm map[string]float64, prefs ...*tastePreferences) (*tasteModel, *tasteData) {
	d := newTasteData(listings, events)
	prior := map[string]float64{}
	for _, p := range prefs {
		for key, mean := range p.priorMeans() {
			prior[key] = mean
			if _, ok := d.all[key]; !ok {
				d.all[key] = p.feature(key)
			}
		}
	}
	var start []map[string]float64
	if warm != nil {
		start = append(start, warm)
	}
	m := fitTaste(d.observations(events, now, nil), d.all, prior, start...)
	for _, e := range events {
		if e.undone {
			continue
		}
		switch e.Kind {
		case "swipe":
			m.swipes++
		case "pair":
			m.pairs++
		}
		if m.updatedAt == nil || e.CreatedAt.After(*m.updatedAt) {
			at := e.CreatedAt
			m.updatedAt = &at
		}
	}
	for _, h := range d.horns {
		if f := h.primary.Feedback; f.Rating != nil || f.Favorite || f.InterestState == "pass" {
			m.ratings++
		}
	}
	return m, d
}

// applyTasteScores sets each listing's learned and blended scores.
func applyTasteScores(listings []trumpetListing, m *tasteModel, d *tasteData) {
	for i := range listings {
		s := m.score(listings[i].SearchScore, d.keys(listings[i].ID))
		listings[i].TasteScore = &s
	}
}

func markUndone(events []tasteEvent) []tasteEvent {
	undone := map[int64]bool{}
	for _, e := range events {
		if e.Kind == "undo" && e.Undoes != nil {
			undone[*e.Undoes] = true
		}
	}
	for i := range events {
		events[i].undone = undone[events[i].ID]
	}
	return events
}

// ---- Active learning ------------------------------------------------------

func (d *tasteData) eligible(h *tasteHorn) bool {
	l := h.primary
	return !l.Acquired && l.Status != "acquired" && l.Feedback.InterestState != "pass"
}

// tasteQueue: unseen horns first (active, then newest), then horns not swiped
// recently, most uncertain first.
func tasteQueue(m *tasteModel, d *tasteData, events []tasteEvent, now time.Time) []tasteQueueItem {
	lastSwipe := map[string]time.Time{}
	for _, e := range events {
		if e.Kind != "swipe" || e.undone {
			continue
		}
		if l, ok := d.byListing[e.ListingID]; ok && e.CreatedAt.After(lastSwipe[l.HornID]) {
			lastSwipe[l.HornID] = e.CreatedAt
		}
	}
	type item struct {
		tasteQueueItem
		active      bool
		first       time.Time
		uncertainty float64
	}
	unseen, seen := []item{}, []item{}
	for _, horn := range d.order {
		h := d.horns[horn]
		if !d.eligible(h) {
			continue
		}
		z, v := m.affinity(d.keys(h.primary.ID))
		p := sigmoid(z)
		it := item{tasteQueueItem: tasteQueueItem{ListingID: h.primary.ID, Taste: math.Round(1000*p) / 10, SD: math.Round(1000*math.Sqrt(v)) / 1000}, active: h.primary.Status == "active", first: h.primary.FirstSeen, uncertainty: math.Sqrt(v) * 4 * p * (1 - p)}
		last, swiped := lastSwipe[horn]
		switch {
		case !swiped:
			it.Reason = "new"
			unseen = append(unseen, it)
		case now.Sub(last) >= tasteReseeAfter:
			it.Reason = "uncertain"
			seen = append(seen, it)
		}
	}
	sort.SliceStable(unseen, func(i, j int) bool {
		if unseen[i].active != unseen[j].active {
			return unseen[i].active
		}
		return unseen[i].first.After(unseen[j].first)
	})
	sort.SliceStable(seen, func(i, j int) bool { return seen[i].uncertainty > seen[j].uncertainty })
	out := []tasteQueueItem{}
	for _, it := range append(unseen, seen...) {
		out = append(out, it.tasteQueueItem)
		if len(out) == tasteQueueLimit {
			break
		}
	}
	return out
}

// tastePairs picks the comparisons whose outcome the model is least sure of:
// largest predictive sd of the difference times p(1-p), skipping pairs
// compared in the last two weeks; each horn appears at most twice.
func tastePairs(m *tasteModel, d *tasteData, events []tasteEvent, now time.Time) []tastePair {
	recent := map[string]bool{}
	pairKey := func(a, b string) string {
		if a > b {
			a, b = b, a
		}
		return a + "|" + b
	}
	for _, e := range events {
		if e.Kind != "pair" || e.undone || now.Sub(e.CreatedAt) > tasteReseeAfter {
			continue
		}
		a, okA := d.byListing[e.ListingID]
		b, okB := d.byListing[e.OtherListingID]
		if okA && okB {
			recent[pairKey(a.HornID, b.HornID)] = true
		}
	}
	type cand struct {
		horn  string
		id    string
		keys  []string
		z, v  float64
		score float64
	}
	pool := []cand{}
	for _, horn := range d.order {
		h := d.horns[horn]
		if !d.eligible(h) {
			continue
		}
		keys := d.keys(h.primary.ID)
		z, v := m.affinity(keys)
		pool = append(pool, cand{horn: horn, id: h.primary.ID, keys: keys, z: z, v: v})
	}
	// Bound the O(n²) search to the most uncertain horns.
	sort.SliceStable(pool, func(i, j int) bool { return pool[i].v > pool[j].v })
	if len(pool) > 40 {
		pool = pool[:40]
	}
	type option struct {
		a, b  int
		score float64
		p, sd float64
	}
	options := []option{}
	for i := range pool {
		for j := i + 1; j < len(pool); j++ {
			if recent[pairKey(pool[i].horn, pool[j].horn)] {
				continue
			}
			v := pool[i].v + pool[j].v - 2*m.covariance(pool[i].keys, pool[j].keys)
			if v <= 1e-9 {
				continue
			}
			p := sigmoid(pool[i].z - pool[j].z)
			options = append(options, option{i, j, math.Sqrt(v) * 4 * p * (1 - p), p, math.Sqrt(v)})
		}
	}
	sort.SliceStable(options, func(i, j int) bool {
		if options[i].score != options[j].score {
			return options[i].score > options[j].score
		}
		return pool[options[i].a].id+pool[options[i].b].id < pool[options[j].a].id+pool[options[j].b].id
	})
	used := map[int]int{}
	out := []tastePair{}
	for _, o := range options {
		if used[o.a] >= 2 || used[o.b] >= 2 {
			continue
		}
		used[o.a]++
		used[o.b]++
		out = append(out, tastePair{A: pool[o.a].id, B: pool[o.b].id, P: math.Round(1000*o.p) / 1000, SD: math.Round(1000*o.sd) / 1000})
		if len(out) == tastePairLimit {
			break
		}
	}
	return out
}

// tasteWeekAgoFit is the model fitted on reactions older than a week.
func tasteWeekAgoFit(m *tasteModel, d *tasteData, events []tasteEvent, now time.Time) *tasteModel {
	cut := now.Add(-7 * 24 * time.Hour)
	return fitTaste(d.observations(events, now, &cut), d.all, m.prior, m.thetaMap())
}

// tasteChanges compares the model with the fit from a week ago.
func tasteChanges(m, old *tasteModel) []tasteChange {
	out := []tasteChange{}
	for _, w := range m.weights() {
		before, _ := old.weight(w.Key)
		if math.Abs(w.Weight-before) >= 0.1 {
			out = append(out, tasteChange{tasteFeature: w.tasteFeature, Before: math.Round(1000*before) / 1000, After: w.Weight})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return math.Abs(out[i].After-out[i].Before) > math.Abs(out[j].After-out[j].Before)
	})
	if len(out) > 6 {
		out = out[:6]
	}
	return out
}

// tasteMachineSummary is what search clients see: weights and uncertainties
// by maker, family, trait and source. No listing ids, notes or audio.
func tasteMachineSummary(m *tasteModel) map[string]any {
	return map[string]any{
		"version": 1, "observations": m.swipes + m.pairs + m.ratings, "swipes": m.swipes, "pairs": m.pairs, "ratings": m.ratings,
		"effectiveObservations": math.Round(100*m.effective) / 100, "confidence": math.Round(1000*m.confidence()) / 1000,
		"priorSd": math.Sqrt(1 / tastePriorPrecision), "weights": m.weights(), "updatedAt": m.updatedAt, "statedPreferences": len(m.prior),
		"interpretation": "Log-odds weights from a regularized Bradley-Terry/logistic fit over swipes, head-to-head choices and ratings. Positive = liked. sd is the posterior uncertainty (prior sd 1); explore high-sd options, demote options whose weight+sd stays below zero.",
	}
}

// ---- Storage --------------------------------------------------------------

const tasteEventColumns = `id,client_id,kind,mode,COALESCE(listing_id::text,''),COALESCE(other_listing_id::text,''),action,tag,note,undoes,audio_object IS NOT NULL,audio_duration_ms,created_at`

func scanTasteEvent(row pgx.Row) (tasteEvent, error) {
	var e tasteEvent
	err := row.Scan(&e.ID, &e.ClientID, &e.Kind, &e.Mode, &e.ListingID, &e.OtherListingID, &e.Action, &e.Tag, &e.Note, &e.Undoes, &e.HasAudio, &e.AudioMs, &e.CreatedAt)
	return e, err
}

func (app *application) loadTasteEvents(ctx context.Context, user uuid.UUID) ([]tasteEvent, error) {
	rows, err := app.db.Query(ctx, `SELECT `+tasteEventColumns+` FROM trumpet_taste_events WHERE user_id=$1 ORDER BY id`, user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []tasteEvent{}
	for rows.Next() {
		e, err := scanTasteEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return markUndone(out), rows.Err()
}

// trumpetTaste loads reactions and fits the model for an owner's listings.
func (app *application) trumpetTaste(ctx context.Context, user uuid.UUID, listings []trumpetListing) (*tasteModel, *tasteData, []tasteEvent, error) {
	events, err := app.loadTasteEvents(ctx, user)
	if err != nil {
		return nil, nil, nil, err
	}
	prefs, err := app.loadTastePreferences(ctx, user)
	if err != nil {
		return nil, nil, nil, err
	}
	now := time.Now()
	key := tasteCacheKey(listings, events, prefs, now)
	m, d, warm := tasteCacheGet(user, key)
	if m == nil {
		m, d = buildTasteModelWarm(listings, events, now, warm, prefs)
		m.preferences = prefs
		tasteCachePut(user, key, m, d)
	}
	applyTasteScores(listings, m, d)
	return m, d, events, nil
}

// ---- HTTP -----------------------------------------------------------------

type tasteEventInput struct {
	ClientID       string `json:"clientId"`
	Kind           string `json:"kind"`
	Mode           string `json:"mode"`
	ListingID      string `json:"listingId"`
	OtherListingID string `json:"otherListingId"`
	Action         string `json:"action"`
	Tag            string `json:"tag"`
	Note           string `json:"note"`
	Undoes         int64  `json:"undoes"`
}

func validateTasteEvent(in *tasteEventInput) error {
	if !wbClientIDPattern.MatchString(in.ClientID) {
		return errors.New("clientId must be 8–64 letters, digits, - or _")
	}
	if !containsString([]string{"", "swipe", "pair"}, in.Mode) {
		return errors.New("invalid mode")
	}
	in.Note = strings.TrimSpace(in.Note)
	if len([]rune(in.Note)) > 4000 {
		return errors.New("notes are limited to 4000 characters")
	}
	needListing := in.Kind != "undo"
	if needListing {
		if _, err := uuid.Parse(in.ListingID); err != nil {
			return errors.New("listingId required")
		}
	}
	if in.OtherListingID != "" {
		if _, err := uuid.Parse(in.OtherListingID); err != nil || in.OtherListingID == in.ListingID {
			return errors.New("invalid otherListingId")
		}
	}
	switch in.Kind {
	case "swipe":
		if !containsString([]string{"right", "left", "up"}, in.Action) || in.OtherListingID != "" {
			return errors.New("swipe action must be right, left or up")
		}
	case "pair":
		if !containsString([]string{"a", "b", "both", "neither"}, in.Action) || in.OtherListingID == "" {
			return errors.New("pair needs both listings and a, b, both or neither")
		}
	case "tag":
		if !containsString(tasteChips, in.Tag) || !containsString([]string{"add", "remove"}, in.Action) {
			return errors.New("unknown tag or action")
		}
	case "note":
		if in.Note == "" {
			return errors.New("note text required")
		}
	case "undo":
		if in.Undoes <= 0 {
			return errors.New("undoes must name an event")
		}
	default:
		return errors.New("kind must be swipe, pair, tag, note or undo")
	}
	return nil
}

func (app *application) tasteResponse(ctx context.Context, user uuid.UUID) (map[string]any, error) {
	listings, err := app.loadTrumpets(ctx, user)
	if err != nil {
		return nil, err
	}
	m, d, events, err := app.trumpetTaste(ctx, user, listings)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	scores := map[string]tasteScore{}
	for _, l := range listings {
		scores[l.ID] = *l.TasteScore
	}
	tags := map[string][]string{}
	counts := map[string]int{}
	for horn, chips := range d.tags {
		for _, l := range d.horns[horn].listings {
			tags[l.ID] = chips
		}
		for _, c := range chips {
			counts[c]++
		}
	}
	recent := []tasteEvent{}
	notes := []trumpetListing{}
	for i := len(events) - 1; i >= 0; i-- {
		e := events[i]
		if e.Kind == "undo" || e.undone {
			continue
		}
		if len(recent) < 30 {
			recent = append(recent, e)
		}
		if e.Kind == "note" && e.Note != "" {
			if l, ok := d.byListing[e.ListingID]; ok {
				l.HornID = fmt.Sprintf("note-%d", e.ID) // each note is its own statement source
				l.Feedback.Notes = e.Note
				notes = append(notes, l)
			}
		}
	}
	summary := tasteMachineSummary(m)
	return map[string]any{
		"model": summary, "weights": summary["weights"], "changes": tasteChanges(m, tasteCachedWeekAgo(user, m, d, events, now)),
		"scores": scores, "queue": tasteQueue(m, d, events, now), "pairs": tastePairs(m, d, events, now),
		"tags": tags, "tagCounts": counts, "chips": tasteChips, "recent": recent, "preferences": m.preferences,
		"noteSignals": trumpetNotePreferences(notes),
	}, nil
}

func (app *application) getTrumpetTaste(w http.ResponseWriter, r *http.Request) {
	user, err := app.userID(r.Context())
	if err != nil {
		app.serverError(w, err)
		return
	}
	out, err := app.tasteResponse(r.Context(), user)
	if err != nil {
		app.serverError(w, err)
		return
	}
	writeJSON(w, 200, out)
}

func (app *application) machineTrumpetTaste(w http.ResponseWriter, r *http.Request) {
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
	m, _, _, err := app.trumpetTaste(r.Context(), user, listings)
	if err != nil {
		app.serverError(w, err)
		return
	}
	writeJSON(w, 200, tasteMachineSummary(m))
}

// insertTasteEvent appends one event. A replayed clientId returns the
// original event (replayed=true) without adding history.
func insertTasteEvent(ctx context.Context, tx pgx.Tx, user uuid.UUID, in tasteEventInput, audio *tasteAudio) (tasteEvent, bool, error) {
	existing, err := scanTasteEvent(tx.QueryRow(ctx, `SELECT `+tasteEventColumns+` FROM trumpet_taste_events WHERE user_id=$1 AND client_id=$2`, user, in.ClientID))
	if err == nil {
		return existing, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return tasteEvent{}, false, err
	}
	var undoes *int64
	if in.Kind == "undo" {
		var kind string
		var already bool
		err := tx.QueryRow(ctx, `SELECT kind,EXISTS(SELECT 1 FROM trumpet_taste_events u WHERE u.undoes=e.id) FROM trumpet_taste_events e WHERE e.id=$1 AND e.user_id=$2`, in.Undoes, user).Scan(&kind, &already)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && kind == "undo") {
			return tasteEvent{}, false, errTasteNotFound
		}
		if err != nil {
			return tasteEvent{}, false, err
		}
		if already {
			return tasteEvent{}, false, errTasteAlreadyUndone
		}
		undoes = &in.Undoes
	}
	var object, contentType any
	var size, duration any
	if audio != nil {
		object, contentType, size, duration = audio.object, audio.contentType, audio.size, audio.durationMs
	}
	e, err := scanTasteEvent(tx.QueryRow(ctx, `INSERT INTO trumpet_taste_events(user_id,client_id,kind,mode,listing_id,other_listing_id,action,tag,note,undoes,audio_object,audio_content_type,audio_bytes,audio_duration_ms)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14) RETURNING `+tasteEventColumns,
		user, in.ClientID, in.Kind, in.Mode, nullText(in.ListingID), nullText(in.OtherListingID), in.Action, in.Tag, in.Note, undoes, object, contentType, size, duration))
	return e, false, err
}

var (
	errTasteNotFound      = errors.New("event not found")
	errTasteAlreadyUndone = errors.New("already undone")
)

// listingsOwned checks referenced offers belong to the owner (the composite
// foreign keys enforce it too; this gives a clean 404).
func listingsOwned(ctx context.Context, tx pgx.Tx, user uuid.UUID, ids ...string) (bool, error) {
	for _, id := range ids {
		if id == "" {
			continue
		}
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM trumpet_listings WHERE id=$1 AND user_id=$2)`, id, user).Scan(&ok); err != nil || !ok {
			return false, err
		}
	}
	return true, nil
}

// tasteAfterCommitHook lets tests act between commit and response (nil in
// production).
var tasteAfterCommitHook func()

// commitTasteEvent stores one event. status is 0 on success, else the HTTP
// error to return; undoneAudio names a voice note object whose event was
// just undone (deleted best-effort by the caller after commit).
func (app *application) commitTasteEvent(ctx context.Context, user uuid.UUID, in tasteEventInput, audio *tasteAudio) (event tasteEvent, replayed bool, undoneAudio *string, status int, err error) {
	tx, err := app.db.Begin(ctx)
	if err != nil {
		return event, false, nil, 500, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	if err = lockTrumpetOwner(ctx, tx, user); err != nil {
		return event, false, nil, 500, err
	}
	owned, err := listingsOwned(ctx, tx, user, in.ListingID, in.OtherListingID)
	if err != nil {
		return event, false, nil, 500, err
	}
	if !owned {
		return event, false, nil, 404, errors.New("listing not found")
	}
	event, replayed, err = insertTasteEvent(ctx, tx, user, in, audio)
	switch {
	case errors.Is(err, errTasteNotFound):
		return event, false, nil, 404, err
	case errors.Is(err, errTasteAlreadyUndone):
		return event, false, nil, 409, errors.New("event already undone")
	case err != nil:
		return event, false, nil, 500, err
	}
	if in.Kind == "undo" && !replayed {
		if err = tx.QueryRow(ctx, `SELECT audio_object FROM trumpet_taste_events WHERE id=$1 AND user_id=$2`, in.Undoes, user).Scan(&undoneAudio); err != nil {
			return event, false, nil, 500, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return event, false, nil, 500, err
	}
	if tasteAfterCommitHook != nil {
		tasteAfterCommitHook()
	}
	return event, replayed, undoneAudio, 0, nil
}

// saveTasteEvent commits the event, then answers with the refreshed model.
// The response is built with a context that survives a client disconnect: the
// event is already stored either way.
func (app *application) saveTasteEvent(w http.ResponseWriter, r *http.Request, in tasteEventInput, audio *tasteAudio) {
	user, err := app.userID(r.Context())
	if err != nil {
		app.serverError(w, err)
		return
	}
	event, replayed, undoneAudio, status, err := app.commitTasteEvent(r.Context(), user, in, audio)
	if audio != nil && (status != 0 || replayed) {
		// Not stored (or a concurrent retry stored another copy): drop the upload.
		app.deleteObjectsBestEffort(context.WithoutCancel(r.Context()), []string{audio.object})
	}
	switch {
	case status == 500:
		app.serverError(w, err)
		return
	case status != 0:
		writeError(w, status, err.Error())
		return
	}
	ctx := context.WithoutCancel(r.Context())
	if undoneAudio != nil {
		// An undone voice note is no longer served; remove its audio too.
		app.deleteObjectsBestEffort(ctx, []string{*undoneAudio})
	}
	out, err := app.tasteResponse(ctx, user)
	if err != nil {
		app.serverError(w, err)
		return
	}
	out["event"] = event
	out["replayed"] = replayed
	code := 201
	if replayed {
		code = 200
	}
	writeJSON(w, code, out)
}

func (app *application) postTasteEvent(w http.ResponseWriter, r *http.Request) {
	var in tasteEventInput
	if err := decodeTrumpetJSON(w, r, &in); err != nil {
		writeError(w, 400, "invalid taste event JSON")
		return
	}
	if err := validateTasteEvent(&in); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	app.saveTasteEvent(w, r, in, nil)
}

type tasteAudio struct {
	object, contentType string
	size                int
	durationMs          *int
}

const tasteVoiceLimit = 10 << 20

func tasteVoiceTypeAllowed(contentType string) bool {
	contentType = strings.ToLower(contentType)
	_, ok := cpAudioTypes[contentType]
	_, alias := cpAudioAliases[contentType]
	return ok || alias
}

// postTasteVoice stores a hold-to-talk voice note privately (raw audio body,
// sniffed like Commonplace audio) and appends a note event referencing it.
// No transcription is attempted. The object is removed only when the event
// is not stored; once committed it is never deleted by this request.
func (app *application) postTasteVoice(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	in := tasteEventInput{ClientID: q.Get("clientId"), Kind: "note", Mode: q.Get("mode"), ListingID: q.Get("listingId"), OtherListingID: q.Get("otherListingId")}
	if err := validateTasteEvent(&tasteEventInput{ClientID: in.ClientID, Kind: "note", Mode: in.Mode, ListingID: in.ListingID, OtherListingID: in.OtherListingID, Note: "voice"}); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, tasteVoiceLimit))
	if err != nil {
		writeError(w, 413, "voice notes must be 10 MB or smaller")
		return
	}
	media, err := cpValidateMedia(body, r.Header.Get("Content-Type"), nil, nil)
	if err != nil || media.Kind != "audio" {
		writeError(w, 400, "voice notes must be MP3, M4A, AAC, WAV, WebM or Ogg audio")
		return
	}
	var duration *int
	if v, err := strconv.Atoi(q.Get("durationMs")); err == nil && v >= 0 && v < 10*60*1000 {
		duration = &v
	}
	user, err := app.userID(r.Context())
	if err != nil {
		app.serverError(w, err)
		return
	}
	// A retried upload with the same clientId returns the stored event.
	if existing, err := scanTasteEvent(app.db.QueryRow(r.Context(), `SELECT `+tasteEventColumns+` FROM trumpet_taste_events WHERE user_id=$1 AND client_id=$2`, user, in.ClientID)); err == nil {
		out, err := app.tasteResponse(context.WithoutCancel(r.Context()), user)
		if err != nil {
			app.serverError(w, err)
			return
		}
		out["event"], out["replayed"] = existing, true
		writeJSON(w, 200, out)
		return
	}
	name := "trumpet-taste/" + user.String() + "/" + uuid.NewString() + "-" + media.SHA256[:16] + "." + cpMediaExtension(media.ContentType)
	if err := app.inspirationObjects().Put(r.Context(), name, media.ContentType, map[string]string{"userId": user.String(), "sha256": media.SHA256}, body); err != nil {
		app.serverError(w, err)
		return
	}
	app.saveTasteEvent(w, r, in, &tasteAudio{object: name, contentType: media.ContentType, size: len(body), durationMs: duration})
}

// tasteVoice keeps the bucket private: an owner-checked 302 to a ten-minute
// signed URL, like Commonplace and inspiration media. Undone notes are gone.
func (app *application) tasteVoice(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, 400, "invalid event id")
		return
	}
	user, err := app.userID(r.Context())
	if err != nil {
		app.serverError(w, err)
		return
	}
	var name *string
	err = app.db.QueryRow(r.Context(), `SELECT e.audio_object FROM trumpet_taste_events e WHERE e.id=$1 AND e.user_id=$2
 AND NOT EXISTS(SELECT 1 FROM trumpet_taste_events u WHERE u.undoes=e.id)`, id, user).Scan(&name)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && name == nil) {
		writeError(w, 404, "voice note not found")
		return
	}
	if err != nil {
		app.serverError(w, err)
		return
	}
	signed, err := app.inspirationObjects().SignedGet(r.Context(), *name, time.Now().Add(10*time.Minute))
	if err != nil {
		app.serverError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "private, max-age=300")
	http.Redirect(w, r, signed, http.StatusFound)
}
