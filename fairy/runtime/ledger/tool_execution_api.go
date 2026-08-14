package ledger

import "context"

func (s *Store) CreateToolExecution(ctx context.Context, input CreateToolExecutionInput) (ToolExecutionRecord, error) {
	if !s.usesSeekDB() {
		return ToolExecutionRecord{}, ErrStoreBackendUnavailable
	}
	return s.createToolExecutionSeekDB(ctx, input)
}

func (s *Store) MarkToolExecutionDispatched(ctx context.Context, id string) (ToolExecutionRecord, bool, error) {
	if !s.usesSeekDB() {
		return ToolExecutionRecord{}, false, ErrStoreBackendUnavailable
	}
	return s.markToolExecutionDispatchedSeekDB(ctx, id)
}

func (s *Store) CompleteToolExecution(ctx context.Context, input CompleteToolExecutionInput) (ToolExecutionRecord, bool, error) {
	if !s.usesSeekDB() {
		return ToolExecutionRecord{}, false, ErrStoreBackendUnavailable
	}
	return s.completeToolExecutionSeekDB(ctx, input)
}

func (s *Store) FailToolExecution(ctx context.Context, id, code, message string) (ToolExecutionRecord, bool, error) {
	if !s.usesSeekDB() {
		return ToolExecutionRecord{}, false, ErrStoreBackendUnavailable
	}
	return s.failToolExecutionSeekDB(ctx, id, code, message)
}

func (s *Store) CancelToolExecutionsForTurn(ctx context.Context, conversationID, turnID, code, message string) (int64, error) {
	if !s.usesSeekDB() {
		return 0, ErrStoreBackendUnavailable
	}
	return s.cancelToolExecutionsForTurnSeekDB(ctx, conversationID, turnID, code, message)
}

func (s *Store) ExpireToolExecutions(ctx context.Context, nowUnixMS int64) (int64, error) {
	if !s.usesSeekDB() {
		return 0, ErrStoreBackendUnavailable
	}
	return s.expireToolExecutionsSeekDB(ctx, nowUnixMS)
}

func (s *Store) LoadToolExecution(ctx context.Context, id string) (ToolExecutionRecord, bool, error) {
	if !s.usesSeekDB() {
		return ToolExecutionRecord{}, false, ErrStoreBackendUnavailable
	}
	return s.loadToolExecutionSeekDB(ctx, id)
}

func (s *Store) SettleRecoveredToolExecutions(ctx context.Context) (int64, error) {
	if !s.usesSeekDB() {
		return 0, ErrStoreBackendUnavailable
	}
	return s.settleRecoveredToolExecutionsSeekDB(ctx)
}
