CREATE INDEX IF NOT EXISTS practice_blocks_user_date_position_idx
    ON practice_blocks (user_id, practice_date DESC, position);

CREATE INDEX IF NOT EXISTS recordings_user_block_recorded_idx
    ON recordings (user_id, practice_block_id, recorded_at DESC)
    WHERE status <> 'deleted';

CREATE INDEX IF NOT EXISTS practice_activities_user_occurred_idx
    ON practice_activities (user_id, occurred_at DESC);
