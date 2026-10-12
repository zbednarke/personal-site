package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// roundTrips counts database round trips (a pipelined batch is one) and can
// add a simulated network latency to each, standing in for Cloud Run to Cloud
// SQL, where a round trip costs milliseconds rather than the local ~0.1 ms.
type roundTrips struct {
	n       atomic.Int64
	latency atomic.Int64 // nanoseconds
}

func (r *roundTrips) wait() {
	r.n.Add(1)
	if d := r.latency.Load(); d > 0 {
		time.Sleep(time.Duration(d))
	}
}
func (r *roundTrips) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	r.wait()
	return ctx
}
func (r *roundTrips) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}
func (r *roundTrips) TraceBatchStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceBatchStartData) context.Context {
	r.wait()
	return ctx
}
func (r *roundTrips) TraceBatchQuery(context.Context, *pgx.Conn, pgx.TraceBatchQueryData) {}
func (r *roundTrips) TraceBatchEnd(context.Context, *pgx.Conn, pgx.TraceBatchEndData)     {}

// perfCandidate is a fictional dealer listing; nothing here is real data.
func perfCandidate(i int, price float64) trumpetCandidate {
	makers := []string{"Lawler", "Monette", "Schilke", "Adams", "Taylor", "Eclipse"}
	p := price
	return trumpetCandidate{Maker: makers[i%len(makers)], Model: fmt.Sprintf("Model %d", i), SerialNumber: fmt.Sprintf("SN%05d", i),
		Title: fmt.Sprintf("Fictional %s trumpet %d raw brass", makers[i%len(makers)], i), Description: strings.Repeat("Fictional description. ", 40),
		URL: fmt.Sprintf("https://dealer%d.test/horns/%d", i%20, i), Source: fmt.Sprintf("Dealer %d", i%20), SourceListingID: fmt.Sprintf("offer-%d", i),
		Price: &p, Currency: "USD", Status: "active", SearchScore: 70, Details: hornDetails{Finish: "raw brass"}, Tags: []string{"raw brass"},
		Images: []string{fmt.Sprintf("https://img.test/%d.jpg", i)}, VerifiedVia: "direct", Evidence: "fictional"}
}

func perfSources(n int) []trumpetSourceCheck {
	out := make([]trumpetSourceCheck, n)
	for i := range out {
		out[i] = trumpetSourceCheck{Source: fmt.Sprintf("Dealer %d / dealer%d.test", i, i), Domain: fmt.Sprintf("dealer%d.test", i), Status: "checked", Query: "site:dealer.test trumpet", PagesOpened: 3, VerifiedOffers: 1}
	}
	return out
}

