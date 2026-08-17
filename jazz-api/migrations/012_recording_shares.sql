CREATE TABLE IF NOT EXISTS recording_shares (
    id UUID PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES app_users(id),
    recording_id UUID NOT NULL REFERENCES recordings(id),
    asset TEXT NOT NULL CHECK (asset IN ('audio', 'video')),
    token TEXT NOT NULL UNIQUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    revoked_at TIMESTAMPTZ
);

CREATE UNIQUE INDEX IF NOT EXISTS recording_shares_active_asset_idx
    ON recording_shares (recording_id, asset)
    WHERE revoked_at IS NULL;

CREATE INDEX IF NOT EXISTS recording_shares_user_created_idx
    ON recording_shares (user_id, created_at DESC);
