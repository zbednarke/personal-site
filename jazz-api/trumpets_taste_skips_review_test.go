package main

import (
	"fmt"
	"math"
	"math/rand"
	"testing"
	"time"
)

// Review of #101 (fictional fixtures only).

// An island must not be stranded because its first-choice horn was skipped
// against every possible anchor: another horn of the island bridges.
func TestTasteIslandWithASkippedHornStillBridges(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	for _, blocked := range []string{"h03", "h04"} {
		listings := comparableHorns(now, "h", 8)
		main := []string{"h00", "h01", "h02", "h05", "h06", "h07"}
		events := []tasteEvent{pairEvent(1, "h03", "h04", "a", now.Add(-30*24*time.Hour))}
		id := int64(2)
		for i := 1; i < len(main); i++ {
			events = append(events, pairEvent(id, main[i-1], main[i], "a", now.Add(-10*24*time.Hour+time.Duration(i)*time.Minute)))
			id++
		}
		for _, h := range main {
			events = append(events, skipEvent(id, blocked, h, now.Add(-20*24*time.Hour)))
			id++
		}
		events = markUndone(events)
		m, d := buildTasteModel(listings, events, now)
		pairs := tastePairs(m, d, events, now)
		other := map[string]string{"h03": "h04", "h04": "h03"}[blocked]
		if len(pairs) == 0 || pairs[0].Reason != "bridge" || (pairs[0].A != other && pairs[0].B != other) {
			t.Fatalf("%s skipped against the whole main group: the island is not bridged through %s: %+v", blocked, other, pairs)
		}
	}
}

// While the graph has islands and some bridge is allowed (no live skip
// between its horns), the first pair is a bridge between two components.
func TestTasteBridgeExistsFuzz(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	rng := rand.New(rand.NewSource(101))
	checked := 0
	for round := 0; round < 400; round++ {
		n := 6 + rng.Intn(8)
		listings := comparableHorns(now, "h", n)
		ids := []string{}
		for _, l := range listings {
			ids = append(ids, l.ID)
		}
		events := []tasteEvent{}
		for i := 0; i < rng.Intn(3*n); i++ {
			a, b := ids[rng.Intn(n)], ids[rng.Intn(n)]
			if a == b {
				continue
			}
			at := now.Add(-time.Duration(rng.Intn(40*24)) * time.Hour)
			if rng.Intn(2) == 0 {
				events = append(events, skipEvent(int64(len(events)+1), a, b, at))
			} else {
				events = append(events, pairEvent(int64(len(events)+1), a, b, "a", at))
			}
		}
		events = markUndone(events)
		m, d := buildTasteModel(listings, events, now)
		g := newTastePairGraph(d, events, now)
		if len(g.components()) < 2 {
			continue
		}
		skipped := map[string]bool{}
		for _, e := range events {
			if e.Action == "skip" {
				skipped[hornPair(d, e.ListingID, e.OtherListingID)] = true
			}
		}
		possible := false
		for _, x := range g.nodes {
			for _, y := range g.nodes {
				if x < y && g.uf.find(x) != g.uf.find(y) && !skipped[x+"|"+y] {
					possible = true
				}
			}
		}
		if !possible {
			continue
		}
		checked++
		pairs := tastePairs(m, d, events, now)
		if len(pairs) == 0 || pairs[0].Reason != "bridge" || g.uf.find(d.byListing[pairs[0].A].HornID) == g.uf.find(d.byListing[pairs[0].B].HornID) {
			t.Fatalf("round %d: a bridge is possible but the first pair is %+v (components %v)", round, pairs, g.components())
		}
	}
	if checked < 100 {
		t.Fatalf("only %d rounds had islands", checked)
	}
}

// Lessons never empty Head to head: pairs they rule out come back last.
func TestTasteLessonsNeverEmptyThePairs(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	listings := comparableHorns(now, "h", 6)
	for i := range listings {
		price(math.Round(1000 * math.Pow(1.6, float64(i))))(&listings[i])
	}
	events := []tasteEvent{}
	for i := 1; i < 6; i++ {
		events = append(events, pairEvent(int64(i), listings[i-1].ID, listings[i].ID, "a", now.Add(-30*24*time.Hour)))
	}
	events = markUndone(events)
	m, d := buildTasteModel(listings, events, now)
	guards := tasteGuards{Rules: []tasteLessonRule{{Dimension: "price", Ratio: 1.5, Lessons: 2}}}
	plain, guarded := tastePairs(m, d, events, now), tastePairs(m, d, events, now, guards)
	if len(plain) == 0 || len(guarded) != len(plain) {
		t.Fatalf("lessons emptied Head to head: %d pairs without, %d with", len(plain), len(guarded))
	}
}

func TestTasteExclusionsInForce(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	listings := comparableHorns(now, "h", 3) // checked a day before now
	_, d := buildTasteModel(listings, nil, now)
	ev := func(id int64) *int64 { return &id }
	created := now.Add(-48 * time.Hour)
	xs := []tasteExclusion{
		{ID: 1, ListingID: "h00", Scope: "all", Reason: "wrong_category", Active: true, EventID: ev(1), CreatedAt: created},
		{ID: 2, ListingID: "h01", Scope: "pair", Reason: "duplicate", Active: true, EventID: ev(2), CreatedAt: created},  // skip undone
		{ID: 3, ListingID: "h02", Scope: "pair", Reason: "stale", Active: true, EventID: ev(1), CreatedAt: created},      // verified since
		{ID: 4, ListingID: "h01", Scope: "pair", Reason: "duplicate", Active: false, EventID: ev(1), CreatedAt: created}, // switched off
	}
	got := tasteExclusionsInForce(xs, map[int64]bool{1: true}, d)
	if fmt.Sprint(got) != "map[h00:all]" {
		t.Fatalf("in force %v", got)
	}
	if xs[1].Lifted != "its skip was undone" || xs[2].Lifted != "verified active again" || xs[3].Lifted != "switched off" || !xs[0].InForce {
		t.Fatalf("lifted %+v", xs)
	}
	// A stale exclusion made after the last check stays.
	xs[2].CreatedAt = now
	if got := tasteExclusionsInForce(xs, map[int64]bool{1: true}, d); got["h02"] != "pair" {
		t.Fatalf("stale exclusion lifted without a newer check: %v", got)
	}
}

func TestTasteRecheckDueOnlyForLiveListings(t *testing.T) {
	asked := time.Now().Add(-time.Hour)
	checked := asked.Add(-24 * time.Hour)
	for status, want := range map[string]bool{"active": true, "stale": true, "unverifiable": true, "sold": false, "removed": false} {
		l := tasteListing("r", "M", "R", "lacquer", 1000, func(l *trumpetListing) { l.Status, l.RecheckRequestedAt, l.LastChecked = status, &asked, &checked })
		if tasteRecheckDue(l) != want {
			t.Fatalf("%s: due %v", status, !want)
		}
	}
	passed := tasteListing("r", "M", "R", "lacquer", 1000, func(l *trumpetListing) {
		l.RecheckRequestedAt, l.LastChecked, l.Feedback.InterestState = &asked, &checked, "pass"
	})
	if tasteRecheckDue(passed) {
		t.Fatal("a passed horn is due")
	}
}
