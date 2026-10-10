-- Workbench, phase 1: an owner-only chat that lives inside the private pages.
-- The server owns every piece of state (threads, messages, attachments,
-- drafts, approvals, push subscriptions, spend); browsers keep only a cache
-- and an offline outbox. Each thread has an append-only event log whose ids
-- are allocated under the thread's row lock, so they are gapless, strictly
-- increasing and committed in order: a device that resumes from
-- Last-Event-ID can never skip an event.
-- Later phases (code sessions, previews, overlays) add event types and
-- tables; they reuse wb_events, wb_runs (agent + external_ref) and
-- wb_approvals unchanged. Every statement is replay-safe.

CREATE TABLE IF NOT EXISTS wb_threads (
  id uuid PRIMARY KEY,
  user_id uuid NOT NULL REFERENCES app_users(id) ON DELETE CASCADE,
  title text NOT NULL DEFAULT '' CHECK (char_length(title) <= 160),
  -- Highest event id handed out on this thread (the per-thread sequence).
  last_event_id bigint NOT NULL DEFAULT 0 CHECK (last_event_id >= 0),
  -- One agent loop per thread, across instances: a lease with a heartbeat.
  run_state text NOT NULL DEFAULT 'idle' CHECK (run_state IN ('idle','running')),
  run_owner text NOT NULL DEFAULT '',
  run_heartbeat timestamptz,
  spend_micro_usd bigint NOT NULL DEFAULT 0 CHECK (spend_micro_usd >= 0),
  input_tokens bigint NOT NULL DEFAULT 0,
  output_tokens bigint NOT NULL DEFAULT 0,
  cache_read_tokens bigint NOT NULL DEFAULT 0,
  cache_write_tokens bigint NOT NULL DEFAULT 0,
  archived_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS wb_threads_user_idx ON wb_threads (user_id, updated_at DESC);

CREATE TABLE IF NOT EXISTS wb_events (
  thread_id uuid NOT NULL REFERENCES wb_threads(id) ON DELETE CASCADE,
  id bigint NOT NULL CHECK (id > 0),
  type text NOT NULL CHECK (type ~ '^[a-z][a-z_.]{1,48}$'),
  payload jsonb NOT NULL DEFAULT '{}',
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (thread_id, id)
);

-- What people see. Owner messages carry the page context they were sent from;
-- notes are server-written context for the next turn (approval outcomes).
CREATE TABLE IF NOT EXISTS wb_messages (
  id uuid PRIMARY KEY,
  thread_id uuid NOT NULL REFERENCES wb_threads(id) ON DELETE CASCADE,
  role text NOT NULL CHECK (role IN ('user','assistant','note')),
  text text NOT NULL DEFAULT '' CHECK (char_length(text) <= 100000),
  context jsonb NOT NULL DEFAULT '{}',
  attachment_ids uuid[] NOT NULL DEFAULT '{}',
  -- The offline outbox's idempotency key: replays return the first message.
  client_id text CHECK (client_id IS NULL OR char_length(client_id) BETWEEN 8 AND 64),
  effort text NOT NULL DEFAULT 'low' CHECK (effort IN ('low','medium','high','xhigh')),
  device_id text NOT NULL DEFAULT '' CHECK (char_length(device_id) <= 64),
  run_id uuid,
  handled_at timestamptz,
  status text NOT NULL DEFAULT 'done' CHECK (status IN ('done','streaming','error','blocked')),
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS wb_messages_client_idx ON wb_messages (thread_id, client_id) WHERE client_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS wb_messages_thread_idx ON wb_messages (thread_id, created_at);
CREATE INDEX IF NOT EXISTS wb_messages_pending_idx ON wb_messages (thread_id) WHERE handled_at IS NULL AND role IN ('user','note');

-- The exact Messages API transcript, append-only (prompt caching and
-- thinking blocks both need the history replayed byte for byte).
CREATE TABLE IF NOT EXISTS wb_api_turns (
  thread_id uuid NOT NULL REFERENCES wb_threads(id) ON DELETE CASCADE,
  seq int NOT NULL CHECK (seq >= 0),
  role text NOT NULL CHECK (role IN ('user','assistant')),
  param jsonb NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (thread_id, seq)
);

CREATE TABLE IF NOT EXISTS wb_runs (
  id uuid PRIMARY KEY,
  thread_id uuid NOT NULL REFERENCES wb_threads(id) ON DELETE CASCADE,
  -- 'messages' (phase 1: our loop over the Messages API); later 'managed'
  -- for Managed Agents sessions, whose id goes in external_ref.
  agent text NOT NULL DEFAULT 'messages' CHECK (agent IN ('messages','managed')),
  external_ref text NOT NULL DEFAULT '',
  effort text NOT NULL DEFAULT 'low',
  status text NOT NULL DEFAULT 'running' CHECK (status IN ('running','done','error','refused','capped','interrupted')),
  error text NOT NULL DEFAULT '',
  spend_micro_usd bigint NOT NULL DEFAULT 0,
  started_at timestamptz NOT NULL DEFAULT now(),
  finished_at timestamptz
);
CREATE INDEX IF NOT EXISTS wb_runs_thread_idx ON wb_runs (thread_id, started_at DESC);

CREATE TABLE IF NOT EXISTS wb_attachments (
  id uuid PRIMARY KEY,
  thread_id uuid NOT NULL REFERENCES wb_threads(id) ON DELETE CASCADE,
  user_id uuid NOT NULL REFERENCES app_users(id) ON DELETE CASCADE,
  kind text NOT NULL CHECK (kind IN ('audio','image')),
  content_type text NOT NULL CHECK (char_length(content_type) <= 100),
  size_bytes bigint NOT NULL CHECK (size_bytes > 0),
  duration_ms int CHECK (duration_ms IS NULL OR duration_ms >= 0),
  object_name text NOT NULL,
  client_id text CHECK (client_id IS NULL OR char_length(client_id) BETWEEN 8 AND 64),
  transcript text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS wb_attachments_client_idx ON wb_attachments (thread_id, client_id) WHERE client_id IS NOT NULL;

-- Half-written messages, synced across devices. rev increases on every save.
CREATE TABLE IF NOT EXISTS wb_drafts (
  thread_id uuid PRIMARY KEY REFERENCES wb_threads(id) ON DELETE CASCADE,
  text text NOT NULL DEFAULT '' CHECK (char_length(text) <= 20000),
  rev bigint NOT NULL DEFAULT 0,
  device_id text NOT NULL DEFAULT '',
  updated_at timestamptz NOT NULL DEFAULT now()
);

-- Write tools and triage proposals wait here. The first decision wins
-- (UPDATE ... WHERE status='pending'); every device sees the outcome.
CREATE TABLE IF NOT EXISTS wb_approvals (
  id uuid PRIMARY KEY,
  thread_id uuid NOT NULL REFERENCES wb_threads(id) ON DELETE CASCADE,
  kind text NOT NULL CHECK (kind IN ('tool','triage')),
  tool_name text NOT NULL DEFAULT '',
  tool_input jsonb NOT NULL DEFAULT '{}',
  tool_use_id text NOT NULL DEFAULT '',
  title text NOT NULL DEFAULT '' CHECK (char_length(title) <= 300),
  detail jsonb NOT NULL DEFAULT '{}',
  status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','approved','rejected','failed')),
  decided_by text NOT NULL DEFAULT '',
  decided_at timestamptz,
  result jsonb NOT NULL DEFAULT '{}',
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS wb_approvals_pending_idx ON wb_approvals (thread_id) WHERE status = 'pending';

-- One Web Push subscription per device (the device id lives in the browser).
CREATE TABLE IF NOT EXISTS wb_push_subscriptions (
  user_id uuid NOT NULL REFERENCES app_users(id) ON DELETE CASCADE,
  device_id text NOT NULL CHECK (char_length(device_id) BETWEEN 8 AND 64),
  label text NOT NULL DEFAULT '' CHECK (char_length(label) <= 80),
  endpoint text NOT NULL CHECK (char_length(endpoint) BETWEEN 10 AND 2000),
  p256dh text NOT NULL CHECK (char_length(p256dh) <= 200),
  auth text NOT NULL CHECK (char_length(auth) <= 100),
  nudges boolean NOT NULL DEFAULT false,
  failures int NOT NULL DEFAULT 0,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, device_id)
);

-- Monthly spend, computed from each response's usage (micro-dollars).
CREATE TABLE IF NOT EXISTS wb_spend_months (
  user_id uuid NOT NULL REFERENCES app_users(id) ON DELETE CASCADE,
  month date NOT NULL,
  spend_micro_usd bigint NOT NULL DEFAULT 0 CHECK (spend_micro_usd >= 0),
  requests int NOT NULL DEFAULT 0,
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, month)
);
