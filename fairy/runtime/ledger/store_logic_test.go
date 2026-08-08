package ledger

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
)

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

func TestUsageReportCollectorBoundsRecentAcrossLargeHistory(t *testing.T) {
	const (
		turns = 10_000
		limit = 100
	)
	collector := newUsageReportCollector(limit)
	for index := 0; index < turns; index++ {
		collector.Add(&usageTurnAccumulator{
			conversationID:  fmt.Sprintf("conversation-%05d", index),
			turnID:          fmt.Sprintf("turn-%05d", index),
			createdAtUnixMS: int64(index),
			status:          "completed",
			lanes: map[string]*UsageLaneAggregate{
				"respond": {Lane: "respond", InputTokens: 2, OutputTokens: 1, CallCount: 1},
			},
		}, "character")
		if len(collector.recent) > limit {
			t.Fatalf("retained recent = %d at turn %d", len(collector.recent), index)
		}
	}
	report := collector.Report()
	if report.TurnCount != turns || len(report.Turns) != limit || !report.Truncated {
		t.Fatalf("report bounds = %#v", report)
	}
	overall := findUsageLane(report.Overall, "respond")
	if overall.InputTokens != turns*2 || overall.OutputTokens != turns || overall.CallCount != turns {
		t.Fatalf("overall = %#v", overall)
	}
	if report.Turns[0].TurnID != "turn-09999" || report.Turns[len(report.Turns)-1].TurnID != "turn-09900" {
		t.Fatalf("recent order = first %q last %q", report.Turns[0].TurnID, report.Turns[len(report.Turns)-1].TurnID)
	}
}

func TestProductionUsageReportDoesNotLoadFullHistoryCollections(t *testing.T) {
	source, err := os.ReadFile("store_usage.go")
	if err != nil {
		t.Fatalf("ReadFile(store_usage.go) error = %v", err)
	}
	for _, forbidden := range []string{"LoadConversationCharacters(", "LoadUsageLedgerEvents(", "usageLedgerRowsFromAdapter("} {
		if strings.Contains(string(source), forbidden) {
			t.Fatalf("production usage report still uses full-history loader %q", forbidden)
		}
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
