package turn

import (
	"encoding/json"
	"sort"
	"sync"
	"testing"

	"fairy/reply"
)

func publishTransition(t *testing.T, life *TurnLifecycle, state TurnState) TurnEvent {
	t.Helper()
	event, err := life.Publish(func() (TurnEvent, error) { return life.Transition(state) })
	if err != nil {
		t.Fatal(err)
	}
	return event
}

func TestLifecyclePublishesStableEventsAndJSONShape(t *testing.T) {
	life := NewTurnLifecycle("conversation", "turn")
	if event := publishTransition(t, life, TurnStateInterpreting); event.Sequence != 1 || event.State != TurnStateInterpreting {
		t.Fatalf("interpreting event = %#v", event)
	}
	publishTransition(t, life, TurnStateGathering)
	publishTransition(t, life, TurnStatePlanning)

	event, err := life.Publish(func() (TurnEvent, error) {
		return life.BeatReady(reply.BeatReadyCompletion{BeatID: "beat-1", Kind: reply.BeatKindFinal, DisplayText: "在"})
	})
	if err != nil {
		t.Fatal(err)
	}
	payload, ok := event.Payload.(BeatReadyPayload)
	if !ok || payload.Type != "beat.ready" || payload.Kind != reply.BeatKindFinal {
		t.Fatalf("payload = %#v", event.Payload)
	}
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) == "" {
		t.Fatal("event JSON is empty")
	}
}

func TestLifecyclePublishSerializesConcurrentSequence(t *testing.T) {
	life := NewTurnLifecycle("conversation", "turn")
	publishTransition(t, life, TurnStateInterpreting)
	publishTransition(t, life, TurnStateGathering)
	publishTransition(t, life, TurnStatePlanning)

	const count = 32
	sequences := make([]uint64, 0, count)
	var mu sync.Mutex
	var wg sync.WaitGroup
	for index := 0; index < count; index++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			event, err := life.Publish(func() (TurnEvent, error) {
				return life.Presence("model_stream")
			})
			if err != nil {
				t.Errorf("publish %d: %v", index, err)
				return
			}
			mu.Lock()
			sequences = append(sequences, event.Sequence)
			mu.Unlock()
		}(index)
	}
	wg.Wait()
	if len(sequences) != count {
		t.Fatalf("published %d events, want %d", len(sequences), count)
	}
	sort.Slice(sequences, func(i, j int) bool { return sequences[i] < sequences[j] })
	for index, sequence := range sequences {
		if sequence != uint64(index+4) {
			t.Fatalf("sequence[%d] = %d, want %d", index, sequence, index+4)
		}
	}
}

func TestLifecycleRejectsInvalidInputWithoutAdvancingSequence(t *testing.T) {
	life := NewTurnLifecycle("conversation", "turn")
	if _, err := life.Publish(func() (TurnEvent, error) { return life.Presence("model_stream") }); err == nil {
		t.Fatal("presence from idle unexpectedly succeeded")
	}
	publishTransition(t, life, TurnStateInterpreting)
	publishTransition(t, life, TurnStateGathering)
	publishTransition(t, life, TurnStatePlanning)
	if _, err := life.Publish(func() (TurnEvent, error) {
		return life.BeatReady(reply.BeatReadyCompletion{BeatID: "beat-1"})
	}); err == nil {
		t.Fatal("empty beat unexpectedly succeeded")
	}
	event, err := life.Publish(func() (TurnEvent, error) {
		return life.Presence("model_stream")
	})
	if err != nil {
		t.Fatal(err)
	}
	if event.Sequence != 4 {
		t.Fatalf("sequence after rejected beat = %d, want 4", event.Sequence)
	}
}
