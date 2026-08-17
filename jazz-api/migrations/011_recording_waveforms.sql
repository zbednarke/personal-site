ALTER TABLE recordings
    ADD COLUMN IF NOT EXISTS waveform_peaks jsonb NOT NULL DEFAULT '[]'::jsonb;

ALTER TABLE recordings
    DROP CONSTRAINT IF EXISTS recordings_waveform_peaks_check;

ALTER TABLE recordings
    ADD CONSTRAINT recordings_waveform_peaks_check
    CHECK (jsonb_typeof(waveform_peaks) = 'array' AND jsonb_array_length(waveform_peaks) <= 1200);
