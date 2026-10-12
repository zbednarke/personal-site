package main

import (
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"testing"
	"time"
)

// Fictional fixtures only.
func comparableHorns(now time.Time, prefix string, n int) []trumpetListing {
	finishes := []string{"raw brass", "lacquer", "silver", "gold plate", "satin", "patina"}
	out := []trumpetListing{}
	for i := 0; i < n; i++ {
		out = append(out, tasteListing(fmt.Sprintf("%s%02d", prefix, i), fmt.Sprintf("Maker%d", i%7), fmt.Sprintf("M%d", i), finishes[i%len(finishes)], float64(1500+50*i), comparable(now)))
	}
	return out
}

func pairEvent(id int64, a, b, action string, at time.Time) tasteEvent {
	return tasteEvent{ID: id, Kind: "pair", ListingID: a, OtherListingID: b, Action: action, CreatedAt: at}
}

func TestTasteUnionFind(t *testing.T) {
	u := newTasteUnionFind([]string{"a", "b", "c", "d", "e"})
	if !u.union("a", "b") || !u.union("c", "d") || !u.union("b", "d") {
		t.Fatal("unions of separate sets")
	}
	if u.union("a", "c") {
		t.Fatal("a and c are already one set")
	}
	if u.find("a") != u.find("d") || u.find("e") == u.find("a") || u.size[u.find("a")] != 4 {
		t.Fatalf("sets: %v %v", u.parent, u.size)
	}
}

func TestTastePairGraphEdges(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	listings := comparableHorns(now, "h", 6)
	// A second listing of horn h00 is the same node.
	listings = append(listings, tasteListing("h00-relist", "Maker0", "M0", "raw brass", 1500, comparable(now), func(l *trumpetListing) { l.HornID = "horn-h00" }))
	// And a weak listing is not a node at all.
	listings = append(listings, tasteListing("weak", "Weak", "W", "lacquer", 1500))
	undone := pairEvent(5, "h04", "h05", "a", now)
	events := markUndone([]tasteEvent{
		pairEvent(1, "h00-relist", "h01", "both", now), // both: an edge, through the horn
		pairEvent(2, "h02", "h03", "neither", now),     // neither: an edge
		pairEvent(3, "h01", "weak", "a", now),          // a weak side: no edge
		pairEvent(4, "h03", "h04", "b", now),
		undone,
		{ID: 6, Kind: "undo", Undoes: &undone.ID, CreatedAt: now}, // undone: no edge
	})
	_, d := buildTasteModel(listings, events, now)
	g := newTastePairGraph(d, events, now)
	if len(g.nodes) != 6 {
		t.Fatalf("nodes %v", g.nodes)
	}
	got := []string{}
	for _, c := range g.components() {
		got = append(got, strings.Join(c, ","))
	}
	want := "[horn-h02,horn-h03,horn-h04 horn-h00,horn-h01 horn-h05]"
	if fmt.Sprint(got) != want {
		t.Fatalf("components %v, want %s", got, want)
	}
	if s := g.stats(); s.Eligible != 6 || s.Components != 3 || s.Largest != 3 || s.Share != 0.5 {
		t.Fatalf("stats %+v", s)
	}
}

