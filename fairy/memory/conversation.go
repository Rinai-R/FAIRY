package memory

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type ConversationRecord struct {
	ID              string `json:"id"`
	CharacterID     string `json:"characterId"`
	CreatedAtUnixMS int64  `json:"createdAtUnixMs"`
	UpdatedAtUnixMS int64  `json:"updatedAtUnixMs"`
}

type MessageRecord struct {
	ID              string `json:"id"`
	ConversationID  string `json:"conversationId"`
	TurnID          string `json:"turnId"`
	Sequence        uint64 `json:"sequence"`
	Role            string `json:"role"`
	Content         string `json:"content"`
	CreatedAtUnixMS int64  `json:"createdAtUnixMs"`
}

type PromptWindowRecord struct {
	ConversationID        string  `json:"conversationId"`
	Revision              uint64  `json:"revision"`
	Summary               *string `json:"summary"`
	CutoffMessageSequence uint64  `json:"cutoffMessageSequence"`
	UpdatedAtUnixMS       int64   `json:"updatedAtUnixMs"`
}

type ConversationBootstrap struct {
	Conversation ConversationRecord `json:"conversation"`
	Messages     []MessageRecord    `json:"messages"`
	PromptWindow PromptWindowRecord `json:"promptWindow"`
}

type PersistedTurn struct {
	ID             string        `json:"id"`
	ConversationID string        `json:"conversationId"`
	UserMessage    MessageRecord `json:"userMessage"`
}

