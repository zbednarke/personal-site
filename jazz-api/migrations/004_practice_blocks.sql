CREATE TABLE IF NOT EXISTS practice_blocks (
    id uuid PRIMARY KEY,
    session_id uuid NOT NULL REFERENCES practice_sessions(id) ON DELETE CASCADE,
    user_id uuid NOT NULL REFERENCES app_users(id) ON DELETE CASCADE,
    practice_date date NOT NULL,
    block_key text NOT NULL CHECK (char_length(block_key) BETWEEN 1 AND 80),
    position integer NOT NULL CHECK (position BETWEEN 0 AND 99),
    title text NOT NULL CHECK (char_length(title) BETWEEN 1 AND 160),
    instructions text,
    category text NOT NULL CHECK (char_length(category) BETWEEN 1 AND 40),
    track text NOT NULL CHECK (char_length(track) BETWEEN 1 AND 30),
    target_minutes integer NOT NULL CHECK (target_minutes BETWEEN 1 AND 360),
    notes text,
    elapsed_ms integer NOT NULL DEFAULT 0 CHECK (elapsed_ms BETWEEN 0 AND 21600000),
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'running', 'paused', 'completed')),
    timer_started_at timestamptz,
    completed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (session_id, practice_date, block_key)
);

CREATE INDEX IF NOT EXISTS practice_blocks_session_position_idx
    ON practice_blocks (session_id, practice_date DESC, position);

ALTER TABLE recordings
    ADD COLUMN IF NOT EXISTS practice_block_id uuid REFERENCES practice_blocks(id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS recordings_practice_block_idx
    ON recordings (practice_block_id, recorded_at DESC)
    WHERE status <> 'deleted';