func TestTastePairsBridgeIslandsFirst(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	listings := comparableHorns(now, "h", 10)
	// A well-compared main group h00..h05, an island h06-h07, and two
	// never-compared horns h08 and h09.
	events := []tasteEvent{}
	id := int64(1)
	for _, p := range [][2]string{{"h00", "h01"}, {"h01", "h02"}, {"h02", "h03"}, {"h03", "h04"}, {"h04", "h05"}, {"h00", "h03"}, {"h06", "h07"}} {
		events = append(events, pairEvent(id, p[0], p[1], "a", now.Add(-30*24*time.Hour)))
		id++
	}
	m, d := buildTasteModel(listings, markUndone(events), now)
	main := map[string]bool{"h00": true, "h01": true, "h02": true, "h03": true, "h04": true, "h05": true}
	pairs := tastePairs(m, d, events, now)
	if len(pairs) < 3 {
		t.Fatalf("pairs %+v", pairs)
	}
	// Singletons first, then the island; every one of them against the main
	// group.
	firstThree := map[string]bool{}
	for _, p := range pairs[:3] {
		inMain, other := p.A, p.B
		if !main[inMain] {
			inMain, other = other, inMain
		}
		if !main[inMain] || main[other] {
			t.Fatalf("pair %+v does not bridge the main group and an island (%+v)", p, pairs[:3])
		}
		firstThree[other] = true
	}
	if !firstThree["h08"] || !firstThree["h09"] || !(firstThree["h06"] || firstThree["h07"]) {
		t.Fatalf("first pairs %+v", pairs[:3])
	}
	if !(pairs[0].A == "h08" || pairs[0].B == "h08" || pairs[0].A == "h09" || pairs[0].B == "h09") {
		t.Fatalf("a singleton is bridged first: %+v", pairs[0])
	}
}

func TestTastePairsStrengthenAConnectedGraph(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	listings := comparableHorns(now, "h", 8)
	// A path h00 - h01 - ... - h07: connected, but long and thin.
	events := []tasteEvent{}
	for i := 0; i < 7; i++ {
		events = append(events, pairEvent(int64(i+1), fmt.Sprintf("h%02d", i), fmt.Sprintf("h%02d", i+1), "a", now.Add(-30*24*time.Hour)))
	}
	// The page shows the first pair of each answer: answering it 20 times,
	// about one top pair in five strengthens the graph.
	structures := 0
	for round := 0; round < 20; round++ {
		m, d := buildTasteModel(listings, markUndone(events), now)
		g := newTastePairGraph(d, events, now)
		if len(g.components()) != 1 {
			t.Fatal("the path should stay connected")
		}
		pairs := tastePairs(m, d, events, now)
		if len(pairs) == 0 {
			t.Fatal("no pairs")
		}
		top := pairs[0]
		if top.Reason == "structure" {
			structures++
			a, b := "horn-"+top.A, "horn-"+top.B
			// Least compared among the horns not in the last few choices
			// (those are kept out of the next pair when others exist).
			stale := tasteRecentPairHorns(d, events, tasteFreshAfter)
			minDegree := len(g.nodes)
			for _, h := range g.nodes {
				if !stale[h] {
					minDegree = min(minDegree, len(g.adj[h]))
				}
			}
			// With 8 horns most are stale, so only the least-compared side
			// is sure to be fresh.
			if stale[a] && stale[b] {
				t.Fatalf("round %d: structure pair %+v has only horns from the last %d choices", round, top, tasteFreshAfter)
			}
			if g.adj[a][b] || (len(g.adj[a]) != minDegree && len(g.adj[b]) != minDegree) {
				t.Fatalf("round %d: structure pair %+v is not a least-compared horn against a horn not yet next to it", round, top)
			}
		}
		for _, p := range pairs[1:] {
			if p.Reason == "structure" {
				t.Fatalf("a structure pair below the top: %+v", pairs)
			}
		}
		events = append(events, pairEvent(int64(len(events)+1), top.A, top.B, "a", now))
	}
	if structures < 3 || structures > 5 {
		t.Fatalf("%d structure pairs at the top in 20 answers, want about 4", structures)
	}
}

// A horn that is no longer comparable (sold) still connects the horns it
// was compared with; components count comparable horns only.
func TestTastePairGraphKeepsPathsThroughIneligibleHorns(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	listings := comparableHorns(now, "h", 7)
	listings[6].Status = "sold" // h06
	events := []tasteEvent{
		pairEvent(1, "h00", "h06", "a", now), // h00 > h06
		pairEvent(2, "h06", "h01", "a", now), // h06 > h01
	}
	_, d := buildTasteModel([]trumpetListing{listings[0], listings[1], listings[6]}, markUndone(events), now)
	g := newTastePairGraph(d, events, now)
	if len(g.nodes) != 2 || len(g.components()) != 1 {
		t.Fatalf("nodes %v, components %v", g.nodes, g.components())
	}
	if s := g.stats(); s.Components != 1 || s.Largest != 2 || s.Share != 1 {
		t.Fatalf("stats %+v", s)
	}
}