func OpenOrCreate(path string) (*Store, error) {
	store := NewStore(path)
	db, err := store.openWrite()
	if err != nil {
		return nil, err
	}
	defer db.Close()
	if err := initializeSchema(db); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *Store) OpenOrCreateCharacterConversation(characterID string) (ConversationBootstrap, error) {
	if err := validateID("character_id", characterID); err != nil {
		return ConversationBootstrap{}, err
	}
	db, err := s.openWrite()
	if err != nil {
		return ConversationBootstrap{}, err
	}
	defer db.Close()
	if err := initializeSchema(db); err != nil {
		return ConversationBootstrap{}, err
	}
	now := nowUnixMS()
	tx, err := db.Begin()
	if err != nil {
		return ConversationBootstrap{}, fmt.Errorf("beginning conversation transaction: %w", err)
	}
	defer tx.Rollback()

	conversationID, err := recentConversationID(tx, characterID)
	if err != nil {
		return ConversationBootstrap{}, err
	}
	if conversationID == "" {
		conversationID = newID()
		if _, err := tx.Exec("INSERT INTO conversations(id, character_id, created_at_ms, updated_at_ms) VALUES (?1, ?2, ?3, ?3)", conversationID, characterID, now); err != nil {
			return ConversationBootstrap{}, fmt.Errorf("creating conversation: %w", err)
		}
		if _, err := tx.Exec("INSERT INTO prompt_windows(conversation_id, revision, summary, cutoff_message_sequence, updated_at_ms) VALUES (?1, 1, NULL, 0, ?2)", conversationID, now); err != nil {
			return ConversationBootstrap{}, fmt.Errorf("creating prompt window: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return ConversationBootstrap{}, fmt.Errorf("committing conversation transaction: %w", err)
	}
	return s.LoadConversation(conversationID)
}

func (s *Store) LoadConversation(conversationID string) (ConversationBootstrap, error) {
	if err := validateID("conversation_id", conversationID); err != nil {
		return ConversationBootstrap{}, err
	}
	db, err := s.openReadOnly()
	if err != nil {
		return ConversationBootstrap{}, err
	}
	defer db.Close()
	return loadConversation(db, conversationID)
}

func (s *Store) BeginTurn(conversationID string, userMessage string) (PersistedTurn, error) {
	if err := validateID("conversation_id", conversationID); err != nil {
		return PersistedTurn{}, err
	}
	if err := validateContent("user message", userMessage); err != nil {
		return PersistedTurn{}, err
	}
	db, err := s.openWrite()
	if err != nil {
		return PersistedTurn{}, err
	}
	defer db.Close()
	now := nowUnixMS()
	turnID := newID()
	messageID := newID()
	tx, err := db.Begin()
	if err != nil {
		return PersistedTurn{}, fmt.Errorf("beginning user message transaction: %w", err)
	}
	defer tx.Rollback()
	if err := requireConversation(tx, conversationID); err != nil {
		return PersistedTurn{}, err
	}
	turnSequence, err := nextSequence(tx, "conversation_turns", conversationID)
	if err != nil {
		return PersistedTurn{}, err
	}
	messageSequence, err := nextSequence(tx, "conversation_messages", conversationID)
	if err != nil {
		return PersistedTurn{}, err
	}
	if _, err := tx.Exec("INSERT INTO conversation_turns(id, conversation_id, sequence, status, extraction_state, created_at_ms, updated_at_ms) VALUES (?1, ?2, ?3, 'interpreting', 'ineligible', ?4, ?4)", turnID, conversationID, turnSequence, now); err != nil {
		return PersistedTurn{}, fmt.Errorf("creating turn: %w", err)
	}
	if _, err := tx.Exec("INSERT INTO conversation_messages(id, conversation_id, turn_id, sequence, role, content, created_at_ms) VALUES (?1, ?2, ?3, ?4, 'user', ?5, ?6)", messageID, conversationID, turnID, messageSequence, userMessage, now); err != nil {
		return PersistedTurn{}, fmt.Errorf("writing user message: %w", err)
	}
	if err := touchConversation(tx, conversationID, now); err != nil {
		return PersistedTurn{}, err
	}
	if err := tx.Commit(); err != nil {
		return PersistedTurn{}, fmt.Errorf("committing user message transaction: %w", err)
	}
	return PersistedTurn{ID: turnID, ConversationID: conversationID, UserMessage: MessageRecord{ID: messageID, ConversationID: conversationID, TurnID: turnID, Sequence: uint64(messageSequence), Role: "user", Content: userMessage, CreatedAtUnixMS: now}}, nil
}

func (s *Store) CompleteTurn(conversationID string, turnID string, assistantMessage string) (MessageRecord, error) {
	if err := validateID("conversation_id", conversationID); err != nil {
		return MessageRecord{}, err
	}
	if err := validateID("turn_id", turnID); err != nil {
		return MessageRecord{}, err
	}
	if err := validateContent("assistant message", assistantMessage); err != nil {
		return MessageRecord{}, err
	}
	db, err := s.openWrite()
	if err != nil {
		return MessageRecord{}, err
	}
	defer db.Close()
	now := nowUnixMS()
	messageID := newID()
	tx, err := db.Begin()
	if err != nil {
		return MessageRecord{}, fmt.Errorf("beginning assistant message transaction: %w", err)
	}
	defer tx.Rollback()
	changed, err := tx.Exec("UPDATE conversation_turns SET status = 'completed', extraction_state = 'pending', updated_at_ms = ?3 WHERE id = ?1 AND conversation_id = ?2 AND status IN ('interpreting', 'planning', 'responding')", turnID, conversationID, now)
	if err != nil {
		return MessageRecord{}, fmt.Errorf("updating turn completion: %w", err)
	}
	count, err := changed.RowsAffected()
	if err != nil {
		return MessageRecord{}, fmt.Errorf("checking turn completion update: %w", err)
	}
	if count != 1 {
		return MessageRecord{}, errors.New("turn does not belong to conversation or is terminal")
	}
	messageSequence, err := nextSequence(tx, "conversation_messages", conversationID)
	if err != nil {
		return MessageRecord{}, err
	}
	if _, err := tx.Exec("INSERT INTO conversation_messages(id, conversation_id, turn_id, sequence, role, content, created_at_ms) VALUES (?1, ?2, ?3, ?4, 'assistant', ?5, ?6)", messageID, conversationID, turnID, messageSequence, assistantMessage, now); err != nil {
		return MessageRecord{}, fmt.Errorf("writing assistant message: %w", err)
	}
	if err := touchConversation(tx, conversationID, now); err != nil {
		return MessageRecord{}, err
	}
	if err := tx.Commit(); err != nil {
		return MessageRecord{}, fmt.Errorf("committing assistant message transaction: %w", err)
	}
	return MessageRecord{ID: messageID, ConversationID: conversationID, TurnID: turnID, Sequence: uint64(messageSequence), Role: "assistant", Content: assistantMessage, CreatedAtUnixMS: now}, nil
}

func (s *Store) FailTurn(conversationID string, turnID string, code string, message string, retryable bool) error {
	if err := validateID("conversation_id", conversationID); err != nil {
		return err
	}
	if err := validateID("turn_id", turnID); err != nil {
		return err
	}
	if err := validateContent("error code", code); err != nil {
		return err
	}
	if err := validateContent("error message", message); err != nil {
		return err
	}
	db, err := s.openWrite()
	if err != nil {
		return err
	}
	defer db.Close()
	retryableInt := 0
	if retryable {
		retryableInt = 1
	}
	changed, err := db.Exec("UPDATE conversation_turns SET status = 'failed', extraction_state = 'ineligible', error_code = ?3, error_message = ?4, error_retryable = ?5, updated_at_ms = ?6 WHERE id = ?1 AND conversation_id = ?2 AND status IN ('interpreting', 'planning', 'responding')", turnID, conversationID, code, message, retryableInt, nowUnixMS())
	if err != nil {
		return fmt.Errorf("marking turn failed: %w", err)
	}
	count, err := changed.RowsAffected()
	if err != nil {
		return fmt.Errorf("checking failed turn update: %w", err)
	}
	if count != 1 {
		return errors.New("turn does not belong to conversation or is terminal")
	}
	return nil
}

func (s *Store) openWrite() (*sql.DB, error) {
	if s == nil || s.path == "" {
		return nil, ErrDatabasePathEmpty
	}
	if parent := filepath.Dir(s.path); parent != "." {
		if err := os.MkdirAll(parent, 0o700); err != nil {
			return nil, fmt.Errorf("creating memory database directory: %w", err)
		}
	}
	db, err := sql.Open(driverName, s.path)
	if err != nil {
		return nil, fmt.Errorf("opening memory database: %w", err)
	}
	if _, err := db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		db.Close()
		return nil, fmt.Errorf("enabling memory database foreign keys: %w", err)
	}
	return db, nil
}

func initializeSchema(db *sql.DB) error {
	_, err := db.Exec(`
CREATE TABLE IF NOT EXISTS schema_meta(singleton INTEGER PRIMARY KEY CHECK(singleton = 1), version INTEGER NOT NULL);
INSERT OR IGNORE INTO schema_meta(singleton, version) VALUES (1, 3);
CREATE TABLE IF NOT EXISTS conversations(id TEXT PRIMARY KEY, character_id TEXT NOT NULL, created_at_ms INTEGER NOT NULL, updated_at_ms INTEGER NOT NULL);
CREATE INDEX IF NOT EXISTS conversations_character_updated ON conversations(character_id, updated_at_ms DESC, id ASC);
CREATE TABLE IF NOT EXISTS conversation_turns(id TEXT PRIMARY KEY, conversation_id TEXT NOT NULL REFERENCES conversations(id), sequence INTEGER NOT NULL CHECK(sequence > 0), status TEXT NOT NULL CHECK(status IN ('interpreting', 'planning', 'responding', 'completed', 'interrupted', 'failed')), error_code TEXT, error_message TEXT, error_retryable INTEGER, extraction_state TEXT NOT NULL DEFAULT 'ineligible' CHECK(extraction_state IN ('ineligible', 'pending', 'claimed', 'processed')), created_at_ms INTEGER NOT NULL, updated_at_ms INTEGER NOT NULL, UNIQUE(conversation_id, sequence));
CREATE TABLE IF NOT EXISTS conversation_messages(id TEXT PRIMARY KEY, conversation_id TEXT NOT NULL REFERENCES conversations(id), turn_id TEXT NOT NULL REFERENCES conversation_turns(id), sequence INTEGER NOT NULL CHECK(sequence > 0), role TEXT NOT NULL CHECK(role IN ('user', 'assistant')), content TEXT NOT NULL, created_at_ms INTEGER NOT NULL, UNIQUE(conversation_id, sequence), UNIQUE(turn_id, role));
CREATE TABLE IF NOT EXISTS prompt_windows(conversation_id TEXT PRIMARY KEY REFERENCES conversations(id), revision INTEGER NOT NULL CHECK(revision > 0), summary TEXT, cutoff_message_sequence INTEGER NOT NULL DEFAULT 0 CHECK(cutoff_message_sequence >= 0), updated_at_ms INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS personal_memories(id TEXT PRIMARY KEY, kind TEXT NOT NULL, scope_kind TEXT NOT NULL, character_id TEXT, review_status TEXT NOT NULL, content TEXT NOT NULL, status TEXT NOT NULL, confidence_basis_points INTEGER NOT NULL, source_conversation_id TEXT NOT NULL, source_turn_id TEXT NOT NULL, supersedes_id TEXT REFERENCES personal_memories(id), created_at_ms INTEGER NOT NULL, updated_at_ms INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS knowledge_entries(id TEXT PRIMARY KEY, topic TEXT NOT NULL, statement TEXT NOT NULL, status TEXT NOT NULL, verification_basis TEXT NOT NULL, confidence_basis_points INTEGER NOT NULL, source_conversation_id TEXT NOT NULL, source_turn_id TEXT NOT NULL, supersedes_id TEXT REFERENCES knowledge_entries(id), created_at_ms INTEGER NOT NULL, updated_at_ms INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS knowledge_sources(knowledge_id TEXT NOT NULL REFERENCES knowledge_entries(id), source_id TEXT NOT NULL, title TEXT NOT NULL, url TEXT NOT NULL, snippet TEXT NOT NULL, rank INTEGER NOT NULL, fetched_at_ms INTEGER NOT NULL, PRIMARY KEY(knowledge_id, source_id));
CREATE TABLE IF NOT EXISTS extraction_batches(id TEXT PRIMARY KEY, conversation_id TEXT NOT NULL REFERENCES conversations(id), character_id TEXT NOT NULL, status TEXT NOT NULL CHECK(status IN ('pending', 'running', 'succeeded', 'failed', 'cancelled')), first_turn_sequence INTEGER NOT NULL CHECK(first_turn_sequence > 0), last_turn_sequence INTEGER NOT NULL CHECK(last_turn_sequence >= first_turn_sequence), error_code TEXT, error_message TEXT, error_retryable INTEGER, created_at_ms INTEGER NOT NULL, updated_at_ms INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS extraction_batch_turns(batch_id TEXT NOT NULL REFERENCES extraction_batches(id), turn_id TEXT NOT NULL REFERENCES conversation_turns(id), turn_sequence INTEGER NOT NULL CHECK(turn_sequence > 0), PRIMARY KEY(batch_id, turn_id), UNIQUE(batch_id, turn_sequence));
CREATE UNIQUE INDEX IF NOT EXISTS extraction_batches_one_running ON extraction_batches(conversation_id) WHERE status = 'running';
CREATE VIRTUAL TABLE IF NOT EXISTS personal_memories_fts USING fts5(
  content,
  content='personal_memories',
  content_rowid='rowid',
  tokenize='trigram'
);
CREATE TRIGGER IF NOT EXISTS personal_memories_ai AFTER INSERT ON personal_memories BEGIN
  INSERT INTO personal_memories_fts(rowid, content) VALUES (new.rowid, new.content);
END;
CREATE TRIGGER IF NOT EXISTS personal_memories_ad AFTER DELETE ON personal_memories BEGIN
  INSERT INTO personal_memories_fts(personal_memories_fts, rowid, content)
  VALUES ('delete', old.rowid, old.content);
END;
CREATE TRIGGER IF NOT EXISTS personal_memories_au AFTER UPDATE OF content ON personal_memories BEGIN
  INSERT INTO personal_memories_fts(personal_memories_fts, rowid, content)
  VALUES ('delete', old.rowid, old.content);
  INSERT INTO personal_memories_fts(rowid, content) VALUES (new.rowid, new.content);
END;
CREATE VIRTUAL TABLE IF NOT EXISTS knowledge_entries_fts USING fts5(
  topic,
  statement,
  content='knowledge_entries',
  content_rowid='rowid',
  tokenize='trigram'
);
CREATE TRIGGER IF NOT EXISTS knowledge_entries_ai AFTER INSERT ON knowledge_entries BEGIN
  INSERT INTO knowledge_entries_fts(rowid, topic, statement)
  VALUES (new.rowid, new.topic, new.statement);
END;
CREATE TRIGGER IF NOT EXISTS knowledge_entries_ad AFTER DELETE ON knowledge_entries BEGIN
  INSERT INTO knowledge_entries_fts(knowledge_entries_fts, rowid, topic, statement)
  VALUES ('delete', old.rowid, old.topic, old.statement);
END;
CREATE TRIGGER IF NOT EXISTS knowledge_entries_au AFTER UPDATE OF topic, statement ON knowledge_entries BEGIN
  INSERT INTO knowledge_entries_fts(knowledge_entries_fts, rowid, topic, statement)
  VALUES ('delete', old.rowid, old.topic, old.statement);
  INSERT INTO knowledge_entries_fts(rowid, topic, statement)
  VALUES (new.rowid, new.topic, new.statement);
END;
`)
	if err != nil {
		return fmt.Errorf("initializing memory schema: %w", err)
	}
	return nil
}

func recentConversationID(tx *sql.Tx, characterID string) (string, error) {
	var id string
	err := tx.QueryRow("SELECT id FROM conversations WHERE character_id = ?1 ORDER BY updated_at_ms DESC, id ASC LIMIT 1", characterID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("reading recent conversation: %w", err)
	}
	return id, nil
}

func loadConversation(db *sql.DB, conversationID string) (ConversationBootstrap, error) {
	var conversation ConversationRecord
	if err := db.QueryRow("SELECT id, character_id, created_at_ms, updated_at_ms FROM conversations WHERE id = ?1", conversationID).Scan(&conversation.ID, &conversation.CharacterID, &conversation.CreatedAtUnixMS, &conversation.UpdatedAtUnixMS); err != nil {
		return ConversationBootstrap{}, fmt.Errorf("loading conversation: %w", err)
	}
	var prompt PromptWindowRecord
	var summary sql.NullString
	if err := db.QueryRow("SELECT conversation_id, revision, summary, cutoff_message_sequence, updated_at_ms FROM prompt_windows WHERE conversation_id = ?1", conversationID).Scan(&prompt.ConversationID, &prompt.Revision, &summary, &prompt.CutoffMessageSequence, &prompt.UpdatedAtUnixMS); err != nil {
		return ConversationBootstrap{}, fmt.Errorf("loading prompt window: %w", err)
	}
	if summary.Valid {
		prompt.Summary = &summary.String
	}
	rows, err := db.Query("SELECT id, conversation_id, turn_id, sequence, role, content, created_at_ms FROM conversation_messages WHERE conversation_id = ?1 ORDER BY sequence ASC", conversationID)
	if err != nil {
		return ConversationBootstrap{}, fmt.Errorf("loading conversation messages: %w", err)
	}
	defer rows.Close()
	messages := make([]MessageRecord, 0)
	for rows.Next() {
		var message MessageRecord
		if err := rows.Scan(&message.ID, &message.ConversationID, &message.TurnID, &message.Sequence, &message.Role, &message.Content, &message.CreatedAtUnixMS); err != nil {
			return ConversationBootstrap{}, fmt.Errorf("scanning conversation message: %w", err)
		}
		messages = append(messages, message)
	}
	if err := rows.Err(); err != nil {
		return ConversationBootstrap{}, fmt.Errorf("iterating conversation messages: %w", err)
	}
	return ConversationBootstrap{Conversation: conversation, Messages: messages, PromptWindow: prompt}, nil
}

func requireConversation(tx *sql.Tx, conversationID string) error {
	var exists bool
	if err := tx.QueryRow("SELECT EXISTS(SELECT 1 FROM conversations WHERE id = ?1)", conversationID).Scan(&exists); err != nil {
		return fmt.Errorf("checking conversation: %w", err)
	}
	if !exists {
		return errors.New("conversation does not exist")
	}
	return nil
}

func nextSequence(tx *sql.Tx, table string, conversationID string) (int64, error) {
	var maxSequence int64
	query := "SELECT COALESCE(MAX(sequence), 0) FROM " + table + " WHERE conversation_id = ?1"
	if err := tx.QueryRow(query, conversationID).Scan(&maxSequence); err != nil {
		return 0, fmt.Errorf("reading next sequence from %s: %w", table, err)
	}
	return maxSequence + 1, nil
}

func touchConversation(tx *sql.Tx, conversationID string, now int64) error {
	if _, err := tx.Exec("UPDATE conversations SET updated_at_ms = ?2 WHERE id = ?1", conversationID, now); err != nil {
		return fmt.Errorf("touching conversation: %w", err)
	}
	return nil
}

func validateID(label string, value string) error {
	if value == "" || strings.TrimSpace(value) != value || containsDisallowedControl(value) {
		return fmt.Errorf("%s is invalid", label)
	}
	return nil
}

func validateContent(label string, value string) error {
	if value == "" || strings.TrimSpace(value) == "" || containsDisallowedControl(value) {
		return fmt.Errorf("%s is invalid", label)
	}
	return nil
}

func containsDisallowedControl(value string) bool {
	for _, character := range value {
		if character == 0 || character < 32 && character != '\n' && character != '\r' && character != '\t' {
			return true
		}
	}
	return false
}

func nowUnixMS() int64 {
	return time.Now().UnixMilli()
}

func newID() string {
	var data [16]byte
	if _, err := rand.Read(data[:]); err != nil {
		panic(err)
	}
	data[6] = (data[6] & 0x0f) | 0x40
	data[8] = (data[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(data[:])
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32]
}
