package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestWatchNotesRetainStyleAndPricePreference(t *testing.T) {
	price := 4500.0
	listings := []trumpetListing{{trumpetCandidate: trumpetCandidate{HornID: "a", Maker: "Taylor", Price: &price, Currency: "USD"}, Feedback: trumpetFeedback{Notes: "I love the weird engineering and raw brass, but too expensive. Don't like gold plate."}}}
	signals := trumpetNotePreferences(listings)
	if signals["favoredAttributes"].(map[string]int)["engineering"] != 1 || signals["favoredAttributes"].(map[string]int)["raw brass"] != 1 || signals["dislikedAttributes"].(map[string]int)["gold plate"] != 1 {
		t.Fatal("explicit note sentiment not reflected", signals)
	}
	if len(signals["pricePreferences"].([]map[string]any)) != 1 || signals["makerWeights"].(map[string]int)["taylor"] != 0 {
		t.Fatal("price reaction discarded maker/style")
	}
}
func TestWatchAlertsGroupLimitAndExclusions(t *testing.T) {
	listings := []trumpetListing{}
	events := []json.RawMessage{}
	for i := 0; i < 12; i++ {
		id := fmt.Sprint(i)
		listings = append(listings, trumpetListing{trumpetCandidate: trumpetCandidate{ID: id, HornID: id, Status: "active", SearchScore: 90, VerificationState: "verified"}, Feedback: trumpetFeedback{InterestState: "watch"}})
		events = append(events, json.RawMessage(`{"listingId":"`+id+`","kind":"newly discovered"}`))
	}
	listings[0].Acquired = true
	listings[1].Feedback.InterestState = "pass"
	listings[2].VerificationState = "candidate"
	listings[3].HornID = listings[4].HornID
	alerts := selectTrumpetAlerts(listings, events)
	if len(alerts) != 8 {
		t.Fatal("selective feed size", len(alerts))
	}
	for _, e := range alerts {
		if strings.Contains(string(e), `"listingId":"0"`) || strings.Contains(string(e), `"listingId":"1"`) || strings.Contains(string(e), `"listingId":"2"`) {
			t.Fatal("excluded listing alerted")
		}
	}
}
func TestWatchPriceAndFreshnessEvidence(t *testing.T) {
	old, small, drop, increase := 3000.0, 2999.0, 2900.0, 3500.0
	if significantTrumpetPrice(&old, &small, "USD", "USD") || significantTrumpetPrice(&old, &drop, "USD", "EUR") || !significantTrumpetPrice(&old, &drop, "USD", "USD") || !significantTrumpetPrice(&old, &increase, "USD", "USD") {
		t.Fatal("significance threshold")
	}
	c := trumpetCandidate{Maker: "Taylor", Model: "Chicago", Title: "Taylor Chicago", Source: "Shop", URL: "https://shop.test/horn", DiscoveryType: "new listing"}
	if err := normalizeTrumpet(&c); err != nil || c.DiscoveryType != "newly discovered" {
		t.Fatal("new without posted date")
	}
	future := time.Now().Add(time.Hour)
	c.PostedAt = &future
	c.DiscoveryType = "new listing"
	normalizeTrumpet(&c)
	if c.DiscoveryType != "newly discovered" {
		t.Fatal("future date claimed new")
	}
	recent := time.Now().Add(-time.Hour)
	c.PostedAt = &recent
	c.DiscoveryType = "new listing"
	normalizeTrumpet(&c)
	if c.DiscoveryType != "new listing" {
		t.Fatal("recent seller date ignored")
	}
	for _, source := range []trumpetSourceCheck{{Domain: "127.0.0.1"}, {Domain: "private.local"}, {Domain: "https://shop.test"}, {Domain: "shop.test", PagesOpened: 1, VerifiedOffers: 2}} {
		if validateTrumpetSource(source) == nil {
			t.Fatal("invalid source accepted")
		}
	}
}
