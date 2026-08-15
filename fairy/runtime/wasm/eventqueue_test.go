package wasm

import "testing"

func TestEventQueuePollsByCursorAndDropsOldestWhenBounded(t *testing.T) {
	queue := &EventQueue{}
	if items := queue.Poll(0, 8); len(items) != 0 {
		t.Fatalf("empty poll = %#v", items)
	}
	first := queue.Push([]byte(`{"n":1}`))
	second := queue.Push([]byte(`{"n":2}`))
	got := queue.Poll(0, 8)
	if len(got) != 2 || got[0].Sequence != first || got[1].Sequence != second || string(got[0].Payload) != `{"n":1}` {
		t.Fatalf("poll = %#v", got)
	}
	if later := queue.Poll(first, 8); len(later) != 1 || later[0].Sequence != second {
		t.Fatalf("cursor poll = %#v", later)
	}
	for i := 0; i < MaxEventQueue+8; i++ {
		queue.Push([]byte(`x`))
	}
	if queue.Depth() != MaxEventQueue {
		t.Fatalf("depth = %d", queue.Depth())
	}
	window := queue.Poll(0, MaxEventQueue+1)
	if len(window) != MaxEventQueue || window[0].Sequence <= first {
		t.Fatalf("bounded window = seq %d len %d", window[0].Sequence, len(window))
	}
}
