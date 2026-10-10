-- Commonplace, phase 1: a private archive of Moments (conversations, dreams,
-- ideas, quotes). Originals (screenshots, audio, verbatim text, links) are
-- stored exactly as received; the margin (annotations) is kept separately.
-- Image and audio bytes live in the private bucket under
-- commonplace/<user>/<moment>/<artifact>-<sha>.<ext>; only metadata is here.
-- Every statement is replay-safe: migrations run on every startup.

CREATE TABLE IF NOT EXISTS cp_people (
  id uuid PRIMARY KEY,
  user_id uuid NOT NULL REFERENCES app_users(id) ON DELETE CASCADE,
  -- Stable import identity, e.g. "discord:<author id>" or a bundle person key.
  person_key text CHECK (person_key IS NULL OR char_length(person_key) BETWEEN 1 AND 200),
  display_name text NOT NULL CHECK (char_length(display_name) BETWEEN 1 AND 120),
  aliases text[] NOT NULL DEFAULT '{}',
  -- Set by the owner: Moments with a special person get a warm signature.
  special boolean NOT NULL DEFAULT false,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (id, user_id)
);
CREATE UNIQUE INDEX IF NOT EXISTS cp_people_key_idx ON cp_people (user_id, person_key) WHERE person_key IS NOT NULL;

CREATE TABLE IF NOT EXISTS cp_moments (
  id uuid PRIMARY KEY,
  user_id uuid NOT NULL REFERENCES app_users(id) ON DELETE CASCADE,
  kind text NOT NULL DEFAULT 'conversation' CHECK (kind IN ('conversation','dream','idea','quote')),
  occurred_at timestamptz,
  ended_at timestamptz,
  timezone text NOT NULL DEFAULT 'UTC' CHECK (char_length(timezone) BETWEEN 1 AND 64),
  source text NOT NULL DEFAULT 'other' CHECK (source IN ('imessage','discord','voice','text','link','other')),
  source_detail text NOT NULL DEFAULT '' CHECK (char_length(source_detail) <= 300),
  title text NOT NULL DEFAULT '' CHECK (char_length(title) <= 300),
  why text NOT NULL DEFAULT '' CHECK (char_length(why) <= 8000),
  external_key text CHECK (external_key IS NULL OR char_length(external_key) BETWEEN 1 AND 300),
  client_capture_id uuid,
  imported_from text NOT NULL DEFAULT 'capture' CHECK (imported_from IN ('capture','bundle','discord')),
  revision bigint NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  search tsvector GENERATED ALWAYS AS (
    setweight(to_tsvector('english', coalesce(title, '')), 'A') ||
    setweight(to_tsvector('english', coalesce(why, '')), 'B') ||
    setweight(to_tsvector('english', coalesce(source_detail, '')), 'C')
  ) STORED,
  UNIQUE (id, user_id),
  CHECK (ended_at IS NULL OR occurred_at IS NULL OR ended_at >= occurred_at)
);
CREATE UNIQUE INDEX IF NOT EXISTS cp_moments_external_key_idx ON cp_moments (user_id, external_key) WHERE external_key IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS cp_moments_capture_idx ON cp_moments (user_id, client_capture_id) WHERE client_capture_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS cp_moments_timeline_idx ON cp_moments (user_id, (coalesce(occurred_at, created_at)) DESC, id DESC);
CREATE INDEX IF NOT EXISTS cp_moments_search_idx ON cp_moments USING gin (search);

CREATE TABLE IF NOT EXISTS cp_moment_people (
  moment_id uuid NOT NULL REFERENCES cp_moments(id) ON DELETE CASCADE,
  person_id uuid NOT NULL REFERENCES cp_people(id) ON DELETE CASCADE,
  user_id uuid NOT NULL REFERENCES app_users(id) ON DELETE CASCADE,
  role text NOT NULL CHECK (role IN ('sender','recipient','mentioned')),
  position integer NOT NULL DEFAULT 0,
  PRIMARY KEY (moment_id, person_id, role)
);
CREATE INDEX IF NOT EXISTS cp_moment_people_person_idx ON cp_moment_people (person_id);

