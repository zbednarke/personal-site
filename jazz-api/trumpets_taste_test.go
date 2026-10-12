package main

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// Fictional fixtures only: no real listings, sellers, prices or feedback.
func tasteListing(id, maker, model, finish string, price float64, extra ...func(*trumpetListing)) trumpetListing {
	p := price
	l := trumpetListing{trumpetCandidate: trumpetCandidate{ID: id, HornID: "horn-" + id, Maker: maker, Model: model, Title: maker + " " + model + " · FIXTURE", URL: "https://fixtures.invalid/" + id, Source: "Fixture dealer", Price: &p, Currency: "USD", Status: "active", VerificationState: "verified", SearchScore: 60, Details: hornDetails{Finish: finish}}, FirstSeen: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)}
	l.Feedback.InterestState = "watch"
	for _, f := range extra {
		f(&l)
	}
	return l
}

func tasteKeys(fs []tasteFeature) map[string]bool {
	out := map[string]bool{}
	for _, f := range fs {
		out[f.Key] = true
	}
	return out
}

func TestTasteFeatures(t *testing.T) {
	year := 1952
	listings := []trumpetListing{
		tasteListing("a", "Fixturecraft", "Zephyr upswept", "raw brass", 1500, func(l *trumpetListing) {
			l.Details.Year = &year
			l.URL = "https://www.ebay.co.uk/itm/123456789012"
			l.Description = "Engraved bell, reversed leadpipe, one-off prototype with provenance letter."
		}),
		tasteListing("b", "Fixturecraft", "Zephyr", "lacquer", 3000),
		tasteListing("c", "Fixturecraft", "Zephyr", "lacquer", 3100),
		tasteListing("d", "Fixturecraft", "Zephyr", "gold plate", 3200),
	}
	market := newTasteMarket(listings)
	got := tasteKeys(tasteFeatures(listings[0], market, nil))
	for _, k := range []string{"maker:fixturecraft", "family:fixturecraft/zephyr", "era:vintage", "finish:raw brass", "bell:upswept", "leadpipe:reverse", "trait:engraving", "trait:one-off", "trait:provenance", "price:below", "source:ebay"} {
		if !got[k] {
			t.Errorf("missing %s in %v", k, got)
		}
	}
	if got["finish:lacquer"] {
		t.Error("unlacquered/raw brass should not read as lacquer")
	}
	// Owner chips assert features; a price chip replaces the computed bucket.
	tagged := tasteKeys(tasteFeatures(listings[0], market, []string{"too pricey", "odd bell", "wrong vibe"}))
	if !tagged["price:above"] || tagged["price:below"] || !tagged["bell:unusual"] {
		t.Errorf("owner chips not applied: %v", tagged)
	}
	if k := tasteKeys(tasteFeatures(listings[3], market, nil)); !k["finish:gold plate"] || !k["price:near"] || !k["source:fixtures.invalid"] {
		t.Errorf("gold/near/source: %v", k)
	}
}

func swipe(id int64, listing, action string, at time.Time) tasteEvent {
	return tasteEvent{ID: id, Kind: "swipe", ListingID: listing, Action: action, CreatedAt: at}
}

