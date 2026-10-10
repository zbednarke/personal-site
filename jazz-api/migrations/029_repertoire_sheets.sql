-- Private sheet-music library linked directly to repertoire tunes. Uploaded
-- files live in the existing private object bucket; source_url is retained for
-- bundled/public-domain material and provenance.
CREATE TABLE IF NOT EXISTS repertoire_sheets (
    user_id uuid NOT NULL REFERENCES app_users(id) ON DELETE CASCADE,
    id text NOT NULL CHECK (char_length(id) BETWEEN 1 AND 80),
    tune_id text NOT NULL,
    title text NOT NULL CHECK (char_length(title) BETWEEN 1 AND 160),
    part text NOT NULL DEFAULT 'bb' CHECK (part IN ('concert','bb','score','other')),
    rights text NOT NULL DEFAULT 'owned_copy' CHECK (rights IN ('original','public_domain','licensed','owned_copy','unknown')),
    original_name text NOT NULL DEFAULT '' CHECK (char_length(original_name) <= 200),
    source_url text CHECK (source_url IS NULL OR (char_length(source_url) <= 1000 AND (source_url ~ '^https://' OR source_url ~ '^/assets/jazz/sheets/'))),
    object_name text CHECK (object_name IS NULL OR char_length(object_name) BETWEEN 1 AND 500),
    content_type text NOT NULL DEFAULT 'application/pdf' CHECK (content_type = 'application/pdf'),
    size_bytes bigint NOT NULL CHECK (size_bytes >= 0 AND size_bytes <= 26214400),
    sha256 text NOT NULL DEFAULT '' CHECK (sha256 = '' OR sha256 ~ '^[0-9a-f]{64}$'),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, id),
    FOREIGN KEY (user_id, tune_id) REFERENCES repertoire_tunes(user_id, tune_id) ON DELETE CASCADE,
    CHECK (object_name IS NOT NULL OR source_url IS NOT NULL)
);
CREATE INDEX IF NOT EXISTS repertoire_sheets_tune_idx
    ON repertoire_sheets (user_id, tune_id, created_at, id);

-- This chart contains an original practice form, not a copyrighted melody.
-- Existing repertoires receive it here; first-time repertoires also receive it
-- from ensureRepertoireSeeded after their tune rows are created.
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM jazz_backfills WHERE name='029_repertoire_sheets') THEN
    INSERT INTO repertoire_sheets
        (user_id,id,tune_id,title,part,rights,original_name,source_url,size_bytes)
    SELECT user_id,'bb-blues-practice-chart','bb-blues','B♭ Blues Practice Chart','bb','original',
           'bb-blues-practice-chart.pdf','/assets/jazz/sheets/bb-blues.pdf',0
    FROM repertoire_tunes WHERE tune_id='bb-blues'
    ON CONFLICT (user_id,id) DO NOTHING;
    INSERT INTO jazz_backfills(name) VALUES ('029_repertoire_sheets');
  END IF;
END $$;
