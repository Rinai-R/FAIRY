package runtime

import (
	"errors"
	"sync"

	"fairy/companion"
)

const eventHubBuffer = 64

var ErrEventSubscriberOverflow = errors.New("event subscriber overflow")

type eventSubscriber struct {
	events   chan companion.TurnEvent
	failures chan error
}

// EventSubscription is one ordered per-conversation turn-event stream.
type EventSubscription struct {
	Events      <-chan companion.TurnEvent
	Failures    <-chan error
	unsubscribe func()
}

func (s EventSubscription) Unsubscribe() {
	if s.unsubscribe != nil {
		s.unsubscribe()
	}
}

// EventHub fans turn events out to per-conversation WebSocket watchers.
type EventHub struct {
	mu     sync.Mutex
	subs   map[string]map[*eventSubscriber]struct{}
	closed bool
}

func NewEventHub() *EventHub {
	return &EventHub{subs: make(map[string]map[*eventSubscriber]struct{})}
}

// Subscribe returns one ordered stream and a separate terminal-failure signal.
func (h *EventHub) Subscribe(conversationID string) EventSubscription {
	if h == nil || conversationID == "" {
		return closedEventSubscription()
	}
	subscriber := &eventSubscriber{
		events:   make(chan companion.TurnEvent, eventHubBuffer),
		failures: make(chan error, 1),
	}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		close(subscriber.events)
		close(subscriber.failures)
		return EventSubscription{Events: subscriber.events, Failures: subscriber.failures, unsubscribe: func() {}}
	}
	if h.subs[conversationID] == nil {
		h.subs[conversationID] = make(map[*eventSubscriber]struct{})
	}
	h.subs[conversationID][subscriber] = struct{}{}
	h.mu.Unlock()

	var once sync.Once
	return EventSubscription{
		Events:   subscriber.events,
		Failures: subscriber.failures,
		unsubscribe: func() {
			once.Do(func() {
				h.mu.Lock()
				h.removeLocked(conversationID, subscriber, nil)
				h.mu.Unlock()
			})
		},
	}
}

func closedEventSubscription() EventSubscription {
	events := make(chan companion.TurnEvent)
	failures := make(chan error)
	close(events)
	close(failures)
	return EventSubscription{Events: events, Failures: failures, unsubscribe: func() {}}
}

// Publish never blocks Core turn execution. A slow subscriber is failed and removed.
func (h *EventHub) Publish(event companion.TurnEvent) {
	if h == nil || event.ConversationID == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for subscriber := range h.subs[event.ConversationID] {
		select {
		case subscriber.events <- event:
		default:
			h.removeLocked(event.ConversationID, subscriber, ErrEventSubscriberOverflow)
		}
	}
}

func (h *EventHub) removeLocked(conversationID string, subscriber *eventSubscriber, failure error) {
	subscribers, ok := h.subs[conversationID]
	if !ok {
		return
	}
	if _, ok := subscribers[subscriber]; !ok {
		return
	}
	delete(subscribers, subscriber)
	if len(subscribers) == 0 {
		delete(h.subs, conversationID)
	}
	if failure != nil {
		subscriber.failures <- failure
	}
	close(subscriber.failures)
	close(subscriber.events)
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
		for subscriber := range subscribers {
			h.removeLocked(conversationID, subscriber, nil)
		}
	}
}
