package companion

import (
	"context"
	"testing"
)

func TestCloseCancelsActiveTurn(t *testing.T) {
	s := NewCompanionService()
	ctx, err := s.reserveTurn("conversation-1")
	if err != nil {
		t.Fatalf("reserveTurn() error = %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if ctx.Err() == nil {
		t.Fatal("Close() did not cancel the active turn context")
	}
	s.gateMu.Lock()
	gate := s.gates["conversation-1"]
	s.gateMu.Unlock()
	if gate == nil {
		t.Fatal("gate missing after Close()")
	}
	gate.mu.Lock()
	active := gate.activeTurn
	gate.mu.Unlock()
	if active != nil {
		t.Fatalf("activeTurn = %#v, want nil after Close()", active)
	}
}

func TestCloseCancelsExtractionTimers(t *testing.T) {
	s := NewCompanionService()
	ctx, cancel := context.WithCancel(context.Background())
	s.extractionMu.Lock()
	s.extractionIdle["conversation-1"] = cancel
	s.extractionMu.Unlock()

	if err := s.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if ctx.Err() == nil {
		t.Fatal("Close() did not cancel the extraction idle timer")
	}
	s.extractionMu.Lock()
	remaining := len(s.extractionIdle)
	s.extractionMu.Unlock()
	if remaining != 0 {
		t.Fatalf("extractionIdle length = %d, want 0 after Close()", remaining)
	}
}

func TestCloseIdempotentAndSafeWithoutRuntime(t *testing.T) {
	s := NewCompanionService()
	if _, err := s.reserveTurn("conversation-1"); err != nil {
		t.Fatalf("reserveTurn() error = %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("first Close() error = %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
}
