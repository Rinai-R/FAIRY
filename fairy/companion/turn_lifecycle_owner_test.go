package companion

import (
	"encoding/json"
	"sort"
	"sync"
	"testing"

	"fairy/reply"
	"fairy/session"
)

func publishTransition(t *testing.T, life *turnLifecycle, state turnState) session.Event {
	t.Helper()
	event, err := life.Publish(func() (session.Event, error) { return life.Transition(state) })
	if err != nil {
		t.Fatal(err)
	}
	return event
}

func TestLifecyclePublishesStableEventsAndJSONShape(t *testing.T) {
	life := newTurnLifecycle("conversation", "turn")
	if event := publishTransition(t, life, turnStateInterpreting); event.Sequence != 1 || event.State != string(turnStateInterpreting) {
		t.Fatalf("interpreting event = %#v", event)
	}
	publishTransition(t, life, turnStateGathering)
	publishTransition(t, life, turnStatePlanning)

	event, err := life.Publish(func() (session.Event, error) {
		return life.BeatReady(reply.BeatReadyCompletion{BeatID: "beat-1", Kind: reply.BeatKindFinal, DisplayText: "在"})
	})
	if err != nil {
		t.Fatal(err)
	}
	payload := decodeEventPayload[beatReadyPayload](t, event.Payload)
	if payload.Type != "beat.ready" || payload.Kind != reply.BeatKindFinal {
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
	life := newTurnLifecycle("conversation", "turn")
	publishTransition(t, life, turnStateInterpreting)
	publishTransition(t, life, turnStateGathering)
	publishTransition(t, life, turnStatePlanning)

	const count = 32
	sequences := make([]uint64, 0, count)
	var mu sync.Mutex
	var wg sync.WaitGroup
	for index := 0; index < count; index++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			event, err := life.Publish(func() (session.Event, error) {
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
	life := newTurnLifecycle("conversation", "turn")
	if _, err := life.Publish(func() (session.Event, error) { return life.Presence("model_stream") }); err == nil {
		t.Fatal("presence from idle unexpectedly succeeded")
	}
	publishTransition(t, life, turnStateInterpreting)
	publishTransition(t, life, turnStateGathering)
	publishTransition(t, life, turnStatePlanning)
	if _, err := life.Publish(func() (session.Event, error) {
		return life.BeatReady(reply.BeatReadyCompletion{BeatID: "beat-1"})
	}); err == nil {
		t.Fatal("empty beat unexpectedly succeeded")
	}
	event, err := life.Publish(func() (session.Event, error) {
		return life.Presence("model_stream")
	})
	if err != nil {
		t.Fatal(err)
	}
	if event.Sequence != 4 {
		t.Fatalf("sequence after rejected beat = %d, want 4", event.Sequence)
	}
}
