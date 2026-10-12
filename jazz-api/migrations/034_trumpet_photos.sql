-- Listing photos. A one-time (and repeatable) image backfill is stored as a
-- run of its own kind, 'images': it carries image updates only and never
-- counts as a search or a recheck. Replay-safe: the constraint is replaced only
-- while it does not yet allow 'images'.
DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint
    WHERE conrelid = 'trumpet_runs'::regclass AND conname = 'trumpet_runs_kind_check'
      AND pg_get_constraintdef(oid) LIKE '%images%'
  ) THEN
    ALTER TABLE trumpet_runs DROP CONSTRAINT IF EXISTS trumpet_runs_kind_check;
    ALTER TABLE trumpet_runs ADD CONSTRAINT trumpet_runs_kind_check
      CHECK (kind IN ('search','recheck','combined','images'));
  END IF;
END $$;

-- Small per-event metadata for taste reactions, e.g. {"hadPhoto": false} when
-- the card showed no photo (the model gives such reactions half weight).
-- Existing events keep '{}' (unknown). Adding a column never fires the
-- append-only UPDATE trigger.
ALTER TABLE trumpet_taste_events ADD COLUMN IF NOT EXISTS meta jsonb NOT NULL DEFAULT '{}'
  CHECK (jsonb_typeof(meta) = 'object' AND length(meta::text) <= 500);
