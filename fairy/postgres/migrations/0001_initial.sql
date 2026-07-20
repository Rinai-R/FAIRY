CREATE EXTENSION IF NOT EXISTS pg_trgm WITH SCHEMA public;

CREATE TABLE conversations (
  id text PRIMARY KEY,
  character_id text NOT NULL,
  created_at_ms bigint NOT NULL CHECK (created_at_ms >= 0),
  updated_at_ms bigint NOT NULL CHECK (updated_at_ms >= created_at_ms)
);

CREATE INDEX conversations_character_updated
  ON conversations(character_id, updated_at_ms DESC, id ASC);

CREATE TABLE conversation_turns (
  id text PRIMARY KEY,
  conversation_id text NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
  sequence bigint NOT NULL CHECK (sequence > 0),
  status text NOT NULL CHECK (status IN ('interpreting', 'planning', 'responding', 'completed', 'interrupted', 'failed')),
  error_code text,
  error_message text,
  error_retryable boolean,
  extraction_state text NOT NULL DEFAULT 'ineligible' CHECK (extraction_state IN ('ineligible', 'pending', 'claimed', 'processed')),
  created_at_ms bigint NOT NULL CHECK (created_at_ms >= 0),
  updated_at_ms bigint NOT NULL CHECK (updated_at_ms >= created_at_ms),
  UNIQUE(conversation_id, sequence),
  CHECK ((status = 'failed') = (error_code IS NOT NULL AND error_message IS NOT NULL))
);

CREATE INDEX conversation_turns_conversation_status
  ON conversation_turns(conversation_id, status, sequence ASC);

CREATE INDEX conversation_turns_extraction
  ON conversation_turns(conversation_id, extraction_state, sequence ASC)
  WHERE status = 'completed';

CREATE TABLE conversation_messages (
  id text PRIMARY KEY,
  conversation_id text NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
  turn_id text NOT NULL REFERENCES conversation_turns(id) ON DELETE CASCADE,
  sequence bigint NOT NULL CHECK (sequence > 0),
  role text NOT NULL CHECK (role IN ('user', 'assistant')),
  content text NOT NULL CHECK (content <> ''),
  created_at_ms bigint NOT NULL CHECK (created_at_ms >= 0),
  UNIQUE(conversation_id, sequence),
  UNIQUE(turn_id, role)
);

CREATE INDEX conversation_messages_conversation_sequence
  ON conversation_messages(conversation_id, sequence ASC);

CREATE TABLE prompt_windows (
  conversation_id text PRIMARY KEY REFERENCES conversations(id) ON DELETE CASCADE,
  revision bigint NOT NULL CHECK (revision > 0),
  summary text,
  cutoff_message_sequence bigint NOT NULL DEFAULT 0 CHECK (cutoff_message_sequence >= 0),
  updated_at_ms bigint NOT NULL CHECK (updated_at_ms >= 0)
);

CREATE TABLE turn_runtime_events (
  id text PRIMARY KEY,
  conversation_id text NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
  turn_id text NOT NULL REFERENCES conversation_turns(id) ON DELETE CASCADE,
  sequence bigint NOT NULL CHECK (sequence > 0),
  event_type text NOT NULL,
  state text,
  code text,
  metadata_json jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at_ms bigint NOT NULL CHECK (created_at_ms >= 0),
  UNIQUE(conversation_id, turn_id, sequence)
);

CREATE INDEX turn_runtime_events_turn_sequence
  ON turn_runtime_events(conversation_id, turn_id, sequence ASC);

CREATE INDEX turn_runtime_events_type_created
  ON turn_runtime_events(event_type, created_at_ms ASC, sequence ASC);

CREATE TABLE lane_continuations (
  conversation_id text NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
  lane text NOT NULL,
  previous_response_id text NOT NULL,
  request_shape_hash text NOT NULL,
  input_prefix_hash text NOT NULL,
  response_item_hash text NOT NULL,
  window_revision bigint NOT NULL CHECK (window_revision > 0),
  updated_at_ms bigint NOT NULL CHECK (updated_at_ms >= 0),
  PRIMARY KEY(conversation_id, lane)
);

