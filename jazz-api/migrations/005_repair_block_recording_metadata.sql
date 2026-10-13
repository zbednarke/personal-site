-- Older section recordings fell back to the standalone recorder's selected
-- tune whenever their block intentionally supplied no tune. The practice block
-- is authoritative: only the Blue Bossa repertoire block belongs to that tune.
UPDATE recordings AS recording
SET tune_id = NULL,
    updated_at = now()
FROM practice_blocks AS block
WHERE recording.practice_block_id = block.id
  AND recording.tune_id = 'blue-bossa'
  AND block.block_key IN ('warm-up', 'articulation-flexibility', 'scales', 'horn-down-listening');

UPDATE recordings AS recording
SET tune_id = 'blue-bossa',
    updated_at = now()
FROM practice_blocks AS block
WHERE recording.practice_block_id = block.id
  AND block.block_key = 'blue-bossa-play'
  AND recording.tune_id IS DISTINCT FROM 'blue-bossa';
