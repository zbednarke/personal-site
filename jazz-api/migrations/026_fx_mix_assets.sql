-- Optional processed "FX mix" WAV recorded through the browser effects chain.
-- It is a companion to the dry master and never gates a take's readiness:
-- clients see it only once fx_uploaded_at records a verified upload.
ALTER TABLE recordings
    ADD COLUMN IF NOT EXISTS fx_object_name TEXT,
    ADD COLUMN IF NOT EXISTS fx_content_type TEXT,
    ADD COLUMN IF NOT EXISTS fx_expected_size_bytes BIGINT,
    ADD COLUMN IF NOT EXISTS fx_size_bytes BIGINT,
    ADD COLUMN IF NOT EXISTS fx_object_generation BIGINT,
    ADD COLUMN IF NOT EXISTS fx_checksum TEXT,
    ADD COLUMN IF NOT EXISTS fx_uploaded_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS fx_preset TEXT;

CREATE UNIQUE INDEX IF NOT EXISTS recordings_fx_object_name_idx
    ON recordings (fx_object_name)
    WHERE fx_object_name IS NOT NULL;

-- 012 creates recording_shares with an audio/video-only check. Widen it once;
-- later startups find the fx-aware definition and leave the table alone.
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'recording_shares'::regclass
          AND conname = 'recording_shares_asset_check'
          AND pg_get_constraintdef(oid) LIKE '%fx%'
    ) THEN
        ALTER TABLE recording_shares DROP CONSTRAINT IF EXISTS recording_shares_asset_check;
        ALTER TABLE recording_shares
            ADD CONSTRAINT recording_shares_asset_check CHECK (asset IN ('audio', 'video', 'fx'));
    END IF;
END
$$;
