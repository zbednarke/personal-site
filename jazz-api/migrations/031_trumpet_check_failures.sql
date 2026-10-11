-- Failed daily rechecks are recorded per listing instead of pinning every run
-- to "partial". A listing that fails on several consecutive UTC days becomes
-- 'unverifiable' (server-assigned only): prior facts stay, it is never sold.
-- last_check_attempt is the latest failed attempt (NULL after a success).
-- Replay-safe: runs on every jazz-api and workbench startup.
ALTER TABLE trumpet_listings ADD COLUMN IF NOT EXISTS check_failures integer NOT NULL DEFAULT 0;
ALTER TABLE trumpet_listings ADD COLUMN IF NOT EXISTS last_check_attempt timestamptz;
ALTER TABLE trumpet_listings ADD COLUMN IF NOT EXISTS last_check_error text NOT NULL DEFAULT '';
DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint
    WHERE conrelid = 'trumpet_listings'::regclass
      AND conname = 'trumpet_listings_status_check'
      AND pg_get_constraintdef(oid) LIKE '%unverifiable%'
  ) THEN
    ALTER TABLE trumpet_listings DROP CONSTRAINT IF EXISTS trumpet_listings_status_check;
    ALTER TABLE trumpet_listings ADD CONSTRAINT trumpet_listings_status_check
      CHECK (status IN ('active','sold','removed','stale','acquired','unverifiable'));
  END IF;
END $$;
