CREATE TABLE IF NOT EXISTS guide_tone_drills (
    id UUID PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES app_users(id) ON DELETE CASCADE,
    practice_session_id UUID REFERENCES practice_sessions(id) ON DELETE SET NULL,
    practice_block_id UUID REFERENCES practice_blocks(id) ON DELETE SET NULL,
    tune_id TEXT NOT NULL CHECK (char_length(tune_id) BETWEEN 1 AND 80),
    instrument TEXT NOT NULL CHECK (instrument IN ('bb-trumpet', 'concert')),
    mode TEXT NOT NULL CHECK (mode IN ('learn', 'tempo')),
    tempo INTEGER NOT NULL CHECK (tempo BETWEEN 40 AND 240),
    elapsed_ms INTEGER NOT NULL DEFAULT 0 CHECK (elapsed_ms BETWEEN 0 AND 14400000),
    started_at TIMESTAMPTZ NOT NULL,
    ended_at TIMESTAMPTZ,
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'completed')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS guide_tone_drills_user_started_idx
    ON guide_tone_drills (user_id, started_at DESC);

CREATE INDEX IF NOT EXISTS guide_tone_drills_session_idx
    ON guide_tone_drills (practice_session_id, started_at);

CREATE INDEX IF NOT EXISTS guide_tone_drills_block_idx
    ON guide_tone_drills (practice_block_id, started_at);

CREATE TABLE IF NOT EXISTS guide_tone_attempts (
    id BIGSERIAL PRIMARY KEY,
    drill_id UUID NOT NULL REFERENCES guide_tone_drills(id) ON DELETE CASCADE,
    user_id UUID NOT NULL REFERENCES app_users(id) ON DELETE CASCADE,
    measure_number SMALLINT NOT NULL CHECK (measure_number BETWEEN 1 AND 128),
    chord_index SMALLINT NOT NULL CHECK (chord_index BETWEEN 0 AND 8),
    chord_symbol TEXT NOT NULL CHECK (char_length(chord_symbol) BETWEEN 1 AND 40),
    target_degree SMALLINT NOT NULL CHECK (target_degree IN (3, 7)),
    expected_pitch_class SMALLINT NOT NULL CHECK (expected_pitch_class BETWEEN 0 AND 11),
    played_midi SMALLINT CHECK (played_midi BETWEEN 0 AND 127),
    played_pitch_class SMALLINT CHECK (played_pitch_class BETWEEN 0 AND 11),
    cents REAL CHECK (cents IS NULL OR cents BETWEEN -100 AND 100),
    correct BOOLEAN NOT NULL,
    response_ms INTEGER NOT NULL CHECK (response_ms BETWEEN 0 AND 120000),
    occurred_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (
        (played_midi IS NULL AND played_pitch_class IS NULL AND cents IS NULL AND NOT correct)
        OR
        (played_midi IS NOT NULL AND played_pitch_class IS NOT NULL AND correct = (played_pitch_class = expected_pitch_class))
    )
);

CREATE INDEX IF NOT EXISTS guide_tone_attempts_user_tune_idx
    ON guide_tone_attempts (user_id, occurred_at DESC);

CREATE INDEX IF NOT EXISTS guide_tone_attempts_drill_idx
    ON guide_tone_attempts (drill_id, occurred_at);
