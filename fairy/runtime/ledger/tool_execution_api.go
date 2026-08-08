package ledger

import "context"

func (s *Store) CreateToolExecution(ctx context.Context, input CreateToolExecutionInput) (ToolExecutionRecord, error) {
	return s.createToolExecutionPostgres(ctx, input)
}

func (s *Store) MarkToolExecutionDispatched(ctx context.Context, id string) (ToolExecutionRecord, bool, error) {
	return s.markToolExecutionDispatchedPostgres(ctx, id)
}

func (s *Store) CompleteToolExecution(ctx context.Context, input CompleteToolExecutionInput) (ToolExecutionRecord, bool, error) {
	return s.completeToolExecutionPostgres(ctx, input)
}

func (s *Store) FailToolExecution(ctx context.Context, id, code, message string) (ToolExecutionRecord, bool, error) {
	return s.failToolExecutionPostgres(ctx, id, code, message)
}

func (s *Store) CancelToolExecutionsForTurn(ctx context.Context, conversationID, turnID, code, message string) (int64, error) {
	return s.cancelToolExecutionsForTurnPostgres(ctx, conversationID, turnID, code, message)
}

func (s *Store) ExpireToolExecutions(ctx context.Context, nowUnixMS int64) (int64, error) {
	return s.expireToolExecutionsPostgres(ctx, nowUnixMS)
}

func (s *Store) LoadToolExecution(ctx context.Context, id string) (ToolExecutionRecord, bool, error) {
	return s.loadToolExecutionPostgres(ctx, id)
}

func (s *Store) SettleRecoveredToolExecutions(ctx context.Context) (int64, error) {
	return s.settleRecoveredToolExecutionsPostgres(ctx)
}
