package runtime

import (
	"errors"
	"sync"

	"fairy/companion"
)

const participationHubBuffer = 64

var ErrParticipationSubscriberOverflow = errors.New("participation subscriber overflow")

type participationSubscriber struct {
	events   chan companion.ParticipationEvent
	failures chan error
}

type ParticipationSubscription struct {
	Events      <-chan companion.ParticipationEvent
	Failures    <-chan error
	unsubscribe func()
}

func (s ParticipationSubscription) Unsubscribe() {
	if s.unsubscribe != nil {
		s.unsubscribe()
	}
}

type ParticipationHub struct {
	mu     sync.Mutex
	subs   map[string]map[*participationSubscriber]struct{}
	closed bool
}

func NewParticipationHub() *ParticipationHub {
	return &ParticipationHub{subs: make(map[string]map[*participationSubscriber]struct{})}
}

func (h *ParticipationHub) Subscribe(conversationID string) ParticipationSubscription {
	subscriber := &participationSubscriber{events: make(chan companion.ParticipationEvent, participationHubBuffer), failures: make(chan error, 1)}
	if h == nil || conversationID == "" {
		close(subscriber.events)
		close(subscriber.failures)
		return ParticipationSubscription{Events: subscriber.events, Failures: subscriber.failures}
	}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		close(subscriber.events)
		close(subscriber.failures)
		return ParticipationSubscription{Events: subscriber.events, Failures: subscriber.failures}
	}
	if h.subs[conversationID] == nil {
		h.subs[conversationID] = make(map[*participationSubscriber]struct{})
	}
	h.subs[conversationID][subscriber] = struct{}{}
	h.mu.Unlock()
	var once sync.Once
	return ParticipationSubscription{Events: subscriber.events, Failures: subscriber.failures, unsubscribe: func() {
		once.Do(func() {
			h.mu.Lock()
			h.removeLocked(conversationID, subscriber, nil)
			h.mu.Unlock()
		})
	}}
}

func (h *ParticipationHub) Publish(event companion.ParticipationEvent) {
	if h == nil || event.ConversationID == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for subscriber := range h.subs[event.ConversationID] {
		select {
		case subscriber.events <- event:
		default:
			h.removeLocked(event.ConversationID, subscriber, ErrParticipationSubscriberOverflow)
		}
	}
}

func (h *ParticipationHub) removeLocked(conversationID string, subscriber *participationSubscriber, failure error) {
	subscribers := h.subs[conversationID]
	if _, exists := subscribers[subscriber]; !exists {
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

func (h *ParticipationHub) Close() {
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
