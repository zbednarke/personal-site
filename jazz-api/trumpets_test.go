package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTrumpetURLAndIdentity(t *testing.T) {
	cases := map[string]string{
		"http://www.Example.com/horn/?utm_source=chat&currency=EUR#photo": "https://example.com/horn?currency=EUR",
		"https://ebay.com/itm/123?var=456&fbclid=test":                    "https://ebay.com/itm/123?var=456",
	}
	for input, want := range cases {
		got, err := canonicalTrumpetURL(input)
		if err != nil || got != want {
			t.Fatalf("canonical %q: %q %v", input, got, err)
		}
	}
	for _, bad := range []string{"javascript:alert(1)", "https://user:password@example.com/horn", "/relative"} {
		if _, err := canonicalTrumpetURL(bad); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
	if hornIdentity(" OLDs ", " 27 907 ") != hornIdentity("Olds", "27907") {
		t.Fatal("serial identity normalization")
	}
	if hornIdentity("Taylor", "") != "" {
		t.Fatal("missing serial cannot identify a horn")
	}
	a := trumpetListing{trumpetCandidate: trumpetCandidate{Maker: "Taylor", Model: "Chicago II"}}
	b := a
	if likelySameHorn(a, b) {
		t.Fatal("model alone must not suggest a physical match")
	}
	a.Images = []string{"https://example.com/one-off.jpg"}
	b.Images = a.Images
	if !likelySameHorn(a, b) {
		t.Fatal("shared photo should suggest a match")
	}
	a.SerialNumber = "1"
	b.SerialNumber = "2"
	if likelySameHorn(a, b) {
		t.Fatal("conflicting serials must not match")
	}
}
func TestTrumpetValidation(t *testing.T) {
	good := trumpetCandidate{Maker: "Lawler", Model: "C7", Source: "Shop", Title: "Lawler C7", URL: "https://example.com/horn", SearchScore: 80}
	if err := normalizeTrumpet(&good); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*trumpetCandidate){func(c *trumpetCandidate) { c.Status = "lost" }, func(c *trumpetCandidate) { c.Currency = "US1" }, func(c *trumpetCandidate) { p := -1.0; c.Price = &p }, func(c *trumpetCandidate) { c.SearchScore = 101 }, func(c *trumpetCandidate) { c.Images = []string{"javascript:bad"} }, func(c *trumpetCandidate) { c.DiscoveryType = "price drop" }} {
		c := good
		mutate(&c)
		if err := normalizeTrumpet(&c); err == nil {
			t.Fatal("bad candidate accepted")
		}
	}
	for _, c := range historicalTrumpets() {
		if err := normalizeTrumpet(&c); err != nil {
			t.Fatalf("seed %s: %v", c.Title, err)
		}
		if c.Price != nil || c.URL != "" || len(c.Images) > 0 {
			t.Fatal("seed contains invented market facts")
		}
	}
	f := trumpetFeedback{InterestState: "watch"}
	if err := validateFeedback(&f); err != nil {
		t.Fatal(err)
	}
	rating := 6
	f.Rating = &rating
	if err := validateFeedback(&f); err == nil {
		t.Fatal("rating range")
	}
	run := trumpetRunInput{ExternalID: "day", Kind: "combined", Status: "succeeded"}
	if validateTrumpetRun(&run) == nil {
		t.Fatal("success without coverage")
	}
	run.Sources = []trumpetSourceCheck{{Source: "Shop", Status: "failed"}}
	if validateTrumpetRun(&run) == nil {
		t.Fatal("success with failed source")
	}
}
func TestTrumpetProfileAndAcquiredExclusion(t *testing.T) {
	high, low := 5, 1
	listings := []trumpetListing{
		{trumpetCandidate: trumpetCandidate{HornID: "one", Maker: "Taylor", Model: "Chicago II", Tags: []string{"raw brass"}}, Feedback: trumpetFeedback{Rating: &high, Notes: "Love the geometry", FavoredAttributes: []string{"Upswept"}}},
		{trumpetCandidate: trumpetCandidate{HornID: "one", Maker: "Taylor", Model: "Chicago II"}, Feedback: trumpetFeedback{Rating: &high}},
		{trumpetCandidate: trumpetCandidate{HornID: "two", Maker: "Yamaha", Model: "Production", Tags: []string{"plain"}}, Feedback: trumpetFeedback{Rating: &low, DislikedAttributes: []string{"Ordinary"}}},
		{trumpetCandidate: trumpetCandidate{HornID: "owned", Maker: "Taylor", Model: "Chicago 46 II / Harrelson-modified", Status: "acquired"}, Acquired: true},
	}
	p := trumpetSearchProfile(listings)
	if len(p["highlyRatedExamples"].([]trumpetListing)) != 1 || len(p["negativelyRatedExamples"].([]trumpetListing)) != 1 {
		t.Fatal("example dedupe")
	}
	if len(p["acquiredExclusions"].([]map[string]any)) != 1 || p["favoredAttributes"].(map[string]int)["raw brass"] != 1 {
		t.Fatal("acquired or attributes missing")
	}
	data, _ := json.Marshal(p)
	if !strings.Contains(string(data), "Love the geometry") {
		t.Fatal("notes not fed back")
	}
	if !isAcquiredTaylor(listings[3].trumpetCandidate) {
		t.Fatal("known purchase not recognized")
	}
	if isAcquiredTaylor(trumpetCandidate{Maker: "Taylor", Model: "Chicago II"}) {
		t.Fatal("overbroad acquired exclusion")
	}
}
func TestTrumpetAuthenticationAndPrivacy(t *testing.T) {
	app := &application{cfg: config{GatewayKey: "gateway"}, logger: slog.Default()}
	token := strings.Repeat("test", 16)
	hash := sha256.Sum256([]byte(token))
	t.Setenv("TRUMPETS_MACHINE_TOKEN_SHA256", hex.EncodeToString(hash[:]))
	t.Setenv("TRUMPETS_MACHINE_USER", "owner")
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if r.Context().Value(userSubjectKey) != "owner" {
			t.Error("machine owner spoofed")
		}
		w.WriteHeader(204)
	})
	handler := app.trumpetPrivacy(app.trumpetMachineAuth(next))
	for _, auth := range []string{"", "Bearer wrong", "Basic " + token} {
		called = false
		r := httptest.NewRequest("GET", "/v1/trumpets/machine/profile", nil)
		r.Header.Set("Authorization", auth)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != 401 || called || w.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatalf("unauthorized data %d", w.Code)
		}
	}
	r := httptest.NewRequest("GET", "/v1/trumpets/machine/profile", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("X-Jazz-User", "attacker")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 204 {
		t.Fatalf("machine auth %d", w.Code)
	}
	t.Setenv("TRUMPETS_MACHINE_TOKEN_SHA256", "")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("unconfigured machine auth open")
	}
	for _, path := range []string{"/v1/trumpets/listings", "/v1/trumpets/profile", "/v1/trumpets/machine/profile", "/v1/trumpets/machine/due"} {
		w = httptest.NewRecorder()
		app.routes().ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 401 {
			t.Fatalf("%s leaked %d", path, w.Code)
		}
	}
	w = httptest.NewRecorder()
	r = httptest.NewRequest("PUT", "/v1/trumpets/listings/id/feedback", strings.NewReader(`{}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	app.routes().ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("cross-site write accepted")
	}
	empty := &application{}
	w = httptest.NewRecorder()
	r = httptest.NewRequest("GET", "/", nil)
	r.Header.Set("X-Jazz-User", "owner")
	empty.authenticate(next).ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("empty gateway auth bypass")
	}
}
