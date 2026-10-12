-- The daily fix list (jazz-api/fixlist.go). Replay-safe: runs on every
-- jazz-api and workbench startup.

-- API self-observation: per service, route pattern and UTC hour, how many
-- requests were served, how many answered 5xx and how many took 10 s or more.
-- Each instance keeps counts in memory and adds them here about once a minute
-- in one statement. The digest sums the hours of each 24-hour window; a UTC
-- day's rows add up to that day's count. Rows older than 35 days are pruned.
CREATE TABLE IF NOT EXISTS api_route_counters (
  hour timestamptz NOT NULL,
  service text NOT NULL CHECK (char_length(service) BETWEEN 1 AND 64),
  route text NOT NULL CHECK (char_length(route) BETWEEN 1 AND 200),
  requests bigint NOT NULL DEFAULT 0 CHECK (requests >= 0),
  errors_5xx bigint NOT NULL DEFAULT 0 CHECK (errors_5xx >= 0),
  slow bigint NOT NULL DEFAULT 0 CHECK (slow >= 0),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (hour, service, route)
);

-- One digest per owner and UTC day: the claim that makes delivery idempotent
-- (the runner's trigger, the API's fallback and the Workbench sweep all race
-- for the same row), and the record the page shows.
CREATE TABLE IF NOT EXISTS fix_list_digests (
  user_id uuid NOT NULL REFERENCES app_users(id) ON DELETE CASCADE,
  day date NOT NULL,
  trigger text NOT NULL DEFAULT '' CHECK (char_length(trigger) <= 40),
  claimed_at timestamptz NOT NULL DEFAULT now(),
  delivered_at timestamptz,
  verdict text NOT NULL DEFAULT '' CHECK (char_length(verdict) <= 300),
  all_clear boolean NOT NULL DEFAULT false,
  digest jsonb NOT NULL DEFAULT '{}',
  thread_id uuid REFERENCES wb_threads(id) ON DELETE SET NULL,
  pushed int NOT NULL DEFAULT 0,
  -- Delivered by a process without the VAPID keys: the next one with them
  -- (the Workbench sweep) sends the push, the same day only.
  push_pending boolean NOT NULL DEFAULT false,
  PRIMARY KEY (user_id, day)
);

-- The digest reads the last two days of these.
CREATE INDEX IF NOT EXISTS trumpet_runs_user_started_idx ON trumpet_runs (user_id, started_at DESC);
CREATE INDEX IF NOT EXISTS trumpet_listings_check_attempt_idx ON trumpet_listings (user_id, last_check_attempt) WHERE last_check_attempt IS NOT NULL;
