-- Practice blocks are daily, but the first version allowed one active session
-- to accumulate blocks over several dates. Split those legacy containers into
-- daily sessions while preserving the original day's title and notes.
DO $$
DECLARE
    legacy RECORD;
    practice_day RECORD;
    daily_session_id uuid;
    daily_started_at timestamptz;
    daily_ended_at timestamptz;
BEGIN
    FOR legacy IN
        SELECT session.id,
               session.user_id,
               session.status,
               MIN(block.practice_date) AS first_date,
               MAX(block.practice_date) AS last_date
        FROM practice_sessions AS session
        JOIN practice_blocks AS block ON block.session_id = session.id
        GROUP BY session.id
        HAVING COUNT(DISTINCT block.practice_date) > 1
    LOOP
        SELECT COALESCE(MAX(recording.recorded_at), legacy.first_date::timestamptz + interval '23 hours 59 minutes')
        INTO daily_ended_at
        FROM practice_blocks AS block
        LEFT JOIN recordings AS recording ON recording.practice_block_id = block.id AND recording.status <> 'deleted'
        WHERE block.session_id = legacy.id
          AND block.practice_date = legacy.first_date;

        UPDATE practice_sessions
        SET status = 'completed',
            ended_at = COALESCE(ended_at, daily_ended_at),
            updated_at = now()
        WHERE id = legacy.id;

        FOR practice_day IN
            SELECT DISTINCT block.practice_date
            FROM practice_blocks AS block
            WHERE block.session_id = legacy.id
              AND block.practice_date > legacy.first_date
            ORDER BY block.practice_date
        LOOP
            daily_session_id := (
                substr(md5(legacy.id::text || ':' || practice_day.practice_date::text), 1, 8) || '-' ||
                substr(md5(legacy.id::text || ':' || practice_day.practice_date::text), 9, 4) || '-' ||
                substr(md5(legacy.id::text || ':' || practice_day.practice_date::text), 13, 4) || '-' ||
                substr(md5(legacy.id::text || ':' || practice_day.practice_date::text), 17, 4) || '-' ||
                substr(md5(legacy.id::text || ':' || practice_day.practice_date::text), 21, 12)
            )::uuid;

            SELECT COALESCE(MIN(recording.recorded_at), practice_day.practice_date::timestamptz + interval '12 hours'),
                   COALESCE(MAX(recording.recorded_at), practice_day.practice_date::timestamptz + interval '23 hours 59 minutes')
            INTO daily_started_at, daily_ended_at
            FROM practice_blocks AS block
            LEFT JOIN recordings AS recording ON recording.practice_block_id = block.id AND recording.status <> 'deleted'
            WHERE block.session_id = legacy.id
              AND block.practice_date = practice_day.practice_date;

            INSERT INTO practice_sessions (id, user_id, title, summary, started_at, ended_at, status)
            VALUES (
                daily_session_id,
                legacy.user_id,
                'Practice - ' || to_char(practice_day.practice_date, 'Mon FMDD'),
                NULL,
                daily_started_at,
                CASE WHEN legacy.status = 'active' AND practice_day.practice_date = legacy.last_date THEN NULL ELSE daily_ended_at END,
                CASE WHEN legacy.status = 'active' AND practice_day.practice_date = legacy.last_date THEN 'active' ELSE 'completed' END
            )
            ON CONFLICT (id) DO NOTHING;

            UPDATE practice_activities
            SET session_id = daily_session_id
            WHERE session_id = legacy.id
              AND (occurred_at AT TIME ZONE 'America/Los_Angeles')::date = practice_day.practice_date;

            UPDATE practice_blocks
            SET session_id = daily_session_id,
                updated_at = now()
            WHERE session_id = legacy.id
              AND practice_date = practice_day.practice_date;

            UPDATE recordings AS recording
            SET practice_session_id = daily_session_id::text,
                updated_at = now()
            FROM practice_blocks AS block
            WHERE recording.practice_block_id = block.id
              AND block.session_id = daily_session_id;
        END LOOP;
    END LOOP;
END $$;
