package companion

// speechEnabledForTurn combines the Surface request with the Core-owned speech
// readiness exactly once. A readiness error disables speech for this turn; the
// caller records the diagnostic without failing text delivery.
func (s *CompanionService) speechEnabledForTurn(requested bool) (bool, error) {
	if !requested || s == nil || s.speech == nil {
		return false, nil
	}
	ready, err := s.speech.SpeechReady()
	if err != nil {
		return false, err
	}
	return ready, nil
}
