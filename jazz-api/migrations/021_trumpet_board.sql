-- Offers remain separate from instruments: serial-confirmed relists share feedback.
CREATE TABLE IF NOT EXISTS trumpet_horns (
 id uuid PRIMARY KEY, user_id uuid NOT NULL REFERENCES app_users(id) ON DELETE CASCADE,
 maker text NOT NULL, model text NOT NULL, serial_number text NOT NULL DEFAULT '',
 identity_key text, details jsonb NOT NULL DEFAULT '{}', acquired boolean NOT NULL DEFAULT false,
 UNIQUE(user_id, identity_key), UNIQUE(id,user_id)
);
CREATE TABLE IF NOT EXISTS trumpet_listings (
 id uuid PRIMARY KEY, user_id uuid NOT NULL REFERENCES app_users(id) ON DELETE CASCADE,
 horn_id uuid NOT NULL, canonical_url text, source text NOT NULL, source_listing_id text,
 seed_key text, title text NOT NULL, description text NOT NULL DEFAULT '',
 seller text NOT NULL DEFAULT '', location text NOT NULL DEFAULT '',
 price numeric(14,2) CHECK(price >= 0), currency text NOT NULL DEFAULT 'USD',
 shipping numeric(14,2) CHECK(shipping >= 0), posted_at timestamptz,
 status text NOT NULL CHECK(status IN ('active','sold','removed','stale','acquired')),
 discovery_type text NOT NULL CHECK(discovery_type IN ('new listing','newly discovered','price drop','status change','rediscovered')),
 search_score numeric NOT NULL DEFAULT 0 CHECK(search_score BETWEEN 0 AND 100),
 search_rationale text NOT NULL DEFAULT '', images jsonb NOT NULL DEFAULT '[]', tags jsonb NOT NULL DEFAULT '[]',
 first_seen timestamptz NOT NULL DEFAULT now(), last_checked timestamptz,
 changed_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(horn_id,user_id) REFERENCES trumpet_horns(id,user_id) ON DELETE CASCADE,
 UNIQUE(user_id,canonical_url), UNIQUE(user_id,source,source_listing_id), UNIQUE(user_id,seed_key), UNIQUE(id,user_id)
);
CREATE TABLE IF NOT EXISTS trumpet_feedback (
 horn_id uuid PRIMARY KEY, user_id uuid NOT NULL,
 rating smallint CHECK(rating BETWEEN 1 AND 5),
 interest_state text NOT NULL DEFAULT 'watch' CHECK(interest_state IN ('pass','watch','interested','contact','buy','acquired')),
 notes text NOT NULL DEFAULT '', favorite boolean NOT NULL DEFAULT false,
 favored_attributes jsonb NOT NULL DEFAULT '[]', disliked_attributes jsonb NOT NULL DEFAULT '[]',
 updated_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(horn_id,user_id) REFERENCES trumpet_horns(id,user_id) ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS trumpet_runs (
 id uuid PRIMARY KEY, user_id uuid NOT NULL REFERENCES app_users(id) ON DELETE CASCADE,
 external_id text NOT NULL, started_at timestamptz NOT NULL DEFAULT now(), completed_at timestamptz,
 status text NOT NULL DEFAULT 'running' CHECK(status IN ('running','succeeded','partial','failed')),
 kind text NOT NULL CHECK(kind IN ('search','recheck','combined')), error text NOT NULL DEFAULT '',
 UNIQUE(user_id,external_id), UNIQUE(id,user_id)
);
CREATE TABLE IF NOT EXISTS trumpet_run_sources (
 run_id uuid NOT NULL REFERENCES trumpet_runs(id) ON DELETE CASCADE,
 source text NOT NULL, status text NOT NULL CHECK(status IN ('checked','failed','skipped')),
 candidates integer NOT NULL DEFAULT 0 CHECK(candidates >= 0), note text NOT NULL DEFAULT '',
 PRIMARY KEY(run_id,source)
);
CREATE TABLE IF NOT EXISTS trumpet_observations (
 id bigserial PRIMARY KEY, listing_id uuid NOT NULL, user_id uuid NOT NULL,
 run_id uuid, checked_at timestamptz NOT NULL DEFAULT now(), price numeric(14,2), currency text NOT NULL,
 shipping numeric(14,2), status text NOT NULL, evidence text NOT NULL DEFAULT '',
 FOREIGN KEY(listing_id,user_id) REFERENCES trumpet_listings(id,user_id) ON DELETE CASCADE,
 FOREIGN KEY(run_id,user_id) REFERENCES trumpet_runs(id,user_id), UNIQUE(listing_id,run_id)
);
CREATE TABLE IF NOT EXISTS trumpet_events (
 id bigserial PRIMARY KEY, listing_id uuid NOT NULL, user_id uuid NOT NULL,
 kind text NOT NULL, occurred_at timestamptz NOT NULL DEFAULT now(),
 old_price numeric(14,2), new_price numeric(14,2), currency text NOT NULL DEFAULT 'USD',
 old_status text, new_status text, meaningful boolean NOT NULL DEFAULT true,
 FOREIGN KEY(listing_id,user_id) REFERENCES trumpet_listings(id,user_id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS trumpet_active_check_idx ON trumpet_listings(user_id,last_checked) WHERE status='active';
CREATE INDEX IF NOT EXISTS trumpet_event_today_idx ON trumpet_events(user_id,occurred_at DESC);
CREATE INDEX IF NOT EXISTS trumpet_observation_listing_idx ON trumpet_observations(listing_id,checked_at DESC);
