package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"
)

type UsageLedgerRow struct {
	ConversationID  string
	TurnID          string
	EventType       string
	State           *string
	MetadataJSON    string
	CreatedAtUnixMS int64
}

func LoadConversationCharacters(ctx context.Context, db Querier) (map[string]string, error) {
	rows, err := db.Query(ctx, "SELECT id, character_id FROM conversations")
	if err != nil {
		return nil, fmt.Errorf("querying conversations for usage report: %w", err)
	}
	defer rows.Close()
	characterByConversation := make(map[string]string)
	for rows.Next() {
		var id string
		var characterID string
		if err := rows.Scan(&id, &characterID); err != nil {
			return nil, fmt.Errorf("scanning conversation for usage report: %w", err)
		}
		characterByConversation[id] = characterID
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating conversations for usage report: %w", err)
	}
	return characterByConversation, nil
}

func LoadUsageLedgerEvents(ctx context.Context, db Querier) ([]UsageLedgerRow, error) {
	rows, err := db.Query(ctx, "SELECT conversation_id, turn_id, event_type, state, metadata_json::text, created_at_ms FROM turn_runtime_events WHERE event_type IN ('model', 'terminal') ORDER BY created_at_ms ASC, sequence ASC")
	if err != nil {
		return nil, fmt.Errorf("querying runtime usage events: %w", err)
	}
	defer rows.Close()
	ledgerRows := make([]UsageLedgerRow, 0)
	for rows.Next() {
		var row UsageLedgerRow
		var state pgtype.Text
		if err := rows.Scan(&row.ConversationID, &row.TurnID, &row.EventType, &state, &row.MetadataJSON, &row.CreatedAtUnixMS); err != nil {
			return nil, fmt.Errorf("scanning runtime usage event: %w", err)
		}
		row.State = stringPtrFromPGText(state)
		ledgerRows = append(ledgerRows, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating runtime usage events: %w", err)
	}
	return ledgerRows, nil
}
