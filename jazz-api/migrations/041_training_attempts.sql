-- Restored from the out-of-band Jazz line (deployed Oct 1-5, 2026), where it
-- was migration 023. Production already has these objects, so every
-- statement must stay replay-safe (IF NOT EXISTS, guarded updates).
CREATE TABLE IF NOT EXISTS training_attempts (
    user_id uuid NOT NULL REFERENCES app_users(id) ON DELETE CASCADE,
    id uuid NOT NULL,
    payload jsonb NOT NULL,
    audio_object text,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY(user_id,id)
);
