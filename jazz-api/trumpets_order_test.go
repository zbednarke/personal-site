package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Listings stored in one transaction share first_seen (its now()). Every
// query that orders by first_seen breaks the tie by id, so the board, the
// taste queue and upsert's identity lookup give the same answer every time,
// whatever the rows' physical order.
func TestTrumpetFirstSeenTiesAreStable(t *testing.T) {
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
	schema := "trumpets_order_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err = db.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer db.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err = migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	app := &application{db: pool, cfg: config{GatewayKey: "gateway"}, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	user, err := app.userID(context.WithValue(ctx, userSubjectKey, "owner"))
	if err != nil {
		t.Fatal(err)
	}
	// Six fictional listings in one transaction: one shared first_seen.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ids := []uuid.UUID{}
	for i := 0; i < 6; i++ {
		id, err := upsertTrumpet(ctx, tx, user, perfCandidate(i, 3000), nil)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var distinct int
	if err = pool.QueryRow(ctx, `SELECT count(DISTINCT first_seen) FROM trumpet_listings WHERE user_id=$1`, user).Scan(&distinct); err != nil || distinct != 1 {
		t.Fatalf("fixture listings should share first_seen: %d distinct (%v)", distinct, err)
	}
	sorted := append([]uuid.UUID{}, ids...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].String() < sorted[j].String() })
	// Rewrite the rows lowest id first: each update moves the row to the end
	// of the heap, so the physical order becomes the reverse of id order.
	for _, id := range sorted {
		if _, err = pool.Exec(ctx, `UPDATE trumpet_listings SET description=description||' ' WHERE id=$1`, id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = pool.Exec(ctx, `UPDATE trumpet_listings SET description=description||' ' WHERE id=$1`, sorted[0]); err != nil {
		t.Fatal(err)
	}

	t.Run("the board lists tied listings by id", func(t *testing.T) {
		for round := 0; round < 3; round++ {
			listings, err := app.loadTrumpetListings(ctx, user, false)
			if err != nil {
				t.Fatal(err)
			}
			got := []string{}
			for _, l := range listings {
				got = append(got, l.ID)
			}
			want := []string{}
			for _, id := range sorted {
				want = append(want, id.String())
			}
			if strings.Join(got, ",") != strings.Join(want, ",") {
				t.Fatalf("round %d: board order %v, want id order %v", round, got, want)
			}
		}
	})

	t.Run("an identity matching two tied listings resolves to the lowest id", func(t *testing.T) {
		// A candidate whose URL is one listing's and whose source listing id is
		// another's matches both.
		var lowURL, highSource, highSID string
		if err := pool.QueryRow(ctx, `SELECT canonical_url FROM trumpet_listings WHERE id=$1`, sorted[0]).Scan(&lowURL); err != nil {
			t.Fatal(err)
		}
		if err := pool.QueryRow(ctx, `SELECT source,source_listing_id FROM trumpet_listings WHERE id=$1`, sorted[len(sorted)-1]).Scan(&highSource, &highSID); err != nil {
			t.Fatal(err)
		}
		c := perfCandidate(99, 3100)
		c.URL, c.Source, c.SourceListingID = lowURL, highSource, highSID
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		if err = normalizeTrumpet(&c); err != nil {
			t.Fatal(err)
		}
		for round := 0; round < 3; round++ {
			var got, horn uuid.UUID
			var price *float64
			var status, currency, verification, description string
			var acquired bool
			if err = tx.QueryRow(ctx, trumpetIdentityLookup, user, nullText(c.ID), nullText(c.URL), nullText(c.SourceListingID), c.Source, nullText(c.SeedKey)).Scan(&got, &horn, &price, &status, &currency, &acquired, &verification, &description); err != nil {
				t.Fatal(err)
			}
			if got != sorted[0] {
				t.Fatalf("round %d: lookup resolved to %s, want the lowest id %s", round, got, sorted[0])
			}
		}
	})
}
