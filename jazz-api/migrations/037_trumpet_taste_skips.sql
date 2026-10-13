-- Head to head "skip": a pair event with action 'skip' (optional reasons in
-- meta.skipReasons, an optional note in note). A skip is not a preference:
-- the model and the comparison graph ignore it, and the skipped pair is not
-- offered again. The daily runner investigates each skip and posts one
-- finding; findings can exclude a listing from Head to head (and Swipe),
-- queue a recheck, or record a comparability lesson. Every action is a
-- stored, reversible flag; nothing here changes a listing's price, title or
-- status. Replay-safe: runs on every jazz-api and workbench startup.

-- The kind/action check of migration 032 was unnamed; it is replaced (once)
-- by a named one that also allows 'skip'.
DO $$
DECLARE c record;
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint
    WHERE conrelid = 'trumpet_taste_events'::regclass AND contype = 'c'
      AND conname = 'trumpet_taste_events_kind_action_check'
  ) THEN
    FOR c IN SELECT conname FROM pg_constraint
      WHERE conrelid = 'trumpet_taste_events'::regclass AND contype = 'c'
        AND pg_get_constraintdef(oid) LIKE '%neither%' LOOP
      EXECUTE format('ALTER TABLE trumpet_taste_events DROP CONSTRAINT %I', c.conname);
    END LOOP;
    ALTER TABLE trumpet_taste_events ADD CONSTRAINT trumpet_taste_events_kind_action_check CHECK (
      (kind = 'swipe' AND listing_id IS NOT NULL AND other_listing_id IS NULL AND action IN ('right','left','up')) OR
      (kind = 'pair' AND listing_id IS NOT NULL AND other_listing_id IS NOT NULL AND listing_id <> other_listing_id AND action IN ('a','b','both','neither','skip')) OR
      (kind = 'tag' AND listing_id IS NOT NULL AND action IN ('add','remove') AND tag <> '') OR
      (kind = 'note' AND listing_id IS NOT NULL AND (note <> '' OR audio_object IS NOT NULL)) OR
      (kind = 'undo' AND undoes IS NOT NULL)
    );
  END IF;
END $$;

-- Live skips waiting for the runner are found through this partial index.
CREATE INDEX IF NOT EXISTS trumpet_taste_events_skips_idx ON trumpet_taste_events(user_id, id)
  WHERE kind = 'pair' AND action = 'skip';

-- One finding per skip event (the runner's investigation). cause is the
-- acted-on cause; llm_cause is what the model said (recorded even when it was
-- not acted on). actions is what the API changed, written by the API.
CREATE TABLE IF NOT EXISTS trumpet_taste_skip_findings (
 id bigserial PRIMARY KEY,
 user_id uuid NOT NULL REFERENCES app_users(id) ON DELETE CASCADE,
 event_id bigint NOT NULL UNIQUE REFERENCES trumpet_taste_events(id) ON DELETE CASCADE,
 cause text NOT NULL CHECK (cause IN ('data_problem','wrong_category','stale','duplicate','incomparable','unclear')),
 dimension text NOT NULL DEFAULT '' CHECK (dimension IN ('','price','condition','era','other')),
 side text NOT NULL DEFAULT '' CHECK (side IN ('','a','b','both')),
 llm_cause text NOT NULL DEFAULT '' CHECK (llm_cause IN ('','data_problem','wrong_category','stale','duplicate','incomparable','unclear')),
 llm_valid boolean NOT NULL DEFAULT false,
 mode text NOT NULL DEFAULT '' CHECK (length(mode) <= 20),
 explanation text NOT NULL DEFAULT '' CHECK (length(explanation) <= 1000),
 checks jsonb NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(checks) = 'array' AND length(checks::text) <= 8000),
 actions jsonb NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(actions) = 'array' AND length(actions::text) <= 4000),
 fix_text text NOT NULL DEFAULT '' CHECK (length(fix_text) <= 500),
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS trumpet_taste_skip_findings_user_idx ON trumpet_taste_skip_findings(user_id, created_at);

-- A listing kept out of Head to head ('pair') or out of both Head to head
-- and Swipe ('all') because a skip investigation found it clearly bad. The
-- owner can turn it off (and on again) from the taste page.
CREATE TABLE IF NOT EXISTS trumpet_taste_exclusions (
 id bigserial PRIMARY KEY,
 user_id uuid NOT NULL REFERENCES app_users(id) ON DELETE CASCADE,
 listing_id uuid NOT NULL,
 finding_id bigint REFERENCES trumpet_taste_skip_findings(id) ON DELETE CASCADE,
 scope text NOT NULL CHECK (scope IN ('pair','all')),
 reason text NOT NULL CHECK (reason IN ('wrong_category','stale','duplicate')),
 detail text NOT NULL DEFAULT '' CHECK (length(detail) <= 500),
 active boolean NOT NULL DEFAULT true,
 created_at timestamptz NOT NULL DEFAULT now(),
 changed_at timestamptz NOT NULL DEFAULT now(),
 changed_by text NOT NULL DEFAULT 'runner' CHECK (changed_by IN ('runner','owner')),
 FOREIGN KEY (listing_id, user_id) REFERENCES trumpet_listings(id, user_id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS trumpet_taste_exclusions_user_idx ON trumpet_taste_exclusions(user_id, listing_id);

-- Comparability lessons from genuinely incomparable skips: the measured gap
-- between the two horns on one dimension (price ratio, condition classes,
-- era classes). Pair selection uses a dimension only once at least two live
-- lessons agree; the owner can switch a lesson off (and on again).
CREATE TABLE IF NOT EXISTS trumpet_taste_lessons (
 id bigserial PRIMARY KEY,
 user_id uuid NOT NULL REFERENCES app_users(id) ON DELETE CASCADE,
 finding_id bigint NOT NULL UNIQUE REFERENCES trumpet_taste_skip_findings(id) ON DELETE CASCADE,
 dimension text NOT NULL CHECK (dimension IN ('price','condition','era')),
 ratio double precision CHECK (ratio IS NULL OR ratio >= 1),
 classes text NOT NULL DEFAULT '' CHECK (length(classes) <= 60),
 active boolean NOT NULL DEFAULT true,
 created_at timestamptz NOT NULL DEFAULT now(),
 changed_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS trumpet_taste_lessons_user_idx ON trumpet_taste_lessons(user_id);

-- A recheck the runner asked for (a skipped listing that looks stale). It
-- makes the listing due until a check after the request lands.
ALTER TABLE trumpet_listings ADD COLUMN IF NOT EXISTS recheck_requested_at timestamptz;
