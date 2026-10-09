-- Horn inspiration board: personal links and images, separate from verified
-- market listings. Image bytes live in the private recording bucket under
-- inspiration/<user>/<inspiration>/<image>.<ext>; only metadata is stored here.
CREATE TABLE IF NOT EXISTS horn_inspirations (
  id uuid PRIMARY KEY,
  user_id uuid NOT NULL REFERENCES app_users(id) ON DELETE CASCADE,
  client_capture_id uuid NOT NULL,
  kind text NOT NULL CHECK (kind IN ('link','image')),
  source_url text CHECK (source_url IS NULL OR char_length(source_url) <= 2000),
  canonical_url text CHECK (canonical_url IS NULL OR char_length(canonical_url) <= 2000),
  provider text NOT NULL DEFAULT 'web' CHECK (provider IN ('youtube','instagram','tiktok','facebook','reverb','ebay','web','upload')),
  provider_media_id text CHECK (char_length(provider_media_id) <= 64),
  media_format text CHECK (media_format IN ('short','video','reel','post','page','image')),
  title text CHECK (char_length(title) <= 300),
  author_name text CHECK (char_length(author_name) <= 200),
  site_name text CHECK (char_length(site_name) <= 120),
  description text CHECK (char_length(description) <= 2000),
  metadata_status text NOT NULL DEFAULT 'pending'
     CHECK (metadata_status IN ('pending','fetched','partial','failed','skipped')),
  metadata_error text NOT NULL DEFAULT '',
  metadata_fetched_at timestamptz,
  maker text CHECK (char_length(maker) <= 80),
  model text CHECK (char_length(model) <= 120),
  tags jsonb NOT NULL DEFAULT '[]',
  why text NOT NULL DEFAULT '' CHECK (char_length(why) <= 4000),
  priority text NOT NULL DEFAULT 'someday'
     CHECK (priority IN ('inspiration','someday','want','hunting')),
  price_seen numeric(14,2) CHECK (price_seen >= 0),
  price_currency text NOT NULL DEFAULT 'USD' CHECK (price_currency ~ '^[A-Z]{3}$'),
  price_seen_on date,
  price_auto boolean NOT NULL DEFAULT false,
  horn_id uuid,
  pinned boolean NOT NULL DEFAULT false,
  revision bigint NOT NULL DEFAULT 1,
  archived_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (user_id, client_capture_id),
  UNIQUE (id, user_id),
  FOREIGN KEY (horn_id, user_id) REFERENCES trumpet_horns(id, user_id),
  CHECK (kind = 'image' OR source_url IS NOT NULL)
);
-- One live card per link (archived duplicates allowed so re-adding after archive can offer "restore").
CREATE UNIQUE INDEX IF NOT EXISTS horn_inspirations_live_url_idx
  ON horn_inspirations (user_id, canonical_url) WHERE canonical_url IS NOT NULL AND archived_at IS NULL;
CREATE INDEX IF NOT EXISTS horn_inspirations_board_idx
  ON horn_inspirations (user_id, archived_at, pinned DESC, created_at DESC);
CREATE INDEX IF NOT EXISTS horn_inspirations_horn_idx
  ON horn_inspirations (horn_id) WHERE horn_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS horn_inspiration_images (
  id uuid PRIMARY KEY,
  inspiration_id uuid NOT NULL,
  user_id uuid NOT NULL,
  role text NOT NULL CHECK (role IN ('upload','thumbnail')),
  object_name text NOT NULL UNIQUE,
  content_type text NOT NULL CHECK (content_type IN ('image/jpeg','image/png','image/webp','image/gif')),
  size_bytes integer NOT NULL CHECK (size_bytes BETWEEN 1 AND 10485760),
  width integer, height integer,
  sha256 text NOT NULL CHECK (sha256 ~ '^[0-9a-f]{64}$'),
  source_url text,
  position smallint NOT NULL DEFAULT 0 CHECK (position BETWEEN 0 AND 19),
  created_at timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (inspiration_id, user_id) REFERENCES horn_inspirations(id, user_id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS horn_inspiration_images_entry_idx ON horn_inspiration_images (inspiration_id, position);

-- Durable seed marker: the starter card is offered once, and is not recreated
-- after it is permanently deleted (its row, and so its capture id, is gone).
CREATE TABLE IF NOT EXISTS horn_inspiration_seeds (
  user_id uuid NOT NULL REFERENCES app_users(id) ON DELETE CASCADE,
  seed_key text NOT NULL,
  seeded_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, seed_key)
);
