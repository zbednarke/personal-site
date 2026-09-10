ALTER TABLE practice_blocks ADD COLUMN carry_forward boolean NOT NULL DEFAULT true;

-- This previously scheduled one-off predates explicit day-only metadata.
UPDATE practice_blocks SET carry_forward=false
WHERE block_key='easy-to-love-transcription-2026-09-10';

CREATE TABLE practice_day_layouts (
  session_id uuid NOT NULL REFERENCES practice_sessions(id),
  user_id uuid NOT NULL REFERENCES app_users(id),
  practice_date date NOT NULL,
  PRIMARY KEY(session_id,practice_date)
);
INSERT INTO practice_day_layouts(session_id,user_id,practice_date)
SELECT DISTINCT session_id,user_id,practice_date FROM practice_blocks;
CREATE INDEX practice_day_layouts_latest ON practice_day_layouts(user_id,practice_date DESC);
