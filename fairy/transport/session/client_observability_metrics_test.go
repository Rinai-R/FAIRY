package session

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClientMetricsPreservesExperienceSnapshot(t *testing.T) {
	payload := validMetricsPayload(`{
		"learning":{"enqueued":2,"dropped":1,"succeeded":1,"failed":0,"modelCalls":1,"inputTokens":700,"cachedObservedInputTokens":700,"cachedInputTokens":400,"cacheWriteTokens":30,"outputTokens":50},
		"feedback":{"registered":3,"superseded":1,"dropped":0,"succeeded":1,"failed":1,"modelCalls":2,"inputTokens":500,"cachedObservedInputTokens":500,"cachedInputTokens":300,"cacheWriteTokens":20,"outputTokens":40},
		"cacheIdentityVersion":"v2"
	}`)
	client := metricsTestClient(t, payload)

	metrics, err := client.Metrics(context.Background())
	if err != nil {
		t.Fatalf("Metrics() error = %v", err)
	}
	if metrics.Runtime.Experience.Learning.InputTokens != 700 || metrics.Runtime.Experience.Feedback.Registered != 3 || metrics.Runtime.Experience.CacheIdentityVersion != "v2" {
		t.Fatalf("experience metrics = %#v", metrics.Runtime.Experience)
	}
}

func TestClientMetricsRejectsMissingOrBlankExperience(t *testing.T) {
	missing := strings.Replace(validMetricsPayload("null"), `"runtime":{"experience":null}`, `"runtime":{}`, 1)
	for _, test := range []struct {
		name    string
		payload string
	}{
		{name: "missing", payload: missing},
		{name: "blank cache identity version", payload: validMetricsPayload(`{"learning":{},"feedback":{},"cacheIdentityVersion":" "}`)},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := metricsTestClient(t, test.payload)
			_, err := client.Metrics(context.Background())
			if err == nil || !strings.Contains(err.Error(), "cache identity version is required") {
				t.Fatalf("Metrics() error = %v", err)
			}
		})
	}
}

func TestValidateExperienceMetricsRejectsEveryNegativeCounter(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*ExperienceStats)
	}{
		{name: "learning.enqueued", mutate: func(s *ExperienceStats) { s.Learning.Enqueued = -1 }},
		{name: "learning.dropped", mutate: func(s *ExperienceStats) { s.Learning.Dropped = -1 }},
		{name: "learning.succeeded", mutate: func(s *ExperienceStats) { s.Learning.Succeeded = -1 }},
		{name: "learning.failed", mutate: func(s *ExperienceStats) { s.Learning.Failed = -1 }},
		{name: "learning.modelCalls", mutate: func(s *ExperienceStats) { s.Learning.ModelCalls = -1 }},
		{name: "learning.inputTokens", mutate: func(s *ExperienceStats) { s.Learning.InputTokens = -1 }},
		{name: "learning.cachedObservedInputTokens", mutate: func(s *ExperienceStats) { s.Learning.CachedObservedInputTokens = -1 }},
		{name: "learning.cachedInputTokens", mutate: func(s *ExperienceStats) { s.Learning.CachedInputTokens = -1 }},
		{name: "learning.cacheWriteTokens", mutate: func(s *ExperienceStats) { s.Learning.CacheWriteTokens = -1 }},
		{name: "learning.outputTokens", mutate: func(s *ExperienceStats) { s.Learning.OutputTokens = -1 }},
		{name: "feedback.registered", mutate: func(s *ExperienceStats) { s.Feedback.Registered = -1 }},
		{name: "feedback.superseded", mutate: func(s *ExperienceStats) { s.Feedback.Superseded = -1 }},
		{name: "feedback.dropped", mutate: func(s *ExperienceStats) { s.Feedback.Dropped = -1 }},
		{name: "feedback.succeeded", mutate: func(s *ExperienceStats) { s.Feedback.Succeeded = -1 }},
		{name: "feedback.failed", mutate: func(s *ExperienceStats) { s.Feedback.Failed = -1 }},
		{name: "feedback.modelCalls", mutate: func(s *ExperienceStats) { s.Feedback.ModelCalls = -1 }},
		{name: "feedback.inputTokens", mutate: func(s *ExperienceStats) { s.Feedback.InputTokens = -1 }},
		{name: "feedback.cachedObservedInputTokens", mutate: func(s *ExperienceStats) { s.Feedback.CachedObservedInputTokens = -1 }},
		{name: "feedback.cachedInputTokens", mutate: func(s *ExperienceStats) { s.Feedback.CachedInputTokens = -1 }},
		{name: "feedback.cacheWriteTokens", mutate: func(s *ExperienceStats) { s.Feedback.CacheWriteTokens = -1 }},
		{name: "feedback.outputTokens", mutate: func(s *ExperienceStats) { s.Feedback.OutputTokens = -1 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stats := ExperienceStats{CacheIdentityVersion: "v2"}
			test.mutate(&stats)
			err := validateExperienceMetrics(stats)
			if err == nil || !strings.Contains(err.Error(), test.name) {
				t.Fatalf("validateExperienceMetrics() error = %v", err)
			}
		})
	}
}

func metricsTestClient(t *testing.T, payload string) *Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/metrics" {
			http.NotFound(w, request)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(payload))
	}))
	t.Cleanup(server.Close)
	client, err := New(Options{Endpoint: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func validMetricsPayload(experience string) string {
	return fmt.Sprintf(`{
		"generatedAtUnixMs":1,
		"process":{"goVersion":"go1.26.5"},
		"http":{"routes":[]},
		"messages":{"recent":[]},
		"runtime":{"experience":%s},
		"usage":{"overall":[],"turns":[]},
		"database":{}
	}`, experience)
}
