package memory

import (
	"errors"
	"strings"
	"testing"
)

func TestBuildFTSQueryUsesTrigramsAndRejectsShortRuns(t *testing.T) {
	query, err := buildFTSQuery("太甜的饮料推荐")
	if err != nil {
		t.Fatalf("buildFTSQuery() error = %v", err)
	}
	for _, part := range []string{`"太甜的"`, `"甜的饮"`, `"的饮料"`, `"饮料推"`, `"料推荐"`} {
		if !strings.Contains(query, part) {
			t.Fatalf("query = %q, missing %s", query, part)
		}
	}
	empty, err := buildFTSQuery("饮料")
	if err != nil {
		t.Fatalf("short buildFTSQuery() error = %v", err)
	}
	if empty != "" {
		t.Fatalf("short query = %q, want empty", empty)
	}
}

func TestSemanticContentHashIsDeterministicAndContentSensitive(t *testing.T) {
	first := semanticContentHash("topic\nstatement")
	if len(first) != 64 || first != semanticContentHash("topic\nstatement") {
		t.Fatalf("semanticContentHash() = %q, want stable SHA-256 hex", first)
	}
	if first == semanticContentHash("topic\nchanged") {
		t.Fatal("semanticContentHash() did not change with content")
	}
}

func TestRuntimeStateValidationRejectsInvalidLaneHashAndMetadata(t *testing.T) {
	validHash := strings.Repeat("a", 64)
	if err := validatePromptLane(PromptLaneRespond); err != nil {
		t.Fatalf("validatePromptLane(respond) error = %v", err)
	}
	if err := validatePromptLane("unknown"); err == nil {
		t.Fatal("validatePromptLane(unknown) error = nil")
	}
	if err := validateHash("request_shape_hash", validHash); err != nil {
		t.Fatalf("validateHash(valid) error = %v", err)
	}
	if err := validateHash("request_shape_hash", strings.Repeat("A", 64)); err == nil {
		t.Fatal("validateHash(uppercase) error = nil")
	}
	if _, err := normalizeRuntimeMetadataJSON(`{"usage":[],"api_key":"secret"}`); err == nil {
		t.Fatal("normalizeRuntimeMetadataJSON(secret key) error = nil")
	}
	if _, err := normalizeRuntimeMetadataJSON(`{"usage":[],"message":"Bearer redacted"}`); err == nil {
		t.Fatal("normalizeRuntimeMetadataJSON(secret text) error = nil")
	}
}

func TestAggregateUsageRowsPreservesTotalsCacheAndTruncation(t *testing.T) {
	completed := "completed"
	failed := "failed"
	rows := []usageLedgerRow{
		{conversationID: "conversation-1", turnID: "turn-1", eventType: "model", metadataJSON: `{"usage":[{"lane":"respond","usage":{"inputTokens":100,"outputTokens":20,"cachedInputTokens":{"status":"observed","tokens":40},"cacheWriteTokens":{"status":"missing","tokens":null}}}]}`, createdAtUnixMS: 10},
		{conversationID: "conversation-1", turnID: "turn-1", eventType: "terminal", state: &completed, metadataJSON: `{}`, createdAtUnixMS: 11},
		{conversationID: "conversation-2", turnID: "turn-2", eventType: "model", metadataJSON: `{"usage":[{"lane":"respond","usage":{"inputTokens":200,"outputTokens":30,"cachedInputTokens":{"status":"missing","tokens":null},"cacheWriteTokens":{"status":"missing","tokens":null}}}]}`, createdAtUnixMS: 20},
		{conversationID: "conversation-2", turnID: "turn-2", eventType: "terminal", state: &failed, metadataJSON: `{}`, createdAtUnixMS: 21},
	}
	report, err := aggregateUsageRows(map[string]string{"conversation-1": "character-1", "conversation-2": "character-2"}, rows, 1)
	if err != nil {
		t.Fatalf("aggregateUsageRows() error = %v", err)
	}
	if report.TurnCount != 2 || len(report.Turns) != 1 || !report.Truncated {
		t.Fatalf("report detail = %#v, want 2 total and 1 returned", report)
	}
	overall := findUsageLane(report.Overall, "respond")
	if overall.InputTokens != 300 || overall.OutputTokens != 50 || overall.CachedInputTokens != 40 || overall.CachedObservedInputTokens != 100 || overall.CallCount != 2 {
		t.Fatalf("overall usage = %#v", overall)
	}
	if report.Turns[0].Status != failed || report.Turns[0].CharacterID != "character-2" {
		t.Fatalf("latest turn = %#v", report.Turns[0])
	}
}

func TestAggregateUsageRowsRejectsInvalidModelMetadata(t *testing.T) {
	_, err := aggregateUsageRows(nil, []usageLedgerRow{{conversationID: "conversation-1", turnID: "turn-1", eventType: "model", metadataJSON: `{`}}, 10)
	if err == nil || !strings.Contains(err.Error(), "decoding model usage metadata") {
		t.Fatalf("aggregateUsageRows() error = %v", err)
	}
}

func findUsageLane(lanes []UsageLaneAggregate, lane string) UsageLaneAggregate {
	for _, aggregate := range lanes {
		if aggregate.Lane == lane {
			return aggregate
		}
	}
	panic(errors.New("usage lane not found: " + lane))
}
