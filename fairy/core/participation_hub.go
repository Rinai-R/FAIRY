package core

import (
	"fairy/api"
	"fairy/initiative"
)

const participationHubBuffer = 64

var ErrParticipationSubscriberOverflow = api.ErrParticipationSubscriberOverflow

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
	return &ParticipationHub{inner: newStreamHub[string, initiative.Event](participationHubBuffer, ErrParticipationSubscriberOverflow)}
}

func (h *ParticipationHub) Subscribe(conversationID string) ParticipationSubscription {
	if h == nil || conversationID == "" {
		return closedParticipationSubscription()
	}
	inner := h.inner.Subscribe(conversationID)
	return ParticipationSubscription{Events: inner.Events, Failures: inner.Failures, inner: inner}
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
