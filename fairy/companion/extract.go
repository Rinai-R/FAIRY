package companion

func (s *CompanionService) scheduleBackgroundExtraction(conversationID string) {
	if s == nil || !s.TurnRuntimeReady() || s.retention == nil {
		return
	}
	s.retention.scheduleExtraction(conversationID)
}

func (s *CompanionService) ActiveBackgroundJobs() int64 {
	if s == nil || s.retention == nil {
		return 0
	}
	return s.retention.activeJobs()
}

func (s *CompanionService) setBackgroundError(err error) {
	if s == nil || err == nil {
		return
	}
	s.backgroundErrorMu.Lock()
	s.backgroundError = err
	s.backgroundErrorMu.Unlock()
}

func (s *CompanionService) clearBackgroundError() {
	if s == nil {
		return
	}
	s.backgroundErrorMu.Lock()
	s.backgroundError = nil
	s.backgroundErrorMu.Unlock()
}
