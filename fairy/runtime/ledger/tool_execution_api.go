package ledger

import "context"

func (s *Store) CreateToolExecution(ctx context.Context, input CreateToolExecutionInput) (ToolExecutionRecord, error) {
	if s.usesSeekDB() {
		return s.createToolExecutionSeekDB(ctx, input)
	}
	if !s.usesPostgres() {
		return ToolExecutionRecord{}, ErrStoreBackendUnavailable
	}
	return s.createToolExecutionPostgres(ctx, input)
}

func (s *Store) MarkToolExecutionDispatched(ctx context.Context, id string) (ToolExecutionRecord, bool, error) {
	if s.usesSeekDB() {
		return s.markToolExecutionDispatchedSeekDB(ctx, id)
	}
	if !s.usesPostgres() {
		return ToolExecutionRecord{}, false, ErrStoreBackendUnavailable
	}
	return s.markToolExecutionDispatchedPostgres(ctx, id)
}

func (s *Store) CompleteToolExecution(ctx context.Context, input CompleteToolExecutionInput) (ToolExecutionRecord, bool, error) {
	if s.usesSeekDB() {
		return s.completeToolExecutionSeekDB(ctx, input)
	}
	if !s.usesPostgres() {
		return ToolExecutionRecord{}, false, ErrStoreBackendUnavailable
	}
	return s.completeToolExecutionPostgres(ctx, input)
}

func (s *Store) FailToolExecution(ctx context.Context, id, code, message string) (ToolExecutionRecord, bool, error) {
	if s.usesSeekDB() {
		return s.failToolExecutionSeekDB(ctx, id, code, message)
	}
	if !s.usesPostgres() {
		return ToolExecutionRecord{}, false, ErrStoreBackendUnavailable
	}
	return s.failToolExecutionPostgres(ctx, id, code, message)
}

func (s *Store) CancelToolExecutionsForTurn(ctx context.Context, conversationID, turnID, code, message string) (int64, error) {
	if s.usesSeekDB() {
		return s.cancelToolExecutionsForTurnSeekDB(ctx, conversationID, turnID, code, message)
	}
	if !s.usesPostgres() {
		return 0, ErrStoreBackendUnavailable
	}
	return s.cancelToolExecutionsForTurnPostgres(ctx, conversationID, turnID, code, message)
}

func (s *Store) ExpireToolExecutions(ctx context.Context, nowUnixMS int64) (int64, error) {
	if s.usesSeekDB() {
		return s.expireToolExecutionsSeekDB(ctx, nowUnixMS)
	}
	if !s.usesPostgres() {
		return 0, ErrStoreBackendUnavailable
	}
	return s.expireToolExecutionsPostgres(ctx, nowUnixMS)
}

func (s *Store) LoadToolExecution(ctx context.Context, id string) (ToolExecutionRecord, bool, error) {
	if s.usesSeekDB() {
		return s.loadToolExecutionSeekDB(ctx, id)
	}
	if !s.usesPostgres() {
		return ToolExecutionRecord{}, false, ErrStoreBackendUnavailable
	}
	return s.loadToolExecutionPostgres(ctx, id)
}

func (s *Store) SettleRecoveredToolExecutions(ctx context.Context) (int64, error) {
	if s.usesSeekDB() {
		return s.settleRecoveredToolExecutionsSeekDB(ctx)
	}
	if !s.usesPostgres() {
		return 0, ErrStoreBackendUnavailable
	}
	return s.settleRecoveredToolExecutionsPostgres(ctx)
}
