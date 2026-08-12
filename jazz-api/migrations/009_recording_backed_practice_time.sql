-- Recording duration is durable practice evidence. Repair undercounted block
-- timers without erasing extra time retained from intentionally cancelled
-- takes, then mark every block whose goal has been met as complete. Twenty
-- four-hour takes can legitimately contribute to one section, so align the
-- timer constraint with the recording policy before reconciling values.
ALTER TABLE practice_blocks
    DROP CONSTRAINT IF EXISTS practice_blocks_elapsed_ms_check;

ALTER TABLE practice_blocks
    ADD CONSTRAINT practice_blocks_elapsed_ms_check
    CHECK (elapsed_ms BETWEEN 0 AND 288000000);

WITH recorded AS (
    SELECT practice_block_id, LEAST(288000000, SUM(duration_ms))::integer AS duration_ms
    FROM recordings
    WHERE practice_block_id IS NOT NULL
      AND status IN ('uploading','ready')
    GROUP BY practice_block_id
)
UPDATE practice_blocks AS block
SET elapsed_ms = GREATEST(block.elapsed_ms, recorded.duration_ms),
    status = CASE
        WHEN GREATEST(block.elapsed_ms, recorded.duration_ms) >= block.target_minutes * 60000 THEN 'completed'
        ELSE block.status
    END,
    timer_started_at = CASE
        WHEN GREATEST(block.elapsed_ms, recorded.duration_ms) >= block.target_minutes * 60000 THEN NULL
        ELSE block.timer_started_at
    END,
    completed_at = CASE
        WHEN GREATEST(block.elapsed_ms, recorded.duration_ms) >= block.target_minutes * 60000
            THEN COALESCE(block.completed_at, block.updated_at)
        ELSE block.completed_at
    END,
    updated_at = now()
FROM recorded
WHERE block.id = recorded.practice_block_id
  AND block.elapsed_ms < recorded.duration_ms;
