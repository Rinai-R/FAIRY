package edge

import (
	"context"
	"errors"
	"strings"
	"time"

	"fairy/runtime/observability"
)

const maxTraceSearchResults = 50

func (m *Management) Metrics(ctx context.Context) (MetricsSnapshot, error) {
	rt := m.coreRuntime()
	if rt == nil || rt.Logs == nil || rt.Messages == nil || rt.History == nil {
		return MetricsSnapshot{}, ErrObservabilityUnavailable
	}
	history, err := rt.History.RecentMetrics(ctx, observability.DefaultMetricResponseLimit)
	if err != nil {
		return MetricsSnapshot{}, err
	}
	jobs := int64(0)
	if rt.Turn != nil {
		jobs = rt.Turn.ActiveBackgroundJobs()
	}
	return MetricsSnapshot{
		GeneratedAtUnixMS:    time.Now().UnixMilli(),
		Process:              observability.SnapshotProcess(rt.StartedAt),
		Logs:                 rt.Logs.Stats(),
		Messages:             rt.Messages.Snapshot(),
		History:              history,
		HistoryPersistence:   rt.History.Stats(),
		ActiveBackgroundJobs: jobs,
	}, nil
}

func (m *Management) Traces(ctx context.Context, messageID string) (TraceSearch, error) {
	rt := m.coreRuntime()
	if rt == nil || rt.Messages == nil || rt.History == nil {
		return TraceSearch{}, ErrObservabilityUnavailable
	}
	id := strings.TrimSpace(messageID)
	if !observability.ValidCorrelationID(id) {
		return TraceSearch{}, errors.New("messageId is invalid")
	}
	details := rt.Messages.TracesByMessageID(id, maxTraceSearchResults)
	history, err := rt.History.TracesByMessageID(ctx, id, maxTraceSearchResults)
	if err != nil {
		return TraceSearch{}, err
	}
	merged := mergeTraceDetails(details, history, maxTraceSearchResults)
	traces := make([]observability.MessageTrace, 0, len(merged))
	for _, detail := range merged {
		traces = append(traces, traceSummary(detail))
	}
	return TraceSearch{MessageID: id, Traces: traces}, nil
}

func (m *Management) Trace(ctx context.Context, traceID string) (TraceDetail, error) {
	rt := m.coreRuntime()
	if rt == nil || rt.Messages == nil || rt.History == nil {
		return TraceDetail{}, ErrObservabilityUnavailable
	}
	id := strings.TrimSpace(traceID)
	if !observability.ValidCorrelationID(id) {
		return TraceDetail{}, errors.New("traceId is invalid")
	}
	detail, ok := rt.Messages.Trace(id)
	if !ok {
		var err error
		detail, ok, err = rt.History.Trace(ctx, id)
		if err != nil {
			return TraceDetail{}, err
		}
	}
	if !ok {
		return TraceDetail{}, ErrTraceNotFound
	}
	return detail, nil
}

func (m *Management) Logs(filter LogFilter) (LogSnapshot, error) {
	rt := m.coreRuntime()
	if rt == nil || rt.Logs == nil {
		return LogSnapshot{}, ErrObservabilityUnavailable
	}
	return rt.Logs.Query(filter), nil
}

func (m *Management) SubscribeLogs(filter LogFilter) ([]LogEntry, <-chan LogEntry, func(), error) {
	rt := m.coreRuntime()
	if rt == nil || rt.Logs == nil {
		return nil, nil, nil, ErrObservabilityUnavailable
	}
	return rt.Logs.Subscribe(filter)
}

func mergeTraceDetails(live, history []observability.MessageTraceDetail, limit int) []observability.MessageTraceDetail {
	seen := make(map[string]struct{}, len(live)+len(history))
	merged := make([]observability.MessageTraceDetail, 0, min(limit, len(live)+len(history)))
	for _, detail := range append(live, history...) {
		if detail.TraceID == "" {
			continue
		}
		if _, ok := seen[detail.TraceID]; ok {
			continue
		}
		seen[detail.TraceID] = struct{}{}
		merged = append(merged, detail)
		if len(merged) == limit {
			break
		}
	}
	return merged
}

func traceSummary(detail observability.MessageTraceDetail) observability.MessageTrace {
	return observability.MessageTrace{
		TraceID:           detail.TraceID,
		MessageID:         detail.MessageID,
		Source:            detail.Source,
		ConversationID:    detail.ConversationID,
		TurnID:            detail.TurnID,
		Status:            detail.Status,
		ReceivedAtUnixMS:  detail.StartedAtUnixMS,
		CompletedAtUnixMS: detail.EndedAtUnixMS,
		TotalDurationMS:   detail.DurationMS,
	}
}