// TestTrumpetIngestRoundTrips bounds POST /machine/runs for a production-size
// run (~150 listings, ~150 failed rechecks, ~50 sources over a ~300-listing
// board with history). On Cloud Run every round trip to Cloud SQL costs a few
// milliseconds and the service has concurrency 1, so a slow ingest blocks the
// owner's board loads; the bound is on round trips (latency-independent) and
// on wall time with a simulated 5 ms round trip.
func TestTrumpetIngestRoundTrips(t *testing.T) {
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
	schema := "trumpets_perf_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err = db.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer db.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	trips := &roundTrips{}
	cfg.ConnConfig.Tracer = trips
	cfg.MaxConns = 4
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err = migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	app := &application{db: pool, cfg: config{GatewayKey: "gateway"}, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	routes := app.routes()
	token := strings.Repeat("machine-token-", 4)
	setMachineTestToken(t, token, "owner")
	do := func(method, path string, input any) *httptest.ResponseRecorder {
		t.Helper()
		var body io.Reader
		if input != nil {
			b, _ := json.Marshal(input)
			body = bytes.NewReader(b)
		}
		r := httptest.NewRequest(method, path, body)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		routes.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
		}
		return w
	}
	// History: 300 listings, observed on four earlier runs with price changes.
	const board = 300
	ids := make([]string, board)
	for day := 0; day < 4; day++ {
		listings := make([]trumpetCandidate, board)
		for i := range listings {
			listings[i] = perfCandidate(i, float64(3000+i-day*10*(i%3)))
		}
		out := map[string]any{}
		json.Unmarshal(do("POST", "/v1/trumpets/machine/runs", trumpetRunInput{ExternalID: fmt.Sprintf("history-%d", day), Kind: "search", Status: "succeeded", Sources: perfSources(50), Listings: listings}).Body.Bytes(), &out)
		for i, id := range out["listingIds"].([]any) {
			ids[i] = id.(string)
		}
	}
	// The history is from earlier days: today's rechecks are still due.
	if _, err = pool.Exec(ctx, `UPDATE trumpet_listings SET last_checked=now()-interval '2 days',first_seen=first_seen-interval '5 days'`); err != nil {
		t.Fatal(err)
	}
	// Today's run: 130 rechecked offers (some price drops, one sold), 15 new
	// offers, 5 new leads, and 150 failed rechecks of the other offers.
	run := trumpetRunInput{ExternalID: "today", Kind: "combined", Status: "partial", Sources: perfSources(50)}
	for i := 0; i < 130; i++ {
		c := perfCandidate(i, float64(3000+i-40*(i%3)))
		c.ID = ids[i]
		if i == 7 {
			c.Status = "sold"
		}
		run.Listings = append(run.Listings, c)
	}
	for i := board; i < board+15; i++ {
		run.Listings = append(run.Listings, perfCandidate(i, 4000))
	}
	for i := board + 15; i < board+20; i++ {
		c := perfCandidate(i, 0)
		c.Price, c.SerialNumber, c.VerificationState, c.Status = nil, "", "candidate", "stale"
		run.Listings = append(run.Listings, c)
	}
	for i := 150; i < board; i++ {
		run.CheckFailures = append(run.CheckFailures, trumpetCheckFailure{ID: ids[i], Reason: "dealer.test / HTTP 403"})
	}
	const rtt = 5 * time.Millisecond
	trips.n.Store(0)
	trips.latency.Store(int64(rtt))
	start := time.Now()
	out := map[string]any{}
	json.Unmarshal(do("POST", "/v1/trumpets/machine/runs", run).Body.Bytes(), &out)
	elapsed := time.Since(start)
	n := trips.n.Load()
	trips.latency.Store(0)
	t.Logf("ingest of %d listings + %d failures: %d round trips, %v with %v simulated round trip", len(run.Listings), len(run.CheckFailures), n, elapsed, rtt)
	if len(out["listingIds"].([]any)) != len(run.Listings) || out["checkFailures"].(float64) != 150 {
		t.Fatalf("ingest result: %d listings, %v failures recorded", len(out["listingIds"].([]any)), out["checkFailures"])
	}
	if n > 60 {
		t.Errorf("ingest took %d round trips; want at most 60 (one pipelined batch per listing at worst would be ~150)", n)
	}
	if elapsed > 3*time.Second {
		t.Errorf("ingest took %v with %v round trips; want under 3 s", elapsed, rtt)
	}
	// Within one run, a listing written earlier is looked up live: a second
	// report of the same new URL, or of a rechecked offer's old URL, resolves
	// to that listing (and, as before, the run's first observation wins).
	dup := perfCandidate(900, 5000)
	again := perfCandidate(900, 4500)
	moved := perfCandidate(1, 3001)
	moved.ID, moved.URL = ids[1], "https://dealer1.test/horns/1-renamed"
	old := perfCandidate(1, 2999)
	var same map[string]any
	json.Unmarshal(do("POST", "/v1/trumpets/machine/runs", trumpetRunInput{ExternalID: "same-run", Kind: "search", Status: "succeeded", Sources: perfSources(1), Listings: []trumpetCandidate{dup, again, moved, old}}).Body.Bytes(), &same)
	got := same["listingIds"].([]any)
	if got[0] != got[1] || got[2] != ids[1] || got[3] != ids[1] {
		t.Fatalf("same-run identities: %v (want %s twice at the end)", got, ids[1])
	}
	var dupPrice, movedPrice float64
	pool.QueryRow(ctx, `SELECT price FROM trumpet_listings WHERE id=$1`, got[0]).Scan(&dupPrice)
	pool.QueryRow(ctx, `SELECT price FROM trumpet_listings WHERE id=$1`, ids[1]).Scan(&movedPrice)
	if dupPrice != 5000 || movedPrice != 3001 {
		t.Fatalf("same-run prices: %v %v", dupPrice, movedPrice)
	}
	// A replay is a single lookup.
	trips.n.Store(0)
	if !strings.Contains(do("POST", "/v1/trumpets/machine/runs", run).Body.String(), `"replayed":true`) {
		t.Fatal("replay not idempotent")
	}
	if n := trips.n.Load(); n > 6 {
		t.Errorf("replay took %d round trips", n)
	}
	// The runner's landed-run lookup.
	var found map[string]any
	json.Unmarshal(do("GET", "/v1/trumpets/machine/runs?externalId=today", nil).Body.Bytes(), &found)
	if found["found"] != true || found["status"] == "" {
		t.Fatalf("run lookup %v", found)
	}
	json.Unmarshal(do("GET", "/v1/trumpets/machine/runs?externalId=missing", nil).Body.Bytes(), &found)
	if found["found"] != false {
		t.Fatalf("missing run lookup %v", found)
	}
	// Board and taste reads for the same ~320 listings: few round trips, and
	// the board is compressed for clients that accept gzip.
	for _, path := range []string{"/v1/trumpets/machine/listings", "/v1/trumpets/machine/taste"} {
		for pass := 0; pass < 2; pass++ {
			trips.n.Store(0)
			trips.latency.Store(int64(rtt))
			start := time.Now()
			w := do("GET", path, nil)
			elapsed, n := time.Since(start), trips.n.Load()
			trips.latency.Store(0)
			r := httptest.NewRequest("GET", path, nil)
			r.Header.Set("Authorization", "Bearer "+token)
			r.Header.Set("Accept-Encoding", "gzip, deflate, br")
			zipped := httptest.NewRecorder()
			routes.ServeHTTP(zipped, r)
			t.Logf("GET %s (pass %d): %d round trips, %v with %v simulated round trip, %d bytes (%d gzipped)", path, pass, n, elapsed, rtt, w.Body.Len(), zipped.Body.Len())
			if n > 12 {
				t.Errorf("GET %s took %d round trips", path, n)
			}
			if zipped.Header().Get("Content-Encoding") != "gzip" || (path == "/v1/trumpets/machine/listings" && zipped.Body.Len()*3 > w.Body.Len()) {
				t.Errorf("GET %s: %q, %d of %d bytes", path, zipped.Header().Get("Content-Encoding"), zipped.Body.Len(), w.Body.Len())
			}
			gz, err := gzip.NewReader(zipped.Body)
			if err != nil {
				t.Fatal(err)
			}
			plain, _ := io.ReadAll(gz)
			if !bytes.Equal(plain, w.Body.Bytes()) && path == "/v1/trumpets/machine/taste" {
				t.Errorf("GET %s: gzipped body differs", path)
			}
			if !json.Valid(plain) {
				t.Errorf("GET %s: gzipped body is not JSON", path)
			}
		}
	}
	// Unchanged observation snapshots are sent once.
	var loaded struct {
		Listings []trumpetListing `json:"listings"`
	}
	json.Unmarshal(do("GET", "/v1/trumpets/machine/listings", nil).Body.Bytes(), &loaded)
	for _, l := range loaded.Listings {
		if l.ID != ids[0] {
			continue
		}
		withSnapshot := 0
		for _, o := range l.PriceHistory {
			var observation struct {
				Snapshot json.RawMessage `json:"snapshot"`
			}
			json.Unmarshal(o, &observation)
			if len(observation.Snapshot) > 0 && string(observation.Snapshot) != "null" {
				withSnapshot++
			}
		}
		if len(l.PriceHistory) != 5 || withSnapshot != 1 {
			t.Errorf("listing history: %d observations, %d snapshots; want 5 and 1", len(l.PriceHistory), withSnapshot)
		}
	}
}
