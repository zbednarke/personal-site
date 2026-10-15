-- The owner's optional creative direction for a Magic Film ("quiet and
-- reflective, slow pacing"). Additive and replay-safe: existing films keep an
-- empty direction, which means the editor's default behaviour.
ALTER TABLE magic_film_jobs
    ADD COLUMN IF NOT EXISTS direction text NOT NULL DEFAULT '' CHECK (char_length(direction) <= 1000);
