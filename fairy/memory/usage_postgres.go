package memory

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"
)

func (s *Store) aggregateTokenUsagePostgres(ctx context.Context, limit int) (UsageReport, error) {
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	characterRows, err := s.pool.Raw().Query(queryCtx, "SELECT id, character_id FROM conversations")
	if err != nil {
		return UsageReport{}, fmt.Errorf("querying conversations for usage report: %w", err)
	}
	characterByConversation := make(map[string]string)
	for characterRows.Next() {
		var id string
		var characterID string
		if err := characterRows.Scan(&id, &characterID); err != nil {
			characterRows.Close()
			return UsageReport{}, fmt.Errorf("scanning conversation for usage report: %w", err)
		}
		characterByConversation[id] = characterID
	}
	if err := characterRows.Err(); err != nil {
		characterRows.Close()
		return UsageReport{}, fmt.Errorf("iterating conversations for usage report: %w", err)
	}
	characterRows.Close()

	rows, err := s.pool.Raw().Query(queryCtx, "SELECT conversation_id, turn_id, event_type, state, metadata_json::text, created_at_ms FROM turn_runtime_events WHERE event_type IN ('model', 'terminal') ORDER BY created_at_ms ASC, sequence ASC")
	if err != nil {
		return UsageReport{}, fmt.Errorf("querying runtime usage events: %w", err)
	}
	defer rows.Close()
	ledgerRows := make([]usageLedgerRow, 0)
	for rows.Next() {
		var row usageLedgerRow
		var state pgtype.Text
		if err := rows.Scan(&row.conversationID, &row.turnID, &row.eventType, &state, &row.metadataJSON, &row.createdAtUnixMS); err != nil {
			return UsageReport{}, fmt.Errorf("scanning runtime usage event: %w", err)
		}
		row.state = stringPtrFromPGText(state)
		ledgerRows = append(ledgerRows, row)
	}
	if err := rows.Err(); err != nil {
		return UsageReport{}, fmt.Errorf("iterating runtime usage events: %w", err)
	}
	return aggregateUsageRows(characterByConversation, ledgerRows, limit)
}
