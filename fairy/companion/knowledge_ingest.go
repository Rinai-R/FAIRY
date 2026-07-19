package companion

import (
	"fairy/memory"
)

// scheduleKnowledgeIngest enqueues retrieval snapshots and processes a bounded
// batch asynchronously. Writes verified knowledge with no human Confirm.
func (s *CompanionService) scheduleKnowledgeIngest(snapshots []memory.KnowledgeIngestSnapshot) {
	if s == nil || !s.RespondRuntimeMigrated() || len(snapshots) == 0 {
		return
	}
	s.backgroundJobs.Add(1)
	go func() {
		defer s.backgroundJobs.Add(-1)
		if err := s.memoryStore.EnqueueKnowledgeIngestSnapshots(snapshots); err != nil {
			s.setBackgroundError(err)
			return
		}
		if _, err := s.memoryStore.ProcessKnowledgeIngestJobs(8); err != nil {
			s.setBackgroundError(err)
		}
	}()
}
