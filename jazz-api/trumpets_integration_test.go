package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
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
	t.Run("failed rechecks are recorded and an unreachable offer becomes unverifiable", func(t *testing.T) {
		var otherID string
		var otherPrice float64
		if err := isolated.QueryRow(ctx, `SELECT id::text,price FROM trumpet_listings WHERE source_listing_id='other'`).Scan(&otherID, &otherPrice); err != nil {
			t.Fatal(err)
		}
		state := func() (status string, failures int, reason string) {
			t.Helper()
			if err := isolated.QueryRow(ctx, `SELECT status,check_failures,last_check_error FROM trumpet_listings WHERE id=$1`, otherID).Scan(&status, &failures, &reason); err != nil {
				t.Fatal(err)
			}
			return
		}
		blocked := trumpetRunInput{Kind: "combined", Status: "succeeded", Sources: []trumpetSourceCheck{{Source: c.Source, Status: "checked"}},
			CheckFailures: []trumpetCheckFailure{{ID: otherID, Reason: "dealer.test / HTTP 403 (blocks automated checks)"}}}
		previousDay := func() {
			t.Helper()
			if _, err := isolated.Exec(ctx, `UPDATE trumpet_listings SET last_check_attempt=last_check_attempt-interval '1 day' WHERE id=$1`, otherID); err != nil {
				t.Fatal(err)
			}
		}
		// Day 1: the recorded failure counts as checked; the run is not pinned to partial.
		blocked.ExternalID = "blocked-day-1"
		out := ingest(blocked, 200)
		if out["status"] != "succeeded" || out["remainingActive"].(float64) != 0 || out["checkFailures"].(float64) != 1 {
			t.Fatalf("recorded failure still forced partial: %v", out)
		}
		if status, failures, reason := state(); status != "active" || failures != 1 || !strings.Contains(reason, "HTTP 403") {
			t.Fatalf("day 1 state %s %d %q", status, failures, reason)
		}
		// A same-day rerun does not add a day to the streak.
		blocked.ExternalID = "blocked-day-1-rerun"
		ingest(blocked, 200)
		if _, failures, _ := state(); failures != 1 {
			t.Fatalf("same-day rerun counted twice: %d", failures)
		}
		previousDay()
		blocked.ExternalID = "blocked-day-2"
		ingest(blocked, 200)
		if status, failures, _ := state(); status != "active" || failures != 2 {
			t.Fatalf("day 2 state %s %d", status, failures)
		}
		previousDay()
		blocked.ExternalID = "blocked-day-3"
		out = ingest(blocked, 200)
		if out["status"] != "succeeded" || out["markedUnverifiable"].(float64) != 1 {
			t.Fatalf("day 3 run %v", out)
		}
		status, failures, _ := state()
		var price float64
		isolated.QueryRow(ctx, `SELECT price FROM trumpet_listings WHERE id=$1`, otherID).Scan(&price)
		if status != "unverifiable" || failures != 3 || price != otherPrice {
			t.Fatalf("day 3 state %s %d %v", status, failures, price)
		}
		var detail string
		var meaningful bool
		if err := isolated.QueryRow(ctx, `SELECT detail,meaningful FROM trumpet_events WHERE listing_id=$1 AND new_status='unverifiable'`, otherID).Scan(&detail, &meaningful); err != nil || meaningful || !strings.Contains(detail, "not marked sold") {
			t.Fatalf("unverifiable event %q %v %v", detail, meaningful, err)
		}
		// Unverifiable rows stay due for a daily retry and show the reason on the board.
		dueRequest := httptest.NewRequest("GET", "/v1/trumpets/machine/due", nil)
		dueRequest.Header.Set("Authorization", "Bearer "+token)
		dueResponse := httptest.NewRecorder()
		app.routes().ServeHTTP(dueResponse, dueRequest)
		if !bytes.Contains(dueResponse.Body.Bytes(), []byte(otherID)) {
			t.Fatal("unverifiable listing dropped from the due queue")
		}
		var board struct{ Listings []trumpetListing }
		json.Unmarshal(call("GET", "/v1/trumpets/listings", nil, "owner", 200), &board)
		found := false
		for _, l := range board.Listings {
			if l.ID == otherID {
				found = l.Status == "unverifiable" && l.CheckFailures == 3 && l.LastCheckAttempt != nil && strings.Contains(l.LastCheckError, "403")
			}
		}
		if !found {
			t.Fatal("board does not expose the failed-check state")
		}
		// The runner cannot assert the server-only status itself.
		forged := c
		forged.ID = otherID
		forged.Status = "unverifiable"
		ingest(trumpetRunInput{ExternalID: "forged-unverifiable", Kind: "recheck", Status: "succeeded", Sources: blocked.Sources, Listings: []trumpetCandidate{forged}}, 400)
		ingest(trumpetRunInput{ExternalID: "bad-failure-id", Kind: "recheck", Status: "succeeded", Sources: blocked.Sources, CheckFailures: []trumpetCheckFailure{{ID: "nope", Reason: "x"}}}, 400)
		// A later successful check restores it and resets the streak.
		previousDay()
		isolated.Exec(ctx, `UPDATE trumpet_listings SET last_checked=now()-interval '4 days' WHERE id=$1`, otherID)
		restored := c
		restored.ID = otherID
		ingest(trumpetRunInput{ExternalID: "blocked-recovered", Kind: "recheck", Status: "succeeded", Sources: blocked.Sources, Listings: []trumpetCandidate{restored}}, 200)
		if status, failures, reason := state(); status != "active" || failures != 0 || reason != "" {
			t.Fatalf("recovery state %s %d %q", status, failures, reason)
		}
		// Recovering from a blocked page is the same offer, not a meaningful rediscovery.
		var recoveredKind, discoveryType string
		var recoveredMeaningful bool
		if err := isolated.QueryRow(ctx, `SELECT kind,meaningful FROM trumpet_events WHERE listing_id=$1 AND old_status='unverifiable' AND new_status='active'`, otherID).Scan(&recoveredKind, &recoveredMeaningful); err != nil || recoveredKind != "status change" || recoveredMeaningful {
			t.Fatalf("recovery event %q %v %v", recoveredKind, recoveredMeaningful, err)
		}
		if err := isolated.QueryRow(ctx, `SELECT discovery_type FROM trumpet_listings WHERE id=$1`, otherID).Scan(&discoveryType); err != nil || discoveryType == "rediscovered" {
			t.Fatalf("recovery discovery type %q %v", discoveryType, err)
		}
		// A failure on a listing already verified today is ignored.
		blocked.ExternalID = "blocked-after-success"
		if ingest(blocked, 200)["checkFailures"].(float64) != 0 {
			t.Fatal("failure overrode a successful check from the same day")
		}
		// Candidates record attempts (rotating the lead queue) but never become unverifiable;
		// a dead lead page retires the lead out of the due queue.
		lead := trumpetCandidate{Maker: "Lawler", Model: "URL hint", Title: "Lawler trumpet lead", URL: "https://leads.test/products/lawler", Source: "Leads", Status: "stale", VerificationState: "candidate", SearchScore: 70}
		leadID := ingest(trumpetRunInput{ExternalID: "lead-new", Kind: "search", Status: "succeeded", Sources: blocked.Sources, Listings: []trumpetCandidate{lead}}, 200)["listingIds"].([]any)[0].(string)
		for day := 1; day <= 4; day++ {
			ingest(trumpetRunInput{ExternalID: fmt.Sprintf("lead-fail-%d", day), Kind: "search", Status: "succeeded", Sources: blocked.Sources, CheckFailures: []trumpetCheckFailure{{ID: leadID, Reason: "no single verifiable offer"}}}, 200)
			isolated.Exec(ctx, `UPDATE trumpet_listings SET last_check_attempt=last_check_attempt-interval '1 day' WHERE id=$1`, leadID)
		}
		var leadStatus string
		var leadFailures int
		var attempted, checked bool
		isolated.QueryRow(ctx, `SELECT status,check_failures,last_check_attempt IS NOT NULL,last_checked IS NOT NULL FROM trumpet_listings WHERE id=$1`, leadID).Scan(&leadStatus, &leadFailures, &attempted, &checked)
		if leadStatus != "stale" || leadFailures != 4 || !attempted || checked {
			t.Fatalf("candidate failure state %s %d %v %v", leadStatus, leadFailures, attempted, checked)
		}
		lead.ID = leadID
		lead.Status = "removed"
		lead.Evidence = "HTTP 404; search lead no longer exists"
		ingest(trumpetRunInput{ExternalID: "lead-dead", Kind: "recheck", Status: "succeeded", Sources: blocked.Sources, Listings: []trumpetCandidate{lead}}, 200)
		isolated.QueryRow(ctx, `SELECT status FROM trumpet_listings WHERE id=$1`, leadID).Scan(&leadStatus)
		dueResponse = httptest.NewRecorder()
		app.routes().ServeHTTP(dueResponse, dueRequest)
		if leadStatus != "removed" || bytes.Contains(dueResponse.Body.Bytes(), []byte(leadID)) {
			t.Fatal("dead lead not retired from the due queue", leadStatus)
		}
		lead.Status = "stale"
		lead.Price = &price
		if err := normalizeTrumpet(&lead); err == nil {
			t.Fatal("candidate asserted a price")
		}
		if err := migrate(ctx, isolated); err != nil {
			t.Fatal("check failure migration replay", err)
		}
		var checks int
		isolated.QueryRow(ctx, `SELECT count(*) FROM pg_constraint WHERE conrelid='trumpet_listings'::regclass AND conname='trumpet_listings_status_check'`).Scan(&checks)
		if checks != 1 {
			t.Fatal("status constraint duplicated on replay", checks)
		}
	})
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

	t.Run("accumulated source evidence is private replay safe and atomic", func(t *testing.T) {
		input := trumpetRunInput{ExternalID: "source-registry", Kind: "search", Status: "succeeded", Sources: []trumpetSourceCheck{{Source: "New specialist", Domain: "specialist.test", Geography: "Japan", Specialty: "vintage / custom", Query: "site:specialist.test used trumpet", Status: "checked", Candidates: 2, PagesOpened: 3, VerifiedOffers: 1, StaleResults: 1}}}
		ingest(input, 200)
		ingest(input, 200)
		var queries, pages, offers, stale int
		if err := isolated.QueryRow(ctx, `SELECT queries,pages_opened,verified_offers,stale_results FROM trumpet_sources WHERE user_id=$1 AND domain='specialist.test'`, user).Scan(&queries, &pages, &offers, &stale); err != nil || queries != 1 || pages != 3 || offers != 1 || stale != 1 {
			t.Fatalf("registry replay: %d %d %d %d %v", queries, pages, offers, stale, err)
		}
		input.ExternalID = "registry-failed-query"
		input.Status = "partial"
		input.Sources[0].Status = "failed"
		input.Sources[0].PagesOpened = 0
		input.Sources[0].VerifiedOffers = 0
		input.Sources[0].StaleResults = 0
		ingest(input, 200)
		isolated.QueryRow(ctx, `SELECT queries,successful_queries FROM trumpet_sources WHERE user_id=$1 AND domain='specialist.test'`, user).Scan(&queries, &pages)
		if queries != 2 || pages != 1 {
			t.Fatal("query reliability not retained")
		}

		input.ExternalID = "registry-skipped-query"
		input.Sources[0].Status = "skipped"
		ingest(input, 200)
		isolated.QueryRow(ctx, `SELECT queries FROM trumpet_sources WHERE user_id=$1 AND domain='specialist.test'`, user).Scan(&queries)
		if queries != 2 {
			t.Fatal("skipped query falsely advanced rotation")
		}
		owner := call("GET", "/v1/trumpets/profile", nil, "owner", 200)
		other := call("GET", "/v1/trumpets/profile", nil, "other", 200)
		if !bytes.Contains(owner, []byte("specialist.test")) || bytes.Contains(other, []byte("specialist.test")) {
			t.Fatal("source universe owner scope")
		}
		input.ExternalID = "registry-rollback"
		input.Sources[0].Domain = "rolledback.test"
		input.Listings = []trumpetCandidate{{Maker: "Taylor", Model: "Invalid owner", Title: "Invalid", Source: "Shop", URL: "https://rolledback.test/horn", HornID: uuid.NewString()}}
		ingest(input, 400)
		isolated.QueryRow(ctx, `SELECT count(*) FROM trumpet_sources WHERE domain='rolledback.test'`).Scan(&queries)
		if queries != 0 {
			t.Fatal("source survived rejected run")
		}
		if err := migrate(ctx, isolated); err != nil {
			t.Fatal("source registry migration replay", err)
		}
	})
	t.Run("serial confirmed candidate promotion retains both private note histories", func(t *testing.T) {
		price := 4000.0
		verified := trumpetCandidate{Maker: "Eclipse", Model: "Custom", SerialNumber: "CUSTOM-77", Title: "Eclipse Custom Bb trumpet", URL: "https://eclipse.test/products/custom", Source: "Eclipse", Status: "active", Price: &price, Currency: "USD", SearchScore: 90, Description: "Original condition"}
		r := trumpetRunInput{ExternalID: "canonical-horn", Kind: "search", Status: "succeeded", Sources: []trumpetSourceCheck{{Source: "Eclipse", Status: "checked"}}, Listings: []trumpetCandidate{verified}}
		knownID := ingest(r, 200)["listingIds"].([]any)[0].(string)
		call("PUT", "/v1/trumpets/listings/"+knownID+"/feedback", trumpetFeedback{InterestState: "interested", Notes: "Love the engineering", Favorite: true}, "owner", 200)
		lead := trumpetCandidate{Maker: "Eclipse", Model: "URL hint", Title: "Eclipse trumpet", URL: "https://crosspost.test/item/eclipse", Source: "Marketplace", Status: "stale", VerificationState: "candidate", SearchScore: 70}
		r.ExternalID = "crosspost-lead"
		r.Listings = []trumpetCandidate{lead}
		leadID := ingest(r, 200)["listingIds"].([]any)[0].(string)
		call("PUT", "/v1/trumpets/listings/"+leadID+"/feedback", trumpetFeedback{InterestState: "watch", Notes: "Cool but too expensive"}, "owner", 200)
		verified.ID = leadID
		verified.URL = lead.URL
		verified.Source = lead.Source
		r.ExternalID = "serial-promotion"
		r.Listings = []trumpetCandidate{verified}
		ingest(r, 200)
		var horns, meaningful int
		var notes string
		isolated.QueryRow(ctx, `SELECT count(DISTINCT horn_id) FROM trumpet_listings WHERE id::text=ANY($1)`, []string{knownID, leadID}).Scan(&horns)
		isolated.QueryRow(ctx, `SELECT f.notes FROM trumpet_feedback f JOIN trumpet_listings l ON l.horn_id=f.horn_id WHERE l.id=$1`, leadID).Scan(&notes)
		if horns != 1 || !strings.Contains(notes, "Love the engineering") || !strings.Contains(notes, "Cool but too expensive") {
			t.Fatal("crosspost promotion lost identity or notes", horns)
		}
		isolated.QueryRow(ctx, `SELECT count(*) FROM trumpet_events WHERE listing_id=$1 AND meaningful`, leadID).Scan(&meaningful)
		if meaningful != 0 {
			t.Fatal("same-price promoted crosspost alerted as discovery")
		}

		// Tiny changes accumulate without generating a price alert.
		price = 3999
		verified.Price = &price
		verified.ID = knownID
		verified.URL = "https://eclipse.test/products/custom"
		verified.Source = "Eclipse"
		r.ExternalID = "tiny-adjustment"
		r.Listings = []trumpetCandidate{verified}
		ingest(r, 200)
		isolated.QueryRow(ctx, `SELECT count(*) FROM trumpet_events WHERE listing_id=$1 AND kind='price drop' AND meaningful`, knownID).Scan(&meaningful)
		if meaningful != 0 {
			t.Fatal("tiny adjustment alerted")
		}
		verified.Description = "Seller adds artist provenance and condition documentation"
		verified.Details.Condition = "UsedCondition"
		r.ExternalID = "documentation-update"
		r.Listings = []trumpetCandidate{verified}
		ingest(r, 200)
		isolated.QueryRow(ctx, `SELECT count(*) FROM trumpet_events WHERE listing_id=$1 AND kind='details change' AND meaningful`, knownID).Scan(&meaningful)
		if meaningful != 1 {
			t.Fatal("description update not recorded")
		}
		var snapshots int
		isolated.QueryRow(ctx, `SELECT count(*) FROM trumpet_observations WHERE listing_id=$1 AND snapshot->>'description'<>''`, knownID).Scan(&snapshots)
		if snapshots != 3 {
			t.Fatal("description snapshots missing", snapshots)
		}
	})

	t.Run("photos fill on re-ingest and are never wiped by a recheck without them", func(t *testing.T) {
		images := func(id string) []string {
			t.Helper()
			var raw []byte
			if err := isolated.QueryRow(ctx, `SELECT images FROM trumpet_listings WHERE id=$1`, id).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			var out []string
			json.Unmarshal(raw, &out)
			return out
		}
		p := 5100.0
		horn := trumpetCandidate{Maker: "Monette", Model: "Raja Samadhi", Title: "Monette Raja Samadhi · fixture", URL: "https://photos.test/products/raja", Source: "Photo fixture dealer", Price: &p, Currency: "USD", Status: "active", SearchScore: 80, VerificationState: "verified"}
		sources := []trumpetSourceCheck{{Source: horn.Source, Status: "checked", Candidates: 1}}
		r := trumpetRunInput{ExternalID: "photos-1", Kind: "combined", Status: "succeeded", Sources: sources, Listings: []trumpetCandidate{horn}}
		hornID := ingest(r, 200)["listingIds"].([]any)[0].(string)
		if got := images(hornID); len(got) != 0 {
			t.Fatalf("new listing without photos has %v", got)
		}
		// A recheck that sees photos fills an empty gallery.
		horn.Images = []string{"https://photos.test/raja-1.jpg", "https://photos.test/raja-2.jpg"}
		r.ExternalID, r.Kind, r.Listings = "photos-2", "recheck", []trumpetCandidate{horn}
		ingest(r, 200)
		if got := images(hornID); len(got) != 2 || got[0] != "https://photos.test/raja-1.jpg" {
			t.Fatalf("recheck did not add photos: %v", got)
		}
		// A recheck that sees none keeps them.
		horn.Images = nil
		r.ExternalID, r.Listings = "photos-3", []trumpetCandidate{horn}
		ingest(r, 200)
		if got := images(hornID); len(got) != 2 {
			t.Fatalf("recheck without photos wiped them: %v", got)
		}
		// A search rediscovery as a lead (no photos) never wipes them either.
		lead := horn
		lead.VerificationState, lead.Price, lead.Status = "candidate", nil, "stale"
		r.ExternalID, r.Kind, r.Listings = "photos-4", "search", []trumpetCandidate{lead}
		ingest(r, 200)
		if got := images(hornID); len(got) != 2 {
			t.Fatalf("lead rediscovery wiped photos: %v", got)
		}
		// New photos from the source replace the old ones.
		horn.Images = []string{"https://photos.test/raja-3.jpg"}
		r.ExternalID, r.Kind, r.Listings = "photos-5", "recheck", []trumpetCandidate{horn}
		ingest(r, 200)
		if got := images(hornID); len(got) != 1 || got[0] != "https://photos.test/raja-3.jpg" {
			t.Fatalf("recheck with new photos did not replace them: %v", got)
		}

		// The image backfill: an 'images' run only fills empty galleries.
		bare := horn
		bare.URL, bare.Title, bare.Images = "https://photos.test/products/bare", "Monette bare · fixture", nil
		r.ExternalID, r.Kind, r.Listings = "photos-6", "combined", []trumpetCandidate{bare}
		bareID := ingest(r, 200)["listingIds"].([]any)[0].(string)
		var checkedBefore string
		isolated.QueryRow(ctx, `SELECT last_checked::text FROM trumpet_listings WHERE id=$1`, bareID).Scan(&checkedBefore)
		var observations int
		isolated.QueryRow(ctx, `SELECT count(*) FROM trumpet_observations WHERE listing_id=$1`, bareID).Scan(&observations)
		backfill := trumpetRunInput{ExternalID: "image-backfill-1", Kind: "images", Status: "succeeded", ImageUpdates: []trumpetImageUpdate{
			{ID: bareID, Images: []string{"https://photos.test/bare-1.jpg"}},
			{ID: hornID, Images: []string{"https://photos.test/should-not-replace.jpg"}},
			{ID: uuid.NewString(), Images: []string{"https://photos.test/not-owned.jpg"}},
		}}
		out := ingest(backfill, 200)
		if out["status"] != "succeeded" || out["imagesAdded"].(float64) != 1 {
			t.Fatalf("image run %v", out)
		}
		if got := images(bareID); len(got) != 1 || got[0] != "https://photos.test/bare-1.jpg" {
			t.Fatalf("backfill did not fill: %v", got)
		}
		if got := images(hornID); len(got) != 1 || got[0] != "https://photos.test/raja-3.jpg" {
			t.Fatalf("backfill replaced existing photos: %v", got)
		}
		// Not an observation: no check, no history, and a retry replays.
		var checkedAfter string
		var after int
		isolated.QueryRow(ctx, `SELECT last_checked::text FROM trumpet_listings WHERE id=$1`, bareID).Scan(&checkedAfter)
		isolated.QueryRow(ctx, `SELECT count(*) FROM trumpet_observations WHERE listing_id=$1`, bareID).Scan(&after)
		if checkedAfter != checkedBefore || after != observations {
			t.Fatalf("image run counted as a check: %s -> %s, %d -> %d observations", checkedBefore, checkedAfter, observations, after)
		}
		if ingest(backfill, 200)["replayed"] != true {
			t.Fatal("image run retry was not idempotent")
		}
		// The board's run freshness ignores image runs.
		var board map[string]any
		json.Unmarshal(call("GET", "/v1/trumpets/listings", nil, "owner", 200), &board)
		if latest, _ := board["latestRun"].(map[string]any); latest == nil || latest["kind"] == "images" {
			t.Fatalf("image run shown as the latest run: %v", board["latestRun"])
		}
		if success, _ := board["lastSuccess"].(map[string]any); success["images"] != nil {
			t.Fatalf("image run counted as a successful run: %v", success)
		}
		// Image runs carry image updates only, with HTTPS photos.
		bad := backfill
		bad.ExternalID, bad.ImageUpdates = "image-backfill-http", []trumpetImageUpdate{{ID: bareID, Images: []string{"http://photos.test/x.jpg"}}}
		ingest(bad, 400)
		bad.ExternalID, bad.ImageUpdates, bad.Listings = "image-backfill-listings", nil, []trumpetCandidate{bare}
		ingest(bad, 400)
		// ... and no source coverage either (it searched nothing).
		bad.ExternalID, bad.Listings, bad.ImageUpdates, bad.Sources = "image-backfill-sources", nil, []trumpetImageUpdate{{ID: bareID, Images: []string{"https://photos.test/c.jpg"}}}, sources
		ingest(bad, 400)
		bad.Sources = nil
		bad.ExternalID, bad.Listings, bad.ImageUpdates = "image-backfill-dup", nil, []trumpetImageUpdate{{ID: bareID, Images: []string{"https://photos.test/a.jpg"}}, {ID: bareID, Images: []string{"https://photos.test/b.jpg"}}}
		ingest(bad, 400)
		// A daily run can carry image updates for pages it reached but could
		// not verify; they fill only empty galleries too.
		other := horn
		other.URL, other.Title, other.Images = "https://photos.test/products/other", "Monette other · fixture", nil
		r.ExternalID, r.Kind, r.Listings = "photos-7", "combined", []trumpetCandidate{other}
		otherID := ingest(r, 200)["listingIds"].([]any)[0].(string)
		daily := trumpetRunInput{ExternalID: "photos-8", Kind: "combined", Status: "succeeded", Sources: sources, ImageUpdates: []trumpetImageUpdate{{ID: otherID, Images: []string{"https://photos.test/other-1.jpg"}}}}
		if out := ingest(daily, 200); out["imagesAdded"].(float64) != 1 {
			t.Fatalf("daily image update %v", out)
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
