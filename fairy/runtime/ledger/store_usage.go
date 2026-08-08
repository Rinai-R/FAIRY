package ledger

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"
)

const usageLedgerStreamQuery = `
SELECT event.conversation_id,
       event.turn_id,
       conversation.character_id,
       event.event_type,
       event.state,
       event.metadata_json::text,
       event.created_at_ms
FROM turn_runtime_events AS event
LEFT JOIN conversations AS conversation ON conversation.id = event.conversation_id
WHERE event.event_type IN ('model', 'terminal')
ORDER BY event.conversation_id ASC, event.turn_id ASC, event.sequence ASC`

func (s *Store) aggregateTokenUsagePostgres(ctx context.Context, limit int) (UsageReport, error) {
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	rows, err := s.pool.Raw().Query(queryCtx, usageLedgerStreamQuery)
	if err != nil {
		return UsageReport{}, fmt.Errorf("querying runtime usage events: %w", err)
	}
	defer rows.Close()

	collector := newUsageReportCollector(limit)
	var current *usageTurnAccumulator
	currentKey := ""
	currentCharacterID := ""
	for rows.Next() {
		var row usageLedgerRow
		var state pgtype.Text
		var characterID pgtype.Text
		if err := rows.Scan(
			&row.conversationID,
			&row.turnID,
			&characterID,
			&row.eventType,
			&state,
			&row.metadataJSON,
			&row.createdAtUnixMS,
		); err != nil {
			return UsageReport{}, fmt.Errorf("scanning runtime usage event: %w", err)
		}
		row.state = stringPtrFromPGText(state)
		key := row.conversationID + "\x00" + row.turnID
		if currentKey != key {
			collector.Add(current, currentCharacterID)
			current = &usageTurnAccumulator{
				conversationID: row.conversationID,
				turnID:         row.turnID,
				status:         usageTurnStatusUnknown,
				lanes:          make(map[string]*UsageLaneAggregate),
			}
			currentKey = key
			currentCharacterID = ""
			if characterID.Valid {
				currentCharacterID = characterID.String
			}
		}
		if err := applyUsageLedgerRow(current, row); err != nil {
			return UsageReport{}, err
		}
	}
	if err := rows.Err(); err != nil {
		return UsageReport{}, fmt.Errorf("iterating runtime usage events: %w", err)
	}
	collector.Add(current, currentCharacterID)
	return collector.Report(), nil
}
