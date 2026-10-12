-- Verification provenance 'page-llm': the runner fetched the listing page
-- itself and a model read it; the offer is kept only when its quoted price
-- text, price and currency are found on that page. Replay-safe: the
-- constraint is replaced only while it does not yet allow 'page-llm'. Older
-- runners never send it.
DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint
    WHERE conrelid = 'trumpet_observations'::regclass AND conname = 'trumpet_observations_verified_via_check'
      AND pg_get_constraintdef(oid) LIKE '%page-llm%'
  ) THEN
    ALTER TABLE trumpet_observations DROP CONSTRAINT IF EXISTS trumpet_observations_verified_via_check;
    ALTER TABLE trumpet_observations ADD CONSTRAINT trumpet_observations_verified_via_check
      CHECK (verified_via IN ('direct','api','search-provider','page-llm'));
  END IF;
END $$;
