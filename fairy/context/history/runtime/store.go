package runtime

import "context"

func (s *Store) AppendTurnRuntimeEvent(input TurnRuntimeEventInput) (TurnRuntimeEventRecord, error) {
	return s.AppendTurnRuntimeEventContext(context.Background(), input)
}

func (s *Store) AppendTurnRuntimeEventContext(ctx context.Context, input TurnRuntimeEventInput) (TurnRuntimeEventRecord, error) {
	if s.usesSeekDB() {
		return s.appendTurnRuntimeEventSeekDB(ctx, input)
	}
	if !s.usesPostgres() {
		return TurnRuntimeEventRecord{}, ErrStoreBackendUnavailable
	}
	return s.appendTurnRuntimeEventPostgres(ctx, input)
}

func (s *Store) ListTurnRuntimeEvents(conversationID string, turnID string) ([]TurnRuntimeEventRecord, error) {
	return s.ListTurnRuntimeEventsContext(context.Background(), conversationID, turnID)
}

func (s *Store) ListTurnRuntimeEventsContext(ctx context.Context, conversationID string, turnID string) ([]TurnRuntimeEventRecord, error) {
	if s.usesSeekDB() {
		return s.listTurnRuntimeEventsSeekDB(ctx, conversationID, turnID)
	}
	if !s.usesPostgres() {
		return nil, ErrStoreBackendUnavailable
	}
	return s.listTurnRuntimeEventsPostgres(ctx, conversationID, turnID)
}

func (s *Store) SaveLaneContinuation(record LaneContinuationRecord) (LaneContinuationRecord, error) {
	return s.SaveLaneContinuationContext(context.Background(), record)
}

func (s *Store) SaveLaneContinuationContext(ctx context.Context, record LaneContinuationRecord) (LaneContinuationRecord, error) {
	if s.usesSeekDB() {
		return s.saveLaneContinuationSeekDB(ctx, record)
	}
	if !s.usesPostgres() {
		return LaneContinuationRecord{}, ErrStoreBackendUnavailable
	}
	return s.saveLaneContinuationPostgres(ctx, record)
}

func (s *Store) LoadLaneContinuation(conversationID string, lane string) (LaneContinuationRecord, bool, error) {
	return s.LoadLaneContinuationContext(context.Background(), conversationID, lane)
}

func (s *Store) LoadLaneContinuationContext(ctx context.Context, conversationID string, lane string) (LaneContinuationRecord, bool, error) {
	if s.usesSeekDB() {
		return s.loadLaneContinuationSeekDB(ctx, conversationID, lane)
	}
	if !s.usesPostgres() {
		return LaneContinuationRecord{}, false, ErrStoreBackendUnavailable
	}
	return s.loadLaneContinuationPostgres(ctx, conversationID, lane)
}

func (s *Store) ClearLaneContinuation(conversationID string, lane string) error {
	return s.ClearLaneContinuationContext(context.Background(), conversationID, lane)
}

func (s *Store) ClearLaneContinuationContext(ctx context.Context, conversationID string, lane string) error {
	if s.usesSeekDB() {
		return s.clearLaneContinuationSeekDB(ctx, conversationID, lane)
	}
	if !s.usesPostgres() {
		return ErrStoreBackendUnavailable
	}
	return s.clearLaneContinuationPostgres(ctx, conversationID, lane)
}

func (s *Store) SaveContextWindow(record ContextWindowRecord) (ContextWindowRecord, error) {
	return s.SaveContextWindowContext(context.Background(), record)
}

func (s *Store) SaveContextWindowContext(ctx context.Context, record ContextWindowRecord) (ContextWindowRecord, error) {
	if s.usesSeekDB() {
		return s.saveContextWindowSeekDB(ctx, record)
	}
	if !s.usesPostgres() {
		return ContextWindowRecord{}, ErrStoreBackendUnavailable
	}
	return s.saveContextWindowPostgres(ctx, record)
}

func (s *Store) LoadContextWindow(conversationID string, lane string) (ContextWindowRecord, bool, error) {
	return s.LoadContextWindowContext(context.Background(), conversationID, lane)
}

func (s *Store) LoadContextWindowContext(ctx context.Context, conversationID string, lane string) (ContextWindowRecord, bool, error) {
	if s.usesSeekDB() {
		return s.loadContextWindowSeekDB(ctx, conversationID, lane)
	}
	if !s.usesPostgres() {
		return ContextWindowRecord{}, false, ErrStoreBackendUnavailable
	}
	return s.loadContextWindowPostgres(ctx, conversationID, lane)
}