// A horn whose primary listing is not comparable is still a node when another
// of its listings is, and that listing is the one compared.
func TestTastePairsUseAHornsComparableListing(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	listings := comparableHorns(now, "h", 6)
	// Same horn as h00, listed first and ranked as high, but without a price.
	stale := tasteListing("h00-unpriced", "Maker0", "M0", "raw brass", 1500, comparable(now), func(l *trumpetListing) {
		l.HornID = "horn-h00"
		l.Price = nil
	})
	listings = append([]trumpetListing{stale}, listings...)
	m, d := buildTasteModel(listings, nil, now)
	if d.horns["horn-h00"].primary.ID != "h00-unpriced" {
		t.Fatalf("fixture: the unpriced listing should be the primary, got %s", d.horns["horn-h00"].primary.ID)
	}
	g := newTastePairGraph(d, nil, now)
	if len(g.nodes) != 6 || g.listed["horn-h00"].ID != "h00" {
		t.Fatalf("nodes %v, horn-h00 listed as %q", g.nodes, g.listed["horn-h00"].ID)
	}
	seen := false
	for _, p := range tastePairs(m, d, nil, now) {
		if p.A == "h00-unpriced" || p.B == "h00-unpriced" {
			t.Fatalf("pair with the unpriced listing: %+v", p)
		}
		seen = seen || p.A == "h00" || p.B == "h00"
	}
	if !seen {
		t.Fatal("horn h00 is never compared")
	}
}

// Answering the top pair each time connects 40 never-compared horns in about
// n-1 choices; horns added later are linked within two appearances.
func TestTastePairsConnectTheGraphQuickly(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	rng := rand.New(rand.NewSource(7))
	actions := []string{"a", "b", "both", "neither"}
	listings := comparableHorns(now, "h", 40)
	events := []tasteEvent{}
	answer := func() tastePair {
		t.Helper()
		m, d := buildTasteModel(listings, markUndone(events), now)
		pairs := tastePairs(m, d, events, now)
		if len(pairs) == 0 {
			t.Fatal("no pairs")
		}
		top := pairs[0]
		events = append(events, pairEvent(int64(len(events)+1), top.A, top.B, actions[rng.Intn(len(actions))], now))
		return top
	}
	components := func() int {
		_, d := buildTasteModel(listings, markUndone(events), now)
		return len(newTastePairGraph(d, events, now).components())
	}
	choices := 0
	for components() > 1 {
		if choices == 45 {
			t.Fatalf("still %d components after %d choices", components(), choices)
		}
		answer()
		choices++
	}
	t.Logf("40 horns connected after %d choices", choices)
	if choices > 39+6 {
		t.Fatalf("%d choices to connect 40 horns", choices)
	}
	// Three new horns arrive.
	listings = append(listings, comparableHorns(now, "new", 3)...)
	appearances := map[string]int{}
	for round := 0; round < 9 && components() > 1; round++ {
		top := answer()
		for _, id := range []string{top.A, top.B} {
			if strings.HasPrefix(id, "new") {
				appearances[id]++
			}
		}
	}
	if components() != 1 {
		t.Fatal("new horns not connected")
	}
	for id, n := range appearances {
		if n > 2 {
			t.Fatalf("%s took %d appearances to connect", id, n)
		}
	}
	if len(appearances) != 3 {
		t.Fatalf("appearances %v", appearances)
	}
}

