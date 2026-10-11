package main

import (
	"fmt"
	"hash/fnv"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
)

// The taste fit is cached in memory per owner, keyed by everything it depends
// on: the reactions (latest event id and count), the stated preferences'
// revision, the listings' model-relevant fields and the UTC day (reactions
// decay). Any new reaction, preference save or listing change misses the
// cache; a miss warm-starts Newton from the previous fit. The "a week ago" fit
// used for Recent changes is cached the same way, so it is only recomputed
// when its own inputs change.

type tasteCacheEntry struct {
	key     string
	model   *tasteModel
	data    *tasteData
	weekKey string
	week    *tasteModel
	used    time.Time
}

var tasteCache = struct {
	sync.Mutex
	entries map[uuid.UUID]*tasteCacheEntry
}{entries: map[uuid.UUID]*tasteCacheEntry{}}

const tasteCacheLimit = 32

func tasteCacheKey(listings []trumpetListing, events []tasteEvent, prefs *tastePreferences, now time.Time) string {
	h := fnv.New64a()
	ids := make([]int, len(listings))
	for i := range listings {
		ids[i] = i
	}
	sort.Slice(ids, func(a, b int) bool { return listings[ids[a]].ID < listings[ids[b]].ID })
	for _, i := range ids {
		l := listings[i]
		f := l.Feedback
		rating := 0
		if f.Rating != nil {
			rating = *f.Rating
		}
		price := -1.0
		if l.Price != nil {
			price = *l.Price
		}
		year := 0
		if l.Details.Year != nil {
			year = *l.Details.Year
		}
		fmt.Fprintf(h, "%s|%s|%s|%s|%s|%s|%d|%s|%s|%s|%s|%s|%v|%d|%v|%s|%v|%s|%s|%s|%d|%v;", l.ID, l.HornID, l.Maker, l.Model, l.Title, l.URL, len(l.Description), l.Source,
			l.Details.Finish, l.Details.Bell, l.Details.Bore, l.Details.Provenance, l.Details.NotableFeatures, year, price, l.Currency, l.Tags, l.Status,
			l.VerificationState, f.InterestState, rating, f.Favorite)
		fmt.Fprintf(h, "%v|%d|%s;", l.Acquired, len(l.Images), l.FirstSeen.UTC().Format(time.RFC3339))
	}
	var last int64
	for _, e := range events {
		if e.ID > last {
			last = e.ID
		}
	}
	revision := 0
	if prefs != nil {
		revision = prefs.Revision
	}
	return fmt.Sprintf("%x/%d/%d/%d/%s", h.Sum64(), last, len(events), revision, now.UTC().Format("2006-01-02"))
}

// tasteCacheGet returns the cached fit for key, or the previous fit's weights
// to warm-start a new one.
func tasteCacheGet(user uuid.UUID, key string) (*tasteModel, *tasteData, map[string]float64) {
	tasteCache.Lock()
	defer tasteCache.Unlock()
	e := tasteCache.entries[user]
	if e == nil {
		return nil, nil, nil
	}
	e.used = time.Now()
	if e.key == key {
		return e.model, e.data, nil
	}
	return nil, nil, e.model.thetaMap()
}

func tasteCachePut(user uuid.UUID, key string, m *tasteModel, d *tasteData) {
	tasteCache.Lock()
	defer tasteCache.Unlock()
	if len(tasteCache.entries) >= tasteCacheLimit {
		var oldest uuid.UUID
		var at time.Time
		for id, e := range tasteCache.entries {
			if at.IsZero() || e.used.Before(at) {
				oldest, at = id, e.used
			}
		}
		delete(tasteCache.entries, oldest)
	}
	previous := tasteCache.entries[user]
	e := &tasteCacheEntry{key: key, model: m, data: d, used: time.Now()}
	if previous != nil {
		e.weekKey, e.week = previous.weekKey, previous.week
	}
	tasteCache.entries[user] = e
}

// tasteCachedWeekAgo returns the fit on reactions older than a week, refitting
// only when those reactions (or the listings/preferences/day) changed.
func tasteCachedWeekAgo(user uuid.UUID, m *tasteModel, d *tasteData, events []tasteEvent, now time.Time) *tasteModel {
	cut := now.Add(-7 * 24 * time.Hour)
	older, undone := []tasteEvent{}, 0
	for _, e := range events {
		if e.CreatedAt.Before(cut) {
			older = append(older, e)
			if e.undone {
				undone++ // an undo after the cut still removes an older reaction
			}
		}
	}
	listings := make([]trumpetListing, 0, len(d.byListing))
	for _, l := range d.byListing {
		listings = append(listings, l)
	}
	key := fmt.Sprintf("%s/u%d", tasteCacheKey(listings, older, m.preferences, now), undone)
	tasteCache.Lock()
	if e := tasteCache.entries[user]; e != nil && e.weekKey == key && e.week != nil {
		week := e.week
		tasteCache.Unlock()
		return week
	}
	tasteCache.Unlock()
	week := tasteWeekAgoFit(m, d, events, now)
	tasteCache.Lock()
	if e := tasteCache.entries[user]; e != nil {
		e.weekKey, e.week = key, week
	}
	tasteCache.Unlock()
	return week
}
