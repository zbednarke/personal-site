-- A recording may be refiled under another section without changing where
-- its practice time was originally earned. Keeping the timing attribution
-- immutable prevents a move from double-counting a source timer and the same
-- recording under its destination section.
ALTER TABLE recordings
    ADD COLUMN IF NOT EXISTS practice_time_block_id uuid REFERENCES practice_blocks(id) ON DELETE SET NULL,
    ADD COLUMN IF NOT EXISTS section_assignment_revision bigint NOT NULL DEFAULT 0 CHECK (section_assignment_revision >= 0);

UPDATE recordings
SET practice_time_block_id = practice_block_id
WHERE practice_time_block_id IS NULL AND practice_block_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS recordings_practice_time_block_idx
    ON recordings (practice_time_block_id, recorded_at DESC)
    WHERE status IN ('uploading','ready');

ALTER TABLE clip_day_analyses
    ADD COLUMN IF NOT EXISTS outdated_at timestamptz,
    ADD COLUMN IF NOT EXISTS outdated_reason text;
