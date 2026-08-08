package observability

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMetricHistoryPointExperienceCountersStayLowSensitivity(t *testing.T) {
	point := MetricHistoryPoint{
		LearningEnqueued: 4, LearningSucceeded: 3, LearningFailed: 1, LearningDropped: 2,
		FeedbackRegistered: 8, FeedbackSucceeded: 6, FeedbackFailed: 1, FeedbackDropped: 1,
		CompactionL1Applied: 2, CompactionL2Applied: 3, CompactionL3Applied: 1, CompactionFailed: 4,
	}
	payload, err := json.Marshal(point)
	if err != nil {
		t.Fatal(err)
	}
	encoded := strings.ToLower(string(payload))
	for _, expected := range []string{
		`"learningEnqueued":4`, `"learningSucceeded":3`, `"learningFailed":1`, `"learningDropped":2`,
		`"feedbackRegistered":8`, `"feedbackSucceeded":6`, `"feedbackFailed":1`, `"feedbackDropped":1`,
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
	if point.LearningEnqueued != 0 || point.FeedbackRegistered != 0 || point.FeedbackDropped != 0 ||
		point.CompactionL1Applied != 0 || point.CompactionL3Applied != 0 || point.CompactionFailed != 0 {
		t.Fatalf("legacy experience counters = %#v, want zero values", point)
	}
}
