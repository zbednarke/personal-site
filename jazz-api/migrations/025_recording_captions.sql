CREATE TABLE IF NOT EXISTS recording_caption_jobs (
    id uuid PRIMARY KEY,
    recording_id uuid NOT NULL REFERENCES recordings(id) ON DELETE CASCADE,
    user_id uuid NOT NULL REFERENCES app_users(id) ON DELETE CASCADE,
    source_object_name text NOT NULL,
    source_generation bigint NOT NULL CHECK (source_generation > 0),
    source_checksum text NOT NULL,
    analysis_version text NOT NULL,
    status text NOT NULL CHECK (status IN ('pending','running','ready','failed')),
    operation_name text,
    segments jsonb NOT NULL DEFAULT '[]'::jsonb,
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

CREATE UNIQUE INDEX IF NOT EXISTS recording_caption_identity_idx
    ON recording_caption_jobs
    (recording_id, source_object_name, source_generation, source_checksum, analysis_version);

CREATE INDEX IF NOT EXISTS recording_caption_claim_idx
    ON recording_caption_jobs (available_at, created_at)
    WHERE status IN ('pending','running');

CREATE INDEX IF NOT EXISTS recording_caption_owner_idx
    ON recording_caption_jobs (user_id, recording_id, created_at DESC);
