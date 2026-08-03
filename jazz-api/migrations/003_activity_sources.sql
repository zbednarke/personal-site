ALTER TABLE practice_activities
    ADD COLUMN IF NOT EXISTS source_id text;

CREATE UNIQUE INDEX IF NOT EXISTS practice_activities_user_source_idx
    ON practice_activities (user_id, source_id)
    WHERE source_id IS NOT NULL;
