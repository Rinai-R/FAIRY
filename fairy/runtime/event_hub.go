package runtime

import (
	"sync"

	"fairy/companion"
)

const eventHubBuffer = 64

// EventHub fans harness events out to per-conversation SSE subscribers.
type EventHub struct {
	mu   sync.Mutex
	subs map[string]map[chan companion.HarnessEvent]struct{}
}

func NewEventHub() *EventHub {
	return &EventHub{subs: make(map[string]map[chan companion.HarnessEvent]struct{})}
}

// Subscribe returns a buffered channel of events for conversationID and an unsubscribe func.
func (h *EventHub) Subscribe(conversationID string) (<-chan companion.HarnessEvent, func()) {
	if h == nil || conversationID == "" {
		ch := make(chan companion.HarnessEvent)
		close(ch)
		return ch, func() {}
	}
	ch := make(chan companion.HarnessEvent, eventHubBuffer)
	h.mu.Lock()
	if h.subs[conversationID] == nil {
		h.subs[conversationID] = make(map[chan companion.HarnessEvent]struct{})
	}
	h.subs[conversationID][ch] = struct{}{}
	h.mu.Unlock()
	var once sync.Once
	unsub := func() {
		once.Do(func() {
			h.mu.Lock()
			if set, ok := h.subs[conversationID]; ok {
				delete(set, ch)
				if len(set) == 0 {
					delete(h.subs, conversationID)
				}
			}
			h.mu.Unlock()
			close(ch)
		})
	}
	return ch, unsub
}

// Publish delivers an event to all subscribers of its conversation (non-blocking per sub).
func (h *EventHub) Publish(event companion.HarnessEvent) {
	if h == nil || event.ConversationID == "" {
		return
	}
	h.mu.Lock()
	set := h.subs[event.ConversationID]
	subs := make([]chan companion.HarnessEvent, 0, len(set))
	for ch := range set {
		subs = append(subs, ch)
	}
	h.mu.Unlock()
	for _, ch := range subs {
		select {
		case ch <- event:
		default:
			// Drop if subscriber is slow; SSE client can reconnect.
		}
	}
}
