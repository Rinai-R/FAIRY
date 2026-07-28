package core

import (
	"fairy/api"
	"fairy/initiative"
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
	Events   <-chan initiative.Event
	Failures <-chan error
	inner    streamSubscription[initiative.Event]
}

func (s ParticipationSubscription) Unsubscribe() {
	s.inner.Unsubscribe()
}

type ParticipationHub struct {
	inner *streamHub[string, initiative.Event]
}

func NewParticipationHub() *ParticipationHub {
	return &ParticipationHub{inner: newStreamHub[string, initiative.Event](
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
	events := make(chan initiative.Event)
	failures := make(chan error)
	close(events)
	close(failures)
	return ParticipationSubscription{Events: events, Failures: failures}
}

func (h *ParticipationHub) Publish(event initiative.Event) {
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
