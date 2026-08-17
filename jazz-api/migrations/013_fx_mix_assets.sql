ALTER TABLE recordings
    ADD COLUMN IF NOT EXISTS fx_object_name TEXT,
    ADD COLUMN IF NOT EXISTS fx_content_type TEXT,
    ADD COLUMN IF NOT EXISTS fx_expected_size_bytes BIGINT,
    ADD COLUMN IF NOT EXISTS fx_size_bytes BIGINT,
    ADD COLUMN IF NOT EXISTS fx_object_generation BIGINT,
    ADD COLUMN IF NOT EXISTS fx_checksum TEXT,
    ADD COLUMN IF NOT EXISTS fx_uploaded_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS fx_preset TEXT;

ALTER TABLE recording_shares DROP CONSTRAINT IF EXISTS recording_shares_asset_check;
ALTER TABLE recording_shares
    ADD CONSTRAINT recording_shares_asset_check CHECK (asset IN ('audio', 'video', 'fx'));
