package runtime

import (
	"sync"

	"fairy/companion"
)

const eventHubBuffer = 64

// EventHub fans harness events out to per-conversation WebSocket watchers.
type EventHub struct {
	mu     sync.Mutex
	subs   map[string]map[chan companion.HarnessEvent]struct{}
	closed bool
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
	if h.closed {
		h.mu.Unlock()
		close(ch)
		return ch, func() {}
	}
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
				close(ch)
				if len(set) == 0 {
					delete(h.subs, conversationID)
				}
			}
			h.mu.Unlock()
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
	defer h.mu.Unlock()
	for ch := range h.subs[event.ConversationID] {
		select {
		case ch <- event:
		default:
			// Drop if subscriber is slow; SSE client can reconnect.
		}
	}
}

func (h *EventHub) SubscriberCount() uint64 {
	if h == nil {
		return 0
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	var count uint64
	for _, subscribers := range h.subs {
		count += uint64(len(subscribers))
	}
	return count
}

func (h *EventHub) Close() {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	h.closed = true
	for conversationID, subscribers := range h.subs {
		for ch := range subscribers {
			delete(subscribers, ch)
			close(ch)
		}
		delete(h.subs, conversationID)
	}
}
