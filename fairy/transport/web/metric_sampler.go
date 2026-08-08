package web

import (
	"context"
	"errors"
	"sync"
	"time"

	"go.uber.org/zap"
)

const defaultMetricSampleInterval = 5 * time.Second

func enqueueMetricSample(ctx context.Context, collect metricCollector, history ObservabilityHistory) error {
	_, point, err := collect(ctx)
	if err != nil {
		return err
	}
	history.EnqueueMetric(point)
	return nil
}

type metricSampler struct {
	interval time.Duration
	logger   *zap.Logger
	sample   func(context.Context) error

	ctx     context.Context
	cancel  context.CancelFunc
	done    chan struct{}
	mu      sync.Mutex
	started bool
	stopped bool
}

func newMetricSampler(interval time.Duration, logger *zap.Logger, sample func(context.Context) error) *metricSampler {
	ctx, cancel := context.WithCancel(context.Background())
	return &metricSampler{
		interval: interval,
		logger:   logger,
		sample:   sample,
		ctx:      ctx,
		cancel:   cancel,
		done:     make(chan struct{}),
	}
}

func (s *metricSampler) Start() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started || s.stopped {
		return
	}
	s.started = true
	go s.run()
}

func (s *metricSampler) Stop() {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	s.stopped = true
	started := s.started
	s.cancel()
	s.mu.Unlock()
	if started {
		<-s.done
	}
}

func (s *metricSampler) run() {
	defer close(s.done)
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	runMetricSampling(s.ctx, ticker.C, s.sample, s.reporter())
}

func (s *metricSampler) reporter() func(error) {
	failed := false
	return func(err error) {
		if err == nil {
			if failed {
				s.logger.Info("observability metric sampling recovered")
				failed = false
			}
			return
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		if failed {
			return
		}
		failed = true
		s.logger.Warn("observability metric sampling failed", zap.Error(err))
	}
}

func runMetricSampling(
	ctx context.Context,
	ticks <-chan time.Time,
	sample func(context.Context) error,
	report func(error),
) {
	report(sample(ctx))
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			report(sample(ctx))
		}
	}
}
