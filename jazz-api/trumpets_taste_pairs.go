package main

import (
	"math"
	"sort"
	"time"
)

// ---- Head to head: which pairs to ask next --------------------------------
//
// The comparison graph has one node per comparable horn (tasteComparable on
// its primary listing; a horn's listings are one node, as the model compares
// horns) and one edge per completed pair choice between two such horns.
// "Both" and "neither" are edges too; undone choices are not. Swipes are not
// edges (they anchor each horn on their own).
//
// Pair choices only say how horns compare with each other, so horns in
// different connected components ("islands") are never compared, however
// many choices each island has. Selection is therefore connectivity first:
//
//  1. While there is more than one component, the first pairs bridge them:
//     the largest component against the others, singletons (new horns)
//     first. Each side is represented by its least-compared horn (fewest
//     pair edges), then the most uncertain, then board order, so the
//     largest component grows as a balanced tree rather than a star around
//     one hub. (Uncertainty comes from a horn's features, not from how often
//     it was compared, so "most uncertain" alone kept picking one anchor.)
//     A newly comparable horn is a singleton, so it is asked about at once.
//  2. Then the most informative pairs (largest predictive sd of the
//     difference times p(1-p)), as before.
//  3. Once the graph is connected, every fifth choice (tasteStructureEvery,
//     counted over live pair choices) is asked first a structure pair: the
//     least-compared horn (lowest degree) against the horn farthest from it
//     (breadth-first distance), which adds a second path to weakly connected
//     horns and shortens the graph's diameter. It goes first because the
//     page always shows the first pair of the newest answer.
//
// A horn is represented by its comparable listing (tasteComparableListing).
// Edges are every live choice between two horns, comparable or not, so a
// horn that stops being comparable (sold) still connects its neighbours;
// components are counted over the comparable horns only.
//
// The page shows only the first pair of each answer and asks again after
// every choice, so variety has to hold across answers, not only within one.
// Horns in the owner's last tasteFreshAfter live pair choices are "stale":
// in every phase a side that is not stale is preferred whenever one exists
// (stale horns go last; they are never dropped), so the next pair shows
// different horns. This never reorders the phases: while the graph has
// islands, the first pair is still a bridge.
//
// The bridges of one answer are pairwise non-redundant: each one joins two
// components of the graph plus that answer's earlier bridges.
//
// In every case each horn appears at most twice per answer (horns not yet
// used in this answer are preferred), and pairs compared in the last two
// weeks are not offered again.
//
// Skips (action "skip", trumpets_taste_skips.go) are not choices: they are
// never edges and never counted as choices, so a skipped bridge leaves its
// islands apart and the next answer bridges them again. A skipped pair (by
// horn) is never offered again while the skip stands, and its horns count
// as recent ("stale") like a choice's, so the next answer shows different
// horns. Listings a skip investigation excluded are not nodes, and
// comparability lessons that recur (tasteGuards) keep pairs with the same gap
// out, except where a bridge has no other partner: lessons never stall
// connectivity.

const (
	tasteStructureEvery = 5
	// tasteFreshAfter: horns in this many of the latest pair choices are kept
	// out of the next pairs when there is an alternative.
	tasteFreshAfter = 3
)

// tasteRecentPairHorns is the set of horns in the latest n live pair choices
// or skips between two different known horns (latest by CreatedAt, then
// id). One pass
// keeping the n latest (n is small); correct in any order, and fast for
// events loaded oldest first, as the scan runs backward: once n are kept,
// older events are rejected before any lookup.
func tasteRecentPairHorns(d *tasteData, events []tasteEvent, n int) map[string]bool {
	later := func(x, y *tasteEvent) bool {
		if !x.CreatedAt.Equal(y.CreatedAt) {
			return x.CreatedAt.After(y.CreatedAt)
		}
		return x.ID > y.ID
	}
	top := make([]*tasteEvent, 0, n+1) // latest first
	for i := len(events) - 1; i >= 0; i-- {
		e := &events[i]
		if n <= 0 || e.Kind != "pair" || e.undone {
			continue
		}
		if len(top) == n && !later(e, top[n-1]) {
			continue
		}
		a, okA := d.byListing[e.ListingID]
		b, okB := d.byListing[e.OtherListingID]
		if !okA || !okB || a.HornID == b.HornID {
			continue
		}
		j := len(top)
		for j > 0 && later(e, top[j-1]) {
			j--
		}
		top = append(top, nil)
		copy(top[j+1:], top[j:])
		top[j] = e
		if len(top) > n {
			top = top[:n]
		}
	}
	out := map[string]bool{}
	for _, e := range top {
		out[d.byListing[e.ListingID].HornID] = true
		out[d.byListing[e.OtherListingID].HornID] = true
	}
	return out
}

