package companion

import (
	"sync"
	"testing"
)

func TestPublishLifeSerializesConcurrentSpeechEvents(t *testing.T) {
	service := NewCompanionService()
	var mu sync.Mutex
	var sequences []uint64
	AttachEventEmitter(service, func(event TurnEvent) {
		mu.Lock()
		sequences = append(sequences, event.Sequence)
		mu.Unlock()
	})
	life := NewTurnLifecycle("c1", "t1")
	for _, state := range []TurnState{TurnStateInterpreting, TurnStateGathering, TurnStatePlanning} {
		if _, err := service.publishLife(life, func() (TurnEvent, error) {
			return life.Transition(state)
		}); err != nil {
			t.Fatalf("Transition(%s) error = %v", state, err)
		}
	}

	const workers = 32
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			_, _ = service.publishLife(life, func() (TurnEvent, error) {
				return life.SpeechRequested(TurnCompletion{
					Text:              "稍等一下哦。",
					SpeechText:        "稍等一下哦。",
					CharacterRevision: 1,
				})
			})
		}()
	}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if len(sequences) != 3+workers {
		t.Fatalf("emitted %d events, want %d: %v", len(sequences), 3+workers, sequences)
	}
	for i, seq := range sequences {
		want := uint64(i + 1)
		if seq != want {
			t.Fatalf("sequences[%d] = %d, want %d (full=%v)", i, seq, want, sequences)
		}
	}
}
