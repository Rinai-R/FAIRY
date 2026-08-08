package interaction

import (
	"errors"
	"strings"
	"sync"

	"fairy/transport/session"
)

const DefaultCapabilityLeaseCapacity = 256

var ErrCapabilityCapacity = errors.New("output capability lease capacity reached")

// CapabilityRegistry owns the process-local leases advertised by live Surface
// connections. A connection owner can replace its lease without consuming a
// second slot; capabilities from concurrent owners are merged on read.
type CapabilityRegistry struct {
	mu             sync.RWMutex
	capacity       int
	leases         int
	byConversation map[string]map[string]session.OutputCapabilities
}

func NewCapabilityRegistry(capacity int) *CapabilityRegistry {
	if capacity < 1 {
		capacity = DefaultCapabilityLeaseCapacity
	}
	return &CapabilityRegistry{
		capacity:       capacity,
		byConversation: make(map[string]map[string]session.OutputCapabilities),
	}
}

func (r *CapabilityRegistry) Bind(ownerID, conversationID string, capabilities session.OutputCapabilities) error {
	if r == nil {
		return errors.New("output capability registry is nil")
	}
	ownerID = strings.TrimSpace(ownerID)
	if ownerID == "" {
		return errors.New("capability owner_id is required")
	}
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return errors.New("conversation_id is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	owners := r.byConversation[conversationID]
	if owners != nil {
		if _, exists := owners[ownerID]; exists {
			owners[ownerID] = capabilities
			return nil
		}
	}
	if r.leases >= r.capacity {
		return ErrCapabilityCapacity
	}
	if owners == nil {
		owners = make(map[string]session.OutputCapabilities)
		r.byConversation[conversationID] = owners
	}
	owners[ownerID] = capabilities
	r.leases++
	return nil
}

func (r *CapabilityRegistry) Unbind(ownerID, conversationID string) {
	if r == nil {
		return
	}
	ownerID = strings.TrimSpace(ownerID)
	conversationID = strings.TrimSpace(conversationID)
	if ownerID == "" || conversationID == "" {
		return
	}
	r.mu.Lock()
	if owners := r.byConversation[conversationID]; owners != nil {
		if _, exists := owners[ownerID]; exists {
			delete(owners, ownerID)
			r.leases--
		}
		if len(owners) == 0 {
			delete(r.byConversation, conversationID)
		}
	}
	r.mu.Unlock()
}

func (r *CapabilityRegistry) Resolve(conversationID string) session.OutputCapabilities {
	if r == nil {
		return session.OutputCapabilities{}
	}
	r.mu.RLock()
	var capabilities session.OutputCapabilities
	for _, lease := range r.byConversation[strings.TrimSpace(conversationID)] {
		capabilities.Sticker = capabilities.Sticker || lease.Sticker
	}
	r.mu.RUnlock()
	return capabilities
}

func (r *CapabilityRegistry) Clear() {
	if r == nil {
		return
	}
	r.mu.Lock()
	clear(r.byConversation)
	r.leases = 0
	r.mu.Unlock()
}

func (r *CapabilityRegistry) LeaseCount() int {
	if r == nil {
		return 0
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.leases
}

func (r *CapabilityRegistry) ConversationCount() int {
	if r == nil {
		return 0
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.byConversation)
}

func (r *CapabilityRegistry) OwnerCount(conversationID string) int {
	if r == nil {
		return 0
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.byConversation[strings.TrimSpace(conversationID)])
}
