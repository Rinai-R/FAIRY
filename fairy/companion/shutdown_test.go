package companion

import (
	"testing"

	"fairy/participation"
	"fairy/sociallearning"
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
	if _, err := s.reserveTurn("conversation-1"); err != nil {
		t.Fatalf("reserveTurn() after Close() = %v, want available slot", err)
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

func TestCloseClosesSocialLearningOwners(t *testing.T) {
	s := NewCompanionServiceWithRuntime("", nil, nil, nil)
	if s.socialLearning == nil || s.socialFeedback == nil {
		t.Fatal("social learning owners were not wired")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if s.socialLearning.Enqueue(sociallearning.LearningSnapshot{ConversationID: "conversation-1", Messages: []participation.AmbientObservation{{MessageID: "m1"}}}) {
		t.Fatal("learning enqueue accepted after CompanionService.Close")
	}
	if s.socialFeedback.Register(sociallearning.FeedbackRegistration{ConversationID: "conversation-1", TurnID: "turn-1", ReplyText: "reply"}) {
		t.Fatal("feedback registration accepted after CompanionService.Close")
	}
}
