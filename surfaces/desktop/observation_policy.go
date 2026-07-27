package main

import (
	"errors"
	"sync"
	"time"

	"fairy/session"
)

const (
	desktopObservationQueueLimit  = 32
	desktopObservationFreshness   = 10 * time.Minute
	desktopObservationMinInterval = 20 * time.Second
)

type observationPolicy struct {
	mu         sync.Mutex
	last       session.DesktopObservation
	lastSentAt time.Time
	queue      []session.DesktopObservation
	dropped    uint64
	stale      uint64
}

func (p *observationPolicy) Accept(sample session.DesktopObservation, now time.Time) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	privacyTransition := sample.Lifecycle == session.DesktopLifecyclePrivacyOn || sample.Lifecycle == session.DesktopLifecyclePrivacyOff
	if sample.Privacy != session.DesktopPrivacyNormal && !privacyTransition {
		return false
	}
	if now.Sub(time.UnixMilli(sample.TimestampUnixMS)) > desktopObservationFreshness {
		p.stale++
		return false
	}
	if p.last.Activity == sample.Activity && p.last.Lifecycle == sample.Lifecycle && p.last.Privacy == sample.Privacy && now.Sub(p.lastSentAt) < desktopObservationMinInterval {
		return false
	}
	p.last = sample
	p.lastSentAt = now
	return true
}

func (p *observationPolicy) Enqueue(sample session.DesktopObservation, now time.Time) error {
	if p == nil {
		return errors.New("observation policy is unavailable")
	}
	if !p.Accept(sample, now) {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.queue) >= desktopObservationQueueLimit {
		p.queue = p.queue[1:]
		p.dropped++
	}
	p.queue = append(p.queue, sample)
	return nil
}

func (p *observationPolicy) Next(now time.Time) (session.DesktopObservation, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for len(p.queue) > 0 {
		sample := p.queue[0]
		if now.Sub(time.UnixMilli(sample.TimestampUnixMS)) > desktopObservationFreshness {
			p.queue = p.queue[1:]
			p.stale++
			continue
		}
		return sample, true
	}
	return session.DesktopObservation{}, false
}

func (p *observationPolicy) Ack(observationID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.queue) == 0 || p.queue[0].ObservationID != observationID {
		return errors.New("desktop observation ack does not match queued head")
	}
	p.queue = p.queue[1:]
	return nil
}

func (p *observationPolicy) Drain(now time.Time) []session.DesktopObservation {
	out := make([]session.DesktopObservation, 0)
	for {
		sample, ok := p.Next(now)
		if !ok {
			return out
		}
		out = append(out, sample)
		_ = p.Ack(sample.ObservationID)
	}
}

func (p *observationPolicy) Counters() (dropped, stale uint64) {
	if p == nil {
		return 0, 0
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.dropped, p.stale
}
