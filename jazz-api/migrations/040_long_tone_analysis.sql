-- Restored from the out-of-band Jazz line (deployed Oct 1-5, 2026), where it
-- was migration 022. Production already has these objects, so every
-- statement must stay replay-safe (IF NOT EXISTS, guarded updates).
CREATE TABLE IF NOT EXISTS long_tone_analysis_jobs (
    id uuid PRIMARY KEY,
    recording_id uuid NOT NULL REFERENCES recordings(id) ON DELETE CASCADE,
    user_id uuid NOT NULL REFERENCES app_users(id) ON DELETE CASCADE,
    asset text NOT NULL CHECK (asset IN ('audio','video')),
    source_object_name text NOT NULL,
    source_generation bigint NOT NULL CHECK (source_generation > 0),
    source_checksum text NOT NULL,
    analysis_version text NOT NULL,
    status text NOT NULL CHECK (status IN ('pending','running','ready','failed')),
    duration_ms bigint NOT NULL DEFAULT 0 CHECK (duration_ms BETWEEN 0 AND 14400000),
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts BETWEEN 0 AND 3),
    available_at timestamptz NOT NULL DEFAULT now(),
    lease_token uuid,
    leased_until timestamptz,
    error_code text,
    retryable boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz
);

CREATE UNIQUE INDEX IF NOT EXISTS long_tone_analysis_identity_idx
    ON long_tone_analysis_jobs
    (recording_id, asset, source_object_name, source_generation, source_checksum, analysis_version);

CREATE INDEX IF NOT EXISTS long_tone_analysis_claim_idx
    ON long_tone_analysis_jobs (available_at, created_at)
    WHERE status IN ('pending','running');

CREATE INDEX IF NOT EXISTS long_tone_analysis_owner_idx
    ON long_tone_analysis_jobs (user_id, recording_id, asset, created_at DESC);

CREATE TABLE IF NOT EXISTS long_tone_analysis_chunks (
    analysis_id uuid NOT NULL REFERENCES long_tone_analysis_jobs(id) ON DELETE CASCADE,
    chunk_start_ms bigint NOT NULL CHECK (chunk_start_ms >= 0),
    chunk_end_ms bigint NOT NULL CHECK (chunk_end_ms >= chunk_start_ms),
    frames jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (analysis_id, chunk_start_ms)
);