func TestTasteModelLearnsFromSwipesAndPairs(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	listings := []trumpetListing{}
	for i := 0; i < 6; i++ {
		listings = append(listings, tasteListing(fmt.Sprintf("raw%d", i), "Brassworks", fmt.Sprintf("R%d", i), "raw brass", 2000))
		listings = append(listings, tasteListing(fmt.Sprintf("gold%d", i), "Goldsmith", fmt.Sprintf("G%d", i), "gold plate", 2000))
	}
	// No reactions: blended score is exactly the rule score; prior sd 1.
	m, d := buildTasteModel(listings, nil, now)
	for _, l := range listings {
		if s := m.score(l.SearchScore, d.keys(l.ID)); s.Blended != l.SearchScore || s.Taste != 50 {
			t.Fatalf("untrained score changed: %+v", s)
		}
	}
	if _, sd := m.weight("finish:raw brass"); sd != 1 {
		t.Fatalf("prior sd = %v", sd)
	}
	events := []tasteEvent{}
	for i := 0; i < 6; i++ {
		events = append(events, swipe(int64(2*i+1), fmt.Sprintf("raw%d", i), "right", now.Add(-time.Hour)))
		events = append(events, swipe(int64(2*i+2), fmt.Sprintf("gold%d", i), "left", now.Add(-time.Hour)))
	}
	m, d = buildTasteModel(listings, events, now)
	raw, rawSD := m.weight("finish:raw brass")
	gold, _ := m.weight("finish:gold plate")
	if raw <= 0.3 || gold >= -0.3 {
		t.Fatalf("weights raw=%v gold=%v", raw, gold)
	}
	if rawSD >= 1 {
		t.Fatalf("uncertainty did not shrink: %v", rawSD)
	}
	liked := m.score(60, d.keys("raw0"))
	disliked := m.score(60, d.keys("gold0"))
	if liked.Blended <= 65 || disliked.Blended >= 60 || liked.Taste <= 50 {
		t.Fatalf("blended liked=%+v disliked=%+v", liked, disliked)
	}
	// An undo removes the reaction from the fit.
	undo := int64(1)
	withUndo := markUndone(append(append([]tasteEvent{}, events...), tasteEvent{ID: 99, Kind: "undo", Undoes: &undo, CreatedAt: now}))
	m2, _ := buildTasteModel(listings, withUndo, now)
	if m2.swipes != len(events)-1 {
		t.Fatalf("undone swipe counted: %d", m2.swipes)
	}
	// Bradley-Terry: Goldsmith consistently beats a third maker head to head.
	listings = append(listings, tasteListing("plain0", "Plainhorn", "P0", "lacquer", 2000), tasteListing("plain1", "Plainhorn", "P1", "lacquer", 2000))
	pairs := []tasteEvent{}
	for i := 0; i < 8; i++ {
		pairs = append(pairs, tasteEvent{ID: int64(200 + i), Kind: "pair", ListingID: fmt.Sprintf("gold%d", i%6), OtherListingID: fmt.Sprintf("plain%d", i%2), Action: "a", CreatedAt: now})
	}
	m3, _ := buildTasteModel(listings, pairs, now)
	g, _ := m3.weight("maker:goldsmith")
	p, _ := m3.weight("maker:plainhorn")
	if g <= p {
		t.Fatalf("pairwise preference not learned: goldsmith=%v plainhorn=%v", g, p)
	}
	if m3.pairs != 8 {
		t.Fatalf("pairs = %d", m3.pairs)
	}
	summary, _ := json.Marshal(tasteMachineSummary(m3))
	if !json.Valid(summary) {
		t.Fatal("summary not JSON")
	}
}

