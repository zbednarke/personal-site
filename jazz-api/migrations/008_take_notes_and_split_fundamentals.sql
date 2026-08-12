-- Section titles already provide context, so remove only generated placeholder
-- copy. User-authored recording notes are preserved.
UPDATE recordings AS recording
SET notes = NULL,
    updated_at = now()
FROM practice_blocks AS block
WHERE recording.practice_block_id = block.id
  AND recording.notes = block.title || ' section take';

-- Preserve completed sessions as historical truth. Split the combined block
-- only in active sessions. Existing time, notes, and recordings stay with
-- Articulation; Flexibility starts as its own focused block.
DO $$
DECLARE
    combined RECORD;
    flexibility_id uuid;
BEGIN
    FOR combined IN
        SELECT block.*
        FROM practice_blocks AS block
        JOIN practice_sessions AS session ON session.id = block.session_id
        WHERE block.block_key = 'articulation-flexibility'
          AND session.status = 'active'
        ORDER BY block.practice_date, block.position
    LOOP
        UPDATE practice_blocks
        SET position = LEAST(position + 1, 99),
            updated_at = now()
        WHERE session_id = combined.session_id
          AND practice_date = combined.practice_date
          AND position > combined.position;

        UPDATE practice_blocks
        SET block_key = 'articulation',
            title = 'Articulation',
            instructions = 'Light, centered attacks across comfortable registers. Keep the air moving and the tongue economical.',
            category = 'fundamentals',
            track = 'trumpet',
            target_minutes = 10,
            updated_at = now()
        WHERE id = combined.id;

        flexibility_id := (
            substr(md5(combined.id::text || ':flexibility'), 1, 8) || '-' ||
            substr(md5(combined.id::text || ':flexibility'), 9, 4) || '-' ||
            substr(md5(combined.id::text || ':flexibility'), 13, 4) || '-' ||
            substr(md5(combined.id::text || ':flexibility'), 17, 4) || '-' ||
            substr(md5(combined.id::text || ':flexibility'), 21, 12)
        )::uuid;

        INSERT INTO practice_blocks
            (id,session_id,user_id,practice_date,block_key,position,title,instructions,category,track,target_minutes)
        VALUES
            (flexibility_id,combined.session_id,combined.user_id,combined.practice_date,'flexibility',combined.position + 1,
             'Flexibility','Easy lip slurs with an even air stream, centered slots, and no forcing through register changes.',
             'fundamentals','trumpet',10)
        ON CONFLICT (session_id,practice_date,block_key) DO NOTHING;
    END LOOP;
END $$;