CREATE TABLE context_windows (
  conversation_id text NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
  lane text NOT NULL,
  window_number bigint NOT NULL CHECK (window_number >= 0),
  first_window_id text NOT NULL,
  previous_window_id text,
  window_id text NOT NULL,
  observed_prefill_tokens bigint CHECK (observed_prefill_tokens IS NULL OR observed_prefill_tokens >= 0),
  estimated_prefill_tokens bigint CHECK (estimated_prefill_tokens IS NULL OR estimated_prefill_tokens >= 0),
  last_trigger text NOT NULL DEFAULT 'created',
  failure_count bigint NOT NULL DEFAULT 0 CHECK (failure_count >= 0),
  prompt_window_revision bigint NOT NULL CHECK (prompt_window_revision > 0),
  updated_at_ms bigint NOT NULL CHECK (updated_at_ms >= 0),
  PRIMARY KEY(conversation_id, lane)
);

CREATE TABLE personal_memories (
  id text PRIMARY KEY,
  kind text NOT NULL,
  scope_kind text NOT NULL CHECK (scope_kind IN ('global', 'character', 'relationship', 'unassigned_legacy')),
  character_id text,
  review_status text NOT NULL,
  content text NOT NULL CHECK (content <> ''),
  status text NOT NULL CHECK (status IN ('active', 'superseded', 'tombstone')),
  confidence_basis_points integer NOT NULL CHECK (confidence_basis_points BETWEEN 0 AND 10000),
  source_conversation_id text NOT NULL REFERENCES conversations(id) ON DELETE RESTRICT,
  source_turn_id text NOT NULL REFERENCES conversation_turns(id) ON DELETE RESTRICT,
  supersedes_id text REFERENCES personal_memories(id) ON DELETE RESTRICT,
  created_at_ms bigint NOT NULL CHECK (created_at_ms >= 0),
  updated_at_ms bigint NOT NULL CHECK (updated_at_ms >= created_at_ms),
  CHECK ((scope_kind = 'character') = (character_id IS NOT NULL) OR scope_kind <> 'character')
);

CREATE INDEX personal_memories_scope_status
  ON personal_memories(scope_kind, character_id, status, updated_at_ms DESC, id ASC);

CREATE INDEX personal_memories_content_trgm
  ON personal_memories USING gin (content public.gin_trgm_ops);

CREATE TABLE knowledge_entries (
  id text PRIMARY KEY,
  topic text NOT NULL CHECK (topic <> ''),
  statement text NOT NULL CHECK (statement <> ''),
  status text NOT NULL CHECK (status IN ('candidate', 'verified', 'superseded', 'rejected', 'tombstone')),
  verification_basis text NOT NULL,
  confidence_basis_points integer NOT NULL CHECK (confidence_basis_points BETWEEN 0 AND 10000),
  source_conversation_id text NOT NULL REFERENCES conversations(id) ON DELETE RESTRICT,
  source_turn_id text NOT NULL REFERENCES conversation_turns(id) ON DELETE RESTRICT,
  supersedes_id text REFERENCES knowledge_entries(id) ON DELETE RESTRICT,
  created_at_ms bigint NOT NULL CHECK (created_at_ms >= 0),
  updated_at_ms bigint NOT NULL CHECK (updated_at_ms >= created_at_ms)
);

CREATE INDEX knowledge_entries_status_updated
  ON knowledge_entries(status, updated_at_ms DESC, id ASC);

CREATE INDEX knowledge_entries_topic_trgm
  ON knowledge_entries USING gin (topic public.gin_trgm_ops);

CREATE INDEX knowledge_entries_statement_trgm
  ON knowledge_entries USING gin (statement public.gin_trgm_ops);

CREATE TABLE knowledge_sources (
  knowledge_id text NOT NULL REFERENCES knowledge_entries(id) ON DELETE CASCADE,
  source_id text NOT NULL,
  title text NOT NULL,
  url text NOT NULL,
  snippet text NOT NULL,
  rank integer NOT NULL CHECK (rank >= 0),
  fetched_at_ms bigint NOT NULL CHECK (fetched_at_ms >= 0),
  PRIMARY KEY(knowledge_id, source_id)
);

CREATE TABLE extraction_batches (
  id text PRIMARY KEY,
  conversation_id text NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
  character_id text NOT NULL,
  status text NOT NULL CHECK (status IN ('pending', 'running', 'succeeded', 'failed', 'cancelled')),
  first_turn_sequence bigint NOT NULL CHECK (first_turn_sequence > 0),
  last_turn_sequence bigint NOT NULL CHECK (last_turn_sequence >= first_turn_sequence),
  lease_owner text,
  lease_expires_at_ms bigint CHECK (lease_expires_at_ms IS NULL OR lease_expires_at_ms >= 0),
  attempt_count integer NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
  error_code text,
  error_message text,
  error_retryable boolean,
  created_at_ms bigint NOT NULL CHECK (created_at_ms >= 0),
  updated_at_ms bigint NOT NULL CHECK (updated_at_ms >= created_at_ms),
  CHECK ((lease_owner IS NULL) = (lease_expires_at_ms IS NULL)),
  CHECK (status = 'running' OR lease_owner IS NULL)
);