CREATE TABLE IF NOT EXISTS cp_artifacts (
  id uuid PRIMARY KEY,
  moment_id uuid NOT NULL REFERENCES cp_moments(id) ON DELETE CASCADE,
  user_id uuid NOT NULL REFERENCES app_users(id) ON DELETE CASCADE,
  artifact_key text NOT NULL CHECK (char_length(artifact_key) BETWEEN 1 AND 200),
  position integer NOT NULL DEFAULT 0,
  kind text NOT NULL CHECK (kind IN ('image','audio','text','link')),
  -- Stored media (image, audio). NULL object_name means the file has not been
  -- uploaded yet (e.g. an import whose media arrives in a later batch).
  object_name text UNIQUE,
  content_type text CHECK (content_type IS NULL OR content_type IN
    ('image/jpeg','image/png','image/webp','image/gif','audio/mpeg','audio/mp4','audio/aac','audio/wav','audio/webm','audio/ogg')),
  size_bytes bigint CHECK (size_bytes IS NULL OR size_bytes > 0),
  sha256 text CHECK (sha256 IS NULL OR sha256 ~ '^[0-9a-f]{64}$'),
  width integer CHECK (width IS NULL OR width > 0),
  height integer CHECK (height IS NULL OR height > 0),
  duration_ms integer CHECK (duration_ms IS NULL OR duration_ms >= 0),
  original_name text NOT NULL DEFAULT '' CHECK (char_length(original_name) <= 500),
  source_url text NOT NULL DEFAULT '' CHECK (char_length(source_url) <= 4000),
  caption text NOT NULL DEFAULT '' CHECK (char_length(caption) <= 500),
  alt text NOT NULL DEFAULT '' CHECK (char_length(alt) <= 2000),
  -- Text artifacts: the verbatim text. Links: URL, title and captured text.
  text_content text NOT NULL DEFAULT '' CHECK (char_length(text_content) <= 200000),
  url text NOT NULL DEFAULT '' CHECK (char_length(url) <= 4000),
  link_title text NOT NULL DEFAULT '' CHECK (char_length(link_title) <= 500),
  site_name text NOT NULL DEFAULT '' CHECK (char_length(site_name) <= 200),
  captured_text text NOT NULL DEFAULT '' CHECK (char_length(captured_text) <= 200000),
  captured_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  search tsvector GENERATED ALWAYS AS (
    setweight(to_tsvector('english', coalesce(link_title, '') || ' ' || coalesce(caption, '')), 'B') ||
    setweight(to_tsvector('english', coalesce(text_content, '') || ' ' || coalesce(captured_text, '')), 'C')
  ) STORED,
  UNIQUE (moment_id, artifact_key),
  CHECK (kind NOT IN ('text') OR object_name IS NULL),
  CHECK (kind <> 'link' OR url <> '')
);
CREATE INDEX IF NOT EXISTS cp_artifacts_moment_idx ON cp_artifacts (moment_id, position);
CREATE INDEX IF NOT EXISTS cp_artifacts_search_idx ON cp_artifacts USING gin (search);

CREATE TABLE IF NOT EXISTS cp_lines (
  id uuid PRIMARY KEY,
  moment_id uuid NOT NULL REFERENCES cp_moments(id) ON DELETE CASCADE,
  user_id uuid NOT NULL REFERENCES app_users(id) ON DELETE CASCADE,
  line_key text NOT NULL CHECK (char_length(line_key) BETWEEN 1 AND 200),
  -- Discord message ids ("discord:<id>"), unique per owner so a message is
  -- never imported twice even if grouping settings change.
  external_id text CHECK (external_id IS NULL OR char_length(external_id) <= 200),
  position integer NOT NULL DEFAULT 0,
  speaker_is_me boolean NOT NULL DEFAULT false,
  speaker_person_id uuid REFERENCES cp_people(id) ON DELETE SET NULL,
  speaker_label text NOT NULL DEFAULT '' CHECK (char_length(speaker_label) <= 120),
  body text NOT NULL DEFAULT '' CHECK (char_length(body) <= 40000),
  said_at timestamptz,
  time_label text NOT NULL DEFAULT '' CHECK (char_length(time_label) <= 80),
  day_label text NOT NULL DEFAULT '' CHECK (char_length(day_label) <= 120),
  meta jsonb NOT NULL DEFAULT '{}',
  artifact_id uuid REFERENCES cp_artifacts(id) ON DELETE SET NULL,
  -- Percent box on the artifact the line comes from: [x, y, w, h], 0..100.
  rect jsonb,
  revision bigint NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  search tsvector GENERATED ALWAYS AS (to_tsvector('english', coalesce(body, ''))) STORED,
  UNIQUE (moment_id, line_key)
);
CREATE UNIQUE INDEX IF NOT EXISTS cp_lines_external_idx ON cp_lines (user_id, external_id) WHERE external_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS cp_lines_moment_idx ON cp_lines (moment_id, position);
CREATE INDEX IF NOT EXISTS cp_lines_speaker_idx ON cp_lines (speaker_person_id) WHERE speaker_person_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS cp_lines_search_idx ON cp_lines USING gin (search);