// tasteUnionFind: disjoint sets over string nodes (path halving, union by
// size).
type tasteUnionFind struct {
	parent map[string]string
	size   map[string]int
}

func newTasteUnionFind(nodes []string) *tasteUnionFind {
	u := &tasteUnionFind{parent: map[string]string{}, size: map[string]int{}}
	for _, n := range nodes {
		u.parent[n], u.size[n] = n, 1
	}
	return u
}

// clone is an independent copy (for unions that must not touch the graph).
func (u *tasteUnionFind) clone() *tasteUnionFind {
	c := &tasteUnionFind{parent: make(map[string]string, len(u.parent)), size: make(map[string]int, len(u.size))}
	for k, v := range u.parent {
		c.parent[k] = v
	}
	for k, v := range u.size {
		c.size[k] = v
	}
	return c
}

func (u *tasteUnionFind) find(n string) string {
	if _, ok := u.parent[n]; !ok { // a node first seen in an edge
		u.parent[n], u.size[n] = n, 1
	}
	for u.parent[n] != n {
		u.parent[n] = u.parent[u.parent[n]]
		n = u.parent[n]
	}
	return n
}

// union joins the sets of a and b; false when they already were one set.
func (u *tasteUnionFind) union(a, b string) bool {
	ra, rb := u.find(a), u.find(b)
	if ra == rb {
		return false
	}
	if u.size[ra] < u.size[rb] {
		ra, rb = rb, ra
	}
	u.parent[rb] = ra
	u.size[ra] += u.size[rb]
	return true
}

// tasteComparableListing is the listing that represents a horn in Head to
// head: its primary when that is comparable, else its first comparable other
// listing (a stale primary must not hide a horn that has a live offer).
// A listing a skip investigation excluded never represents its horn.
func tasteComparableListing(h *tasteHorn, now time.Time, guards ...tasteGuards) (trumpetListing, bool) {
	excluded := func(l trumpetListing) bool {
		for _, g := range guards {
			if g.Excluded[l.ID] != "" {
				return true
			}
		}
		return false
	}
	if tasteComparable(h.primary, now) && !excluded(h.primary) {
		return h.primary, true
	}
	for _, l := range h.listings {
		if tasteComparable(l, now) && !excluded(l) {
			return l, true
		}
	}
	return trumpetListing{}, false
}

// tastePairGraph is the comparison graph. Its nodes are the comparable
// horns; its edges are every live pair choice between any two horns, so a
// path through a horn that is no longer comparable (sold, say) still
// connects the horns on either side of it.
type tastePairGraph struct {
	nodes  []string                  // comparable horns, in board order
	listed map[string]trumpetListing // the listing that represents each node
	all    []string                  // every horn, in board order (for walks)
	uf     *tasteUnionFind
	adj    map[string]map[string]bool
	live   int // live (not undone) pair choices between two different horns
}

func newTastePairGraph(d *tasteData, events []tasteEvent, now time.Time, guards ...tasteGuards) *tastePairGraph {
	g := &tastePairGraph{adj: map[string]map[string]bool{}, listed: map[string]trumpetListing{}}
	for _, horn := range d.order {
		g.all = append(g.all, horn)
		g.adj[horn] = map[string]bool{}
		h := d.horns[horn]
		if !d.eligible(h) {
			continue
		}
		if l, ok := tasteComparableListing(h, now, guards...); ok {
			g.nodes = append(g.nodes, horn)
			g.listed[horn] = l
		}
	}
	g.uf = newTasteUnionFind(g.all)
	for _, e := range events {
		if e.Kind != "pair" || e.undone || e.Action == "skip" { // a skip is no edge
			continue
		}
		a, okA := d.byListing[e.ListingID]
		b, okB := d.byListing[e.OtherListingID]
		if !okA || !okB || a.HornID == b.HornID || g.adj[a.HornID] == nil || g.adj[b.HornID] == nil {
			continue
		}
		g.live++
		g.adj[a.HornID][b.HornID], g.adj[b.HornID][a.HornID] = true, true
		g.uf.union(a.HornID, b.HornID)
	}
	return g
}

