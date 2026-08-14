package runtime

import "context"

func (s *Store) AppendTurnRuntimeEvent(input TurnRuntimeEventInput) (TurnRuntimeEventRecord, error) {
	return s.AppendTurnRuntimeEventContext(context.Background(), input)
}

func (s *Store) AppendTurnRuntimeEventContext(ctx context.Context, input TurnRuntimeEventInput) (TurnRuntimeEventRecord, error) {
	if !s.usesSeekDB() {
		return TurnRuntimeEventRecord{}, ErrStoreBackendUnavailable
	}
	return s.appendTurnRuntimeEventSeekDB(ctx, input)
}

func (s *Store) ListTurnRuntimeEvents(conversationID string, turnID string) ([]TurnRuntimeEventRecord, error) {
	return s.ListTurnRuntimeEventsContext(context.Background(), conversationID, turnID)
}

func (s *Store) ListTurnRuntimeEventsContext(ctx context.Context, conversationID string, turnID string) ([]TurnRuntimeEventRecord, error) {
	if !s.usesSeekDB() {
		return nil, ErrStoreBackendUnavailable
	}
	return s.listTurnRuntimeEventsSeekDB(ctx, conversationID, turnID)
}

func (s *Store) SaveLaneContinuation(record LaneContinuationRecord) (LaneContinuationRecord, error) {
	return s.SaveLaneContinuationContext(context.Background(), record)
}

func (s *Store) SaveLaneContinuationContext(ctx context.Context, record LaneContinuationRecord) (LaneContinuationRecord, error) {
	if !s.usesSeekDB() {
		return LaneContinuationRecord{}, ErrStoreBackendUnavailable
	}
	return s.saveLaneContinuationSeekDB(ctx, record)
}

func (s *Store) LoadLaneContinuation(conversationID string, lane string) (LaneContinuationRecord, bool, error) {
	return s.LoadLaneContinuationContext(context.Background(), conversationID, lane)
}

func (s *Store) LoadLaneContinuationContext(ctx context.Context, conversationID string, lane string) (LaneContinuationRecord, bool, error) {
	if !s.usesSeekDB() {
		return LaneContinuationRecord{}, false, ErrStoreBackendUnavailable
	}
	return s.loadLaneContinuationSeekDB(ctx, conversationID, lane)
}

func (s *Store) ClearLaneContinuation(conversationID string, lane string) error {
	return s.ClearLaneContinuationContext(context.Background(), conversationID, lane)
}

func (s *Store) ClearLaneContinuationContext(ctx context.Context, conversationID string, lane string) error {
	if !s.usesSeekDB() {
		return ErrStoreBackendUnavailable
	}
	return s.clearLaneContinuationSeekDB(ctx, conversationID, lane)
}

func (s *Store) SaveContextWindow(record ContextWindowRecord) (ContextWindowRecord, error) {
	return s.SaveContextWindowContext(context.Background(), record)
}

func (s *Store) SaveContextWindowContext(ctx context.Context, record ContextWindowRecord) (ContextWindowRecord, error) {
	if !s.usesSeekDB() {
		return ContextWindowRecord{}, ErrStoreBackendUnavailable
	}
	return s.saveContextWindowSeekDB(ctx, record)
}

func (s *Store) LoadContextWindow(conversationID string, lane string) (ContextWindowRecord, bool, error) {
	return s.LoadContextWindowContext(context.Background(), conversationID, lane)
}

func (s *Store) LoadContextWindowContext(ctx context.Context, conversationID string, lane string) (ContextWindowRecord, bool, error) {
	if !s.usesSeekDB() {
		return ContextWindowRecord{}, false, ErrStoreBackendUnavailable
	}
	return s.loadContextWindowSeekDB(ctx, conversationID, lane)
}
