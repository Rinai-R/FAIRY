package companion

import (
	"encoding/json"
	"strings"
	"testing"

	"fairy/memory"
	"fairy/model"
	"fairy/reply"
)

func TestRuntimePromptLedgerRecordsOnlyCacheIdentityDiagnostics(t *testing.T) {
	cacheInput := model.NewCacheKeyInput(model.PromptLaneRespond, "model-1", "conversation-secret", "stable instructions")
	metadata := runtimePromptLedgerMetadata(nil, nil, memory.PromptWindowRecord{}, nil, nil, memory.RetrievalContext{}, cacheInput, true)
	raw, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	wire := string(raw)
	for _, required := range []string{`"version":"v2"`, `"supported":true`, `"identityHash":`, `"stablePromptHashPresent":true`, `"dynamicInputsExcluded":true`} {
		if !strings.Contains(wire, required) {
			t.Fatalf("metadata missing %s: %s", required, wire)
		}
	}
	for _, forbidden := range []string{"conversation-secret", "stable instructions", cacheInput.StablePromptHash} {
		if strings.Contains(wire, forbidden) {
			t.Fatalf("metadata leaked %q: %s", forbidden, wire)
		}
	}
}

func TestRuntimeBeatDeliveryLedgerMetadataContainsOnlyDiagnostics(t *testing.T) {
	metadata := runtimeBeatDeliveryLedgerMetadata("published", reply.BeatKindFinal, 1, 2, 920, 370, 2)
	raw, err := json.Marshal(metadata)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	wire := string(raw)
	for _, required := range []string{
		`"status":"published"`,
		`"kind":"final"`,
		`"chainIndex":1`,
		`"playIndex":2`,
		`"targetIntervalMs":920`,
		`"paceWaitMs":370`,
		`"publishedPrefixCount":2`,
	} {
		if !strings.Contains(wire, required) {
			t.Fatalf("metadata missing %s: %s", required, wire)
		}
	}
	for _, forbidden := range []string{"displayText", "speechText", "prompt", "Authorization", "Bearer"} {
		if strings.Contains(wire, forbidden) {
			t.Fatalf("metadata contains forbidden field %q: %s", forbidden, wire)
		}
	}
}

func TestKnowledgeIngestLedgerMetadataDoesNotContainBatchContent(t *testing.T) {
	metadata := runtimeKnowledgeIngestLedgerMetadata(
		[]model.StreamEvent{{Type: "text_delta", Data: "secret statement"}},
		[]LaneModelUsage{{Lane: string(model.PromptLaneKnowledgeIngest)}},
		"secret-batch-id",
		3,
	)
	raw, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	wire := string(raw)
	for _, required := range []string{`"status":"knowledge_ingest"`, `"batchIdHash":`, `"sourceCount":3`} {
		if !strings.Contains(wire, required) {
			t.Fatalf("metadata missing %s: %s", required, wire)
		}
	}
	for _, forbidden := range []string{"secret statement", "secret-batch-id", "https://source.example", "snippet", "query"} {
		if strings.Contains(wire, forbidden) {
			t.Fatalf("metadata leaked %q: %s", forbidden, wire)
		}
	}
}
