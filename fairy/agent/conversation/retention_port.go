package conversation

import (
	"fairy/agent/conversation/lifecycle"
	"fairy/context/knowledge"
	"fairy/runtime/model"
)

// RetentionPort is the post-Turn lifecycle consumed by reactive orchestration.
// Core owns construction and attaches the concrete retention service.
type RetentionPort interface {
	ScheduleCompaction(func()) error
	ActiveJobs() int64
	ObserveCompletedTurn(RetentionCompletion)
	TakeCommittedCoverage(string) bool
	Close()
}

type RetentionCompletion struct {
	ConversationID        string
	ExtractPersonalMemory bool
	KnowledgeTasks        []knowledge.IngestTask
}

func AttachRetention(service *Service, retention RetentionPort) {
	if service != nil {
		service.retention = retention
	}
}

func (service *Service) ActiveBackgroundJobs() int64 {
	if service == nil || service.retention == nil {
		return 0
	}
	return service.retention.ActiveJobs()
}

func (service *Service) setBackgroundError(err error) {
	if service == nil || err == nil {
		return
	}
	service.backgroundErrorMu.Lock()
	service.backgroundError = err
	service.backgroundErrorMu.Unlock()
}

func (service *Service) clearBackgroundError() {
	if service == nil {
		return
	}
	service.backgroundErrorMu.Lock()
	service.backgroundError = nil
	service.backgroundErrorMu.Unlock()
}

func ObserveBackgroundError(service *Service, err error) {
	if service != nil {
		service.setBackgroundError(err)
	}
}

func ClearBackgroundError(service *Service) {
	if service != nil {
		service.clearBackgroundError()
	}
}

func RecordKnowledgeRun(service *Service, task knowledge.IngestTask, events []model.StreamEvent, usage []model.LaneModelUsage) {
	if service == nil {
		return
	}
	service.appendRuntimeLedger(
		task.ConversationID,
		task.TurnID,
		runtimeLedgerEventModel,
		lifecycle.StateCompleted,
		"",
		runtimeKnowledgeIngestLedgerMetadata(events, usage, task.ID),
	)
}
