CREATE TABLE IF NOT EXISTS clip_candidates (
    id UUID PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES app_users(id) ON DELETE CASCADE,
    recording_id UUID NOT NULL REFERENCES recordings(id) ON DELETE CASCADE,
    start_ms INTEGER NOT NULL CHECK (start_ms >= 0),
    end_ms INTEGER NOT NULL CHECK (end_ms > start_ms),
    score REAL NOT NULL CHECK (score BETWEEN 0 AND 1),
    reasons JSONB NOT NULL DEFAULT '[]'::jsonb,
    score_breakdown JSONB NOT NULL DEFAULT '{}'::jsonb,
    source TEXT NOT NULL DEFAULT 'activity-scan' CHECK (source IN ('activity-scan', 'manual', 'marker')),
    review_status TEXT NOT NULL DEFAULT 'suggested' CHECK (review_status IN ('suggested', 'kept', 'rejected')),
    title TEXT,
    notes TEXT,
    analysis_version TEXT NOT NULL DEFAULT 'waveform-v1',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (recording_id, start_ms, end_ms, analysis_version)
);

CREATE INDEX IF NOT EXISTS clip_candidates_user_recording_idx
    ON clip_candidates (user_id, recording_id, score DESC);

CREATE INDEX IF NOT EXISTS clip_candidates_user_status_idx
    ON clip_candidates (user_id, review_status, updated_at DESC);
