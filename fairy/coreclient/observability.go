package coreclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"fairy/observability"
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
	if err == nil && (result.GeneratedAtUnixMS == 0 || result.Process.GoVersion == "" || result.HTTP.Routes == nil || len(result.Usage.Overall) == 0 || len(result.Usage.Turns) == 0 || len(result.Database) == 0 || len(result.Qdrant) == 0) {
		err = errors.New("metrics response is missing required fields")
	}
	return result, err
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
