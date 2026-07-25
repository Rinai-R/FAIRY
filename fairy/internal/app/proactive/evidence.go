package proactive

import (
	"errors"
	"sync"
	"time"

	"fairy/contracts/observation"
)

type evidence struct {
	ID         string
	AcceptedAt time.Time
}

// EvidenceRegistry tracks recently accepted desktop observation IDs without
// retaining raw observation payloads.
type EvidenceRegistry struct {
	mu      sync.Mutex
	items   map[string]evidence
	maxAge  time.Duration
	maxSize int
}

func NewEvidenceRegistry() *EvidenceRegistry {
	return &EvidenceRegistry{items: make(map[string]evidence), maxAge: 10 * time.Minute, maxSize: 128}
}

func (r *EvidenceRegistry) Accept(obs observation.DesktopObservation, now time.Time) error {
	if r == nil {
		return errors.New("desktop evidence registry is unavailable")
	}
	if err := obs.Validate(now); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.items) >= r.maxSize {
		for id := range r.items {
			delete(r.items, id)
			break
		}
	}
	r.items[obs.ObservationID] = evidence{ID: obs.ObservationID, AcceptedAt: now}
	return nil
}

func (r *EvidenceRegistry) ContainsFresh(id string, now time.Time) bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	item, ok := r.items[id]
	if !ok {
		return false
	}
	if now.Sub(item.AcceptedAt) > r.maxAge {
		delete(r.items, id)
		return false
	}
	return true
}
