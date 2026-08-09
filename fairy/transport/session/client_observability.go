package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"fairy/runtime/observability"
)

func (c *Client) Logs(ctx context.Context, query LogQuery) (LogResponse, error) {
	path, err := logPath("/v1/logs", query, true)
	if err != nil {
		return LogResponse{}, err
	}
	var result LogResponse
	err = c.doJSON(ctx, "query logs", http.MethodGet, path, nil, &result)
	if err == nil && result.Entries == nil {
		err = errors.New("log response is missing entries")
	}
	return result, err
}

func (c *Client) OpenLogs(ctx context.Context, query LogQuery, readyTimeout time.Duration) (EventStream, error) {
	path, err := logPath("/v1/logs/stream", query, false)
	if err != nil {
		return nil, err
	}
	return c.openReadyStream(ctx, "follow logs", path, readyTimeout)
}

func (c *Client) Metrics(ctx context.Context) (Metrics, error) {
	var result Metrics
	err := c.doJSON(ctx, "read metrics", http.MethodGet, "/v1/metrics", nil, &result)
	if err == nil && (result.GeneratedAtUnixMS == 0 || result.Process.GoVersion == "" || result.HTTP.Routes == nil || result.Messages.Recent == nil || len(result.Usage.Overall) == 0 || len(result.Usage.Turns) == 0 || len(result.Database) == 0) {
		err = errors.New("metrics response is missing required fields")
	}
	if err == nil {
		err = validateExperienceMetrics(result.Runtime.Experience)
	}
	return result, err
}

func validateExperienceMetrics(stats ExperienceStats) error {
	if strings.TrimSpace(stats.CacheIdentityVersion) == "" {
		return errors.New("metrics response experience cache identity version is required")
	}
	values := []struct {
		name  string
		value int64
	}{
		{name: "learning.enqueued", value: stats.Learning.Enqueued},
		{name: "learning.dropped", value: stats.Learning.Dropped},
		{name: "learning.succeeded", value: stats.Learning.Succeeded},
		{name: "learning.failed", value: stats.Learning.Failed},
		{name: "learning.modelCalls", value: stats.Learning.ModelCalls},
		{name: "learning.inputTokens", value: stats.Learning.InputTokens},
		{name: "learning.cachedObservedInputTokens", value: stats.Learning.CachedObservedInputTokens},
		{name: "learning.cachedInputTokens", value: stats.Learning.CachedInputTokens},
		{name: "learning.cacheWriteTokens", value: stats.Learning.CacheWriteTokens},
		{name: "learning.outputTokens", value: stats.Learning.OutputTokens},
		{name: "feedback.registered", value: stats.Feedback.Registered},
		{name: "feedback.superseded", value: stats.Feedback.Superseded},
		{name: "feedback.dropped", value: stats.Feedback.Dropped},
		{name: "feedback.succeeded", value: stats.Feedback.Succeeded},
		{name: "feedback.failed", value: stats.Feedback.Failed},
		{name: "feedback.modelCalls", value: stats.Feedback.ModelCalls},
		{name: "feedback.inputTokens", value: stats.Feedback.InputTokens},
		{name: "feedback.cachedObservedInputTokens", value: stats.Feedback.CachedObservedInputTokens},
		{name: "feedback.cachedInputTokens", value: stats.Feedback.CachedInputTokens},
		{name: "feedback.cacheWriteTokens", value: stats.Feedback.CacheWriteTokens},
		{name: "feedback.outputTokens", value: stats.Feedback.OutputTokens},
	}
	for _, item := range values {
		if item.value < 0 {
			return fmt.Errorf("metrics response experience metric %s is negative", item.name)
		}
	}
	return nil
}

type TraceSearchResponse struct {
	MessageID string         `json:"messageId"`
	Traces    []MessageTrace `json:"traces"`
}

type MessageTrace = observability.MessageTrace
type MessageTraceDetail = observability.MessageTraceDetail
type TraceSpan = observability.TraceSpan

func ValidCorrelationID(value string) bool {
	return observability.ValidCorrelationID(value)
}

func (c *Client) TracesByMessageID(ctx context.Context, messageID string) (TraceSearchResponse, error) {
	if !observability.ValidCorrelationID(messageID) {
		return TraceSearchResponse{}, errors.New("message ID is invalid")
	}
	values := url.Values{}
	values.Set("messageId", messageID)
	var result TraceSearchResponse
	if err := c.doJSON(ctx, "search traces", http.MethodGet, "/v1/traces?"+values.Encode(), nil, &result); err != nil {
		return TraceSearchResponse{}, err
	}
	if result.MessageID != messageID || result.Traces == nil {
		return TraceSearchResponse{}, errors.New("trace search response is invalid")
	}
	for _, trace := range result.Traces {
		if !validTraceSummary(trace, messageID) {
			return TraceSearchResponse{}, errors.New("trace search response contains invalid trace")
		}
	}
	return result, nil
}

func (c *Client) Trace(ctx context.Context, traceID string) (MessageTraceDetail, error) {
	if !observability.ValidCorrelationID(traceID) {
		return MessageTraceDetail{}, errors.New("trace ID is invalid")
	}
	var result MessageTraceDetail
	if err := c.doJSON(ctx, "read trace", http.MethodGet, "/v1/traces/"+url.PathEscape(traceID), nil, &result); err != nil {
		return MessageTraceDetail{}, err
	}
	if result.TraceID != traceID || result.ConversationID == "" || result.Status == "" || result.StartedAtUnixMS <= 0 || result.Spans == nil {
		return observability.MessageTraceDetail{}, errors.New("trace response is invalid")
	}
	return result, nil
}

func validTraceSummary(trace MessageTrace, messageID string) bool {
	if !observability.ValidCorrelationID(trace.TraceID) || trace.MessageID != messageID || trace.ConversationID == "" || trace.Status == "" || trace.ReceivedAtUnixMS <= 0 {
		return false
	}
	return true
}

func DecodeLogEntry(event SSEEvent) (observability.LogEntry, error) {
	if event.Event != "log" {
		return observability.LogEntry{}, errors.New("SSE event is not log")
	}
	var result observability.LogEntry
	if err := json.Unmarshal(event.Data, &result); err != nil {
		return observability.LogEntry{}, err
	}
	if result.Sequence == 0 || result.TimestampUnixMS == 0 || !validLogLevel(result.Level) || result.Fields == nil {
		return observability.LogEntry{}, errors.New("invalid log entry")
	}
	return result, nil
}

func logPath(path string, query LogQuery, includeLimit bool) (string, error) {
	if query.Level != "" && !validLogLevel(query.Level) {
		return "", errors.New("level must be one of debug, info, warn, error")
	}
	if !includeLimit && query.Limit != 0 {
		return "", errors.New("limit is not supported for log streams")
	}
	if includeLimit && (query.Limit < 0 || query.Limit > 500) {
		return "", errors.New("limit must be between 1 and 500")
	}
	values := url.Values{}
	if query.Level != "" {
		values.Set("level", query.Level)
	}
	if query.LoggerPrefix != "" {
		values.Set("logger", query.LoggerPrefix)
	}
	if query.AfterSequence != 0 {
		values.Set("afterSequence", strconv.FormatUint(query.AfterSequence, 10))
	}
	if query.Limit != 0 {
		values.Set("limit", strconv.Itoa(query.Limit))
	}
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	return path, nil
}

func validLogLevel(level string) bool {
	switch level {
	case "debug", "info", "warn", "error":
		return true
	default:
		return false
	}
}
