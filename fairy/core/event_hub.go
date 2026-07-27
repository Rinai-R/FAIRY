package core

import (
	"fairy/api"
	"fairy/session"
)

const eventHubBuffer = 64

var ErrEventSubscriberOverflow = api.ErrEventSubscriberOverflow

// EventSubscription is one ordered per-conversation turn-event stream.
type EventSubscription struct {
	Events   <-chan session.Event
	Failures <-chan error
	inner    streamSubscription[session.Event]
}

func (s EventSubscription) Unsubscribe() {
	s.inner.Unsubscribe()
}

// EventHub fans turn events out to per-conversation WebSocket watchers.
type EventHub struct {
	inner *streamHub[string, session.Event]
}

func NewEventHub() *EventHub {
	return &EventHub{inner: newStreamHub[string, session.Event](eventHubBuffer, ErrEventSubscriberOverflow)}
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
	events := make(chan session.Event)
	failures := make(chan error)
	close(events)
	close(failures)
	return EventSubscription{Events: events, Failures: failures}
}

// Publish never blocks Core turn execution. A slow subscriber is failed and removed.
func (h *EventHub) Publish(event session.Event) {
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
