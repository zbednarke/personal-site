package main

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"
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
	listings := []trumpetListing{tasteListing("liked", "Brassworks", "R", "raw brass", 2000), tasteListing("other", "Brassworks", "S", "raw brass", 2000)}
	for i := range listings {
		listings[i].SearchScore = 60
	}
	event := json.RawMessage(`{"listingId":"liked","kind":"new listing"}`)
	if got := selectTrumpetAlerts(listings, []json.RawMessage{event}); len(got) != 0 {
		t.Fatal("rule score 60 is below the 65 threshold")
	}
	events := []tasteEvent{}
	for i := 0; i < 12; i++ {
		events = append(events, swipe(int64(i+1), "other", "up", now))
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
	liked, disliked, plain := m.score(60, d.keys("liked")), m.score(60, d.keys("disliked")), m.score(60, d.keys("plain"))
	if liked.Blended <= 60 || disliked.Blended >= 60 || plain.Blended != 60 {
		t.Fatalf("stated preferences did not steer: %v %v %v", liked, disliked, plain)
	}
	if _, err := normalizeTastePreferences(tastePreferencesInput{Statements: []string{strings.Repeat("x", 201)}}); err == nil {
		t.Fatal("long statement accepted")
	}
	if _, err := normalizeTastePreferences(tastePreferencesInput{PriceGuidance: strings.Repeat("x", 501)}); err == nil {
		t.Fatal("long price note accepted")
	}
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
