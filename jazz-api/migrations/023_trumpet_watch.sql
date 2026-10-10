-- Accumulated, owner-private source universe. Search checks and live-page
-- checks are deliberately separate: a successful query is not stock evidence.
CREATE TABLE IF NOT EXISTS trumpet_sources (
  user_id uuid NOT NULL REFERENCES app_users(id) ON DELETE CASCADE,
  domain text NOT NULL,
  name text NOT NULL,
  geography text NOT NULL DEFAULT 'International / unknown',
  specialty text NOT NULL DEFAULT 'Discovered source',
  first_seen timestamptz NOT NULL DEFAULT now(),
  last_searched timestamptz,
  last_live_check timestamptz,
  last_useful timestamptz,
  queries integer NOT NULL DEFAULT 0,
  successful_queries integer NOT NULL DEFAULT 0,
  pages_opened integer NOT NULL DEFAULT 0,
  verified_offers integer NOT NULL DEFAULT 0,
  stale_results integer NOT NULL DEFAULT 0,
  last_status text NOT NULL DEFAULT 'unsearched',
  last_note text NOT NULL DEFAULT '',
  PRIMARY KEY(user_id,domain)
);
ALTER TABLE trumpet_run_sources ADD COLUMN IF NOT EXISTS domain text NOT NULL DEFAULT '';
ALTER TABLE trumpet_run_sources ADD COLUMN IF NOT EXISTS query text NOT NULL DEFAULT '';
ALTER TABLE trumpet_run_sources ADD COLUMN IF NOT EXISTS pages_opened integer NOT NULL DEFAULT 0;
ALTER TABLE trumpet_run_sources ADD COLUMN IF NOT EXISTS verified_offers integer NOT NULL DEFAULT 0;
ALTER TABLE trumpet_run_sources ADD COLUMN IF NOT EXISTS stale_results integer NOT NULL DEFAULT 0;
ALTER TABLE trumpet_observations ADD COLUMN IF NOT EXISTS snapshot jsonb NOT NULL DEFAULT '{}';
ALTER TABLE trumpet_events ADD COLUMN IF NOT EXISTS detail text NOT NULL DEFAULT '';
