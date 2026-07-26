package memory

import (
	"context"
)

func (s *Store) aggregateTokenUsagePostgres(ctx context.Context, limit int) (UsageReport, error) {
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	characterByConversation, err := LoadConversationCharacters(queryCtx, s.pool.Raw())
	if err != nil {
		return UsageReport{}, err
	}
	ledgerRows, err := LoadUsageLedgerEvents(queryCtx, s.pool.Raw())
	if err != nil {
		return UsageReport{}, err
	}
	return aggregateUsageRows(characterByConversation, usageLedgerRowsFromAdapter(ledgerRows), limit)
}

func usageLedgerRowsFromAdapter(rows []UsageLedgerRow) []usageLedgerRow {
	result := make([]usageLedgerRow, 0, len(rows))
	for _, row := range rows {
		result = append(result, usageLedgerRow{
			conversationID:  row.ConversationID,
			turnID:          row.TurnID,
			eventType:       row.EventType,
			state:           row.State,
			metadataJSON:    row.MetadataJSON,
			createdAtUnixMS: row.CreatedAtUnixMS,
		})
	}
	return result
}
