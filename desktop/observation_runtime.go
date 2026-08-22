package main

import (
	"context"
	"errors"
	"sync"
	"time"

	"fairy/transport/session"
)

// desktopObservationSampler is the only platform-specific seam. Implementations
// may report coarse facts, but must never return prompt text, window titles or
// model-derived interpretation.
type desktopObservationSampler func(context.Context) (session.DesktopObservation, error)

type desktopObservationSubmitter func(context.Context, session.DesktopObservation) error

type desktopObservationRuntime struct {
	mu       sync.Mutex
	sampler  desktopObservationSampler
	submit   desktopObservationSubmitter
	policy   observationPolicy
	schedule *observationScheduler
	cancel   context.CancelFunc
	runCtx   context.Context
}

func newDesktopObservationRuntime(sampler desktopObservationSampler, submit desktopObservationSubmitter, config observationSchedulerConfig) (*desktopObservationRuntime, error) {
	if sampler == nil || submit == nil {
		return nil, errors.New("desktop observation runtime requires sampler and submitter")
	}
	runtime := &desktopObservationRuntime{sampler: sampler, submit: submit}
	scheduler, err := newObservationScheduler(config, runtime.evaluate)
	if err != nil {
		return nil, err
	}
	runtime.schedule = scheduler
	return runtime, nil
}

func (r *desktopObservationRuntime) evaluate(ctx context.Context) error {
	observation, err := r.sampler(ctx)
	if err != nil {
		return err
	}
	now := time.Now()
	if err := r.policy.Enqueue(observation, now); err != nil {
		return err
	}
	for {
		queued, ok := r.policy.Next(now)
		if !ok {
			break
		}
		if err := r.submit(ctx, queued); err != nil {
			return err
		}
		if err := r.policy.Ack(queued.ObservationID); err != nil {
			return err
		}
	}
	return nil
}

// Start is intentionally explicit: production composition must opt in after
// resolving privacy settings and a trusted platform sampler.
func (r *desktopObservationRuntime) Start(ctx context.Context) error {
	if ctx == nil {
		return errors.New("desktop observation runtime context is required")
	}
	r.mu.Lock()
	if r.cancel != nil {
		r.mu.Unlock()
		return errors.New("desktop observation runtime is already running")
	}
	runCtx, cancel := context.WithCancel(ctx)
	r.cancel = cancel
	r.runCtx = runCtx
	scheduler := r.schedule
	r.mu.Unlock()
	go func() {
		_ = scheduler.Run(runCtx)
		r.mu.Lock()
		if r.runCtx == runCtx {
			r.cancel = nil
			r.runCtx = nil
		}
		r.mu.Unlock()
	}()
	return nil
}

func (r *desktopObservationRuntime) Stop() {
	r.mu.Lock()
	cancel := r.cancel
	r.cancel = nil
	r.runCtx = nil
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (r *desktopObservationRuntime) Resume() {
	r.schedule.Resume()
}
