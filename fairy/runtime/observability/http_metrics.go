package observability

import (
	"net/http"
	"sort"
	"sync"
	"time"
)

const httpMetricMethodOther = "OTHER"

type HTTPRouteMetrics struct {
	Method          string `json:"method"`
	Route           string `json:"route"`
	LongLived       bool   `json:"longLived"`
	RequestCount    uint64 `json:"requestCount"`
	ErrorCount      uint64 `json:"errorCount"`
	TotalDurationMS uint64 `json:"totalDurationMs"`
	MaxDurationMS   uint64 `json:"maxDurationMs"`
}

type HTTPMetricsSnapshot struct {
	InFlight  uint64             `json:"inFlight"`
	Total     uint64             `json:"total"`
	Status2xx uint64             `json:"status2xx"`
	Status4xx uint64             `json:"status4xx"`
	Status5xx uint64             `json:"status5xx"`
	Routes    []HTTPRouteMetrics `json:"routes"`
}

type routeKey struct {
	method string
	route  string
}

type HTTPMetrics struct {
	mu        sync.Mutex
	inFlight  uint64
	total     uint64
	status2xx uint64
	status4xx uint64
	status5xx uint64
	routes    map[routeKey]*HTTPRouteMetrics
}

type HTTPObservation struct {
	key       routeKey
	started   time.Time
	longLived bool
}

func NewHTTPMetrics() *HTTPMetrics {
	return &HTTPMetrics{routes: make(map[routeKey]*HTTPRouteMetrics)}
}

func (m *HTTPMetrics) Begin(method, route string, longLived bool) HTTPObservation {
	method = normalizeHTTPMetricMethod(method)
	if route == "" {
		route = "unmatched"
	}
	observation := HTTPObservation{
		key: routeKey{method: method, route: route}, started: time.Now(), longLived: longLived,
	}
	if m == nil {
		return observation
	}
	m.mu.Lock()
	m.inFlight++
	m.total++
	aggregate := m.routes[observation.key]
	if aggregate == nil {
		aggregate = &HTTPRouteMetrics{
			Method: observation.key.method, Route: observation.key.route, LongLived: observation.longLived,
		}
		m.routes[observation.key] = aggregate
	}
	aggregate.RequestCount++
	m.mu.Unlock()
	return observation
}

func (m *HTTPMetrics) Finish(observation HTTPObservation, status int) {
	if m == nil {
		return
	}
	duration := time.Since(observation.started).Milliseconds()
	if duration < 0 {
		duration = 0
	}
	durationMS := uint64(duration)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.inFlight > 0 {
		m.inFlight--
	}
	switch {
	case status >= 200 && status < 300:
		m.status2xx++
	case status >= 400 && status < 500:
		m.status4xx++
	case status >= 500:
		m.status5xx++
	}
	aggregate := m.routes[observation.key]
	if aggregate == nil {
		return
	}
	if status >= 400 {
		aggregate.ErrorCount++
	}
	if !observation.longLived {
		aggregate.TotalDurationMS += durationMS
		if durationMS > aggregate.MaxDurationMS {
			aggregate.MaxDurationMS = durationMS
		}
	}
}

func normalizeHTTPMetricMethod(method string) string {
	switch method {
	case http.MethodConnect,
		http.MethodDelete,
		http.MethodGet,
		http.MethodHead,
		http.MethodOptions,
		http.MethodPatch,
		http.MethodPost,
		http.MethodPut,
		http.MethodTrace:
		return method
	default:
		return httpMetricMethodOther
	}
}

func (m *HTTPMetrics) Snapshot() HTTPMetricsSnapshot {
	if m == nil {
		return HTTPMetricsSnapshot{Routes: []HTTPRouteMetrics{}}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	snapshot := HTTPMetricsSnapshot{
		InFlight: m.inFlight, Total: m.total, Status2xx: m.status2xx,
		Status4xx: m.status4xx, Status5xx: m.status5xx,
		Routes: make([]HTTPRouteMetrics, 0, len(m.routes)),
	}
	for _, aggregate := range m.routes {
		snapshot.Routes = append(snapshot.Routes, *aggregate)
	}
	sort.Slice(snapshot.Routes, func(i, j int) bool {
		if snapshot.Routes[i].Method != snapshot.Routes[j].Method {
			return snapshot.Routes[i].Method < snapshot.Routes[j].Method
		}
		return snapshot.Routes[i].Route < snapshot.Routes[j].Route
	})
	return snapshot
}