CREATE UNIQUE INDEX extraction_batches_one_running
  ON extraction_batches(conversation_id)
  WHERE status = 'running';

CREATE INDEX extraction_batches_claimable
  ON extraction_batches(status, lease_expires_at_ms ASC NULLS FIRST, updated_at_ms ASC, id ASC)
  WHERE status IN ('pending', 'running');

CREATE TABLE extraction_batch_turns (
  batch_id text NOT NULL REFERENCES extraction_batches(id) ON DELETE CASCADE,
  turn_id text NOT NULL REFERENCES conversation_turns(id) ON DELETE RESTRICT,
  turn_sequence bigint NOT NULL CHECK (turn_sequence > 0),
  PRIMARY KEY(batch_id, turn_id),
  UNIQUE(batch_id, turn_sequence)
);

CREATE TABLE knowledge_ingest_jobs (
  id text PRIMARY KEY,
  conversation_id text NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
  turn_id text NOT NULL REFERENCES conversation_turns(id) ON DELETE CASCADE,
  query text NOT NULL DEFAULT '',
  title text NOT NULL DEFAULT '',
  url text NOT NULL DEFAULT '',
  snippet text NOT NULL DEFAULT '',
  rank integer NOT NULL DEFAULT 0 CHECK (rank >= 0),
  fetched_at_ms bigint NOT NULL DEFAULT 0 CHECK (fetched_at_ms >= 0),
  status text NOT NULL CHECK (status IN ('pending', 'running', 'succeeded', 'failed', 'dropped')),
  lease_owner text,
  lease_expires_at_ms bigint CHECK (lease_expires_at_ms IS NULL OR lease_expires_at_ms >= 0),
  attempt_count integer NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
  error_message text,
  created_at_ms bigint NOT NULL CHECK (created_at_ms >= 0),
  updated_at_ms bigint NOT NULL CHECK (updated_at_ms >= created_at_ms),
  CHECK ((lease_owner IS NULL) = (lease_expires_at_ms IS NULL)),
  CHECK (status = 'running' OR lease_owner IS NULL)
);

CREATE INDEX knowledge_ingest_jobs_status
  ON knowledge_ingest_jobs(status, lease_expires_at_ms ASC NULLS FIRST, created_at_ms ASC, id ASC);

CREATE TABLE memory_embedding_items (
  id text PRIMARY KEY,
  item_kind text NOT NULL CHECK (item_kind IN ('personal_memory', 'knowledge')),
  item_id text NOT NULL,
  model_id text NOT NULL,
  dimensions integer NOT NULL CHECK (dimensions = 512),
  point_id uuid NOT NULL,
  content_hash text NOT NULL CHECK (content_hash <> ''),
  status text NOT NULL CHECK (status IN ('pending', 'embedded', 'failed')),
  error_code text,
  error_message text,
  embedded_at_ms bigint CHECK (embedded_at_ms IS NULL OR embedded_at_ms >= 0),
  legacy_vector_rowid bigint UNIQUE,
  created_at_ms bigint NOT NULL CHECK (created_at_ms >= 0),
  updated_at_ms bigint NOT NULL CHECK (updated_at_ms >= created_at_ms),
  UNIQUE(item_kind, item_id, model_id),
  UNIQUE(point_id),
  CHECK ((status = 'embedded') = (embedded_at_ms IS NOT NULL))
);

CREATE INDEX memory_embedding_items_item
  ON memory_embedding_items(item_kind, item_id, model_id);

CREATE INDEX memory_embedding_items_status
  ON memory_embedding_items(status, updated_at_ms ASC, id ASC);

CREATE TABLE memory_embedding_jobs (
  id text PRIMARY KEY,
  item_kind text NOT NULL CHECK (item_kind IN ('personal_memory', 'knowledge')),
  item_id text NOT NULL,
  model_id text NOT NULL,
  dimensions integer NOT NULL CHECK (dimensions = 512),
  point_id uuid NOT NULL,
  content_hash text NOT NULL CHECK (content_hash <> ''),
  status text NOT NULL CHECK (status IN ('pending', 'running', 'succeeded', 'failed')),
  lease_owner text,
  lease_expires_at_ms bigint CHECK (lease_expires_at_ms IS NULL OR lease_expires_at_ms >= 0),
  attempt_count integer NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
  error_code text,
  error_message text,
  retryable boolean NOT NULL DEFAULT false,
  created_at_ms bigint NOT NULL CHECK (created_at_ms >= 0),
  updated_at_ms bigint NOT NULL CHECK (updated_at_ms >= created_at_ms),
  UNIQUE(item_kind, item_id, model_id, content_hash),
  CHECK ((lease_owner IS NULL) = (lease_expires_at_ms IS NULL)),
  CHECK (status = 'running' OR lease_owner IS NULL)
);

