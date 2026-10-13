-- Restored from the out-of-band Jazz line (deployed Oct 1-5, 2026), where it
-- was migration 024. Production already has these objects, so every
-- statement must stay replay-safe (IF NOT EXISTS, guarded updates).
ALTER TABLE recordings ADD COLUMN IF NOT EXISTS client_capture_payload jsonb;
