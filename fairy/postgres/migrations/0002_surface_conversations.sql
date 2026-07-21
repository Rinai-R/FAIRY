CREATE TABLE surface_conversations (
  character_id text NOT NULL,
  surface text NOT NULL CHECK (surface IN ('desktop', 'im_private', 'im_group')),
  surface_key_digest text NOT NULL CHECK (surface_key_digest ~ '^[0-9a-f]{64}$'),
  conversation_id text NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
  created_at_ms bigint NOT NULL CHECK (created_at_ms >= 0),
  updated_at_ms bigint NOT NULL CHECK (updated_at_ms >= created_at_ms),
  PRIMARY KEY(character_id, surface, surface_key_digest),
  UNIQUE(conversation_id)
);

CREATE INDEX surface_conversations_conversation
  ON surface_conversations(conversation_id);
