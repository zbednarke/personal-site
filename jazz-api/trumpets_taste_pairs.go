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
//     first, each side represented by its most uncertain horn. A newly
//     comparable horn is a singleton, so it is asked about at once.
//  2. Then the most informative pairs (largest predictive sd of the
//     difference times p(1-p)), as before.
//  3. Once the graph is connected, one pair in five (tasteStructureEvery)
//     instead strengthens its structure: the least-compared horn (lowest
//     degree) against the horn farthest from it (breadth-first distance),
//     which adds a second path to weakly connected horns and shortens the
//     graph's diameter.
//
// In every case each horn appears at most twice per answer, and pairs compared
// in the last two weeks are not offered again.

const tasteStructureEvery = 5

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

func (u *tasteUnionFind) find(n string) string {
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

// tastePairGraph is the comparison graph over comparable horns.
type tastePairGraph struct {
	nodes []string // horns, in board order
	uf    *tasteUnionFind
	adj   map[string]map[string]bool
}

func newTastePairGraph(d *tasteData, events []tasteEvent, now time.Time) *tastePairGraph {
	g := &tastePairGraph{adj: map[string]map[string]bool{}}
	for _, horn := range d.order {
		if h := d.horns[horn]; d.eligible(h) && tasteComparable(h.primary, now) {
			g.nodes = append(g.nodes, horn)
			g.adj[horn] = map[string]bool{}
		}
	}
	g.uf = newTasteUnionFind(g.nodes)
	for _, e := range events {
		if e.Kind != "pair" || e.undone {
			continue
		}
		a, okA := d.byListing[e.ListingID]
		b, okB := d.byListing[e.OtherListingID]
		if !okA || !okB || a.HornID == b.HornID || g.adj[a.HornID] == nil || g.adj[b.HornID] == nil {
			continue
		}
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
		for _, m := range g.nodes { // board order keeps ties deterministic
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
func tastePairs(m *tasteModel, d *tasteData, events []tasteEvent, now time.Time) []tastePair {
	g := newTastePairGraph(d, events, now)
	if len(g.nodes) < tastePairMinimum {
		return []tastePair{}
	}
	pairKey := func(a, b string) string {
		if a > b {
			a, b = b, a
		}
		return a + "|" + b
	}
	recent := map[string]bool{}
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
		id   string
		keys []string
		z, v float64
	}
	cands := map[string]cand{}
	for _, horn := range g.nodes {
		id := d.horns[horn].primary.ID
		keys := d.keys(id)
		z, v := m.affinity(keys)
		cands[horn] = cand{id: id, keys: keys, z: z, v: v}
	}
	used := map[string]int{}
	offered := map[string]bool{}
	out := []tastePair{}
	free := func(h string) bool { return used[h] < 2 }
	ok := func(a, b string) bool {
		return a != b && free(a) && free(b) && !recent[pairKey(a, b)] && !offered[pairKey(a, b)]
	}
	add := func(a, b string) {
		ca, cb := cands[a], cands[b]
		v := math.Max(0, ca.v+cb.v-2*m.covariance(ca.keys, cb.keys))
		p := sigmoid(ca.z - cb.z)
		used[a]++
		used[b]++
		offered[pairKey(a, b)] = true
		out = append(out, tastePair{A: ca.id, B: cb.id, P: math.Round(1000*p) / 1000, SD: math.Round(1000*math.Sqrt(v)) / 1000})
	}
	// The most uncertain free horn of a group (ties: board order).
	pick := func(group []string, partner string) string {
		best := ""
		for _, h := range group {
			if (partner == "" && free(h)) || (partner != "" && ok(partner, h)) {
				if best == "" || cands[h].v > cands[best].v {
					best = h
				}
			}
		}
		return best
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
		for _, island := range islands {
			if len(out) == tastePairLimit {
				break
			}
			b := pick(island, "")
			if b == "" {
				continue
			}
			for _, anchor := range comps {
				if g.uf.find(anchor[0]) == g.uf.find(island[0]) {
					continue
				}
				if a := pick(anchor, b); a != "" {
					add(a, b)
					break
				}
			}
		}
	}

	// 3. A structure pair: the least-compared horn against the horn farthest
	// from it (unreachable never happens once connected).
	structure := func() bool {
		low := ""
		for _, h := range g.nodes {
			if free(h) && (low == "" || len(g.adj[h]) < len(g.adj[low]) || (len(g.adj[h]) == len(g.adj[low]) && cands[h].v > cands[low].v)) {
				low = h
			}
		}
		if low == "" {
			return false
		}
		dist := g.distances(low)
		far := ""
		for _, h := range g.nodes {
			if dist[h] >= 2 && ok(low, h) && (far == "" || dist[h] > dist[far] || (dist[h] == dist[far] && cands[h].v > cands[far].v)) {
				far = h
			}
		}
		if far == "" { // low is next to everyone it may be paired with
			for _, h := range g.nodes {
				if !g.adj[low][h] && ok(low, h) && (far == "" || cands[h].v > cands[far].v) {
					far = h
				}
			}
		}
		if far == "" {
			return false
		}
		add(low, far)
		return true
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
			if recent[pairKey(a, b)] {
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
	for _, o := range options {
		if len(out) == tastePairLimit {
			break
		}
		if connected && len(out)%tasteStructureEvery == tasteStructureEvery-1 {
			structure()
		}
		if len(out) < tastePairLimit && ok(o.a, o.b) {
			add(o.a, o.b)
		}
	}
	return out
}
