package coalescer

import "sync"

// Owner serializes work so at most one invocation runs and at most one
// additional pending rerun is retained while busy.
type Owner struct {
	mu      sync.Mutex
	active  bool
	pending bool
}

// Start claims ownership when idle. When already active it marks one pending
// rerun and returns false.
func (o *Owner) Start() bool {
	if o == nil {
		return false
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.active {
		o.pending = true
		return false
	}
	o.active = true
	return true
}

// Finish ends the current run. When a pending rerun exists it keeps ownership
// and returns true so the caller should loop; otherwise ownership is released.
func (o *Owner) Finish() (runAgain bool) {
	if o == nil {
		return false
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.pending {
		o.pending = false
		return true
	}
	o.active = false
	return false
}

// Abort releases ownership and clears any pending rerun.
func (o *Owner) Abort() {
	if o == nil {
		return
	}
	o.mu.Lock()
	o.active = false
	o.pending = false
	o.mu.Unlock()
}

// Snapshot reports whether work is active and whether a pending rerun is set.
func (o *Owner) Snapshot() (active, pending bool) {
	if o == nil {
		return false, false
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.active, o.pending
}