CREATE INDEX memory_embedding_jobs_status
  ON memory_embedding_jobs(status, lease_expires_at_ms ASC NULLS FIRST, updated_at_ms ASC, id ASC);

CREATE INDEX memory_embedding_jobs_item
  ON memory_embedding_jobs(item_kind, item_id, model_id, content_hash);

CREATE TABLE secret_values (
  namespace text NOT NULL,
  name text NOT NULL,
  key_version integer NOT NULL CHECK (key_version > 0),
  nonce bytea NOT NULL CHECK (octet_length(nonce) = 12),
  ciphertext bytea NOT NULL CHECK (octet_length(ciphertext) > 0),
  aad text NOT NULL,
  created_at_ms bigint NOT NULL CHECK (created_at_ms >= 0),
  updated_at_ms bigint NOT NULL CHECK (updated_at_ms >= created_at_ms),
  PRIMARY KEY(namespace, name)
);

CREATE TABLE sqlite_import_runs (
  id text PRIMARY KEY,
  source_intelligence_path text NOT NULL,
  source_secret_path text,
  source_fingerprint jsonb NOT NULL,
  status text NOT NULL CHECK (status IN ('pending', 'running', 'verified', 'failed')),
  phase text NOT NULL DEFAULT '',
  report_json jsonb NOT NULL DEFAULT '{}'::jsonb,
  error_message text,
  created_at_ms bigint NOT NULL CHECK (created_at_ms >= 0),
  updated_at_ms bigint NOT NULL CHECK (updated_at_ms >= created_at_ms),
  UNIQUE(source_fingerprint)
);

CREATE INDEX sqlite_import_runs_status
  ON sqlite_import_runs(status, updated_at_ms DESC, id ASC);

CREATE TABLE sqlite_import_checkpoints (
  run_id text NOT NULL REFERENCES sqlite_import_runs(id) ON DELETE CASCADE,
  phase text NOT NULL,
  checkpoint_json jsonb NOT NULL DEFAULT '{}'::jsonb,
  updated_at_ms bigint NOT NULL CHECK (updated_at_ms >= 0),
  PRIMARY KEY(run_id, phase)
);

CREATE TABLE vector_rebuild_runs (
  id text PRIMARY KEY,
  collection_name text NOT NULL,
  model_id text NOT NULL,
  status text NOT NULL CHECK (status IN ('pending', 'running', 'succeeded', 'failed')),
  scanned_items bigint NOT NULL DEFAULT 0 CHECK (scanned_items >= 0),
  upserted_points bigint NOT NULL DEFAULT 0 CHECK (upserted_points >= 0),
  checkpoint_json jsonb NOT NULL DEFAULT '{}'::jsonb,
  error_message text,
  created_at_ms bigint NOT NULL CHECK (created_at_ms >= 0),
  updated_at_ms bigint NOT NULL CHECK (updated_at_ms >= created_at_ms)
);

CREATE INDEX vector_rebuild_runs_status
  ON vector_rebuild_runs(status, updated_at_ms DESC, id ASC);

CREATE TABLE vector_reconciliation_runs (
  id text PRIMARY KEY,
  collection_name text NOT NULL,
  dry_run boolean NOT NULL,
  status text NOT NULL CHECK (status IN ('running', 'succeeded', 'failed')),
  missing_points bigint NOT NULL DEFAULT 0 CHECK (missing_points >= 0),
  stale_points bigint NOT NULL DEFAULT 0 CHECK (stale_points >= 0),
  orphan_points bigint NOT NULL DEFAULT 0 CHECK (orphan_points >= 0),
  report_json jsonb NOT NULL DEFAULT '{}'::jsonb,
  error_message text,
  created_at_ms bigint NOT NULL CHECK (created_at_ms >= 0),
  updated_at_ms bigint NOT NULL CHECK (updated_at_ms >= created_at_ms)
);

CREATE INDEX vector_reconciliation_runs_status
  ON vector_reconciliation_runs(status, updated_at_ms DESC, id ASC);