// The owner's case: one component of 15 horns, 40 new horns, and one horn
// whose features make it far more uncertain than the rest (a would-be hub).
// Answering the first pair each time must not keep showing one horn, must
// grow the component as a tree of small degree rather than a star, and must
// still merge one island per answer.
func TestTastePairsRotateBridgeAnchors(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	listings := comparableHorns(now, "h", 15)
	listings = append(listings, comparableHorns(now, "s", 40)...)
	// The hub: in the main component, with features nothing else shares.
	listings[7] = tasteListing("h07", "Hubcraft", "One-off", "engraved copper", 9900, comparable(now), func(l *trumpetListing) {
		l.Description = "Prototype with reversed leadpipe, engraved bell and provenance letter."
	})
	events := []tasteEvent{}
	for i := 0; i < 14; i++ { // a path h00 - h01 - ... - h14
		events = append(events, pairEvent(int64(i+1), fmt.Sprintf("h%02d", i), fmt.Sprintf("h%02d", i+1), "a", now.Add(-30*24*time.Hour)))
	}
	{ // Fixture: the hub is by far the most uncertain horn.
		m, d := buildTasteModel(listings, markUndone(events), now)
		_, hubV := m.affinity(d.keys("h07"))
		for _, l := range listings {
			if _, v := m.affinity(d.keys(l.ID)); l.ID != "h07" && v*1.5 > hubV {
				t.Fatalf("fixture: %s has v %.3f, hub %.3f", l.ID, v, hubV)
			}
		}
	}
	graph := func() *tastePairGraph {
		_, d := buildTasteModel(listings, markUndone(events), now)
		return newTastePairGraph(d, events, now)
	}
	tops := []tastePair{}
	connectedAfter := 0
	for answer := 0; answer < 45; answer++ {
		before := len(graph().components())
		m, d := buildTasteModel(listings, markUndone(events), now)
		pairs := tastePairs(m, d, events, now)
		if len(pairs) == 0 {
			t.Fatalf("answer %d: no pairs", answer)
		}
		top := pairs[0]
		if before > 1 && top.Reason != "bridge" {
			t.Fatalf("answer %d: %d components but the first pair is %+v", answer, before, top)
		}
		// No horn of the last few pairs comes straight back.
		for k := max(0, len(tops)-tasteFreshAfter); k < len(tops); k++ {
			for _, id := range []string{top.A, top.B} {
				if id == tops[k].A || id == tops[k].B {
					t.Fatalf("answer %d: %s again, from answer %d (%+v then %+v)", answer, id, k, tops[k], top)
				}
			}
		}
		tops = append(tops, top)
		events = append(events, pairEvent(int64(len(events)+1), top.A, top.B, "a", now.Add(time.Duration(answer-60)*time.Minute)))
		after := len(graph().components())
		if before > 1 && after != before-1 {
			t.Fatalf("answer %d: bridge %+v left %d components (was %d)", answer, top, after, before)
		}
		if before > 1 && after == 1 {
			connectedAfter = answer + 1
		}
	}
	if connectedAfter != 40 {
		t.Fatalf("connected after %d answers, want 40 (one island per answer)", connectedAfter)
	}
	for i := 0; i+10 <= len(tops); i++ {
		count := map[string]int{}
		for _, p := range tops[i : i+10] {
			count[p.A]++
			count[p.B]++
		}
		for id, n := range count {
			if n > 3 {
				t.Fatalf("%s in %d of the first pairs %d..%d", id, n, i, i+9)
			}
		}
	}
	g := graph()
	maxDegree, hub := 0, ""
	for _, h := range g.nodes {
		if len(g.adj[h]) > maxDegree {
			maxDegree, hub = len(g.adj[h]), h
		}
	}
	t.Logf("connected after %d answers; max degree %d (%s), hub degree %d", connectedAfter, maxDegree, hub, len(g.adj["horn-h07"]))
	if maxDegree > 5 { // 4 today (the would-be hub); 5 leaves room for tie-order changes
		t.Fatalf("max degree %d (%s): the bridges form a star", maxDegree, hub)
	}
}

