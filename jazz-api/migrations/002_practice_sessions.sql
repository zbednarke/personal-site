CREATE TABLE IF NOT EXISTS practice_sessions (
    id uuid PRIMARY KEY,
    user_id uuid NOT NULL REFERENCES app_users(id) ON DELETE CASCADE,
    title text NOT NULL CHECK (char_length(title) BETWEEN 1 AND 120),
    summary text,
    started_at timestamptz NOT NULL,
    ended_at timestamptz,
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'completed')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS practice_sessions_user_started_idx
    ON practice_sessions (user_id, started_at DESC);

CREATE UNIQUE INDEX IF NOT EXISTS practice_sessions_one_active_idx
    ON practice_sessions (user_id)
    WHERE status = 'active';

CREATE TABLE IF NOT EXISTS practice_activities (
    id uuid PRIMARY KEY,
    session_id uuid NOT NULL REFERENCES practice_sessions(id) ON DELETE CASCADE,
    user_id uuid NOT NULL REFERENCES app_users(id) ON DELETE CASCADE,
    category text NOT NULL CHECK (char_length(category) BETWEEN 1 AND 40),
    title text NOT NULL CHECK (char_length(title) BETWEEN 1 AND 160),
    duration_minutes integer NOT NULL CHECK (duration_minutes BETWEEN 1 AND 360),
    notes text,
    occurred_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS practice_activities_session_time_idx
    ON practice_activities (session_id, occurred_at, created_at);

ALTER TABLE recordings
    ADD COLUMN IF NOT EXISTS practice_activity_id uuid REFERENCES practice_activities(id) ON DELETE SET NULL;

ALTER TABLE recordings
    DROP CONSTRAINT IF EXISTS recordings_duration_ms_check;

ALTER TABLE recordings
    ADD CONSTRAINT recordings_duration_ms_check
    CHECK (duration_ms IS NULL OR duration_ms BETWEEN 0 AND 3600000);
