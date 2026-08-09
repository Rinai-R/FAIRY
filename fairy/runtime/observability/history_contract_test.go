package observability

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMetricHistoryPointExperienceCountersStayLowSensitivity(t *testing.T) {
	point := MetricHistoryPoint{
		LearningEnqueued: 4, LearningSucceeded: 3, LearningFailed: 1, LearningDropped: 2,
		LearningModelCalls: 4, LearningInputTokens: 900, LearningCachedObservedInputTokens: 800,
		LearningCachedInputTokens: 600, LearningCacheWriteTokens: 70, LearningOutputTokens: 60,
		FeedbackRegistered: 8, FeedbackSuperseded: 2, FeedbackSucceeded: 6, FeedbackFailed: 1, FeedbackDropped: 1,
		FeedbackModelCalls: 5, FeedbackInputTokens: 1200, FeedbackCachedObservedInputTokens: 1000,
		FeedbackCachedInputTokens: 700, FeedbackCacheWriteTokens: 100, FeedbackOutputTokens: 80,
		CompactionL1Applied: 2, CompactionL2Applied: 3, CompactionL3Applied: 1, CompactionFailed: 4,
	}
	payload, err := json.Marshal(point)
	if err != nil {
		t.Fatal(err)
	}
	encoded := strings.ToLower(string(payload))
	for _, expected := range []string{
		`"learningEnqueued":4`, `"learningSucceeded":3`, `"learningFailed":1`, `"learningDropped":2`,
		`"learningModelCalls":4`, `"learningInputTokens":900`, `"learningCachedObservedInputTokens":800`,
		`"learningCachedInputTokens":600`, `"learningCacheWriteTokens":70`, `"learningOutputTokens":60`,
		`"feedbackRegistered":8`, `"feedbackSuperseded":2`, `"feedbackSucceeded":6`, `"feedbackFailed":1`, `"feedbackDropped":1`,
		`"feedbackModelCalls":5`, `"feedbackInputTokens":1200`, `"feedbackCachedObservedInputTokens":1000`,
		`"feedbackCachedInputTokens":700`, `"feedbackCacheWriteTokens":100`, `"feedbackOutputTokens":80`,
		`"compactionL1Applied":2`, `"compactionL2Applied":3`, `"compactionL3Applied":1`, `"compactionFailed":4`,
	} {
		if !strings.Contains(string(payload), expected) {
			t.Fatalf("metric history omitted %s: %s", expected, payload)
		}
	}
	for _, forbidden := range []string{"content", "candidate", "sender", "evidence", "messageid", "cachekey", "hash"} {
		if strings.Contains(encoded, forbidden) {
			t.Fatalf("metric history exposed forbidden key %q: %s", forbidden, payload)
		}
	}
}

func TestMetricHistoryPointDecodesLegacyPayloadWithZeroExperienceCounters(t *testing.T) {
	var point MetricHistoryPoint
	if err := json.Unmarshal([]byte(`{"timestampUnixMs":1,"processStartedAtUnixMs":1,"httpScope":"conversation"}`), &point); err != nil {
		t.Fatal(err)
	}
	if point.LearningEnqueued != 0 || point.LearningModelCalls != 0 || point.LearningInputTokens != 0 ||
		point.LearningCachedObservedInputTokens != 0 || point.LearningCachedInputTokens != 0 || point.LearningCacheWriteTokens != 0 || point.LearningOutputTokens != 0 ||
		point.FeedbackRegistered != 0 || point.FeedbackSuperseded != 0 || point.FeedbackDropped != 0 ||
		point.FeedbackModelCalls != 0 || point.FeedbackInputTokens != 0 || point.FeedbackCachedObservedInputTokens != 0 ||
		point.FeedbackCachedInputTokens != 0 || point.FeedbackCacheWriteTokens != 0 || point.FeedbackOutputTokens != 0 ||
		point.CompactionL1Applied != 0 || point.CompactionL3Applied != 0 || point.CompactionFailed != 0 {
		t.Fatalf("legacy experience counters = %#v, want zero values", point)
	}
}