// components lists the connected components, largest first (ties: the one
// whose first horn comes first on the board); horns keep board order.
func (g *tastePairGraph) components() [][]string {
	index := map[string]int{}
	out := [][]string{}
	for _, n := range g.nodes {
		r := g.uf.find(n)
		i, ok := index[r]
		if !ok {
			i = len(out)
			index[r] = i
			out = append(out, nil)
		}
		out[i] = append(out[i], n)
	}
	sort.SliceStable(out, func(i, j int) bool { return len(out[i]) > len(out[j]) })
	return out
}

// distances from one horn (breadth first); unreachable horns are absent.
func (g *tastePairGraph) distances(from string) map[string]int {
	dist := map[string]int{from: 0}
	queue := []string{from}
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		for _, m := range g.all { // board order keeps ties deterministic
			if g.adj[n][m] {
				if _, seen := dist[m]; !seen {
					dist[m] = dist[n] + 1
					queue = append(queue, m)
				}
			}
		}
	}
	return dist
}

// tastePairGraphStats is what the page shows ("Comparisons connect 84 of 92
// eligible horns (3 islands)").
type tastePairGraphStats struct {
	Eligible   int     `json:"eligible"`
	Components int     `json:"components"`
	Largest    int     `json:"largest"`
	Share      float64 `json:"share"`
}

func (g *tastePairGraph) stats() tastePairGraphStats {
	s := tastePairGraphStats{Eligible: len(g.nodes)}
	comps := g.components()
	s.Components = len(comps)
	if len(comps) > 0 {
		s.Largest = len(comps[0])
		s.Share = math.Round(1000*float64(s.Largest)/float64(len(g.nodes))) / 1000
	}
	return s
}

