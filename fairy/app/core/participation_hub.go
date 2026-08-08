package core

import (
	api "fairy/transport/web"
)

const (
	participationHubBuffer             = 64
	participationHubSubscriberCapacity = 256
)

var (
	ErrParticipationSubscriberOverflow = api.ErrParticipationSubscriberOverflow
	ErrParticipationSubscriberCapacity = api.ErrParticipationSubscriberCapacity
)

type ParticipationSubscription struct {
	Events   <-chan api.ParticipationEvent
	Failures <-chan error
	inner    streamSubscription[api.ParticipationEvent]
}

func (s ParticipationSubscription) Unsubscribe() {
	s.inner.Unsubscribe()
}

type ParticipationHub struct {
	inner *streamHub[string, api.ParticipationEvent]
}

func NewParticipationHub() *ParticipationHub {
	return &ParticipationHub{inner: newStreamHub[string, api.ParticipationEvent](
		participationHubBuffer, participationHubSubscriberCapacity,
		ErrParticipationSubscriberOverflow, ErrParticipationSubscriberCapacity,
	)}
}

func (h *ParticipationHub) Subscribe(conversationID string) (ParticipationSubscription, error) {
	if h == nil || conversationID == "" {
		return closedParticipationSubscription(), nil
	}
	inner, err := h.inner.Subscribe(conversationID)
	return ParticipationSubscription{Events: inner.Events, Failures: inner.Failures, inner: inner}, err
}

func closedParticipationSubscription() ParticipationSubscription {
	events := make(chan api.ParticipationEvent)
	failures := make(chan error)
	close(events)
	close(failures)
	return ParticipationSubscription{Events: events, Failures: failures}
}

func (h *ParticipationHub) Publish(event api.ParticipationEvent) {
	if h == nil || event.ConversationID == "" {
		return
	}
	h.inner.Publish(event.ConversationID, event)
}

func (h *ParticipationHub) Close() {
	if h == nil {
		return
	}
	h.inner.Close()
}