func TestTasteActiveLearning(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	listings := []trumpetListing{
		tasteListing("known1", "Knowncraft", "K1", "raw brass", 2000),
		tasteListing("known2", "Knowncraft", "K2", "raw brass", 2000),
		tasteListing("fresh1", "Newmaker", "N1", "gold plate", 2000, func(l *trumpetListing) { l.FirstSeen = now.Add(-time.Hour) }),
		tasteListing("fresh2", "Othermaker", "O1", "patina", 2000, func(l *trumpetListing) { l.Status = "sold" }),
		tasteListing("owned", "Ownedcraft", "X", "raw brass", 2000, func(l *trumpetListing) { l.Acquired = true }),
	}
	events := []tasteEvent{}
	for i := 0; i < 10; i++ {
		events = append(events, swipe(int64(i+1), "known1", "right", now.Add(-20*24*time.Hour)), swipe(int64(i+100), "known2", "right", now.Add(-time.Hour)))
	}
	m, d := buildTasteModel(listings, markUndone(events), now)
	queue := tasteQueue(m, d, events, now)
	ids := []string{}
	for _, q := range queue {
		ids = append(ids, q.ListingID)
	}
	// Unseen first (active before sold), then seen horns not swiped recently;
	// acquired horns and recently swiped horns are left out.
	want := []string{"fresh1", "fresh2", "known1"}
	if fmt.Sprint(ids) != fmt.Sprint(want) {
		t.Fatalf("queue %v, want %v", ids, want)
	}
	if queue[0].Reason != "new" || queue[2].Reason != "uncertain" {
		t.Fatalf("reasons %+v", queue)
	}
	pairs := tastePairs(m, d, events, now)
	if len(pairs) == 0 {
		t.Fatal("no pairs")
	}
	// The most informative pair involves the unfamiliar horns, never two
	// already well-understood Knowncraft horns.
	first := pairs[0]
	if (first.A == "known1" || first.A == "known2") && (first.B == "known1" || first.B == "known2") {
		t.Fatalf("picked a low-information pair %+v", first)
	}
	for _, p := range pairs {
		if p.A == "owned" || p.B == "owned" {
			t.Fatal("acquired horn offered for comparison")
		}
	}
	// A pair compared recently is not offered again.
	recent := append(events, tasteEvent{ID: 500, Kind: "pair", ListingID: first.A, OtherListingID: first.B, Action: "a", CreatedAt: now})
	m2, d2 := buildTasteModel(listings, markUndone(recent), now)
	for _, p := range tastePairs(m2, d2, recent, now) {
		if (p.A == first.A && p.B == first.B) || (p.A == first.B && p.B == first.A) {
			t.Fatal("recently compared pair repeated")
		}
	}
}

func TestTasteBlendedScoreDrivesAlertsWithSameThreshold(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	listings := []trumpetListing{tasteListing("liked", "Brassworks", "R", "raw brass", 2000), tasteListing("other", "Brassworks", "S", "raw brass", 2000), tasteListing("third", "Brassworks", "T", "raw brass", 2000)}
	for i := range listings {
		listings[i].SearchScore = 60
	}
	event := json.RawMessage(`{"listingId":"liked","kind":"new listing"}`)
	if got := selectTrumpetAlerts(listings, []json.RawMessage{event}); len(got) != 0 {
		t.Fatal("rule score 60 is below the 65 threshold")
	}
	events := []tasteEvent{}
	for i := 0; i < 12; i++ {
		events = append(events, swipe(int64(i+1), []string{"other", "third"}[i%2], "up", now))
	}
	m, d := buildTasteModel(listings, events, now)
	applyTasteScores(listings, m, d)
	if listings[0].TasteScore.Blended < 65 {
		t.Fatalf("blended %v", listings[0].TasteScore.Blended)
	}
	if got := selectTrumpetAlerts(listings, []json.RawMessage{event}); len(got) != 1 {
		t.Fatal("learned taste should lift the alert over 65")
	}
}

func TestValidateTasteEvent(t *testing.T) {
	id := "11111111-1111-4111-8111-111111111111"
	other := "22222222-2222-4222-8222-222222222222"
	ok := []tasteEventInput{
		{ClientID: "client-0001", Kind: "swipe", ListingID: id, Action: "right", Mode: "swipe"},
		{ClientID: "client-0002", Kind: "pair", ListingID: id, OtherListingID: other, Action: "neither", Mode: "pair"},
		{ClientID: "client-0003", Kind: "tag", ListingID: id, Tag: "raw brass", Action: "add"},
		{ClientID: "client-0004", Kind: "note", ListingID: id, Note: " Love the bell. "},
		{ClientID: "client-0005", Kind: "undo", Undoes: 4},
	}
	for _, in := range ok {
		if err := validateTasteEvent(&in); err != nil {
			t.Errorf("%+v: %v", in, err)
		}
	}
	bad := []tasteEventInput{
		{ClientID: "short", Kind: "swipe", ListingID: id, Action: "right"},
		{ClientID: "client-0001", Kind: "swipe", ListingID: id, Action: "down"},
		{ClientID: "client-0001", Kind: "swipe", ListingID: "nope", Action: "right"},
		{ClientID: "client-0001", Kind: "pair", ListingID: id, OtherListingID: id, Action: "a"},
		{ClientID: "client-0001", Kind: "pair", ListingID: id, Action: "a"},
		{ClientID: "client-0001", Kind: "tag", ListingID: id, Tag: "<script>", Action: "add"},
		{ClientID: "client-0001", Kind: "note", ListingID: id, Note: "   "},
		{ClientID: "client-0001", Kind: "undo"},
		{ClientID: "client-0001", Kind: "rating", ListingID: id},
		{ClientID: "client-0001", Kind: "swipe", ListingID: id, Action: "right", Mode: "carousel"},
	}
	for _, in := range bad {
		if err := validateTasteEvent(&in); err == nil {
			t.Errorf("accepted %+v", in)
		}
	}
}

