package postgres

import (
	"context"
	"errors"
	"fmt"

	domainmemory "fairy/internal/domain/memory"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type scanner interface {
	Scan(dest ...any) error
}

type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

func ListPersonalMemories(ctx context.Context, db Querier, scopeKind, characterID, reviewStatus string) ([]domainmemory.PersonalMemoryRecord, error) {
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
	records := make([]domainmemory.PersonalMemoryRecord, 0)
	for rows.Next() {
		record, err := ScanPersonalMemory(rows)
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

func PersonalMemoryByID(ctx context.Context, tx pgx.Tx, id string, forUpdate bool) (domainmemory.PersonalMemoryRecord, error) {
	query := "SELECT id, kind, scope_kind, character_id, review_status, content, status, confidence_basis_points, source_conversation_id, source_turn_id, supersedes_id, created_at_ms, updated_at_ms FROM personal_memories WHERE id = $1"
	if forUpdate {
		query += " FOR UPDATE"
	}
	return ScanPersonalMemory(tx.QueryRow(ctx, query, id))
}

func ScanPersonalMemory(row scanner) (domainmemory.PersonalMemoryRecord, error) {
	var record domainmemory.PersonalMemoryRecord
	var scopeKind string
	var characterID pgtype.Text
	var confidence int
	var supersedesID pgtype.Text
	if err := row.Scan(&record.ID, &record.Kind, &scopeKind, &characterID, &record.ReviewStatus, &record.Content, &record.Status, &confidence, &record.SourceConversationID, &record.SourceTurnID, &supersedesID, &record.CreatedAtUnixMS, &record.UpdatedAtUnixMS); err != nil {
		return domainmemory.PersonalMemoryRecord{}, fmt.Errorf("scanning personal memory: %w", err)
	}
	if confidence < 0 || confidence > 10000 {
		return domainmemory.PersonalMemoryRecord{}, errors.New("memory confidence is invalid")
	}
	record.ConfidenceBasisPoints = uint16(confidence)
	record.Scope = domainmemory.MemoryScope{Type: scopeKind}
	if characterID.Valid {
		record.Scope.CharacterID = characterID.String
	}
	if supersedesID.Valid {
		record.SupersedesID = &supersedesID.String
	}
	return record, nil
}

func InsertPersonalMemory(
	ctx context.Context,
	tx pgx.Tx,
	id, kind string,
	scope domainmemory.MemoryScope,
	content string,
	confidence uint16,
	sourceConversationID, sourceTurnID string,
	supersedesID *string,
	now int64,
	enqueueEmbedding func(context.Context, pgx.Tx, string, string, int64) error,
) (domainmemory.PersonalMemoryRecord, error) {
	scopeKind, characterID, reviewStatus := domainmemory.MemoryScopeColumns(scope)
	_, err := tx.Exec(ctx, "INSERT INTO personal_memories(id, kind, scope_kind, character_id, review_status, content, status, confidence_basis_points, source_conversation_id, source_turn_id, supersedes_id, created_at_ms, updated_at_ms) VALUES ($1, $2, $3, $4, $5, $6, 'active', $7, $8, $9, $10, $11, $11)", id, kind, scopeKind, characterID, reviewStatus, content, int(confidence), sourceConversationID, sourceTurnID, supersedesID, now)
	if err != nil {
		return domainmemory.PersonalMemoryRecord{}, fmt.Errorf("inserting personal memory: %w", err)
	}
	if reviewStatus == "ready" {
		if err := enqueueEmbedding(ctx, tx, id, content, now); err != nil {
			return domainmemory.PersonalMemoryRecord{}, err
		}
	}
	return domainmemory.PersonalMemoryRecord{
		ID: id, Kind: kind, Scope: scope, ReviewStatus: reviewStatus, Content: content, Status: "active",
		ConfidenceBasisPoints: confidence, SourceConversationID: sourceConversationID, SourceTurnID: sourceTurnID,
		SupersedesID: supersedesID, CreatedAtUnixMS: now, UpdatedAtUnixMS: now,
	}, nil
}

func LatestMemorySource(ctx context.Context, tx pgx.Tx, scope domainmemory.MemoryScope) (string, string, error) {
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
