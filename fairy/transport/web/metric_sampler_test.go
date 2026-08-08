package web

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"fairy/runtime/observability"

	"github.com/cloudwego/hertz/pkg/app"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestRunMetricSamplingSamplesImmediatelyAndOnTicksUntilCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	ticks := make(chan time.Time)
	sampled := make(chan struct{}, 3)
	done := make(chan struct{})
	go func() {
		defer close(done)
		runMetricSampling(ctx, ticks, func(context.Context) error {
			sampled <- struct{}{}
			return nil
		}, func(error) {})
	}()

	waitForSignal(t, sampled, "immediate sample")
	ticks <- time.Now()
	waitForSignal(t, sampled, "tick sample")
	cancel()
	waitForSignal(t, done, "sampler shutdown")

	select {
	case <-sampled:
		t.Fatal("sampled after cancellation")
	default:
	}
}

func TestMetricSamplerStartsOnceAndStopWaitsForWorker(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int64
	sampler := newMetricSampler(time.Hour, zap.NewNop(), func(context.Context) error {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		return nil
	})

	sampler.Start()
	sampler.Start()
	waitForSignal(t, started, "initial sample")
	stopped := make(chan struct{})
	go func() {
		sampler.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
		t.Fatal("Stop returned before the active sample completed")
	default:
	}
	close(release)
	waitForSignal(t, stopped, "sampler stop")
	sampler.Stop()
	if got := calls.Load(); got != 1 {
		t.Fatalf("sample calls = %d, want 1", got)
	}
}

func TestMetricSamplerReportsFailureWindowAndRecoveryOnce(t *testing.T) {
	core, logs := observer.New(zap.DebugLevel)
	sampler := newMetricSampler(time.Hour, zap.New(core), func(context.Context) error { return nil })
	report := sampler.reporter()
	failure := errors.New("temporary metrics dependency failure")

	report(failure)
	report(failure)
	report(nil)
	report(nil)
	report(context.Canceled)

	if got := logs.FilterMessage("observability metric sampling failed").Len(); got != 1 {
		t.Fatalf("failure logs = %d, want 1", got)
	}
	if got := logs.FilterMessage("observability metric sampling recovered").Len(); got != 1 {
		t.Fatalf("recovery logs = %d, want 1", got)
	}
}

func TestEnqueueMetricSampleWritesOnlySuccessfulCollection(t *testing.T) {
	history := &metricHistoryFake{}
	failure := errors.New("metrics unavailable")
	collect := func(context.Context) (metricsResponse, observability.MetricHistoryPoint, error) {
		return metricsResponse{}, observability.MetricHistoryPoint{}, failure
	}
	if err := enqueueMetricSample(t.Context(), collect, history); !errors.Is(err, failure) {
		t.Fatalf("failure = %v, want %v", err, failure)
	}
	if got := history.enqueued.Load(); got != 0 {
		t.Fatalf("failed collection enqueued %d samples", got)
	}

	want := observability.MetricHistoryPoint{TimestampUnixMS: 42, HTTPScope: httpMetricScope}
	collect = func(context.Context) (metricsResponse, observability.MetricHistoryPoint, error) {
		return metricsResponse{}, want, nil
	}
	if err := enqueueMetricSample(t.Context(), collect, history); err != nil {
		t.Fatal(err)
	}
	if got := history.enqueued.Load(); got != 1 {
		t.Fatalf("successful collection enqueued %d samples, want 1", got)
	}
	if history.last != want {
		t.Fatalf("enqueued point = %#v, want %#v", history.last, want)
	}
}

func TestHandleMetricsReadsHistoryWithoutEnqueueing(t *testing.T) {
	history := &metricHistoryFake{
		metrics: []observability.MetricHistoryPoint{{
			TimestampUnixMS: 100,
			HTTPScope:       httpMetricScope,
		}},
	}
	server := &Server{
		rt: &Dependencies{History: history},
		metricCollector: func(context.Context) (metricsResponse, observability.MetricHistoryPoint, error) {
			response := metricsResponse{GeneratedAtUnixMS: 200}
			point := observability.MetricHistoryPoint{TimestampUnixMS: 200, HTTPScope: httpMetricScope}
			return response, point, nil
		},
	}

	for range 3 {
		var request app.RequestContext
		server.handleMetrics(t.Context(), &request)
		if got := request.Response.StatusCode(); got != 200 {
			t.Fatalf("status = %d, body = %s", got, request.Response.Body())
		}
	}
	if got := history.enqueued.Load(); got != 0 {
		t.Fatalf("HTTP metric reads enqueued %d persistent samples", got)
	}
}

type metricHistoryFake struct {
	metrics  []observability.MetricHistoryPoint
	enqueued atomic.Int64
	last     observability.MetricHistoryPoint
}

func (f *metricHistoryFake) RecentLogs(context.Context, int) ([]observability.LogEntry, error) {
	return nil, nil
}

func (f *metricHistoryFake) RecentTraces(context.Context, int) ([]observability.MessageTraceDetail, error) {
	return nil, nil
}

func (f *metricHistoryFake) Trace(context.Context, string) (observability.MessageTraceDetail, bool, error) {
	return observability.MessageTraceDetail{}, false, nil
}

func (f *metricHistoryFake) TracesByMessageID(context.Context, string, int) ([]observability.MessageTraceDetail, error) {
	return nil, nil
}

func (f *metricHistoryFake) RecentMetrics(context.Context, int) ([]observability.MetricHistoryPoint, error) {
	return f.metrics, nil
}

func (f *metricHistoryFake) EnqueueMetric(point observability.MetricHistoryPoint) bool {
	f.last = point
	f.enqueued.Add(1)
	return true
}

func (f *metricHistoryFake) Stats() observability.HistoryStats {
	return observability.HistoryStats{}
}

func waitForSignal(t *testing.T, signal <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s", name)
	}
}
