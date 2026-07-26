package vectorindex

import (
	"context"
	"sort"
	"sync"
	"time"
)

type OperationMetrics struct {
	Operation       string `json:"operation"`
	Requests        uint64 `json:"requests"`
	Errors          uint64 `json:"errors"`
	TotalDurationMS uint64 `json:"totalDurationMs"`
	MaxDurationMS   uint64 `json:"maxDurationMs"`
}

type MetricsSnapshot struct {
	Requests   uint64             `json:"requests"`
	Errors     uint64             `json:"errors"`
	PointCount uint64             `json:"pointCount"`
	Operations []OperationMetrics `json:"operations"`
}

type clientMetrics struct {
	mu         sync.Mutex
	requests   uint64
	errors     uint64
	operations map[string]*OperationMetrics
}

func newClientMetrics() *clientMetrics {
	return &clientMetrics{operations: make(map[string]*OperationMetrics)}
}

func (c *Client) observe(operation string, started time.Time, err error) {
	if c == nil {
		return
	}
	if c.metrics == nil {
		c.metrics = newClientMetrics()
	}
	duration := time.Since(started).Milliseconds()
	if duration < 0 {
		duration = 0
	}
	c.metrics.mu.Lock()
	defer c.metrics.mu.Unlock()
	c.metrics.requests++
	metric := c.metrics.operations[operation]
	if metric == nil {
		metric = &OperationMetrics{Operation: operation}
		c.metrics.operations[operation] = metric
	}
	metric.Requests++
	if err != nil {
		c.metrics.errors++
		metric.Errors++
	}
	metric.TotalDurationMS += uint64(duration)
	if uint64(duration) > metric.MaxDurationMS {
		metric.MaxDurationMS = uint64(duration)
	}
}

func (c *Client) Metrics(ctx context.Context) (MetricsSnapshot, error) {
	status, err := c.VerifyCollection(ctx)
	if err != nil {
		return MetricsSnapshot{}, err
	}
	snapshot := MetricsSnapshot{PointCount: status.PointsCount, Operations: []OperationMetrics{}}
	if c == nil || c.metrics == nil {
		return snapshot, nil
	}
	c.metrics.mu.Lock()
	defer c.metrics.mu.Unlock()
	snapshot.Requests = c.metrics.requests
	snapshot.Errors = c.metrics.errors
	snapshot.Operations = make([]OperationMetrics, 0, len(c.metrics.operations))
	for _, operation := range c.metrics.operations {
		snapshot.Operations = append(snapshot.Operations, *operation)
	}
	sort.Slice(snapshot.Operations, func(i, j int) bool {
		return snapshot.Operations[i].Operation < snapshot.Operations[j].Operation
	})
	return snapshot, nil
}
