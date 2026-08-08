package conversation

import (
	"fairy/agent/conversation/lifecycle"
	"fairy/transport/session"
	"sync"
	"testing"
)

func TestPublishLifeSerializesConcurrentPresenceEvents(t *testing.T) {
	service := NewService()
	var mu sync.Mutex
	var sequences []uint64
	AttachEventEmitter(service, func(event session.Event) {
		mu.Lock()
		sequences = append(sequences, event.Sequence)
		mu.Unlock()
	})
	life := lifecycle.New("c1", "t1")
	for _, state := range []lifecycle.State{lifecycle.StateInterpreting, lifecycle.StateGathering, lifecycle.StatePlanning} {
		if _, err := service.publishLife(life, func() (session.Event, error) {
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
			_, _ = service.publishLife(life, func() (session.Event, error) {
				return life.Presence("working")
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
