-- Read-only audit: sections that may have been credited twice for a moved take.
--
-- Between October 11 and the restore of the out-of-band Jazz line, main's
-- original migration 009 replayed on every API start and set each section's
-- elapsed_ms to at least the total of the takes FILED in it. A take moved with
-- "Change section" on the out-of-band line (Oct 1-11) stays credited to the
-- section it was RECORDED in (practice_time_block_id), so that replay could
-- also credit its time to the section it was moved into.
--
-- A section is listed when some take filed there was recorded elsewhere, its
-- elapsed_ms equals LEAST(288000000, total filed there) (what the replay
-- would have written), and that filed total is larger than the time actually
-- recorded there. That is a strong sign of the double credit, not proof: a
-- timer can legitimately reach the same number. Nothing is changed.
--
--   psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -f jazz-api/audit/moved_take_double_credit.sql
BEGIN TRANSACTION READ ONLY;

WITH filed AS (
    SELECT practice_block_id AS block_id, SUM(duration_ms)::bigint AS ms, COUNT(*) AS takes,
           COUNT(*) FILTER (WHERE practice_time_block_id IS NOT NULL AND practice_time_block_id <> practice_block_id) AS moved_in
    FROM recordings
    WHERE practice_block_id IS NOT NULL AND status IN ('uploading','ready')
    GROUP BY practice_block_id
),
recorded AS (
    SELECT COALESCE(practice_time_block_id, practice_block_id) AS block_id, SUM(duration_ms)::bigint AS ms
    FROM recordings
    WHERE COALESCE(practice_time_block_id, practice_block_id) IS NOT NULL AND status IN ('uploading','ready')
    GROUP BY COALESCE(practice_time_block_id, practice_block_id)
)
SELECT pb.practice_date,
       pb.id AS block_id,
       pb.title,
       pb.elapsed_ms,
       f.ms AS filed_ms,
       COALESCE(r.ms, 0) AS recorded_here_ms,
       f.moved_in AS takes_moved_in,
       f.ms - COALESCE(r.ms, 0) AS possible_excess_ms
FROM practice_blocks pb
JOIN filed f ON f.block_id = pb.id
LEFT JOIN recorded r ON r.block_id = pb.id
WHERE f.moved_in > 0
  AND pb.elapsed_ms = LEAST(288000000, f.ms)
  AND f.ms > COALESCE(r.ms, 0)
ORDER BY pb.practice_date, pb.position;

ROLLBACK;
