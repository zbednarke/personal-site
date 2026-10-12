package main

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// trumpetPipe pipelines a run ingest's writes. On Cloud Run every round trip
// to Cloud SQL costs milliseconds, and an ingest of ~150 listings used to make
// ~1,700 of them (one statement at a time, ~27 s in production). Writes whose
// results are not needed are queued; they are sent together with the next read
// (in order, in the same transaction), so reads always see every earlier write
// and the transaction's semantics are unchanged. Commit sends whatever is left.
type trumpetPipe struct {
	pgx.Tx
	batch *pgx.Batch
	// known is the owner's listings as of the start of the ingest, so rechecks
	// of tracked offers need no lookup query; nil means "query live".
	known *trumpetKnownListings
}

func newTrumpetPipe(tx pgx.Tx) *trumpetPipe {
	return &trumpetPipe{Tx: tx, batch: &pgx.Batch{}}
}

// Queue adds a write whose result is not needed.
func (p *trumpetPipe) Queue(sql string, args ...any) {
	p.batch.Queue(sql, args...)
}

// Flush sends the queued writes (one round trip).
func (p *trumpetPipe) Flush(ctx context.Context) error {
	if p.batch.Len() == 0 {
		return nil
	}
	batch := p.batch
	p.batch = &pgx.Batch{}
	return p.Tx.SendBatch(ctx, batch).Close()
}

// send sends the queued writes followed by one more statement, returning the
// open results positioned at that statement.
func (p *trumpetPipe) send(ctx context.Context, sql string, args ...any) (pgx.BatchResults, error) {
	batch := p.batch
	p.batch = &pgx.Batch{}
	pending := batch.Len()
	batch.Queue(sql, args...)
	br := p.Tx.SendBatch(ctx, batch)
	for i := 0; i < pending; i++ {
		if _, err := br.Exec(); err != nil {
			br.Close()
			return nil, err
		}
	}
	return br, nil
}

func (p *trumpetPipe) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if p.batch.Len() == 0 {
		return p.Tx.Exec(ctx, sql, args...)
	}
	br, err := p.send(ctx, sql, args...)
	if err != nil {
		return pgconn.CommandTag{}, err
	}
	tag, err := br.Exec()
	if cerr := br.Close(); err == nil {
		err = cerr
	}
	return tag, err
}

type trumpetPipeRow struct {
	br  pgx.BatchResults
	err error
}

func (r trumpetPipeRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	err := r.br.QueryRow().Scan(dest...)
	if cerr := r.br.Close(); err == nil {
		err = cerr
	}
	return err
}

// QueryRow sends the queued writes and this read in one round trip.
func (p *trumpetPipe) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	if p.batch.Len() == 0 {
		return p.Tx.QueryRow(ctx, sql, args...)
	}
	br, err := p.send(ctx, sql, args...)
	return trumpetPipeRow{br: br, err: err}
}

func (p *trumpetPipe) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	if err := p.Flush(ctx); err != nil {
		return nil, err
	}
	return p.Tx.Query(ctx, sql, args...)
}

type trumpetFailedBatch struct{ err error }

func (b trumpetFailedBatch) Exec() (pgconn.CommandTag, error) { return pgconn.CommandTag{}, b.err }
func (b trumpetFailedBatch) Query() (pgx.Rows, error)         { return nil, b.err }
func (b trumpetFailedBatch) QueryRow() pgx.Row                { return trumpetPipeRow{err: b.err} }
func (b trumpetFailedBatch) Close() error                     { return b.err }

func (p *trumpetPipe) SendBatch(ctx context.Context, b *pgx.Batch) pgx.BatchResults {
	if err := p.Flush(ctx); err != nil {
		return trumpetFailedBatch{err}
	}
	return p.Tx.SendBatch(ctx, b)
}

func (p *trumpetPipe) CopyFrom(ctx context.Context, table pgx.Identifier, columns []string, src pgx.CopyFromSource) (int64, error) {
	if err := p.Flush(ctx); err != nil {
		return 0, err
	}
	return p.Tx.CopyFrom(ctx, table, columns, src)
}

func (p *trumpetPipe) Commit(ctx context.Context) error {
	if err := p.Flush(ctx); err != nil {
		return err
	}
	return p.Tx.Commit(ctx)
}

// trumpetWrite queues a write on a pipe, or runs it at once on a plain tx.
func trumpetWrite(ctx context.Context, tx pgx.Tx, sql string, args ...any) error {
	if p, ok := tx.(*trumpetPipe); ok {
		p.Queue(sql, args...)
		return nil
	}
	_, err := tx.Exec(ctx, sql, args...)
	return err
}

// ---- The owner's listings, prefetched once per ingest ----------------------

