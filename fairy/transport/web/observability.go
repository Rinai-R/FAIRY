package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"fairy/runtime/ledger"
	"fairy/runtime/observability"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/sse"
)

const (
	defaultLogLimit = 200
	maxLogLimit     = 500
	httpMetricScope = "conversation"
)

type runtimeMetrics struct {
	ActiveBackgroundJobs uint64           `json:"activeBackgroundJobs"`
	EventSubscribers     uint64           `json:"eventSubscribers"`
	AgentLoop            AgentLoopMetrics `json:"agentLoop"`
	Experience           ExperienceStats  `json:"experience"`
}

type metricsResponse struct {
	GeneratedAtUnixMS  int64                                `json:"generatedAtUnixMs"`
	Process            observability.ProcessMetrics         `json:"process"`
	HTTP               observability.HTTPMetricsSnapshot    `json:"http"`
	Logs               observability.LogStats               `json:"logs"`
	Messages           observability.MessageMetricsSnapshot `json:"messages"`
	Runtime            runtimeMetrics                       `json:"runtime"`
	Usage              ledger.UsageReport                   `json:"usage"`
	Database           databaseMetrics                      `json:"database"`
	History            []observability.MetricHistoryPoint   `json:"history"`
	HistoryPersistence observability.HistoryStats           `json:"historyPersistence"`
}

type metricCollector func(context.Context) (metricsResponse, observability.MetricHistoryPoint, error)

func (s *Server) registerObservabilityRoutes() {
	v1 := s.engine.Group("/v1")
	v1.Use(s.authMiddleware)
	v1.GET("/logs", s.handleLogs)
	v1.GET("/logs/stream", s.handleLogStream)
	v1.GET("/metrics", s.handleMetrics)
	v1.GET("/traces", s.handleTraceSearch)
	v1.GET("/traces/:traceId", s.handleTraceDetail)
}

const maxTraceSearchResults = 50

func (s *Server) handleTraceSearch(ctx context.Context, c *app.RequestContext) {
	messageID := strings.TrimSpace(c.Query("messageId"))
	if !validTraceCorrelationID(messageID) {
		writeErr(c, http.StatusBadRequest, errors.New("messageId is invalid"))
		return
	}
	details := s.rt.Messages.TracesByMessageID(messageID, maxTraceSearchResults)
	if s.rt.History != nil {
		history, err := s.rt.History.TracesByMessageID(ctx, messageID, maxTraceSearchResults)
		if err != nil {
			writeErr(c, http.StatusInternalServerError, err)
			return
		}
		details = mergeTraceDetails(details, history, maxTraceSearchResults)
	}
	traces := make([]observability.MessageTrace, 0, len(details))
	for _, detail := range details {
		traces = append(traces, traceSummary(detail))
	}
	c.JSON(http.StatusOK, map[string]any{"messageId": messageID, "traces": traces})
}

