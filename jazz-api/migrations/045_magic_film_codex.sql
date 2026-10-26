-- Private, serialized subscription worker. Existing API jobs keep their provider.
ALTER TABLE magic_film_jobs ADD COLUMN IF NOT EXISTS provider text NOT NULL DEFAULT 'api' CHECK (provider IN ('api','codex'));
ALTER TABLE magic_film_jobs ADD COLUMN IF NOT EXISTS next_attempt_at timestamptz;
ALTER TABLE magic_film_jobs DROP CONSTRAINT IF EXISTS magic_film_jobs_status_check;
ALTER TABLE magic_film_jobs ADD CONSTRAINT magic_film_jobs_status_check CHECK (status IN
 ('queued','starting','preparing','editing','rendering','waiting_usage','sign_in_required','complete','failed','cancelled'));
DROP INDEX IF EXISTS magic_film_jobs_one_active_idx;
CREATE UNIQUE INDEX magic_film_jobs_one_active_idx ON magic_film_jobs(user_id)
 WHERE status IN ('queued','starting','preparing','editing','rendering','waiting_usage','sign_in_required');
