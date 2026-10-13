ALTER TABLE recordings ADD COLUMN IF NOT EXISTS client_capture_payload jsonb;
