package presence

import (
	"context"
	"errors"
	"strconv"

	"fairy/runtime/model"
)

const (
	participationTraceContextUnavailable = "context_unavailable"
	participationTraceModelFailed        = "model_request_failed"
	participationTraceInvalidDecision    = "invalid_decision"
	participationTraceSuperseded         = "superseded"
)

type ParticipationTraceObserver interface {
	StartParticipationSpan(traceID, operation, category string, attributes map[string]string) string
	FinishParticipationSpan(spanID, status string, attributes map[string]string)
}

type participationTrace struct {
	observer ParticipationTraceObserver
	traceID  string
}

func newParticipationTrace(observer ParticipationTraceObserver, messages []AmbientObservation) participationTrace {
	return participationTrace{observer: observer, traceID: authoritativeParticipationTraceID(messages)}
}

func authoritativeParticipationTraceID(messages []AmbientObservation) string {
	for index := len(messages) - 1; index >= 0; index-- {
		if messages[index].IsNew && messages[index].TraceID != "" {
			return messages[index].TraceID
		}
	}
	for index := len(messages) - 1; index >= 0; index-- {
		if messages[index].TraceID != "" {
			return messages[index].TraceID
		}
	}
	return ""
}

func (trace participationTrace) start(operation, category string, attributes map[string]string) string {
	if trace.observer == nil || trace.traceID == "" {
		return ""
	}
	return trace.observer.StartParticipationSpan(trace.traceID, operation, category, attributes)
}

func (trace participationTrace) finish(spanID, status string, attributes map[string]string) {
	if trace.observer == nil || spanID == "" {
		return
	}
	trace.observer.FinishParticipationSpan(spanID, status, attributes)
}

func participationTraceFailure(ctx context.Context, defaultCode string) (string, map[string]string) {
	if ctx != nil && errors.Is(ctx.Err(), context.Canceled) {
		return "interrupted", map[string]string{"errorCode": participationTraceSuperseded}
	}
	return "failed", map[string]string{"errorCode": defaultCode}
}

func participationModelTraceAttributes(attempt int, events []model.StreamEvent) map[string]string {
	attributes := map[string]string{
		"attempt": strconv.Itoa(attempt),
		"lane":    string(model.PromptLaneParticipate),
	}
	usage := model.LaneUsageFromEvents(model.PromptLaneParticipate, events, 0)
	if len(usage) == 0 {
		return attributes
	}
	observed := usage[0].Usage
	if observed.InputTokens != nil {
		attributes["inputTokens"] = strconv.FormatUint(*observed.InputTokens, 10)
	}
	if observed.OutputTokens != nil {
		attributes["outputTokens"] = strconv.FormatUint(*observed.OutputTokens, 10)
	}
	if observed.CachedInputTokens.Tokens != nil {
		attributes["cachedInputTokens"] = strconv.FormatUint(*observed.CachedInputTokens.Tokens, 10)
	}
	if observed.CacheWriteTokens.Tokens != nil {
		attributes["cacheWriteTokens"] = strconv.FormatUint(*observed.CacheWriteTokens.Tokens, 10)
	}
	return attributes
}