// tasteRecentPairHornsBySort is the obvious reference: sort every live pair
// between two known horns by CreatedAt then id, latest first, and take n.
func tasteRecentPairHornsBySort(d *tasteData, events []tasteEvent, n int) map[string]bool {
	pairs := []tasteEvent{}
	for _, e := range events {
		if e.Kind != "pair" || e.undone {
			continue
		}
		a, okA := d.byListing[e.ListingID]
		b, okB := d.byListing[e.OtherListingID]
		if okA && okB && a.HornID != b.HornID {
			pairs = append(pairs, e)
		}
	}
	sort.SliceStable(pairs, func(i, j int) bool {
		if !pairs[i].CreatedAt.Equal(pairs[j].CreatedAt) {
			return pairs[i].CreatedAt.After(pairs[j].CreatedAt)
		}
		return pairs[i].ID > pairs[j].ID
	})
	out := map[string]bool{}
	for i := 0; i < len(pairs) && i < n; i++ {
		out[d.byListing[pairs[i].ListingID].HornID] = true
		out[d.byListing[pairs[i].OtherListingID].HornID] = true
	}
	return out
}

func TestTasteRecentPairHorns(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	listings := comparableHorns(now, "h", 10)
	listings = append(listings, tasteListing("h00-relist", "Maker0", "M0", "raw brass", 1500, comparable(now), func(l *trumpetListing) { l.HornID = "horn-h00" }))
	at := func(m int) time.Time { return now.Add(time.Duration(m) * time.Minute) }
	undone := pairEvent(20, "h08", "h09", "a", at(9)) // the latest, but undone
	events := markUndone([]tasteEvent{
		pairEvent(1, "h01", "h02", "a", at(1)),           // too old: three live choices are newer
		pairEvent(2, "h03", "h04", "a", at(5)),           // same time as 3, lower id: older
		pairEvent(3, "h05", "h06", "b", at(5)),           //
		pairEvent(4, "h00-relist", "h07", "both", at(4)), // another listing of horn h00
		pairEvent(5, "h00", "h00-relist", "a", at(8)),    // one horn against itself: not a choice
		pairEvent(6, "h01", "gone", "a", at(8)),          // an unknown listing
		{ID: 7, Kind: "swipe", ListingID: "h09", Action: "right", CreatedAt: at(9)},
		undone,
		{ID: 21, Kind: "undo", Undoes: &undone.ID, CreatedAt: at(9)},
	})
	_, d := buildTasteModel(listings, events, now)
	got := tasteRecentPairHorns(d, events, 3)
	want := map[string]bool{"horn-h03": true, "horn-h04": true, "horn-h05": true, "horn-h06": true, "horn-h00": true, "horn-h07": true}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("recent horns %v, want %v", got, want)
	}
	// The same-time tie goes by id: with n=1 only the later id counts.
	if got := tasteRecentPairHorns(d, events, 1); fmt.Sprint(got) != fmt.Sprint(map[string]bool{"horn-h05": true, "horn-h06": true}) {
		t.Fatalf("n=1: %v", got)
	}
	if got := tasteRecentPairHorns(d, events, 0); len(got) != 0 {
		t.Fatalf("n=0: %v", got)
	}
	// Against the sorted reference on shuffled, tied and partly undone events.
	rng := rand.New(rand.NewSource(3))
	ids := []string{"h00", "h00-relist", "h01", "h02", "h03", "h04", "h05", "h06", "h07", "h08", "h09", "gone"}
	for round := 0; round < 300; round++ {
		evs := []tasteEvent{}
		perm := rng.Perm(1000) // unique ids, in no particular order, as event ids are unique
		for i, count := 0, rng.Intn(40); i < count; i++ {
			e := pairEvent(int64(perm[i]), ids[rng.Intn(len(ids))], ids[rng.Intn(len(ids))], "a", at(rng.Intn(6)))
			e.undone = rng.Intn(5) == 0
			evs = append(evs, e)
		}
		for _, n := range []int{1, 3, 5} {
			if got, want := tasteRecentPairHorns(d, evs, n), tasteRecentPairHornsBySort(d, evs, n); fmt.Sprint(got) != fmt.Sprint(want) {
				t.Fatalf("round %d n=%d: %v, want %v (events %+v)", round, n, got, want, evs)
			}
		}
	}
}