// tastePairs picks the next Head to head comparisons (see the top of this
// file). Both sides must be tasteComparable, and with fewer than
// tastePairMinimum such horns there are no pairs at all (the page explains
// why) rather than weak ones.
func tastePairs(m *tasteModel, d *tasteData, events []tasteEvent, now time.Time, guards ...tasteGuards) []tastePair {
	g := newTastePairGraph(d, events, now, guards...)
	if len(g.nodes) < tastePairMinimum {
		return []tastePair{}
	}
	pairKey := func(a, b string) string {
		if a > b {
			a, b = b, a
		}
		return a + "|" + b
	}
	// recent: compared in the last two weeks; skipped: skipped at any time
	// (a live skip), never offered again.
	recent, skipped := map[string]bool{}, map[string]bool{}
	for _, e := range events {
		if e.Kind != "pair" || e.undone {
			continue
		}
		a, okA := d.byListing[e.ListingID]
		b, okB := d.byListing[e.OtherListingID]
		if !okA || !okB {
			continue
		}
		if e.Action == "skip" {
			skipped[pairKey(a.HornID, b.HornID)] = true
		} else if now.Sub(e.CreatedAt) <= tasteReseeAfter {
			recent[pairKey(a.HornID, b.HornID)] = true
		}
	}
	// Recurring comparability lessons; lenient (bridges only, when nothing
	// else joins an island) ignores them.
	lenient := false
	fits := func(a, b string) bool {
		for _, gd := range guards {
			for _, r := range gd.Rules {
				if r.avoids(d, g.listed[a], g.listed[b]) {
					return false
				}
			}
		}
		return true
	}
	type cand struct {
		id   string
		keys []string
		z, v float64
	}
	cands := map[string]cand{}
	for _, horn := range g.nodes {
		id := g.listed[horn].ID
		keys := d.keys(id)
		z, v := m.affinity(keys)
		cands[horn] = cand{id: id, keys: keys, z: z, v: v}
	}
	stale := tasteRecentPairHorns(d, events, tasteFreshAfter)
	used := map[string]int{}
	offered := map[string]bool{}
	out := []tastePair{}
	free := func(h string) bool { return used[h] < 2 }
	ok := func(a, b string) bool {
		k := pairKey(a, b)
		return a != b && free(a) && free(b) && !recent[k] && !skipped[k] && !offered[k] && (lenient || fits(a, b))
	}
	add := func(a, b, reason string) {
		ca, cb := cands[a], cands[b]
		v := math.Max(0, ca.v+cb.v-2*m.covariance(ca.keys, cb.keys))
		p := sigmoid(ca.z - cb.z)
		used[a]++
		used[b]++
		offered[pairKey(a, b)] = true
		out = append(out, tastePair{A: ca.id, B: cb.id, P: math.Round(1000*p) / 1000, SD: math.Round(1000*math.Sqrt(v)) / 1000, Reason: reason})
	}
	// better: whether horn a is a better side of a bridge than b. Not stale
	// first, then not yet used in this answer, then fewest pair edges, then
	// the most uncertain (ties: the caller's order, board order).
	better := func(a, b string) bool {
		switch {
		case stale[a] != stale[b]:
			return stale[b]
		case used[a] != used[b]:
			return used[a] < used[b]
		case len(g.adj[a]) != len(g.adj[b]):
			return len(g.adj[a]) < len(g.adj[b])
		}
		return cands[a].v > cands[b].v
	}
	// The best free horn of a group to pair with partner ("": with anyone);
	// with fresh, only a horn that is not stale; with unused, only one not
	// yet in this answer.
	pick := func(group []string, partner string, fresh, unused bool) string {
		best := ""
		for _, h := range group {
			if (fresh && stale[h]) || (unused && used[h] > 0) {
				continue
			}
			if (partner == "" && free(h)) || (partner != "" && ok(partner, h)) {
				if best == "" || better(h, best) {
					best = h
				}
			}
		}
		return best
	}
	hasFresh := func(c []string) bool {
		for _, h := range c {
			if !stale[h] {
				return true
			}
		}
		return false
	}

	// 1. Bridges: each island against the largest component (or, once its
	// horns are used up, the next largest), singletons first.
	// Among components of one size, the most uncertain goes first (so with no
	// comparisons yet, the most uncertain horns are compared first).
	maxV := func(c []string) float64 {
		best := math.Inf(-1)
		for _, h := range c {
			best = math.Max(best, cands[h].v)
		}
		return best
	}
	comps := g.components()
	sort.SliceStable(comps, func(i, j int) bool {
		if len(comps[i]) != len(comps[j]) {
			return len(comps[i]) > len(comps[j])
		}
		return maxV(comps[i]) > maxV(comps[j])
	})
	connected := len(comps) == 1
	if !connected {
		islands := append([][]string{}, comps[1:]...)
		sort.SliceStable(islands, func(i, j int) bool {
			if len(islands[i]) != len(islands[j]) {
				return len(islands[i]) < len(islands[j])
			}
			return maxV(islands[i]) > maxV(islands[j])
		})
		// Islands made only of stale horns go after the others (in practice
		// never a singleton: a horn just compared is no longer one).
		sort.SliceStable(islands, func(i, j int) bool { return hasFresh(islands[i]) && !hasFresh(islands[j]) })
		// And an island with a horn of the very last answer (a choice or a
		// skip) goes last of all, so a skipped bridge is followed by a
		// bridge to another island whenever there is one.
		last := tasteRecentPairHorns(d, events, 1)
		touches := func(c []string) bool {
			for _, h := range c {
				if last[h] {
					return true
				}
			}
			return false
		}
		sort.SliceStable(islands, func(i, j int) bool { return !touches(islands[i]) && touches(islands[j]) })
		// joined is the graph's components plus this answer's bridges, so a
		// later bridge never pairs two islands an earlier one already joined
		// (possible once a smaller component anchors).
		joined := g.uf.clone()
		for _, island := range islands {
			if len(out) == tastePairLimit {
				break
			}
			b := pick(island, "", false, false) // stale only when the island has nothing else
			if b == "" {
				continue
			}
			// The anchor: from the largest component not yet joined to the
			// island in this answer, preferring a fresh horn, then one not yet
			// used in this answer; only then a smaller component.
			// Lessons first; a bridge ignores them only when nothing else fits.
			a := ""
			for _, ignore := range []bool{false, true} {
				lenient = ignore
				for _, pass := range []struct{ fresh, unused bool }{{true, true}, {true, false}, {false, true}, {false, false}} {
					for _, anchor := range comps {
						if a == "" && joined.find(anchor[0]) != joined.find(b) {
							a = pick(anchor, b, pass.fresh, pass.unused)
						}
					}
				}
			}
			lenient = false
			if a != "" {
				add(a, b, "bridge")
				joined.union(a, b)
			}
		}
	}

	// 3. A structure pair: the least-compared horn against the horn farthest
	// from it (unreachable never happens once connected).
	// On both sides a stale horn is taken only when no fresh one fits.
	// freshFirst(h, best, rest): h beats best when it is fresh and best is
	// stale, or they are alike and rest holds.
	freshFirst := func(h, best string, rest bool) bool {
		if stale[h] != stale[best] {
			return stale[best]
		}
		return rest
	}
	structure := func() bool {
		low := ""
		for _, h := range g.nodes {
			if free(h) && (low == "" || freshFirst(h, low, len(g.adj[h]) < len(g.adj[low]) || (len(g.adj[h]) == len(g.adj[low]) && cands[h].v > cands[low].v))) {
				low = h
			}
		}
		if low == "" {
			return false
		}
		dist := g.distances(low)
		far := ""
		for _, h := range g.nodes {
			if dist[h] >= 2 && ok(low, h) && (far == "" || freshFirst(h, far, dist[h] > dist[far] || (dist[h] == dist[far] && cands[h].v > cands[far].v))) {
				far = h
			}
		}
		if far == "" { // low is next to everyone it may be paired with
			for _, h := range g.nodes {
				if !g.adj[low][h] && ok(low, h) && (far == "" || freshFirst(h, far, cands[h].v > cands[far].v)) {
					far = h
				}
			}
		}
		if far == "" {
			return false
		}
		add(low, far, "structure")
		return true
	}
	// The page shows the first pair of each answer, so the structure pair
	// goes first, on every fifth choice of a connected graph.
	if connected && g.live%tasteStructureEvery == tasteStructureEvery-1 {
		structure()
	}

	// 2. The most informative pairs, bounded to the 40 most uncertain horns.
	pool := append([]string{}, g.nodes...)
	sort.SliceStable(pool, func(i, j int) bool { return cands[pool[i]].v > cands[pool[j]].v })
	if len(pool) > 40 {
		pool = pool[:40]
	}
	type option struct {
		a, b  string
		score float64
	}
	options := []option{}
	for i := range pool {
		for j := i + 1; j < len(pool); j++ {
			a, b := pool[i], pool[j]
			if recent[pairKey(a, b)] || skipped[pairKey(a, b)] {
				continue
			}
			ca, cb := cands[a], cands[b]
			v := ca.v + cb.v - 2*m.covariance(ca.keys, cb.keys)
			if v <= 1e-9 {
				continue
			}
			p := sigmoid(ca.z - cb.z)
			options = append(options, option{a, b, math.Sqrt(v) * 4 * p * (1 - p)})
		}
	}
	sort.SliceStable(options, func(i, j int) bool {
		if options[i].score != options[j].score {
			return options[i].score > options[j].score
		}
		return cands[options[i].a].id+cands[options[i].b].id < cands[options[j].a].id+cands[options[j].b].id
	})
	// Pairs of two fresh horns first, then the rest, so a stale horn comes
	// back only when nothing else is left.
	for _, fresh := range []bool{true, false} {
		for _, o := range options {
			if len(out) == tastePairLimit {
				break
			}
			if fresh && (stale[o.a] || stale[o.b]) {
				continue
			}
			if ok(o.a, o.b) {
				add(o.a, o.b, "uncertain")
			}
		}
	}
	return out
}