CREATE TABLE IF NOT EXISTS cp_annotations (
  id uuid PRIMARY KEY,
  moment_id uuid NOT NULL REFERENCES cp_moments(id) ON DELETE CASCADE,
  user_id uuid NOT NULL REFERENCES app_users(id) ON DELETE CASCADE,
  annotation_key text CHECK (annotation_key IS NULL OR char_length(annotation_key) BETWEEN 1 AND 200),
  state text NOT NULL DEFAULT 'pencil' CHECK (state IN ('pencil','ink','erased')),
  author text NOT NULL DEFAULT 'owner' CHECK (author IN ('owner','import','machine')),
  note_type text NOT NULL DEFAULT 'note' CHECK (note_type ~ '^[a-z][a-z0-9-]{0,31}$'),
  title text NOT NULL DEFAULT '' CHECK (char_length(title) <= 500),
  body text NOT NULL DEFAULT '' CHECK (char_length(body) <= 8000),
  line_id uuid REFERENCES cp_lines(id) ON DELETE SET NULL,
  artifact_id uuid REFERENCES cp_artifacts(id) ON DELETE SET NULL,
  rect jsonb,
  link_url text NOT NULL DEFAULT '' CHECK (char_length(link_url) <= 4000),
  link_moment_id uuid REFERENCES cp_moments(id) ON DELETE SET NULL,
  link_label text NOT NULL DEFAULT '' CHECK (char_length(link_label) <= 200),
  -- Set when the owner keeps, erases or edits: a re-import never overwrites it.
  owner_touched_at timestamptz,
  revision bigint NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  search tsvector GENERATED ALWAYS AS (
    setweight(to_tsvector('english', coalesce(title, '')), 'B') ||
    setweight(to_tsvector('english', coalesce(body, '')), 'C')
  ) STORED
);
CREATE UNIQUE INDEX IF NOT EXISTS cp_annotations_key_idx ON cp_annotations (moment_id, annotation_key) WHERE annotation_key IS NOT NULL;
CREATE INDEX IF NOT EXISTS cp_annotations_moment_idx ON cp_annotations (moment_id);
CREATE INDEX IF NOT EXISTS cp_annotations_search_idx ON cp_annotations USING gin (search);

CREATE TABLE IF NOT EXISTS cp_threads (
  id uuid PRIMARY KEY,
  user_id uuid NOT NULL REFERENCES app_users(id) ON DELETE CASCADE,
  thread_key text CHECK (thread_key IS NULL OR char_length(thread_key) BETWEEN 1 AND 200),
  -- Empty until the owner names it: the system never invents titles.
  title text NOT NULL DEFAULT '' CHECK (char_length(title) <= 300),
  description text NOT NULL DEFAULT '' CHECK (char_length(description) <= 4000),
  state text NOT NULL DEFAULT 'ink' CHECK (state IN ('pencil','ink')),
  revision bigint NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS cp_threads_key_idx ON cp_threads (user_id, thread_key) WHERE thread_key IS NOT NULL;

CREATE TABLE IF NOT EXISTS cp_thread_knots (
  id uuid PRIMARY KEY,
  thread_id uuid NOT NULL REFERENCES cp_threads(id) ON DELETE CASCADE,
  user_id uuid NOT NULL REFERENCES app_users(id) ON DELETE CASCADE,
  moment_id uuid NOT NULL REFERENCES cp_moments(id) ON DELETE CASCADE,
  line_id uuid REFERENCES cp_lines(id) ON DELETE SET NULL,
  position integer NOT NULL DEFAULT 0,
  label text NOT NULL DEFAULT '' CHECK (char_length(label) <= 200),
  style text NOT NULL DEFAULT 'plain' CHECK (style IN ('fire','plain')),
  -- The bundle Moment that imported this knot (re-imports replace only these).
  imported_by uuid REFERENCES cp_moments(id) ON DELETE CASCADE,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS cp_thread_knots_thread_idx ON cp_thread_knots (thread_id, position);
CREATE INDEX IF NOT EXISTS cp_thread_knots_moment_idx ON cp_thread_knots (moment_id);

-- Owner-given names for Idea-space regions. The client groups Moments into
-- regions with stable keys (e.g. "person:<id>", "kind:dream"); a region stays
-- nameless until the owner names it here.
CREATE TABLE IF NOT EXISTS cp_regions (
  user_id uuid NOT NULL REFERENCES app_users(id) ON DELETE CASCADE,
  region_key text NOT NULL CHECK (region_key ~ '^[a-z]+:[A-Za-z0-9:_-]{1,120}$'),
  name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 120),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, region_key)
);

-- Owner edits survive re-imports: a line the owner edited is never changed by
-- an import; Discord bodies change only when Discord's own edit is newer.
ALTER TABLE cp_lines ADD COLUMN IF NOT EXISTS owner_touched_at timestamptz;
ALTER TABLE cp_lines ADD COLUMN IF NOT EXISTS source_edited_at timestamptz;
-- The name an import gave a person: imports rename only while the owner has not.
ALTER TABLE cp_people ADD COLUMN IF NOT EXISTS imported_name text;
-- People an import attached to a Moment (re-imports replace only these).
ALTER TABLE cp_moment_people ADD COLUMN IF NOT EXISTS imported boolean NOT NULL DEFAULT false;
