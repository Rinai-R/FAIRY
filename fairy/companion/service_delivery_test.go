package companion

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"

	"fairy/internal/app/reply"

	"go.uber.org/zap"
)

func respondingLifecycle(t *testing.T) *TurnLifecycle {
	t.Helper()
	life := NewTurnLifecycle("conversation", "turn")
	for _, state := range []TurnState{TurnStateInterpreting, TurnStateGathering, TurnStatePlanning, TurnStateResponding} {
		if _, err := life.Transition(state); err != nil {
			t.Fatalf("Transition(%s) error = %v", state, err)
		}
	}
	return life
}

func TestHandleFinalSpeechCancellationDoesNotPublishTextOnlyBeat(t *testing.T) {
	service := NewCompanionService()
	service.logger = zap.NewNop()
	life := respondingLifecycle(t)
	var published []BeatReadyCompletion
	delivery := reply.NewDelivery(t.Context(), 1, func(completion BeatReadyCompletion) error {
		published = append(published, completion)
		return nil
	}, nil)

	service.handleSpeechResult(life, "conversation", "turn", delivery, reply.SpeechResult{
		BeatID:      "final-0",
		Kind:        reply.BeatKindFinal,
		PlayIndex:   0,
		ChainIndex:  0,
		DisplayText: "不应发布",
		VisualState: "idle",
		Err:         context.Canceled,
	})

	if len(published) != 0 {
		t.Fatalf("published = %#v, want none", published)
	}
	if !errors.Is(delivery.Err(), ErrTurnInterrupted) {
		t.Fatalf("delivery.Err() = %v, want ErrTurnInterrupted", delivery.Err())
	}
}

func TestHandleFinalSpeechProviderFailurePublishesTextOnlyBeat(t *testing.T) {
	service := NewCompanionService()
	service.logger = zap.NewNop()
	life := respondingLifecycle(t)
	var published []BeatReadyCompletion
	delivery := reply.NewDelivery(t.Context(), 1, func(completion BeatReadyCompletion) error {
		published = append(published, completion)
		return nil
	}, nil)

	service.handleSpeechResult(life, "conversation", "turn", delivery, reply.SpeechResult{
		BeatID:      "final-0",
		Kind:        reply.BeatKindFinal,
		PlayIndex:   0,
		ChainIndex:  0,
		DisplayText: "仍然显示",
		VisualState: "idle",
		Text:        "仍然显示",
		Err:         errors.New("provider unavailable"),
	})

	if len(published) != 1 || published[0].DisplayText != "仍然显示" || published[0].Audio != nil {
		t.Fatalf("published = %#v, want one text-only beat", published)
	}
	if !delivery.Complete() {
		t.Fatalf("delivery incomplete: %v", delivery.Err())
	}
}

func TestSpeechPipelineKeepsSkippedFinalBeatInOrder(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		service := NewCompanionService()
		service.logger = zap.NewNop()
		life := respondingLifecycle(t)
		var published []BeatReadyCompletion
		delivery := reply.NewDelivery(t.Context(), 2, func(completion BeatReadyCompletion) error {
			published = append(published, completion)
			return nil
		}, nil)
		pipeline := reply.NewSpeechPipeline(t.Context(), &recordingSynth{}, 2, func(result reply.SpeechResult) {
			service.handleSpeechResult(life, "conversation", "turn", delivery, result)
		})
		pipeline.Enqueue(reply.SpeechJob{
			BeatID:      "final-0",
			Kind:        reply.BeatKindFinal,
			PlayIndex:   0,
			ChainIndex:  0,
			DisplayText: "第一拍",
			VisualState: "idle",
			Resolve:     func() (string, error) { return "第一拍", nil },
		})
		pipeline.Enqueue(reply.SpeechJob{
			BeatID:      "final-1",
			Kind:        reply.BeatKindFinal,
			PlayIndex:   1,
			ChainIndex:  1,
			DisplayText: "第二拍",
			VisualState: "idle",
			Resolve:     func() (string, error) { return "", nil },
		})
		pipeline.Close()

		if len(published) != 2 || published[0].ChainIndex != 0 || published[1].ChainIndex != 1 {
			t.Fatalf("published order = %#v", published)
		}
		if published[0].Audio == nil || published[1].Audio != nil {
			t.Fatalf("audio pairing = %#v", published)
		}
	})
}
