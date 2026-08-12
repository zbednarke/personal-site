ALTER TABLE recordings
    DROP CONSTRAINT IF EXISTS recordings_duration_ms_check;

ALTER TABLE recordings
    ADD CONSTRAINT recordings_duration_ms_check
    CHECK (duration_ms IS NULL OR duration_ms BETWEEN 0 AND 14400000);

ALTER TABLE recordings
    ADD COLUMN IF NOT EXISTS media_kind text NOT NULL DEFAULT 'audio';

ALTER TABLE recordings
    DROP CONSTRAINT IF EXISTS recordings_media_kind_check;

ALTER TABLE recordings
    ADD CONSTRAINT recordings_media_kind_check
    CHECK (media_kind IN ('audio', 'video'));

ALTER TABLE recordings
    ADD COLUMN IF NOT EXISTS video_bucket text,
    ADD COLUMN IF NOT EXISTS video_object_name text,
    ADD COLUMN IF NOT EXISTS video_object_generation bigint,
    ADD COLUMN IF NOT EXISTS video_content_type text,
    ADD COLUMN IF NOT EXISTS video_codec text,
    ADD COLUMN IF NOT EXISTS video_expected_size_bytes bigint,
    ADD COLUMN IF NOT EXISTS video_size_bytes bigint,
    ADD COLUMN IF NOT EXISTS video_width integer,
    ADD COLUMN IF NOT EXISTS video_height integer,
    ADD COLUMN IF NOT EXISTS video_frame_rate real,
    ADD COLUMN IF NOT EXISTS video_uploaded_at timestamptz,
    ADD COLUMN IF NOT EXISTS video_checksum text;

CREATE UNIQUE INDEX IF NOT EXISTS recordings_video_object_name_idx
    ON recordings (video_object_name)
    WHERE video_object_name IS NOT NULL;
