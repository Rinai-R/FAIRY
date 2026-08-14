package personal

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"fairy/runtime/embedding"
)

const seekDBRecordColumns = `
id, kind, scope_kind, character_id, review_status, content, status,
confidence_basis_points, source_conversation_id, source_turn_id,
supersedes_id, created_at_ms, updated_at_ms`

const seekDBPersonalMemoryWriteGuardID = 1

func normalizedContentHash(content string) [sha256.Size]byte {
	return sha256.Sum256([]byte(NormalizeContent(content)))
}

// LockSeekDBMutationGuardTx serializes personal-memory mutations on the
// singleton revision-eight guard row. Callers must acquire it before locking
// any personal_memories row so inserts and duplicate revalidation share one
// provable lock order.
func (s *Store) LockSeekDBMutationGuardTx(ctx context.Context, tx *sql.Tx) error {
	if !s.usesSeekDB() {
		return ErrStoreBackendUnavailable
	}
	if tx == nil {
		return ErrSeekDBTransactionEmpty
	}
	var id int
	if err := tx.QueryRowContext(ctx, `
SELECT id
FROM personal_memory_write_guard
WHERE id = ?
FOR UPDATE`, seekDBPersonalMemoryWriteGuardID).Scan(&id); err != nil {
		return fmt.Errorf("locking SeekDB personal memory mutation guard: %w", err)
	}
	if id != seekDBPersonalMemoryWriteGuardID {
		return errors.New("SeekDB personal memory mutation guard is invalid")
	}
	return nil
}

func validateSeekDBID(label, value string) error {
	if value == "" || strings.TrimSpace(value) != value || !utf8.ValidString(value) || utf8.RuneCountInString(value) > 128 {
		return fmt.Errorf("%s is invalid", label)
	}
	for _, character := range value {
		if character > unicode.MaxASCII || unicode.IsControl(character) {
			return fmt.Errorf("%s is invalid", label)
		}
	}
	return nil
}

func validateSeekDBEvidenceID(value string) error {
	if value == "" || strings.TrimSpace(value) != value || !utf8.ValidString(value) || utf8.RuneCountInString(value) > 128 {
		return errors.New("memory evidence id is invalid")
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return errors.New("memory evidence id is invalid")
		}
	}
	return nil
}

func validateSeekDBScope(scope Scope) error {
	if scope.Type == "character" {
		return validateSeekDBID("character_id", scope.CharacterID)
	}
	if scope.CharacterID != "" {
		return errors.New("non-character memory scope must not contain character_id")
	}
	return nil
}

func scanSeekDBRecord(row scanner) (Record, error) {
	var (
		record       Record
		scopeKind    string
		characterID  sql.NullString
		confidence   int64
		supersedesID sql.NullString
	)
	if err := row.Scan(
		&record.ID, &record.Kind, &scopeKind, &characterID, &record.ReviewStatus,
		&record.Content, &record.Status, &confidence, &record.SourceConversationID,
		&record.SourceTurnID, &supersedesID, &record.CreatedAtUnixMS, &record.UpdatedAtUnixMS,
	); err != nil {
		return Record{}, fmt.Errorf("scanning SeekDB personal memory: %w", err)
	}
	if confidence < 0 || confidence > 10000 {
		return Record{}, errors.New("memory confidence is invalid")
	}
	if err := ValidatePersistedContent(record.ID, record.Content); err != nil {
		return Record{}, err
	}
	record.ConfidenceBasisPoints = uint16(confidence)
	record.Scope = Scope{Type: scopeKind}
	if characterID.Valid {
		record.Scope.CharacterID = characterID.String
	}
	if supersedesID.Valid {
		value := supersedesID.String
		record.SupersedesID = &value
	}
	return record, nil
}

