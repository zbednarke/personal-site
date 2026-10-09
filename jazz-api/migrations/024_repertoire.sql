-- Repertoire tracker. Replayed on every startup, so every statement is
-- idempotent and the one-time backfill is guarded by a marker row.
CREATE TABLE IF NOT EXISTS repertoire_tunes (
    user_id uuid NOT NULL REFERENCES app_users(id) ON DELETE CASCADE,
    tune_id text NOT NULL CHECK (tune_id ~ '^[a-z0-9][a-z0-9-]{0,47}$'),
    title text NOT NULL CHECK (char_length(title) BETWEEN 1 AND 160),
    category text NOT NULL CHECK (category IN ('ballad','upbeat','pop','standard')),
    chosen boolean NOT NULL DEFAULT true,
    position integer NOT NULL DEFAULT 0 CHECK (position BETWEEN 0 AND 999),
    reference_artist text CHECK (char_length(reference_artist) <= 160),
    reference_title text CHECK (char_length(reference_title) <= 160),
    reference_url text CHECK (reference_url IS NULL OR (char_length(reference_url) <= 500 AND reference_url ~ '^https://')),
    concert_key text CHECK (char_length(concert_key) <= 12),
    keys_known text[] NOT NULL DEFAULT '{}' CHECK (cardinality(keys_known) <= 12),
    melody_by_ear text NOT NULL DEFAULT 'not_started' CHECK (melody_by_ear IN ('not_started','learning','solid')),
    lyrics text NOT NULL DEFAULT 'not_started' CHECK (lyrics IN ('not_started','learning','solid')),
    changes text NOT NULL DEFAULT 'not_started' CHECK (changes IN ('not_started','learning','solid')),
    transcription text NOT NULL DEFAULT 'not_started' CHECK (transcription IN ('not_started','learning','solid')),
    improvise text NOT NULL DEFAULT 'not_started' CHECK (improvise IN ('not_started','learning','solid')),
    gig_ready text NOT NULL DEFAULT 'not_started' CHECK (gig_ready IN ('not_started','learning','solid')),
    notes text CHECK (char_length(notes) <= 8000),
    legacy_stage smallint CHECK (legacy_stage BETWEEN 0 AND 6),
    revision bigint NOT NULL DEFAULT 1,
    archived_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, tune_id)
);

-- Append-only history of repertoire changes (mirrors progress_events).
CREATE TABLE IF NOT EXISTS repertoire_events (
    id bigserial PRIMARY KEY,
    user_id uuid NOT NULL REFERENCES app_users(id) ON DELETE CASCADE,
    tune_id text NOT NULL,
    client_mutation_id uuid,
    event_type text NOT NULL CHECK (char_length(event_type) BETWEEN 1 AND 80),
    payload jsonb NOT NULL,
    practice_block_id uuid REFERENCES practice_blocks(id) ON DELETE SET NULL,
    occurred_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (user_id, client_mutation_id)
);
CREATE INDEX IF NOT EXISTS repertoire_events_tune_idx ON repertoire_events (user_id, tune_id, occurred_at DESC);

-- One row per user marks the one-time seed and legacy import as done.
CREATE TABLE IF NOT EXISTS repertoire_settings (
    user_id uuid PRIMARY KEY REFERENCES app_users(id) ON DELETE CASCADE,
    seeded_at timestamptz NOT NULL DEFAULT now(),
    set_target_date date NOT NULL DEFAULT DATE '2027-03-20'
);

-- The practice link: a soft text link like recordings.tune_id and
-- guide_tone_drills.tune_id. Writes are validated in Go.
ALTER TABLE practice_blocks ADD COLUMN IF NOT EXISTS tune_id text;
CREATE INDEX IF NOT EXISTS practice_blocks_user_tune_idx
    ON practice_blocks (user_id, tune_id, practice_date DESC) WHERE tune_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS recordings_user_tune_idx
    ON recordings (user_id, tune_id, recorded_at DESC) WHERE tune_id IS NOT NULL AND status <> 'deleted';

-- The marker keeps a later manual unlink from being undone on restart.
CREATE TABLE IF NOT EXISTS jazz_backfills (name text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now());
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM jazz_backfills WHERE name = '024_practice_block_tunes') THEN
    -- Curriculum Blue Bossa blocks (data.js sessions with tuneId "blue-bossa").
    UPDATE practice_blocks SET tune_id = 'blue-bossa' WHERE tune_id IS NULL AND block_key LIKE 'blue-bossa-%';
    -- Any block whose takes were tagged with a tune.
    UPDATE practice_blocks b SET tune_id = r.tune_id
      FROM (SELECT DISTINCT ON (practice_block_id) practice_block_id, tune_id FROM recordings
            WHERE practice_block_id IS NOT NULL AND COALESCE(tune_id,'') <> '' AND status <> 'deleted'
            ORDER BY practice_block_id, recorded_at) r
     WHERE b.id = r.practice_block_id AND b.tune_id IS NULL;
    -- September 10 transcription block ("Prelude to a Kiss: transcribe and play").
    UPDATE practice_blocks SET tune_id = 'prelude-to-a-kiss'
     WHERE tune_id IS NULL AND block_key = 'easy-to-love-transcription-2026-09-10';
    INSERT INTO jazz_backfills(name) VALUES ('024_practice_block_tunes');
  END IF;
END $$;
