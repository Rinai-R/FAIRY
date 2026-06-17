package health

import (
	"context"
	"time"
)

type Status string

const (
	StatusOK       Status = "ok"
	StatusDegraded Status = "degraded"
	StatusDown     Status = "down"
	StatusUnknown  Status = "unknown"
)

type Result struct {
	Domain    string            `json:"domain"`
	Provider  string            `json:"provider"`
	Status    Status            `json:"status"`
	LatencyMS int64             `json:"latency_ms,omitempty"`
	Message   string            `json:"message,omitempty"`
	CheckedAt time.Time         `json:"checked_at"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}

type Checker interface {
	Check(ctx context.Context) Result
}

func Measure(domain string, provider string, check func(context.Context) (Status, string, map[string]string)) func(context.Context) Result {
	return func(ctx context.Context) Result {
		start := time.Now()
		status, message, metadata := check(ctx)
		return Result{
			Domain:    domain,
			Provider:  provider,
			Status:    status,
			LatencyMS: time.Since(start).Milliseconds(),
			Message:   message,
			CheckedAt: time.Now().UTC(),
			Metadata:  metadata,
		}
	}
}
