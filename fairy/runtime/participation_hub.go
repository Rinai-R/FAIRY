package runtime

import (
	"errors"

	"fairy/internal/app/participation"
	"fairy/pkg/eventstream"
)

const participationHubBuffer = 64

var ErrParticipationSubscriberOverflow = errors.New("participation subscriber overflow")

type ParticipationSubscription struct {
	Events   <-chan participation.Event
	Failures <-chan error
	inner    eventstream.Subscription[participation.Event]
}

func (s ParticipationSubscription) Unsubscribe() {
	s.inner.Unsubscribe()
}

type ParticipationHub struct {
	inner *eventstream.Hub[string, participation.Event]
}

func NewParticipationHub() *ParticipationHub {
	return &ParticipationHub{inner: eventstream.New[string, participation.Event](participationHubBuffer, ErrParticipationSubscriberOverflow)}
}

func (h *ParticipationHub) Subscribe(conversationID string) ParticipationSubscription {
	if h == nil || conversationID == "" {
		return closedParticipationSubscription()
	}
	inner := h.inner.Subscribe(conversationID)
	return ParticipationSubscription{Events: inner.Events, Failures: inner.Failures, inner: inner}
}

func closedParticipationSubscription() ParticipationSubscription {
	events := make(chan participation.Event)
	failures := make(chan error)
	close(events)
	close(failures)
	return ParticipationSubscription{Events: events, Failures: failures}
}

func (h *ParticipationHub) Publish(event participation.Event) {
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
