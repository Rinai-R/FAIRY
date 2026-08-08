package core

import (
	turn "fairy/agent/conversation"
	retention "fairy/agent/learning"
)

type retentionAdapter struct {
	service *retention.Service
}

type deferredTurnAdapter struct {
	service *retention.Service
}

func (adapter deferredTurnAdapter) ScheduleDeferred(job func()) error {
	return adapter.service.ScheduleDeferred(job)
}

func (adapter retentionAdapter) ScheduleCompaction(job func()) error {
	return adapter.service.ScheduleCompaction(job)
}

func (adapter retentionAdapter) ActiveJobs() int64 {
	return adapter.service.ActiveJobs()
}

func (adapter retentionAdapter) ObserveCompletedTurn(completed turn.RetentionCompletion) {
	opportunities := make([]retention.KnowledgeOpportunity, 0, len(completed.KnowledgeTasks))
	for _, task := range completed.KnowledgeTasks {
		opportunities = append(opportunities, retention.KnowledgeOpportunity{Task: task})
	}
	adapter.service.ObserveCompletedTurn(retention.CompletedTurn{
		ConversationID:         completed.ConversationID,
		ExtractPersonalMemory:  completed.ExtractPersonalMemory,
		KnowledgeOpportunities: opportunities,
	})
}

func (adapter retentionAdapter) TakeCommittedCoverage(conversationID string) bool {
	return adapter.service.TakeCommittedCoverage(conversationID)
}

func (adapter retentionAdapter) Close() {
	adapter.service.Close()
}
