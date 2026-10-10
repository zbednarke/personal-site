-- Durable, asynchronous Magic Film jobs. A Cloud Run Job claims one row,
-- processes its frozen candidate snapshot and writes private GCS outputs.
CREATE TABLE IF NOT EXISTS magic_film_jobs (
    id uuid PRIMARY KEY,
    user_id uuid NOT NULL REFERENCES app_users(id) ON DELETE CASCADE,
    client_id uuid NOT NULL,
    practice_date date NOT NULL,
    target_seconds integer NOT NULL CHECK (target_seconds IN (60,120,180,300,600)),
    status text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued','starting','preparing','editing','rendering','complete','failed','cancelled')),
    message text NOT NULL DEFAULT 'Queued' CHECK (char_length(message) <= 1000),
    model text NOT NULL DEFAULT 'gpt-6-astra' CHECK (char_length(model) BETWEEN 1 AND 100),
    effort text NOT NULL DEFAULT 'medium' CHECK (effort IN ('low','medium','high','xhigh')),
    manifest jsonb NOT NULL,
    plan jsonb,
    project jsonb,
    output_object text,
    poster_object text,
    duration_ms integer CHECK (duration_ms IS NULL OR duration_ms >= 0),
    summary text NOT NULL DEFAULT '' CHECK (char_length(summary) <= 4000),
    reviews jsonb NOT NULL DEFAULT '[]',
    error text NOT NULL DEFAULT '' CHECK (char_length(error) <= 4000),
    cancel_requested boolean NOT NULL DEFAULT false,
    invocation_ref text NOT NULL DEFAULT '' CHECK (char_length(invocation_ref) <= 500),
    created_at timestamptz NOT NULL DEFAULT now(),
    started_at timestamptz,
    finished_at timestamptz,
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (user_id, client_id)
);
CREATE INDEX IF NOT EXISTS magic_film_jobs_user_date_idx
    ON magic_film_jobs (user_id, practice_date DESC, created_at DESC);
CREATE UNIQUE INDEX IF NOT EXISTS magic_film_jobs_one_active_idx
    ON magic_film_jobs (user_id) WHERE status IN ('queued','starting','preparing','editing','rendering');
