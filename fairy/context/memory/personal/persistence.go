package personal

import (
	"context"
	"errors"
	"fairy/runtime/embedding"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type scanner interface {
	Scan(dest ...any) error
}

type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

type RowQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func List(ctx context.Context, db Querier, scopeKind, characterID, reviewStatus string) ([]Record, error) {
	var rows pgx.Rows
	var err error
	if characterID == "" {
		rows, err = db.Query(ctx, "SELECT id, kind, scope_kind, character_id, review_status, content, status, confidence_basis_points, source_conversation_id, source_turn_id, supersedes_id, created_at_ms, updated_at_ms FROM personal_memories WHERE scope_kind = $1 AND character_id IS NULL AND review_status = $2 AND status = 'active' ORDER BY updated_at_ms DESC, id ASC LIMIT 100", scopeKind, reviewStatus)
	} else {
		rows, err = db.Query(ctx, "SELECT id, kind, scope_kind, character_id, review_status, content, status, confidence_basis_points, source_conversation_id, source_turn_id, supersedes_id, created_at_ms, updated_at_ms FROM personal_memories WHERE scope_kind = $1 AND character_id = $2 AND review_status = $3 AND status = 'active' ORDER BY updated_at_ms DESC, id ASC LIMIT 100", scopeKind, characterID, reviewStatus)
	}
	if err != nil {
		return nil, fmt.Errorf("querying personal memories: %w", err)
	}
	defer rows.Close()
	records := make([]Record, 0)
	for rows.Next() {
		record, err := ScanRecord(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating personal memories: %w", err)
	}
	return records, nil
}

func ByID(ctx context.Context, db RowQuerier, id string, forUpdate bool) (Record, error) {
	query := "SELECT id, kind, scope_kind, character_id, review_status, content, status, confidence_basis_points, source_conversation_id, source_turn_id, supersedes_id, created_at_ms, updated_at_ms FROM personal_memories WHERE id = $1"
	if forUpdate {
		query += " FOR UPDATE"
	}
	return ScanRecord(db.QueryRow(ctx, query, id))
}

func ScanRecord(row scanner) (Record, error) {
	var record Record
	var scopeKind string
	var characterID pgtype.Text
	var confidence int
	var supersedesID pgtype.Text
	if err := row.Scan(&record.ID, &record.Kind, &scopeKind, &characterID, &record.ReviewStatus, &record.Content, &record.Status, &confidence, &record.SourceConversationID, &record.SourceTurnID, &supersedesID, &record.CreatedAtUnixMS, &record.UpdatedAtUnixMS); err != nil {
		return Record{}, fmt.Errorf("scanning personal memory: %w", err)
	}
	if confidence < 0 || confidence > 10000 {
		return Record{}, errors.New("memory confidence is invalid")
	}
	record.ConfidenceBasisPoints = uint16(confidence)
	record.Scope = Scope{Type: scopeKind}
	if characterID.Valid {
		record.Scope.CharacterID = characterID.String
	}
	if supersedesID.Valid {
		record.SupersedesID = &supersedesID.String
	}
	return record, nil
}

func Insert(
	ctx context.Context,
	tx pgx.Tx,
	id, kind string,
	scope Scope,
	content string,
	confidence uint16,
	sourceConversationID, sourceTurnID string,
	supersedesID *string,
	now int64,
	embedding embedding.EmbeddingValue,
) (Record, error) {
	scopeKind, characterID, reviewStatus := ScopeColumns(scope)
	if reviewStatus != "ready" && embedding.Enabled() {
		return Record{}, errors.New("needs-review memory cannot have an embedding")
	}
	if err := embedding.Validate(); err != nil {
		return Record{}, err
	}
	var modelID, contentHash any
	var vector any
	if embedding.Enabled() {
		modelID = embedding.ModelID
		contentHash = embedding.ContentHash
		vector = embedding.Vector.String()
	}
	_, err := tx.Exec(ctx, `
INSERT INTO personal_memories(
  id, kind, scope_kind, character_id, review_status, content, status,
  confidence_basis_points, source_conversation_id, source_turn_id,
  supersedes_id, embedding_model_id_v2, embedding_content_hash_v2, embedding_v2,
  created_at_ms, updated_at_ms
) VALUES (
  $1, $2, $3, $4, $5, $6, 'active',
  $7, $8, $9, $10, $11, $12, $13::public.vector,
  $14, $14
)`, id, kind, scopeKind, characterID, reviewStatus, content, int(confidence), sourceConversationID, sourceTurnID, supersedesID, modelID, contentHash, vector, now)
	if err != nil {
		return Record{}, fmt.Errorf("inserting personal memory: %w", err)
	}
	return Record{
		ID: id, Kind: kind, Scope: scope, ReviewStatus: reviewStatus, Content: content, Status: "active",
		ConfidenceBasisPoints: confidence, SourceConversationID: sourceConversationID, SourceTurnID: sourceTurnID,
		SupersedesID: supersedesID, CreatedAtUnixMS: now, UpdatedAtUnixMS: now,
	}, nil
}

func LatestSource(ctx context.Context, tx pgx.Tx, scope Scope) (string, string, error) {
	var conversationID, turnID string
	var err error
	if scope.Type == "character" {
		err = tx.QueryRow(ctx, "SELECT c.id, t.id FROM conversations c JOIN conversation_turns t ON t.conversation_id = c.id WHERE c.character_id = $1 ORDER BY c.updated_at_ms DESC, t.sequence DESC LIMIT 1", scope.CharacterID).Scan(&conversationID, &turnID)
	} else {
		err = tx.QueryRow(ctx, "SELECT c.id, t.id FROM conversations c JOIN conversation_turns t ON t.conversation_id = c.id ORDER BY c.updated_at_ms DESC, t.sequence DESC LIMIT 1").Scan(&conversationID, &turnID)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", errors.New("memory write requires an existing conversation turn")
	}
	if err != nil {
		return "", "", fmt.Errorf("reading memory source turn: %w", err)
	}
	return conversationID, turnID, nil
}
