// Package interaction owns process-local interaction bindings and live Surface
// capability leases. Durable endpoint bindings remain outside this package.
package interaction

import (
	"errors"
	"strings"
	"sync"

	"fairy/transport/session"
)

const DefaultBindingCacheCapacity = 1024

type bindingEntry struct {
	conversationID string
	binding        session.Binding
	previous       *bindingEntry
	next           *bindingEntry
}

// BindingCache is a bounded, concurrency-safe read accelerator. It deliberately
// rejects rebinding because one durable conversation has one immutable endpoint.
type BindingCache struct {
	mu       sync.Mutex
	capacity int
	entries  map[string]*bindingEntry
	oldest   *bindingEntry
	newest   *bindingEntry
}

func NewBindingCache(capacity int) *BindingCache {
	if capacity < 1 {
		capacity = 1
	}
	return &BindingCache{
		capacity: capacity,
		entries:  make(map[string]*bindingEntry, capacity),
	}
}

func (c *BindingCache) Get(conversationID string) (session.Binding, bool) {
	if c == nil {
		return session.Binding{}, false
	}
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return session.Binding{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, found := c.entries[conversationID]
	if !found {
		return session.Binding{}, false
	}
	c.touch(entry)
	return entry.binding, true
}

func (c *BindingCache) Put(conversationID string, binding session.Binding) error {
	if c == nil {
		return errors.New("interaction binding cache is nil")
	}
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return errors.New("conversation_id is required")
	}
	if err := binding.Validate(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if entry, found := c.entries[conversationID]; found {
		if entry.binding != binding {
			return errors.New("conversation interaction binding is immutable")
		}
		c.touch(entry)
		return nil
	}
	if len(c.entries) >= c.capacity {
		c.remove(c.oldest)
	}
	entry := &bindingEntry{conversationID: conversationID, binding: binding}
	c.entries[conversationID] = entry
	c.append(entry)
	return nil
}

func (c *BindingCache) touch(entry *bindingEntry) {
	if entry == nil || c.newest == entry {
		return
	}
	c.unlink(entry)
	c.append(entry)
}

func (c *BindingCache) append(entry *bindingEntry) {
	entry.previous = c.newest
	entry.next = nil
	if c.newest != nil {
		c.newest.next = entry
	} else {
		c.oldest = entry
	}
	c.newest = entry
}

func (c *BindingCache) remove(entry *bindingEntry) {
	if entry == nil {
		return
	}
	delete(c.entries, entry.conversationID)
	c.unlink(entry)
}

func (c *BindingCache) unlink(entry *bindingEntry) {
	if entry.previous != nil {
		entry.previous.next = entry.next
	} else {
		c.oldest = entry.next
	}
	if entry.next != nil {
		entry.next.previous = entry.previous
	} else {
		c.newest = entry.previous
	}
	entry.previous = nil
	entry.next = nil
}

func (c *BindingCache) Clear() {
	if c == nil {
		return
	}
	c.mu.Lock()
	clear(c.entries)
	c.oldest = nil
	c.newest = nil
	c.mu.Unlock()
}

func (c *BindingCache) Len() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}