func validTraceCorrelationID(value string) bool {
	if value == "" || utf8.RuneCountInString(value) > 128 {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func (s *Server) handleTraceDetail(ctx context.Context, c *app.RequestContext) {
	traceID := strings.TrimSpace(c.Param("traceId"))
	if !validTraceCorrelationID(traceID) {
		writeErr(c, http.StatusBadRequest, errors.New("traceId is invalid"))
		return
	}
	detail, ok := s.rt.Messages.Trace(traceID)
	if !ok && s.rt.History != nil {
		var err error
		detail, ok, err = s.rt.History.Trace(ctx, traceID)
		if err != nil {
			writeErr(c, http.StatusInternalServerError, err)
			return
		}
	}
	if !ok {
		writeErr(c, http.StatusNotFound, errors.New("trace not found"))
		return
	}
	c.JSON(http.StatusOK, detail)
}

func (s *Server) metricsMiddleware(ctx context.Context, c *app.RequestContext) {
	route := c.FullPath()
	tracked, longLived := conversationHTTPRoute(route)
	if !tracked {
		c.Next(ctx)
		return
	}
	observation := s.rt.HTTPMetrics.Begin(string(c.Method()), route, longLived)
	c.Next(ctx)
	s.rt.HTTPMetrics.Finish(observation, c.Response.StatusCode())
}

func conversationHTTPRoute(route string) (tracked, longLived bool) {
	switch route {
	case "/v1/session/ws":
		return true, true
	case "/v1/session/browser-ticket",
		"/v1/sessions/:conversationId/messages",
		"/v1/sessions/:conversationId/turns/:turnId/runtime":
		return true, false
	default:
		return false, false
	}
}

func (s *Server) handleLogs(ctx context.Context, c *app.RequestContext) {
	filter, err := parseLogFilter(c, true)
	if err != nil {
		writeErr(c, http.StatusBadRequest, err)
		return
	}
	c.JSON(http.StatusOK, s.rt.Logs.Query(filter))
}

func (s *Server) handleLogStream(ctx context.Context, c *app.RequestContext) {
	filter, err := parseLogFilter(c, false)
	if err != nil {
		writeErr(c, http.StatusBadRequest, err)
		return
	}
	backlog, live, unsubscribe, err := s.rt.Logs.Subscribe(filter)
	if err != nil {
		if errors.Is(err, observability.ErrLogSubscriberCapacity) {
			writeErr(c, http.StatusServiceUnavailable, err)
			return
		}
		writeErr(c, http.StatusInternalServerError, err)
		return
	}
	defer unsubscribe()
	w := sse.NewWriter(c)
	defer w.Close()
	if err := w.WriteEvent("0", "ready", []byte(`{"ok":true}`)); err != nil {
		return
	}
	for _, entry := range backlog {
		if !writeLogEvent(w, entry) {
			return
		}
	}
	for {
		select {
		case <-ctx.Done():
			return
		case entry, ok := <-live:
			if !ok || !writeLogEvent(w, entry) {
				return
			}
		}
	}
}

func writeLogEvent(w *sse.Writer, entry observability.LogEntry) bool {
	payload, err := json.Marshal(entry)
	if err != nil {
		return false
	}
	return w.WriteEvent(strconv.FormatUint(entry.Sequence, 10), "log", payload) == nil
}

func (s *Server) handleMetrics(ctx context.Context, c *app.RequestContext) {
	response, point, err := s.currentMetrics(ctx)
	if err != nil {
		writeErr(c, http.StatusInternalServerError, err)
		return
	}
	if s.rt.History != nil {
		historyTraces, historyErr := s.rt.History.RecentTraces(ctx, 50)
		if historyErr != nil {
			writeErr(c, http.StatusInternalServerError, fmt.Errorf("read trace history: %w", historyErr))
			return
		}
		response.Messages.Recent = mergeTraceSummaries(response.Messages.Recent, historyTraces, 50)
		history, historyErr := s.rt.History.RecentMetrics(ctx, observability.DefaultMetricResponseLimit)
		if historyErr != nil {
			writeErr(c, http.StatusInternalServerError, fmt.Errorf("read metric history: %w", historyErr))
			return
		}
		response.History = appendMetricPoint(filterMetricHistoryScope(history, httpMetricScope), point, observability.DefaultMetricResponseLimit)
		response.HistoryPersistence = s.rt.History.Stats()
	} else {
		response.History = []observability.MetricHistoryPoint{point}
	}
	c.JSON(http.StatusOK, response)
}

func (s *Server) currentMetrics(ctx context.Context) (metricsResponse, observability.MetricHistoryPoint, error) {
	if s.metricCollector != nil {
		return s.metricCollector(ctx)
	}
	return s.collectCurrentMetrics(ctx)
}

func (s *Server) collectCurrentMetrics(ctx context.Context) (metricsResponse, observability.MetricHistoryPoint, error) {
	usage, err := s.rt.ObservabilityStore.AggregateTokenUsageContext(ctx, ledger.DefaultUsageTurnLimit)
	if err != nil {
		return metricsResponse{}, observability.MetricHistoryPoint{}, fmt.Errorf("read usage metrics: %w", err)
	}
	activeJobs := s.rt.Turns.ActiveBackgroundJobs()
	if activeJobs < 0 {
		return metricsResponse{}, observability.MetricHistoryPoint{}, errors.New("active background job count is negative")
	}
	database, err := s.infrastructureMetrics(ctx)
	if err != nil {
		return metricsResponse{}, observability.MetricHistoryPoint{}, fmt.Errorf("read infrastructure metrics: %w", err)
	}
	response := metricsResponse{
		Process:  observability.SnapshotProcess(s.rt.StartedAt),
		HTTP:     s.rt.HTTPMetrics.Snapshot(),
		Logs:     s.rt.Logs.Stats(),
		Messages: s.rt.Messages.Snapshot(),
		Runtime: runtimeMetrics{
			ActiveBackgroundJobs: uint64(activeJobs),
			AgentLoop:            s.rt.Turns.AgentLoopMetrics(),
		},
		Usage:    usage,
		Database: database,
	}
	if s.rt.Initiative != nil {
		response.Runtime.Experience = s.rt.Initiative.ExperienceStats()
	}
	if s.rt.TurnEventSubscriberCount != nil {
		response.Runtime.EventSubscribers = s.rt.TurnEventSubscriberCount()
	}
	response.GeneratedAtUnixMS = time.Now().UnixMilli()
	return response, metricHistoryPoint(response, s.rt.StartedAt, activeJobs), nil
}

func mergeTraceSummaries(current []observability.MessageTrace, history []observability.MessageTraceDetail, limit int) []observability.MessageTrace {
	byID := make(map[string]observability.MessageTrace, len(current)+len(history))
	for _, trace := range history {
		byID[trace.TraceID] = traceSummary(trace)
	}
	for _, trace := range current {
		byID[trace.TraceID] = trace
	}
	merged := make([]observability.MessageTrace, 0, len(byID))
	for _, trace := range byID {
		merged = append(merged, trace)
	}
	sort.Slice(merged, func(i, j int) bool {
		if merged[i].ReceivedAtUnixMS != merged[j].ReceivedAtUnixMS {
			return merged[i].ReceivedAtUnixMS > merged[j].ReceivedAtUnixMS
		}
		return merged[i].TraceID > merged[j].TraceID
	})
	if len(merged) > limit {
		merged = merged[:limit]
	}
	return merged
}

func traceSummary(trace observability.MessageTraceDetail) observability.MessageTrace {
	return observability.MessageTrace{
		TraceID: trace.TraceID, MessageID: trace.MessageID, Source: trace.Source,
		ConversationID: trace.ConversationID, TurnID: trace.TurnID, Status: trace.Status,
		ReceivedAtUnixMS: trace.StartedAtUnixMS, CompletedAtUnixMS: trace.EndedAtUnixMS,
		TotalDurationMS: trace.DurationMS,
	}
}

func mergeTraceDetails(current, history []observability.MessageTraceDetail, limit int) []observability.MessageTraceDetail {
	byID := make(map[string]observability.MessageTraceDetail, len(current)+len(history))
	for _, detail := range history {
		byID[detail.TraceID] = detail
	}
	for _, detail := range current {
		byID[detail.TraceID] = detail
	}
	merged := make([]observability.MessageTraceDetail, 0, len(byID))
	for _, detail := range byID {
		merged = append(merged, detail)
	}
	sort.Slice(merged, func(i, j int) bool {
		if merged[i].StartedAtUnixMS != merged[j].StartedAtUnixMS {
			return merged[i].StartedAtUnixMS > merged[j].StartedAtUnixMS
		}
		return merged[i].TraceID > merged[j].TraceID
	})
	if len(merged) > limit {
		merged = merged[:limit]
	}
	return merged
}

func metricHistoryPoint(response metricsResponse, startedAt time.Time, activeJobs int64) observability.MetricHistoryPoint {
	point := observability.MetricHistoryPoint{
		TimestampUnixMS: response.GeneratedAtUnixMS, ProcessStartedUnixMS: startedAt.UnixMilli(),
		HTTPScope: httpMetricScope,
		HTTPTotal: response.HTTP.Total, HTTPInFlight: response.HTTP.InFlight,
		HTTPStatus4xx: response.HTTP.Status4xx, HTTPStatus5xx: response.HTTP.Status5xx,
		MessagesReceived: response.Messages.Received, MessagesSent: response.Messages.Sent,
		MessagesActive: response.Messages.Active, MessagesFailed: response.Messages.Failed,
		Goroutines: response.Process.Goroutines, BackgroundJobs: uint64(activeJobs),
		EventSubscribers: response.Runtime.EventSubscribers, LogSubscribers: response.Logs.ActiveSubscribers,
		HeapMiB: float64(response.Process.HeapAllocBytes) / 1024 / 1024,
	}
	for _, lane := range response.Usage.Overall {
		point.InputTokens += lane.InputTokens
		point.CachedInputTokens += lane.CachedInputTokens
		point.OutputTokens += lane.OutputTokens
		point.ModelCalls += lane.CallCount
	}
	return point
}

func filterMetricHistoryScope(history []observability.MetricHistoryPoint, scope string) []observability.MetricHistoryPoint {
	filtered := make([]observability.MetricHistoryPoint, 0, len(history))
	for _, point := range history {
		if point.HTTPScope == scope {
			filtered = append(filtered, point)
		}
	}
	return filtered
}

func appendMetricPoint(history []observability.MetricHistoryPoint, point observability.MetricHistoryPoint, limit int) []observability.MetricHistoryPoint {
	for index := range history {
		if history[index].TimestampUnixMS == point.TimestampUnixMS && history[index].ProcessStartedUnixMS == point.ProcessStartedUnixMS {
			history[index] = point
			return history
		}
	}
	history = append(history, point)
	sort.Slice(history, func(i, j int) bool { return history[i].TimestampUnixMS < history[j].TimestampUnixMS })
	if len(history) > limit {
		history = history[len(history)-limit:]
	}
	return history
}

func parseLogFilter(c *app.RequestContext, includeLimit bool) (observability.LogFilter, error) {
	filter := observability.LogFilter{MinimumLevel: "debug"}
	level := string(c.Query("level"))
	if level != "" {
		switch level {
		case "debug", "info", "warn", "error":
			filter.MinimumLevel = level
		default:
			return observability.LogFilter{}, fmt.Errorf("level must be one of debug, info, warn, error")
		}
	}
	filter.LoggerPrefix = string(c.Query("logger"))
	after := string(c.Query("afterSequence"))
	if after != "" {
		value, err := strconv.ParseUint(after, 10, 64)
		if err != nil {
			return observability.LogFilter{}, errors.New("afterSequence must be an unsigned integer")
		}
		filter.AfterSequence = value
	}
	limitRaw := string(c.Query("limit"))
	if !includeLimit && limitRaw != "" {
		return observability.LogFilter{}, errors.New("limit is not supported for log streams")
	}
	if includeLimit {
		filter.Limit = defaultLogLimit
		if limitRaw != "" {
			limit, err := strconv.Atoi(limitRaw)
			if err != nil || limit < 1 || limit > maxLogLimit {
				return observability.LogFilter{}, fmt.Errorf("limit must be between 1 and %d", maxLogLimit)
			}
			filter.Limit = limit
		}
	}
	if strings.ContainsAny(filter.LoggerPrefix, "\r\n") {
		return observability.LogFilter{}, errors.New("logger must not contain line breaks")
	}
	return filter, nil
}