// assertBridgesNonRedundant: every bridge of one answer joins two sets of
// the graph's components plus that answer's earlier bridges. It returns the
// number of bridges.
func assertBridgesNonRedundant(t *testing.T, d *tasteData, g *tastePairGraph, pairs []tastePair) int {
	t.Helper()
	joined := g.uf.clone()
	bridges := 0
	for _, p := range pairs {
		if p.Reason != "bridge" {
			continue
		}
		bridges++
		if !joined.union(d.byListing[p.A].HornID, d.byListing[p.B].HornID) {
			t.Fatalf("redundant bridge %+v in %+v", p, pairs)
		}
	}
	return bridges
}

// When every horn of the largest component was just compared, a smaller
// component anchors the bridge, and the answer's bridges stay non-redundant.
func TestTastePairsSmallerComponentAnchors(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	listings := comparableHorns(now, "h", 10)
	events := markUndone([]tasteEvent{
		pairEvent(1, "h03", "h04", "a", now.Add(-48*time.Hour)),  // an island of two, compared long ago
		pairEvent(2, "h00", "h01", "a", now.Add(-3*time.Minute)), // the largest component, all just compared
		pairEvent(3, "h01", "h02", "a", now.Add(-2*time.Minute)),
		pairEvent(4, "h00", "h02", "a", now.Add(-time.Minute)),
	})
	m, d := buildTasteModel(listings, events, now)
	g := newTastePairGraph(d, events, now)
	pairs := tastePairs(m, d, events, now)
	if len(pairs) == 0 {
		t.Fatal("no pairs")
	}
	main := map[string]bool{"h00": true, "h01": true, "h02": true}
	island := map[string]bool{"h03": true, "h04": true}
	top := pairs[0]
	if top.Reason != "bridge" || main[top.A] || main[top.B] || !(island[top.A] || island[top.B]) {
		t.Fatalf("first pair %+v: want a bridge anchored on the island h03-h04, away from the horns just compared", top)
	}
	// 7 components: 6 non-redundant bridges join them all in one answer.
	if n := assertBridgesNonRedundant(t, d, g, pairs); n != 6 {
		t.Fatalf("%d bridges for 7 components: %+v", n, pairs)
	}
}

// Random graphs and recent choices: the bridges of an answer never pair two
// horns that the graph or that answer's earlier bridges already join.
func TestTastePairsBridgesNonRedundantFuzz(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	rng := rand.New(rand.NewSource(11))
	bridges, smaller := 0, 0
	for round := 0; round < 150; round++ {
		n := 8 + rng.Intn(30)
		listings := comparableHorns(now, "h", n)
		events := []tasteEvent{}
		for i, count := 0, rng.Intn(2*n); i < count; i++ {
			a, b := rng.Intn(n), rng.Intn(n)
			if rng.Intn(3) > 0 { // mostly short edges: several components of mixed size
				b = (a + 1 + rng.Intn(2)) % n
			}
			events = append(events, pairEvent(int64(i+1), fmt.Sprintf("h%02d", a), fmt.Sprintf("h%02d", b), "a", now.Add(-time.Duration(rng.Intn(600))*time.Minute)))
		}
		events = markUndone(events)
		m, d := buildTasteModel(listings, events, now)
		g := newTastePairGraph(d, events, now)
		pairs := tastePairs(m, d, events, now)
		bridges += assertBridgesNonRedundant(t, d, g, pairs)
		if comps := g.components(); len(comps) > 1 {
			largest := map[string]bool{}
			for _, h := range comps[0] {
				largest[h] = true
			}
			for _, p := range pairs {
				if p.Reason == "bridge" && !largest[d.byListing[p.A].HornID] && !largest[d.byListing[p.B].HornID] {
					smaller++
					break
				}
			}
		}
	}
	t.Logf("%d bridges; %d answers with a bridge outside the largest component", bridges, smaller)
	if bridges < 200 || smaller == 0 {
		t.Fatalf("fuzz too weak: %d bridges, %d with a smaller anchor", bridges, smaller)
	}
}