func listSeekDBRecords(ctx context.Context, database *sql.DB, scopeKind, characterID, reviewStatus string) ([]Record, error) {
	query := "SELECT " + seekDBRecordColumns + `
FROM personal_memories
WHERE scope_kind = ? AND character_id IS NULL AND review_status = ? AND status = 'active'
ORDER BY updated_at_ms DESC, id ASC
LIMIT 100`
	arguments := []any{scopeKind, reviewStatus}
	if characterID != "" {
		query = "SELECT " + seekDBRecordColumns + `
FROM personal_memories
WHERE scope_kind = ? AND character_id = ? AND review_status = ? AND status = 'active'
ORDER BY updated_at_ms DESC, id ASC
LIMIT 100`
		arguments = []any{scopeKind, characterID, reviewStatus}
	}
	rows, err := database.QueryContext(ctx, query, arguments...)
	if err != nil {
		return nil, fmt.Errorf("querying SeekDB personal memories: %w", err)
	}
	defer rows.Close()
	records := make([]Record, 0)
	for rows.Next() {
		record, err := scanSeekDBRecord(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating SeekDB personal memories: %w", err)
	}
	return records, nil
}

func selectSeekDBRecord(ctx context.Context, database interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id string, forUpdate bool) (Record, error) {
	query := "SELECT " + seekDBRecordColumns + " FROM personal_memories WHERE id = ?"
	if forUpdate {
		query += " FOR UPDATE"
	}
	return scanSeekDBRecord(database.QueryRowContext(ctx, query, id))
}

func latestSourceSeekDB(ctx context.Context, tx *sql.Tx, scope Scope) (string, string, error) {
	query := `
SELECT conversation.id
FROM conversations AS conversation
WHERE EXISTS (
  SELECT 1 FROM conversation_turns AS turn WHERE turn.conversation_id = conversation.id
)`
	arguments := make([]any, 0, 1)
	if scope.Type == "character" {
		query += " AND conversation.character_id = ?"
		arguments = append(arguments, scope.CharacterID)
	}
	query += `
ORDER BY conversation.updated_at_ms DESC, conversation.id ASC
LIMIT 1
FOR UPDATE`
	var conversationID string
	err := tx.QueryRowContext(ctx, query, arguments...).Scan(&conversationID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", errors.New("memory write requires an existing conversation turn")
	}
	if err != nil {
		return "", "", fmt.Errorf("locking SeekDB memory source conversation: %w", err)
	}
	var turnID string
	if err := tx.QueryRowContext(ctx, `
SELECT id
FROM conversation_turns
WHERE conversation_id = ?
ORDER BY sequence DESC, id ASC
LIMIT 1
FOR UPDATE`, conversationID).Scan(&turnID); err != nil {
		return "", "", fmt.Errorf("locking SeekDB memory source turn: %w", err)
	}
	return conversationID, turnID, nil
}

func lockSeekDBSourceTurn(
	ctx context.Context,
	tx *sql.Tx,
	conversationID, turnID string,
) error {
	if err := validateSeekDBID("source_conversation_id", conversationID); err != nil {
		return err
	}
	if err := validateSeekDBID("source_turn_id", turnID); err != nil {
		return err
	}
	var found int
	if err := tx.QueryRowContext(ctx, `
SELECT 1 FROM conversations WHERE id = ? FOR UPDATE`, conversationID).Scan(&found); errors.Is(err, sql.ErrNoRows) {
		return errors.New("personal memory source conversation does not exist")
	} else if err != nil {
		return fmt.Errorf("locking SeekDB personal memory source conversation: %w", err)
	}
	if err := tx.QueryRowContext(ctx, `
SELECT 1
FROM conversation_turns
WHERE conversation_id = ? AND id = ?
FOR UPDATE`, conversationID, turnID).Scan(&found); errors.Is(err, sql.ErrNoRows) {
		return errors.New("personal memory source turn does not exist")
	} else if err != nil {
		return fmt.Errorf("locking SeekDB personal memory source turn: %w", err)
	}
	return nil
}

func seekDBEmbeddingTuple(content string, value embedding.EmbeddingValue) (any, any, any, error) {
	if err := value.Validate(); err != nil {
		return nil, nil, nil, err
	}
	if !value.Enabled() {
		return nil, nil, nil, nil
	}
	if len(value.ModelID) > 256 || strings.TrimSpace(value.ModelID) != value.ModelID {
		return nil, nil, nil, errors.New("embedding space id is invalid")
	}
	for _, character := range value.ModelID {
		if character > unicode.MaxASCII || unicode.IsControl(character) {
			return nil, nil, nil, errors.New("embedding space id is invalid")
		}
	}
	if value.ContentHash != embedding.ContentHash(content) {
		return nil, nil, nil, errors.New("embedding content hash does not match memory content")
	}
	hash, err := hex.DecodeString(value.ContentHash)
	if err != nil || len(hash) != 32 {
		return nil, nil, nil, errors.New("embedding content hash is invalid")
	}
	return value.ModelID, hash, value.Literal(), nil
}

// InsertSeekDBTx inserts one active personal fact into the caller's short
// SeekDB transaction. Provider calls must already have completed.
func (s *Store) InsertSeekDBTx(
	ctx context.Context,
	tx *sql.Tx,
	id, kind string,
	scope Scope,
	content string,
	confidence uint16,
	sourceConversationID, sourceTurnID string,
	supersedesID *string,
	now int64,
	prepared embedding.EmbeddingValue,
) (Record, error) {
	if !s.usesSeekDB() {
		return Record{}, ErrStoreBackendUnavailable
	}
	if tx == nil {
		return Record{}, ErrSeekDBTransactionEmpty
	}
	// These locks are intentionally defensive. Composite callers acquire the
	// same source and guard locks before any personal record; re-locking them is
	// harmless and keeps direct callers on the single source -> guard -> record
	// protocol instead of letting the INSERT foreign key acquire source locks
	// after the guard.
	if err := lockSeekDBSourceTurn(ctx, tx, sourceConversationID, sourceTurnID); err != nil {
		return Record{}, err
	}
	if err := s.LockSeekDBMutationGuardTx(ctx, tx); err != nil {
		return Record{}, err
	}
	for _, item := range []struct {
		label string
		value string
	}{
		{label: "memory_id", value: id},
		{label: "source_conversation_id", value: sourceConversationID},
		{label: "source_turn_id", value: sourceTurnID},
	} {
		if err := validateSeekDBID(item.label, item.value); err != nil {
			return Record{}, err
		}
	}
	if supersedesID != nil {
		if err := validateSeekDBID("supersedes_id", *supersedesID); err != nil {
			return Record{}, err
		}
		if *supersedesID == id {
			return Record{}, errors.New("personal memory cannot supersede itself")
		}
	}
	if err := ValidateInput(kind, scope, content, confidence); err != nil {
		return Record{}, err
	}
	if err := validateSeekDBScope(scope); err != nil {
		return Record{}, err
	}
	scopeKind, characterID, reviewStatus := ScopeColumns(scope)
	if reviewStatus != "ready" && prepared.Enabled() {
		return Record{}, errors.New("needs-review memory cannot have an embedding")
	}
	spaceID, contentHash, vector, err := seekDBEmbeddingTuple(content, prepared)
	if err != nil {
		return Record{}, err
	}
	if now < 1 {
		return Record{}, errors.New("memory timestamp is invalid")
	}
	normalizedHash := normalizedContentHash(content)
	_, err = tx.ExecContext(ctx, `
INSERT INTO personal_memories(
  id, kind, scope_kind, character_id, review_status, content, status,
  confidence_basis_points, source_conversation_id, source_turn_id, evidence_ids,
  supersedes_id, embedding_space_id, embedding_content_hash, embedding,
  created_at_ms, updated_at_ms, normalized_content_hash
) VALUES (?, ?, ?, ?, ?, ?, 'active', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, kind, scopeKind, characterID, reviewStatus, content, int(confidence),
		sourceConversationID, sourceTurnID, `[]`, supersedesID,
		spaceID, contentHash, vector, now, now, normalizedHash[:],
	)
	if err != nil {
		return Record{}, fmt.Errorf("inserting SeekDB personal memory: %w", err)
	}
	return Record{
		ID: id, Kind: kind, Scope: scope, ReviewStatus: reviewStatus, Content: content, Status: "active",
		ConfidenceBasisPoints: confidence, SourceConversationID: sourceConversationID,
		SourceTurnID: sourceTurnID, SupersedesID: supersedesID,
		CreatedAtUnixMS: now, UpdatedAtUnixMS: now,
	}, nil
}