func TestTastePreferencesArePriors(t *testing.T) {
	p, err := normalizeTastePreferences(tastePreferencesInput{
		Statements:  []string{"cool horn, not dark horn", "  ", "cool horn, not dark horn"},
		LikedMakers: []string{"Brassworks"}, DislikedMakers: []string{"Goldsmith"},
		LikedTraits: []string{"Raw brass", "upswept bell", "mystery sparkle"}, DislikedTraits: []string{"gold plating", "too pricey"},
		PriceGuidance: "Prefer value under similar asks.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Statements) != 1 || fmt.Sprint(p.LikedTraitKeys) != "[bell:upswept finish:raw brass]" || fmt.Sprint(p.DislikedTraitKeys) != "[finish:gold plate price:above]" {
		t.Fatalf("normalized %+v", p)
	}
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	listings := []trumpetListing{tasteListing("liked", "Brassworks", "R", "raw brass", 2000), tasteListing("disliked", "Goldsmith", "G", "gold plate", 2000), tasteListing("plain", "Plainhorn", "P", "lacquer", 2000)}
	m, d := buildTasteModel(listings, nil, now, p)
	if w, _ := m.weight("maker:brassworks"); w != 0.5 {
		t.Fatalf("liked maker prior %v", w)
	}
	// Learned taste (incl. stated preferences) leaves scores alone until the
	// owner has reacted; the affinity itself already reflects the preference.
	liked, disliked, plain := m.score(60, d.keys("liked")), m.score(60, d.keys("disliked")), m.score(60, d.keys("plain"))
	if liked.Blended != 60 || disliked.Blended != 60 || plain.Blended != 60 || liked.Taste <= 50 || disliked.Taste >= 50 {
		t.Fatalf("scores moved before any reaction: %v %v %v", liked, disliked, plain)
	}
	listings = append(listings, tasteListing("other", "Elsewhere", "E", "silver plate", 2000))
	events := []tasteEvent{}
	for i := 0; i < 10; i++ {
		action := "right"
		if i%2 == 1 {
			action = "left"
		}
		events = append(events, swipe(int64(i+1), "other", action, now))
	}
	m, d = buildTasteModel(listings, events, now, p)
	liked, disliked, plain = m.score(60, d.keys("liked")), m.score(60, d.keys("disliked")), m.score(60, d.keys("plain"))
	if liked.Blended <= 60 || disliked.Blended >= 60 || plain.Blended != 60 || liked.LearnedShare != 1 {
		t.Fatalf("stated preferences did not steer after reactions: %v %v %v", liked, disliked, plain)
	}
	if _, err := normalizeTastePreferences(tastePreferencesInput{Statements: []string{strings.Repeat("x", 201)}}); err == nil {
		t.Fatal("long statement accepted")
	}
	if _, err := normalizeTastePreferences(tastePreferencesInput{PriceGuidance: strings.Repeat("x", 501)}); err == nil {
		t.Fatal("long price note accepted")
	}
}

// Review fix: ratings alone must not change Today's alerts on deploy, and the
// favorite/rating bonuses are not counted twice once the blend is active.
func TestTasteBlendWaitsForReactionsAndAlertsDoNotDoubleCount(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	listings := []trumpetListing{}
	for i := 0; i < 20; i++ {
		rating := 1 + i%5
		listings = append(listings, tasteListing(fmt.Sprintf("r%d", i), fmt.Sprintf("Maker%d", i%4), "M", []string{"raw brass", "gold plate"}[i%2], 2000, func(l *trumpetListing) {
			l.Feedback.Rating = &rating
			l.Feedback.Favorite = i%3 == 0
			l.SearchScore = 58
		}))
	}
	events := []json.RawMessage{}
	for _, l := range listings {
		events = append(events, json.RawMessage(`{"listingId":"`+l.ID+`","kind":"new listing"}`))
	}
	before := selectTrumpetAlerts(listings, events)
	m, d := buildTasteModel(listings, nil, now)
	applyTasteScores(listings, m, d)
	for _, l := range listings {
		if l.TasteScore.Blended != l.SearchScore || l.TasteScore.LearnedShare != 0 {
			t.Fatalf("ratings alone moved %s: %+v", l.ID, l.TasteScore)
		}
	}
	if after := selectTrumpetAlerts(listings, events); fmt.Sprint(after) != fmt.Sprint(before) {
		t.Fatal("alerts changed on deploy with zero reactions")
	}
	// With a fully phased-in blend, a favorite gets no extra +15 on top.
	fav := listings[0]
	fav.TasteScore = &tasteScore{Blended: 60, LearnedShare: 1}
	one := []json.RawMessage{json.RawMessage(`{"listingId":"r0","kind":"new listing"}`)}
	if got := selectTrumpetAlerts([]trumpetListing{fav}, one); len(got) != 0 {
		t.Fatal("favorite/rating bonus double-counted over the learned score")
	}
	fav.TasteScore.LearnedShare = 0
	if got := selectTrumpetAlerts([]trumpetListing{fav}, one); len(got) != 1 {
		t.Fatal("rule-only scoring lost its favorite bonus")
	}
}

// Review fix: features seen once stay at their prior; the fit is bounded and
// cached, and a cache hit is effectively free.
func TestTasteFitPrunesAndIsFastAndCached(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	listings := []trumpetListing{}
	for i := 0; i < 600; i++ {
		i := i
		listings = append(listings, tasteListing(fmt.Sprintf("l%03d", i), fmt.Sprintf("Maker%02d", i%40), fmt.Sprintf("Model%03d series", i), []string{"raw brass", "gold plate", "lacquer", "silver plate"}[i%4], float64(1500+i*7), func(l *trumpetListing) {
			l.URL = fmt.Sprintf("https://dealer%03d.invalid/item/%d", i, i) // ~600 distinct sources and families
		}))
	}
	events := []tasteEvent{}
	for i := 0; i < 1500; i++ {
		action := []string{"right", "left", "up"}[i%3]
		events = append(events, swipe(int64(i+1), fmt.Sprintf("l%03d", (i*7)%600), action, now.Add(-time.Duration(i)*time.Minute)))
	}
	started := time.Now()
	m, d := buildTasteModel(listings, events, now)
	cold := time.Since(started)
	if len(m.index) > 200 {
		t.Fatalf("rare features were not pruned: d=%d", len(m.index))
	}
	if _, sd := m.weight("source:dealer001.invalid"); sd != 1 {
		t.Fatalf("a once-seen source should keep the prior: sd=%v", sd)
	}
	if cold > 1500*time.Millisecond {
		t.Fatalf("cold fit took %v", cold)
	}
	user := uuid.New()
	key := tasteCacheKey(listings, events, nil, now)
	tasteCachePut(user, key, m, d)
	started = time.Now()
	hit, _, _ := tasteCacheGet(user, tasteCacheKey(listings, events, nil, now))
	if hit != m || time.Since(started) > 200*time.Millisecond {
		t.Fatalf("cache miss or slow hit: %v", time.Since(started))
	}
	more := append(append([]tasteEvent{}, events...), swipe(9999, "l001", "right", now))
	if again, _, warm := tasteCacheGet(user, tasteCacheKey(listings, more, nil, now)); again != nil || warm == nil {
		t.Fatal("a new reaction must miss the cache and offer a warm start")
	}
	started = time.Now()
	week := tasteCachedWeekAgo(user, m, d, events, now)
	if tasteCachedWeekAgo(user, m, d, events, now) != week {
		t.Fatal("week-ago fit not cached")
	}
	if total := time.Since(started); total > 1500*time.Millisecond {
		t.Fatalf("recent-changes fit took %v", total)
	}
	_ = tasteChanges(m, week)
}

func TestCholeskyInverse(t *testing.T) {
	a := [][]float64{{4, 1}, {1, 3}}
	inv := choleskyInverse(a)
	det := 11.0
	want := [][]float64{{3 / det, -1 / det}, {-1 / det, 4 / det}}
	for i := range want {
		for j := range want[i] {
			if math.Abs(inv[i][j]-want[i][j]) > 1e-12 {
				t.Fatalf("inverse %v", inv)
			}
		}
	}
}

func TestTasteNoPhotoReactionsCountHalf(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	listings := []trumpetListing{}
	for i := 0; i < 6; i++ {
		listings = append(listings, tasteListing(fmt.Sprintf("raw%d", i), "Brassworks", fmt.Sprintf("R%d", i), "raw brass", 2000))
		listings = append(listings, tasteListing(fmt.Sprintf("gold%d", i), "Goldsmith", fmt.Sprintf("G%d", i), "gold plate", 2000))
	}
	yes, no := true, false
	reactions := func(meta tasteMeta) []tasteEvent {
		events := []tasteEvent{}
		for i := 0; i < 6; i++ {
			r, l := swipe(int64(2*i+1), fmt.Sprintf("raw%d", i), "right", now), swipe(int64(2*i+2), fmt.Sprintf("gold%d", i), "left", now)
			r.Meta, l.Meta = meta, meta
			events = append(events, r, l)
		}
		return events
	}
	withPhoto, _ := buildTasteModel(listings, reactions(tasteMeta{HadPhoto: &yes}), now)
	unknown, _ := buildTasteModel(listings, reactions(tasteMeta{}), now)
	withoutPhoto, _ := buildTasteModel(listings, reactions(tasteMeta{HadPhoto: &no}), now)
	wp, wpSD := withPhoto.weight("finish:raw brass")
	un, _ := unknown.weight("finish:raw brass")
	np, npSD := withoutPhoto.weight("finish:raw brass")
	// Older events (no metadata) keep full weight, like a reaction with a photo.
	if math.Abs(wp-un) > 1e-9 {
		t.Fatalf("unknown photo state changed the weight: %v vs %v", un, wp)
	}
	// Without a photo the same reactions move the model less and leave it less sure.
	if !(np > 0 && np < wp) || npSD <= wpSD {
		t.Fatalf("no-photo reactions not down-weighted: with=%v±%v without=%v±%v", wp, wpSD, np, npSD)
	}
	if math.Abs(withoutPhoto.effective-withPhoto.effective/2) > 1e-9 {
		t.Fatalf("effective observations: with=%v without=%v", withPhoto.effective, withoutPhoto.effective)
	}
	// Exactly half: two photo-less reactions fit like one with a photo.
	doubled := append(reactions(tasteMeta{HadPhoto: &no}), reactions(tasteMeta{HadPhoto: &no})...)
	for i := range doubled {
		doubled[i].ID = int64(i + 1)
	}
	twice, _ := buildTasteModel(listings, doubled, now)
	if tw, _ := twice.weight("finish:raw brass"); math.Abs(tw-wp) > 1e-3 {
		t.Fatalf("two photo-less reactions = %v, one with a photo = %v", tw, wp)
	}
	// A head-to-head choice without photos counts half as well.
	listings = append(listings, tasteListing("plain0", "Plainhorn", "P0", "lacquer", 2000), tasteListing("plain1", "Plainhorn", "P1", "lacquer", 2000))
	pairs := func(meta tasteMeta) []tasteEvent {
		out := []tasteEvent{}
		for i := 0; i < 8; i++ {
			out = append(out, tasteEvent{ID: int64(200 + i), Kind: "pair", ListingID: fmt.Sprintf("gold%d", i%6), OtherListingID: fmt.Sprintf("plain%d", i%2), Action: "a", Meta: meta, CreatedAt: now})
		}
		return out
	}
	pw, _ := buildTasteModel(listings, pairs(tasteMeta{HadPhoto: &yes}), now)
	pn, _ := buildTasteModel(listings, pairs(tasteMeta{HadPhoto: &no}), now)
	a, _ := pw.weight("maker:goldsmith")
	b, _ := pn.weight("maker:goldsmith")
	if !(b > 0 && b < a) {
		t.Fatalf("pair without photos not down-weighted: with=%v without=%v", a, b)
	}
}

func TestTasteQueuePrefersPhotosButStillShowsPhotoless(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	listings := []trumpetListing{}
	// Newest first alone would put every photo-less horn at the front.
	for i := 0; i < 8; i++ {
		listings = append(listings, tasteListing(fmt.Sprintf("bare%d", i), "Barecraft", fmt.Sprintf("B%d", i), "lacquer", 2000, func(l *trumpetListing) { l.FirstSeen = now.Add(-time.Duration(i) * time.Minute) }))
	}
	for i := 0; i < 8; i++ {
		listings = append(listings, tasteListing(fmt.Sprintf("photo%d", i), "Photocraft", fmt.Sprintf("P%d", i), "raw brass", 2000, func(l *trumpetListing) {
			l.FirstSeen = now.Add(-time.Duration(24+i) * time.Hour)
			l.Images = []string{"https://fixtures.invalid/photo.svg"}
		}))
	}
	m, d := buildTasteModel(listings, nil, now)
	queue := tasteQueue(m, d, nil, now)
	if len(queue) != 16 {
		t.Fatalf("queue length %d", len(queue))
	}
	got := ""
	for _, q := range queue {
		if strings.HasPrefix(q.ListingID, "photo") {
			got += "P"
		} else {
			got += "-"
		}
	}
	// One photo-less card in every five while photo cards remain, then the rest.
	if got != "PPPP-PPPP-------" {
		t.Fatalf("queue order %s", got)
	}
	if queue[0].ListingID != "photo0" || queue[4].ListingID != "bare0" {
		t.Fatalf("order within groups changed: %v %v", queue[0], queue[4])
	}
	if s := preferPhotos([]int{1, 2, 3}, func(int) bool { return false }); fmt.Sprint(s) != "[1 2 3]" {
		t.Fatalf("all photo-less: %v", s)
	}
}

func TestValidateTasteEventMeta(t *testing.T) {
	id := "11111111-1111-4111-8111-111111111111"
	other := "22222222-2222-4222-8222-222222222222"
	no := false
	for _, in := range []tasteEventInput{
		{ClientID: "client-0001", Kind: "swipe", ListingID: id, Action: "left", Meta: &tasteMeta{HadPhoto: &no}},
		{ClientID: "client-0002", Kind: "pair", ListingID: id, OtherListingID: other, Action: "a", Meta: &tasteMeta{HadPhoto: &no}},
	} {
		if err := validateTasteEvent(&in); err != nil {
			t.Errorf("%+v: %v", in, err)
		}
	}
	for _, in := range []tasteEventInput{
		{ClientID: "client-0003", Kind: "note", ListingID: id, Note: "x", Meta: &tasteMeta{HadPhoto: &no}},
		{ClientID: "client-0004", Kind: "undo", Undoes: 3, Meta: &tasteMeta{}},
	} {
		if err := validateTasteEvent(&in); err == nil {
			t.Errorf("accepted meta on %s", in.Kind)
		}
	}
}
