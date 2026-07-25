package runtime

import (
	"errors"

	"fairy/companion"
	"fairy/pkg/eventstream"
)

const eventHubBuffer = 64

var ErrEventSubscriberOverflow = errors.New("event subscriber overflow")

// EventSubscription is one ordered per-conversation turn-event stream.
type EventSubscription struct {
	Events   <-chan companion.TurnEvent
	Failures <-chan error
	inner    eventstream.Subscription[companion.TurnEvent]
}

func (s EventSubscription) Unsubscribe() {
	s.inner.Unsubscribe()
}

// EventHub fans turn events out to per-conversation WebSocket watchers.
type EventHub struct {
	inner *eventstream.Hub[string, companion.TurnEvent]
}

func NewEventHub() *EventHub {
	return &EventHub{inner: eventstream.New[string, companion.TurnEvent](eventHubBuffer, ErrEventSubscriberOverflow)}
}

// Subscribe returns one ordered stream and a separate terminal-failure signal.
func (h *EventHub) Subscribe(conversationID string) EventSubscription {
	if h == nil || conversationID == "" {
		return closedEventSubscription()
	}
	inner := h.inner.Subscribe(conversationID)
	return EventSubscription{Events: inner.Events, Failures: inner.Failures, inner: inner}
}

func closedEventSubscription() EventSubscription {
	events := make(chan companion.TurnEvent)
	failures := make(chan error)
	close(events)
	close(failures)
	return EventSubscription{Events: events, Failures: failures}
}

// Publish never blocks Core turn execution. A slow subscriber is failed and removed.
func (h *EventHub) Publish(event companion.TurnEvent) {
	if h == nil || event.ConversationID == "" {
		return
	}
	h.inner.Publish(event.ConversationID, event)
}

func (h *EventHub) SubscriberCount() uint64 {
	if h == nil {
		return 0
	}
	return h.inner.SubscriberCount()
}

func (h *EventHub) Close() {
	if h == nil {
		return
	}
	h.inner.Close()
}