type trumpetKnownListing struct {
	id, horn                           uuid.UUID
	price                              *float64
	status, currency, verification     string
	description                        string
	acquired                           bool
	url, source, sourceListingID, seed string
	firstSeen                          time.Time
	snapshot                           json.RawMessage
}

type trumpetKnownListings struct {
	rows []trumpetKnownListing
	byID map[uuid.UUID]int
	// Identity keys (id, URL, source listing id, seed) of every listing this
	// ingest has already written: a candidate sharing one is looked up live.
	touched map[string]bool
}

// prefetchTrumpets loads every listing of the owner (locked, as the per-row
// lookup would lock it) with its latest observation snapshot.
func (p *trumpetPipe) prefetchTrumpets(ctx context.Context, user uuid.UUID) error {
	rows, err := p.Query(ctx, `SELECT l.id,l.horn_id,l.price,l.status,l.currency,h.acquired,l.verification_state,l.description,
 COALESCE(l.canonical_url,''),l.source,COALESCE(l.source_listing_id,''),COALESCE(l.seed_key,''),l.first_seen,
 (SELECT o.snapshot FROM trumpet_observations o WHERE o.listing_id=l.id ORDER BY o.checked_at DESC,o.id DESC LIMIT 1)
 FROM trumpet_listings l JOIN trumpet_horns h ON h.id=l.horn_id WHERE l.user_id=$1 FOR UPDATE OF l`, user)
	if err != nil {
		return err
	}
	defer rows.Close()
	k := &trumpetKnownListings{byID: map[uuid.UUID]int{}, touched: map[string]bool{}}
	for rows.Next() {
		var r trumpetKnownListing
		if err = rows.Scan(&r.id, &r.horn, &r.price, &r.status, &r.currency, &r.acquired, &r.verification, &r.description,
			&r.url, &r.source, &r.sourceListingID, &r.seed, &r.firstSeen, &r.snapshot); err != nil {
			return err
		}
		k.byID[r.id] = len(k.rows)
		k.rows = append(k.rows, r)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	p.known = k
	return nil
}

func trumpetIdentityKeys(id, url, source, sourceListingID, seed string) []string {
	keys := []string{}
	if u, err := uuid.Parse(id); err == nil {
		id = u.String()
	}
	if id != "" {
		keys = append(keys, "id\x00"+id)
	}
	if url != "" {
		keys = append(keys, "url\x00"+url)
	}
	if sourceListingID != "" {
		keys = append(keys, "sid\x00"+source+"\x00"+sourceListingID)
	}
	if seed != "" {
		keys = append(keys, "seed\x00"+seed)
	}
	return keys
}

// lookup resolves a candidate exactly as upsertTrumpet's lookup query would.
// ok=false means the answer is not certain from the prefetch (no prefetch, an
// identity this ingest already wrote, or a first_seen tie): query live.
func (k *trumpetKnownListings) lookup(c trumpetCandidate) (row *trumpetKnownListing, ok bool) {
	if k == nil {
		return nil, false
	}
	for _, key := range trumpetIdentityKeys(c.ID, c.URL, c.Source, c.SourceListingID, c.SeedKey) {
		if k.touched[key] {
			return nil, false
		}
	}
	if c.ID != "" {
		id, err := uuid.Parse(c.ID)
		if err != nil {
			return nil, false
		}
		if i, found := k.byID[id]; found {
			return &k.rows[i], true
		}
		return nil, true
	}
	var best *trumpetKnownListing
	tie := false
	for i := range k.rows {
		r := &k.rows[i]
		if !((c.URL != "" && r.url == c.URL) || (c.SourceListingID != "" && r.source == c.Source && r.sourceListingID == c.SourceListingID) || (c.SeedKey != "" && r.seed == c.SeedKey)) {
			continue
		}
		if k.touched["id\x00"+r.id.String()] {
			return nil, false // this ingest already changed that listing
		}
		switch {
		case best == nil || r.firstSeen.Before(best.firstSeen):
			best, tie = r, false
		case r.firstSeen.Equal(best.firstSeen):
			tie = true
		}
	}
	if tie {
		return nil, false
	}
	return best, true
}

// touch records that this ingest wrote a listing with these identities.
func (k *trumpetKnownListings) touch(c trumpetCandidate, row *trumpetKnownListing, id uuid.UUID) {
	if k == nil {
		return
	}
	if id != uuid.Nil {
		k.touched["id\x00"+id.String()] = true
	}
	for _, key := range trumpetIdentityKeys(c.ID, c.URL, c.Source, c.SourceListingID, c.SeedKey) {
		k.touched[key] = true
	}
	if row != nil {
		for _, key := range trumpetIdentityKeys(row.id.String(), row.url, row.source, row.sourceListingID, row.seed) {
			k.touched[key] = true
		}
	}
}
