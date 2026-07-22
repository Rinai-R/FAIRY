package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"fairy/companion"
	"fairy/memory"
	"fairy/observability"
	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/sse"
)

const (
	defaultLogLimit = 200
	maxLogLimit     = 500
)

type runtimeMetrics struct {
	ActiveBackgroundJobs uint64                             `json:"activeBackgroundJobs"`
	EventSubscribers     uint64                             `json:"eventSubscribers"`
	AgentLoop            companion.AgentLoopMetricsSnapshot `json:"agentLoop"`
}

type metricsResponse struct {
	GeneratedAtUnixMS int64                             `json:"generatedAtUnixMs"`
	Process           observability.ProcessMetrics      `json:"process"`
	HTTP              observability.HTTPMetricsSnapshot `json:"http"`
	Logs              observability.LogStats            `json:"logs"`
	Runtime           runtimeMetrics                    `json:"runtime"`
	Usage             memory.UsageReport                `json:"usage"`
	Database          databaseMetrics                   `json:"database"`
	Qdrant            qdrantMetrics                     `json:"qdrant"`
}

func (s *Server) registerObservabilityRoutes() {
	v1 := s.engine.Group("/v1")
	v1.Use(s.authMiddleware)
	v1.GET("/logs", s.handleLogs)
	v1.GET("/logs/stream", s.handleLogStream)
	v1.GET("/metrics", s.handleMetrics)
}

func (s *Server) metricsMiddleware(ctx context.Context, c *app.RequestContext) {
	started := s.rt.HTTPMetrics.Begin()
	c.Next(ctx)
	s.rt.HTTPMetrics.Finish(string(c.Method()), c.FullPath(), c.Response.StatusCode(), started)
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
	backlog, live, unsubscribe := s.rt.Logs.Subscribe(filter)
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
	usage, err := s.rt.Memory.TokenUsageReport()
	if err != nil {
		writeErr(c, http.StatusInternalServerError, fmt.Errorf("read usage metrics: %w", err))
		return
	}
	activeJobs := s.rt.Companion.ActiveBackgroundJobs()
	if activeJobs < 0 {
		writeErr(c, http.StatusInternalServerError, errors.New("active background job count is negative"))
		return
	}
	database, qdrant, err := s.infrastructureMetrics(ctx)
	if err != nil {
		writeErr(c, http.StatusInternalServerError, fmt.Errorf("read infrastructure metrics: %w", err))
		return
	}
	response := metricsResponse{
		Process: observability.SnapshotProcess(s.rt.StartedAt),
		HTTP:    s.rt.HTTPMetrics.Snapshot(),
		Logs:    s.rt.Logs.Stats(),
		Runtime: runtimeMetrics{
			ActiveBackgroundJobs: uint64(activeJobs),
			EventSubscribers:     s.rt.Events.SubscriberCount(),
			AgentLoop:            s.rt.Companion.AgentLoopMetrics(),
		},
		Usage:    usage,
		Database: database,
		Qdrant:   qdrant,
	}
	response.GeneratedAtUnixMS = time.Now().UnixMilli()
	c.JSON(http.StatusOK, response)
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
