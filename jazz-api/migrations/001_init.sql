CREATE TABLE IF NOT EXISTS app_users (
    id uuid PRIMARY KEY,
    auth_subject text NOT NULL UNIQUE,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS campaign_state (
    user_id uuid PRIMARY KEY REFERENCES app_users(id) ON DELETE CASCADE,
    data jsonb NOT NULL,
    revision bigint NOT NULL DEFAULT 0 CHECK (revision >= 0),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS progress_events (
    id bigserial PRIMARY KEY,
    user_id uuid NOT NULL REFERENCES app_users(id) ON DELETE CASCADE,
    client_mutation_id uuid NOT NULL,
    event_type text NOT NULL CHECK (char_length(event_type) BETWEEN 1 AND 80),
    payload jsonb NOT NULL,
    occurred_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (user_id, client_mutation_id)
);

CREATE INDEX IF NOT EXISTS progress_events_user_time_idx
    ON progress_events (user_id, occurred_at DESC);

CREATE TABLE IF NOT EXISTS recordings (
    id uuid PRIMARY KEY,
    user_id uuid NOT NULL REFERENCES app_users(id) ON DELETE CASCADE,
    practice_session_id text,
    bucket text NOT NULL,
    object_name text NOT NULL UNIQUE,
    object_generation bigint,
    content_type text NOT NULL,
    codec text,
    expected_size_bytes bigint NOT NULL CHECK (expected_size_bytes > 0),
    size_bytes bigint,
    duration_ms integer CHECK (duration_ms IS NULL OR duration_ms BETWEEN 0 AND 1800000),
    sample_rate integer,
    channels smallint,
    recorded_at timestamptz NOT NULL,
    uploaded_at timestamptz,
    status text NOT NULL CHECK (status IN ('uploading', 'ready', 'failed', 'deleted')),
    tune_id text,
    mission_id text,
    skill_ids jsonb NOT NULL DEFAULT '[]'::jsonb,
    take_number smallint,
    notes text,
    checksum text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS recordings_user_recorded_idx
    ON recordings (user_id, recorded_at DESC)
    WHERE status <> 'deleted';
