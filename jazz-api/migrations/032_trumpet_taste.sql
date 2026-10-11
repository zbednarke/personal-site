-- Taste training (Swipe and Head to head). Every reaction is an append-only
-- event: swipes, pairwise choices, one-tap tags, notes (optionally a private
-- voice recording in the bucket) and undos, which point at the event they
-- cancel. Nothing is updated in place, so the taste model can be refit from
-- the full history at any time. Events reference offers; the model groups them
-- by physical horn. Owner-scoped through the composite listing foreign keys.
-- Replay-safe: runs on every jazz-api and workbench startup.
CREATE TABLE IF NOT EXISTS trumpet_taste_events (
 id bigserial PRIMARY KEY,
 user_id uuid NOT NULL REFERENCES app_users(id) ON DELETE CASCADE,
 client_id text NOT NULL CHECK (length(client_id) BETWEEN 8 AND 64),
 kind text NOT NULL CHECK (kind IN ('swipe','pair','tag','note','undo')),
 mode text NOT NULL DEFAULT '' CHECK (mode IN ('','swipe','pair')),
 listing_id uuid,
 other_listing_id uuid,
 action text NOT NULL DEFAULT '',
 tag text NOT NULL DEFAULT '',
 note text NOT NULL DEFAULT '' CHECK (length(note) <= 4000),
 undoes bigint REFERENCES trumpet_taste_events(id) ON DELETE CASCADE,
 audio_object text,
 audio_content_type text,
 audio_bytes integer,
 audio_duration_ms integer,
 created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE (user_id, client_id),
 FOREIGN KEY (listing_id, user_id) REFERENCES trumpet_listings(id, user_id) ON DELETE CASCADE,
 FOREIGN KEY (other_listing_id, user_id) REFERENCES trumpet_listings(id, user_id) ON DELETE CASCADE,
 CHECK (
  (kind = 'swipe' AND listing_id IS NOT NULL AND other_listing_id IS NULL AND action IN ('right','left','up')) OR
  (kind = 'pair' AND listing_id IS NOT NULL AND other_listing_id IS NOT NULL AND listing_id <> other_listing_id AND action IN ('a','b','both','neither')) OR
  (kind = 'tag' AND listing_id IS NOT NULL AND action IN ('add','remove') AND tag <> '') OR
  (kind = 'note' AND listing_id IS NOT NULL AND (note <> '' OR audio_object IS NOT NULL)) OR
  (kind = 'undo' AND undoes IS NOT NULL)
 )
);
CREATE INDEX IF NOT EXISTS trumpet_taste_events_user_idx ON trumpet_taste_events(user_id, id);
CREATE UNIQUE INDEX IF NOT EXISTS trumpet_taste_events_undo_once ON trumpet_taste_events(undoes) WHERE undoes IS NOT NULL;

-- Owner-editable preference statements for "What I've learned": short lines
-- plus structured liked/disliked makers and traits and an optional price note.
-- Each save is a new version (history kept); the latest row is current. The
-- taste model uses them as its prior and search clients read them.
CREATE TABLE IF NOT EXISTS trumpet_taste_preferences (
 id bigserial PRIMARY KEY,
 user_id uuid NOT NULL REFERENCES app_users(id) ON DELETE CASCADE,
 revision integer NOT NULL CHECK (revision > 0),
 statements jsonb NOT NULL DEFAULT '[]',
 liked_makers jsonb NOT NULL DEFAULT '[]',
 disliked_makers jsonb NOT NULL DEFAULT '[]',
 liked_traits jsonb NOT NULL DEFAULT '[]',
 disliked_traits jsonb NOT NULL DEFAULT '[]',
 price_guidance text NOT NULL DEFAULT '' CHECK (length(price_guidance) <= 1000),
 created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE (user_id, revision)
);

-- Verification provenance per observation: the listing page itself, an
-- official marketplace API (Reverb, eBay) or a source-matched search-provider
-- check. Existing observations were all direct page checks.
ALTER TABLE trumpet_observations ADD COLUMN IF NOT EXISTS verified_via text NOT NULL DEFAULT 'direct'
  CHECK (verified_via IN ('direct','api','search-provider'));

-- Append-only: rows can only be inserted (cascading deletes of an owner or an
-- offer still remove them).
CREATE OR REPLACE FUNCTION trumpet_taste_events_append_only() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION 'trumpet_taste_events is append-only';
END $$;
DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_trigger
    WHERE tgrelid = 'trumpet_taste_events'::regclass AND tgname = 'trumpet_taste_events_no_update'
  ) THEN
    CREATE TRIGGER trumpet_taste_events_no_update BEFORE UPDATE ON trumpet_taste_events
      FOR EACH ROW EXECUTE FUNCTION trumpet_taste_events_append_only();
  END IF;
END $$;
