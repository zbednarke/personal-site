package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestTrumpetPersistence(t *testing.T) {
	dsn := os.Getenv("TRUMPETS_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TRUMPETS_TEST_DATABASE_URL to disposable Postgres")
	}
	ctx := context.Background()
	db, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	schema := "trumpets_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err = db.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer db.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	isolated, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer isolated.Close()
	for i := 0; i < 2; i++ {
		if err = migrate(ctx, isolated); err != nil {
			t.Fatalf("migration pass %d: %v", i, err)
		}
	}
	app := &application{db: isolated, cfg: config{GatewayKey: "gateway"}, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	ownerctx := context.WithValue(ctx, userSubjectKey, "owner")
	user, err := app.userID(ownerctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Run("migration replay preserves existing long Jazz recordings", func(t *testing.T) {
		for _, duration := range []int{7200000, 14400000} {
			id := uuid.New()
			_, err := isolated.Exec(ctx, `INSERT INTO recordings
 (id,user_id,bucket,object_name,content_type,expected_size_bytes,duration_ms,recorded_at,status)
 VALUES ($1,$2,'test',$3,'audio/webm',1,$4,now(),'ready')`, id, user, id.String(), duration)
			if err != nil {
				t.Fatal(err)
			}
		}
		if err := migrate(ctx, isolated); err != nil {
			t.Fatalf("replay with existing two/four-hour recordings: %v", err)
		}
		var total int
		if err := isolated.QueryRow(ctx, `SELECT sum(duration_ms) FROM recordings WHERE user_id=$1`, user).Scan(&total); err != nil || total != 21600000 {
			t.Fatalf("recording durations changed during migration: %d, %v", total, err)
		}
	})
	call := func(method, path string, input any, subject string, want int) []byte {
		t.Helper()
		body, _ := json.Marshal(input)
		r := httptest.NewRequest(method, path, bytes.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Jazz-Gateway-Key", "gateway")
		r.Header.Set("X-Jazz-User", subject)
		w := httptest.NewRecorder()
		app.routes().ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
		}
		return w.Body.Bytes()
	}
	call("POST", "/v1/trumpets/seed", map[string]any{}, "owner", 200)
	call("POST", "/v1/trumpets/seed", map[string]any{}, "owner", 200)
	var count int
	if err = isolated.QueryRow(ctx, `SELECT count(*) FROM trumpet_listings WHERE user_id=$1`, user).Scan(&count); err != nil || count != len(historicalTrumpets()) {
		t.Fatalf("seed idempotency %d %v", count, err)
	}
	var seedAcquired bool
	if err = isolated.QueryRow(ctx, `SELECT acquired FROM trumpet_horns WHERE user_id=$1 AND maker='Taylor' AND model='Chicago 46 II / Harrelson-modified'`, user).Scan(&seedAcquired); err != nil || !seedAcquired {
		t.Fatal("known horn not acquired", err)
	}
	// Test the HTTP machine workflow as well as database transactions.
	token := strings.Repeat("machine-token-", 4)
	setMachineTestToken(t, token, "owner")
	ingest := func(input trumpetRunInput, want int) map[string]any {
		t.Helper()
		b, _ := json.Marshal(input)
		r := httptest.NewRequest("POST", "/v1/trumpets/machine/runs", bytes.NewReader(b))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		app.routes().ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("ingest %d: %s", w.Code, w.Body.String())
		}
		var out map[string]any
		json.Unmarshal(w.Body.Bytes(), &out)
		return out
	}
	price := 3000.0
	c := trumpetCandidate{Maker: "Lawler", Model: "C7", SerialNumber: "ABC", Title: "Lawler C7 raw brass", URL: "https://dealer.test/horns/c7?utm_source=chat", Source: "Independent dealer", SourceListingID: "offer-7", Price: &price, Currency: "USD", Status: "active", SearchScore: 85, Details: hornDetails{Finish: "raw brass"}, Tags: []string{"raw brass"}}
	run := trumpetRunInput{ExternalID: "day-1", Kind: "combined", Status: "succeeded", Sources: []trumpetSourceCheck{{Source: c.Source, Status: "checked", Candidates: 1}}, Listings: []trumpetCandidate{c}}
	first := ingest(run, 200)
	id := first["listingIds"].([]any)[0].(string)
	if first["status"] != "succeeded" {
		t.Fatal(first)
	}
	if ingest(run, 200)["replayed"] != true {
		t.Fatal("retry was not idempotent")
	}
	// Canonical and source ID identities must survive changed URL slugs.
	price = 2500
	c.Price = &price
	c.URL = "https://dealer.test/horns/c7-renamed"
	run.ExternalID = "day-2"
	run.Listings = []trumpetCandidate{c}
	second := ingest(run, 200)
	if second["listingIds"].([]any)[0] != id {
		t.Fatal("source/listing dedupe failed")
	}
	c.URL = "https://dealer.test/horns/c7-renamed/?utm_campaign=new"
	run.ExternalID = "day-3"
	run.Listings = []trumpetCandidate{c}
	if ingest(run, 200)["listingIds"].([]any)[0] != id {
		t.Fatal("canonical dedupe failed")
	}
	var history, drops int
	isolated.QueryRow(ctx, `SELECT count(*) FROM trumpet_observations WHERE listing_id=$1`, id).Scan(&history)
	isolated.QueryRow(ctx, `SELECT count(*) FROM trumpet_events WHERE listing_id=$1 AND kind='price drop'`, id).Scan(&drops)
	if history != 3 || drops != 1 {
		t.Fatalf("history %d drops %d", history, drops)
	}
	rating := 5
	feedback := trumpetFeedback{Rating: &rating, InterestState: "interested", Notes: "Private upswept preference", Favorite: true, FavoredAttributes: []string{"Raw brass"}}
	call("PUT", "/v1/trumpets/listings/"+id+"/feedback", feedback, "owner", 200)
	call("PUT", "/v1/trumpets/listings/"+id+"/feedback", feedback, "other", 404)
	data := call("GET", "/v1/trumpets/listings", nil, "other", 200)
	if bytes.Contains(data, []byte("Private upswept")) || bytes.Contains(data, []byte("Lawler")) {
		t.Fatal("cross-user board leaked")
	}
	data = call("GET", "/v1/trumpets/profile", nil, "owner", 200)
	if !bytes.Contains(data, []byte("Private upswept preference")) || !bytes.Contains(data, []byte("raw brass")) {
		t.Fatal("profile missing feedback")
	}
	data = call("GET", "/v1/trumpets/listings", nil, "owner", 200)
	var board struct {
		Listings []trumpetListing
		Events   []json.RawMessage
	}
	if err = json.Unmarshal(data, &board); err != nil {
		t.Fatal(err)
	}
	var horn string
	for _, l := range board.Listings {
		if l.ID == id {
			horn = l.HornID
			if l.Feedback.Rating == nil || *l.Feedback.Rating != 5 || len(l.PriceHistory) != 3 {
				t.Fatal("feedback/history readback", l)
			}
		}
	}
	// Same physical horn, a distinct offer at another source.
	relist := c
	relist.URL = "https://another.test/offer"
	relist.Source = "Second shop"
	relist.SourceListingID = "abc"
	run.ExternalID = "relist"
	run.Listings = []trumpetCandidate{relist}
	third := ingest(run, 200)
	relistID := third["listingIds"].([]any)[0].(string)
	var otherHorn string
	isolated.QueryRow(ctx, `SELECT horn_id::text FROM trumpet_listings WHERE id=$1`, relistID).Scan(&otherHorn)
	if otherHorn != horn || relistID == id {
		t.Fatal("serial relist did not share horn")
	}
	c.Status = "sold"
	run.ExternalID = "sold"
	run.Listings = []trumpetCandidate{c}
	ingest(run, 200)
	c.Status = "active"
	run.ExternalID = "rediscover"
	run.Listings = []trumpetCandidate{c}
	ingest(run, 200)
	var rediscovers int
	isolated.QueryRow(ctx, `SELECT count(*) FROM trumpet_events WHERE listing_id=$1 AND kind='rediscovered'`, id).Scan(&rediscovers)
	if rediscovers != 1 {
		t.Fatal("status transition missing")
	}

	for _, status := range []string{"removed", "stale", "active"} {
		c.Status = status
		run.ExternalID = "transition-" + status
		run.Listings = []trumpetCandidate{c}
		ingest(run, 200)
	}
	feedback.InterestState = "acquired"
	call("PUT", "/v1/trumpets/listings/"+id+"/feedback", feedback, "owner", 200)
	var acquiredOffers int
	isolated.QueryRow(ctx, `SELECT count(*) FROM trumpet_listings WHERE horn_id=$1 AND status='acquired' AND canonical_url IS NOT NULL`, horn).Scan(&acquiredOffers)
	if acquiredOffers != 2 {
		t.Fatal("acquisition did not apply to relists")
	}
	run.ExternalID = "after-acquisition"
	run.Listings = []trumpetCandidate{c}
	ingest(run, 200)
	data = call("GET", "/v1/trumpets/listings", nil, "owner", 200)
	json.Unmarshal(data, &board)
	for _, raw := range board.Events {
		if bytes.Contains(raw, []byte(id)) || bytes.Contains(raw, []byte(relistID)) {
			t.Fatal("acquired instrument alerted")
		}
	}
	data = call("GET", "/v1/trumpets/profile", nil, "owner", 200)
	if !bytes.Contains(data, []byte("acquiredExclusions")) {
		t.Fatal("acquired profile missing")
	}

	call("DELETE", "/v1/trumpets/listings/"+id+"/feedback", map[string]any{}, "other", 404)
	call("DELETE", "/v1/trumpets/listings/"+id+"/feedback", map[string]any{}, "owner", 200)
	data = call("GET", "/v1/trumpets/profile", nil, "owner", 200)
	if bytes.Contains(data, []byte("Private upswept preference")) {
		t.Fatal("deleted feedback persisted")
	}
	var stillOwned bool
	if err = isolated.QueryRow(ctx, `SELECT acquired FROM trumpet_horns WHERE id=$1`, horn).Scan(&stillOwned); err != nil || !stillOwned {
		t.Fatal("reset reversed acquired exclusion")
	}
	dueRequest := httptest.NewRequest("GET", "/v1/trumpets/machine/due", nil)
	dueRequest.Header.Set("Authorization", "Bearer "+token)
	dueResponse := httptest.NewRecorder()
	app.routes().ServeHTTP(dueResponse, dueRequest)
	if dueResponse.Code != 200 || bytes.Contains(dueResponse.Body.Bytes(), []byte(id)) {
		t.Fatal("acquired horn in due queue")
	}
	// Recognizable purchased build without a serial is excluded across sources.
	bought := trumpetCandidate{Maker: "Taylor", Model: "Chicago 46 II", Title: "Taylor Chicago 46 II Harrelson modified Bb", URL: "https://another.test/purchased", Source: "Another source", Status: "active", SearchScore: 95}
	run.ExternalID = "known-acquired-relist"
	run.Listings = []trumpetCandidate{bought}
	purchased := ingest(run, 200)["listingIds"].([]any)[0].(string)
	var purchaseStatus string
	isolated.QueryRow(ctx, `SELECT status FROM trumpet_listings WHERE id=$1`, purchased).Scan(&purchaseStatus)
	if purchaseStatus != "acquired" {
		t.Fatal("known purchased build alerted as active")
	}
	// A full run that misses active offers becomes partial, never a false success.
	c.SerialNumber = "OTHER"
	c.URL = "https://dealer.test/other"
	c.SourceListingID = "other"
	run.ExternalID = "other-horn"
	run.Listings = []trumpetCandidate{c}
	ingest(run, 200)
	isolated.Exec(ctx, `UPDATE trumpet_listings SET last_checked=now()-interval '2 days' WHERE source_listing_id='other'`)
	run.ExternalID = "missed-active"
	run.Listings = nil
	if ingest(run, 200)["status"] != "partial" {
		t.Fatal("unchecked active horn accepted as successful recheck")
	}
	t.Run("candidate queue promotes without losing feedback or inventing history", func(t *testing.T) {
		hint := trumpetCandidate{Maker: "Taylor", Model: "URL hint", Title: "Taylor Chicago trumpet", URL: "https://dealer.test/products/taylor-hint", Source: "Dealer", Status: "stale", VerificationState: "candidate", SearchScore: 70}
		r := trumpetRunInput{ExternalID: "hint", Kind: "search", Status: "succeeded", Sources: []trumpetSourceCheck{{Source: "Dealer", Status: "checked"}}, Listings: []trumpetCandidate{hint}}
		hintID := ingest(r, 200)["listingIds"].([]any)[0].(string)
		var observations, meaningful int
		var checked bool
		isolated.QueryRow(ctx, `SELECT count(*) FROM trumpet_observations WHERE listing_id=$1`, hintID).Scan(&observations)
		isolated.QueryRow(ctx, `SELECT count(*) FROM trumpet_events WHERE listing_id=$1 AND meaningful`, hintID).Scan(&meaningful)
		isolated.QueryRow(ctx, `SELECT last_checked IS NOT NULL FROM trumpet_listings WHERE id=$1`, hintID).Scan(&checked)
		if observations != 0 || meaningful != 0 || checked {
			t.Fatal("hint invented market history")
		}
		f := trumpetFeedback{InterestState: "watch", Notes: "Keep this lead", Favorite: true}
		call("PUT", "/v1/trumpets/listings/"+hintID+"/feedback", f, "owner", 200)
		hint.VerificationState = "verified"
		hint.Status = "active"
		hint.Model = "Chicago II"
		p := 2100.0
		hint.Price = &p
		r.ExternalID = "hint-verified"
		r.Listings = []trumpetCandidate{hint}
		if ingest(r, 200)["listingIds"].([]any)[0] != hintID {
			t.Fatal("promotion lost identity")
		}
		data := call("GET", "/v1/trumpets/listings", nil, "owner", 200)
		var b struct{ Listings []trumpetListing }
		json.Unmarshal(data, &b)
		for _, l := range b.Listings {
			if l.ID == hintID && (l.VerificationState != "verified" || l.Model != "Chicago II" || l.Feedback.Notes != "Keep this lead" || len(l.PriceHistory) != 1) {
				t.Fatal("promotion lost feedback or history", l)
			}
		}
		hint.VerificationState = "candidate"
		hint.Status = "stale"
		hint.Price = nil
		r.ExternalID = "hint-rediscovered"
		r.Listings = []trumpetCandidate{hint}
		ingest(r, 200)
		var state, status string
		var money float64
		isolated.QueryRow(ctx, `SELECT verification_state,status,price FROM trumpet_listings WHERE id=$1`, hintID).Scan(&state, &status, &money)
		if state != "verified" || status != "active" || money != 2100 {
			t.Fatal("search hint downgraded verified offer")
		}
		if err := migrate(ctx, isolated); err != nil {
			t.Fatal("candidate migration replay", err)
		}
	})

	// Reject owner hijacking and roll back the entire report.
	c.ID = uuid.NewString()
	run.ExternalID = "bad"
	run.Listings = []trumpetCandidate{c}
	ingest(run, 400)
	if err = isolated.QueryRow(ctx, `SELECT count(*) FROM trumpet_runs WHERE external_id='bad'`).Scan(&count); err != nil || count != 0 {
		t.Fatal("failed run was not rolled back")
	}
}

func setMachineTestToken(t *testing.T, token, subject string) {
	t.Helper()
	hash := sha256.Sum256([]byte(token))
	t.Setenv("TRUMPETS_MACHINE_TOKEN_SHA256", hex.EncodeToString(hash[:]))
	t.Setenv("TRUMPETS_MACHINE_USER", subject)
}
