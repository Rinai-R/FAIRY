package postgres

import (
	"context"
	domainmemory "fairy/internal/domain/memory"
	"fmt"
)

func (s *Store) appendTurnRuntimeEventPostgres(ctx context.Context, input TurnRuntimeEventInput) (TurnRuntimeEventRecord, error) {
	if err := validateTurnRuntimeEventInput(input); err != nil {
		return TurnRuntimeEventRecord{}, err
	}
	metadataJSON, err := normalizeRuntimeMetadataJSON(input.MetadataJSON)
	if err != nil {
		return TurnRuntimeEventRecord{}, err
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	tx, err := s.pool.Raw().Begin(queryCtx)
	if err != nil {
		return TurnRuntimeEventRecord{}, fmt.Errorf("beginning runtime event transaction: %w", err)
	}
	defer tx.Rollback(queryCtx)
	if err := RequireTurn(queryCtx, tx, input.ConversationID, input.TurnID); err != nil {
		return TurnRuntimeEventRecord{}, err
	}
	sequence, err := NextTurnRuntimeEventSequence(queryCtx, tx, input.ConversationID, input.TurnID)
	if err != nil {
		return TurnRuntimeEventRecord{}, err
	}
	record, err := InsertTurnRuntimeEvent(queryCtx, tx, newID(), metadataJSON, nowUnixMS(), input, sequence)
	if err != nil {
		return TurnRuntimeEventRecord{}, err
	}
	if err := tx.Commit(queryCtx); err != nil {
		return TurnRuntimeEventRecord{}, fmt.Errorf("committing runtime event transaction: %w", err)
	}
	return record, nil
}

func (s *Store) listTurnRuntimeEventsPostgres(ctx context.Context, conversationID string, turnID string) ([]TurnRuntimeEventRecord, error) {
	if err := domainmemory.ValidateID("conversation_id", conversationID); err != nil {
		return nil, err
	}
	if err := domainmemory.ValidateID("turn_id", turnID); err != nil {
		return nil, err
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	records, err := ListTurnRuntimeEvents(queryCtx, s.pool.Raw(), conversationID, turnID)
	if err != nil {
		return nil, err
	}
	for i := range records {
		records[i].MetadataJSON, err = normalizeRuntimeMetadataJSON(records[i].MetadataJSON)
		if err != nil {
			return nil, fmt.Errorf("normalizing stored runtime metadata: %w", err)
		}
	}
	return records, nil
}

func (s *Store) saveLaneContinuationPostgres(ctx context.Context, record LaneContinuationRecord) (LaneContinuationRecord, error) {
	if err := validateLaneContinuation(record); err != nil {
		return LaneContinuationRecord{}, err
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	tx, err := s.pool.Raw().Begin(queryCtx)
	if err != nil {
		return LaneContinuationRecord{}, fmt.Errorf("beginning lane continuation transaction: %w", err)
	}
	defer tx.Rollback(queryCtx)
	if err := RequireConversation(queryCtx, tx, record.ConversationID); err != nil {
		return LaneContinuationRecord{}, err
	}
	record, err = SaveLaneContinuation(queryCtx, tx, record, nowUnixMS())
	if err != nil {
		return LaneContinuationRecord{}, err
	}
	if err := tx.Commit(queryCtx); err != nil {
		return LaneContinuationRecord{}, fmt.Errorf("committing lane continuation transaction: %w", err)
	}
	return record, nil
}

func (s *Store) loadLaneContinuationPostgres(ctx context.Context, conversationID string, lane string) (LaneContinuationRecord, bool, error) {
	if err := domainmemory.ValidateID("conversation_id", conversationID); err != nil {
		return LaneContinuationRecord{}, false, err
	}
	if err := validatePromptLane(lane); err != nil {
		return LaneContinuationRecord{}, false, err
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	return LoadLaneContinuation(queryCtx, s.pool.Raw(), conversationID, lane)
}

func (s *Store) clearLaneContinuationPostgres(ctx context.Context, conversationID string, lane string) error {
	if err := domainmemory.ValidateID("conversation_id", conversationID); err != nil {
		return err
	}
	if err := validatePromptLane(lane); err != nil {
		return err
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	tx, err := s.pool.Raw().Begin(queryCtx)
	if err != nil {
		return fmt.Errorf("beginning lane continuation clear transaction: %w", err)
	}
	defer tx.Rollback(queryCtx)
	if err := RequireConversation(queryCtx, tx, conversationID); err != nil {
		return err
	}
	if err := DeleteLaneContinuation(queryCtx, tx, conversationID, lane); err != nil {
		return err
	}
	if err := tx.Commit(queryCtx); err != nil {
		return fmt.Errorf("committing lane continuation clear transaction: %w", err)
	}
	return nil
}

func (s *Store) saveContextWindowPostgres(ctx context.Context, record ContextWindowRecord) (ContextWindowRecord, error) {
	if err := validateContextWindow(record); err != nil {
		return ContextWindowRecord{}, err
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	tx, err := s.pool.Raw().Begin(queryCtx)
	if err != nil {
		return ContextWindowRecord{}, fmt.Errorf("beginning context window transaction: %w", err)
	}
	defer tx.Rollback(queryCtx)
	record, err = SaveContextWindow(queryCtx, tx, record, nowUnixMS())
	if err != nil {
		return ContextWindowRecord{}, err
	}
	if err := tx.Commit(queryCtx); err != nil {
		return ContextWindowRecord{}, fmt.Errorf("committing context window transaction: %w", err)
	}
	return record, nil
}

func (s *Store) loadContextWindowPostgres(ctx context.Context, conversationID string, lane string) (ContextWindowRecord, bool, error) {
	if err := domainmemory.ValidateID("conversation_id", conversationID); err != nil {
		return ContextWindowRecord{}, false, err
	}
	if err := validatePromptLane(lane); err != nil {
		return ContextWindowRecord{}, false, err
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	return LoadContextWindow(queryCtx, s.pool.Raw(), conversationID, lane)
}
