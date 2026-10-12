package main

import (
	"fmt"
	"math/rand"
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
	m, d := buildTasteModel(listings, markUndone(events), now)
	g := newTastePairGraph(d, events, now)
	if len(g.components()) != 1 {
		t.Fatal("fixture should be connected")
	}
	pairs := tastePairs(m, d, events, now)
	if len(pairs) < tasteStructureEvery {
		t.Fatalf("pairs %+v", pairs)
	}
	// One pair in five links a least-compared horn to one far from it.
	p := pairs[tasteStructureEvery-1]
	dist := g.distances("horn-" + p.A)["horn-"+p.B]
	if dist < 2 || (len(g.adj["horn-"+p.A]) > 1 && len(g.adj["horn-"+p.B]) > 1) {
		t.Fatalf("structure pair %+v: distance %d, degrees %d/%d", p, dist, len(g.adj["horn-"+p.A]), len(g.adj["horn-"+p.B]))
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
