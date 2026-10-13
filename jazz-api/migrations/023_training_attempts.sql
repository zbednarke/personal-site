CREATE TABLE IF NOT EXISTS training_attempts (
    user_id uuid NOT NULL REFERENCES app_users(id) ON DELETE CASCADE,
    id uuid NOT NULL,
    payload jsonb NOT NULL,
    audio_object text,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY(user_id,id)
);
