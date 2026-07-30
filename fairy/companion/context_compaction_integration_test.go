package companion

import (
	"strings"
	"testing"
	"time"

	"fairy/model"
	"fairy/persona"
)

func TestCurrentTurnL1ProjectionKeepsProviderToolProtocolValid(t *testing.T) {
	oldResultParts := model.PromptContentParts{{
		Type: model.PromptContentText, Text: strings.Repeat("old result", 100),
	}}
	first, err := toolContextSegments([]model.PromptItem{
		{Type: model.PromptItemToolCall, ToolCallID: "call-old", ToolName: "search", ToolArguments: `{}`},
		{Type: model.PromptItemToolResult, ToolCallID: "call-old", Parts: &oldResultParts},
	}, time.UnixMilli(1))
	if err != nil {
		t.Fatal(err)
	}
	newResultParts := model.PromptContentParts{{Type: model.PromptContentText, Text: "new result"}}
	second, err := toolContextSegments([]model.PromptItem{
		{Type: model.PromptItemToolCall, ToolCallID: "call-new", ToolName: "search", ToolArguments: `{}`},
		{Type: model.PromptItemToolResult, ToolCallID: "call-new", Parts: &newResultParts},
	}, time.UnixMilli(1))
	if err != nil {
		t.Fatal(err)
	}
	segments := append(first, second...)
	for index := range segments {
		segments[index].Ordinal = uint64(index + 1)
		if segments[index].Kind == model.ContextSegmentToolResult {
			segments[index].Recoverability = model.ContextRecoverabilityRefetchable
		}
	}
	plan := planL1ToolResultCompaction(l1PlanningInput{
		Segments: segments, NowUnixMS: time.Now().UnixMilli(),
		CurrentTokens: 1000, TargetTokens: 990, CacheExpired: true,
		RecentProtectedFromOrdinal: 3,
	})
	projected := applyL1CompactionPlan(segments, plan)
	items, err := (persona.ContextProjector{}).Project(projected)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 || items[0].Type != model.PromptItemContextData ||
		!strings.Contains(items[0].Content, `"status":"result_omitted_after_ttl"`) ||
		strings.Contains(items[0].Content, "old result") ||
		items[1].ToolCallID != "call-new" ||
		items[1].Type != model.PromptItemToolCall ||
		items[2].Type != model.PromptItemToolResult {
		t.Fatalf("projected tool items = %#v, plan = %#v", items, plan)
	}
	for _, protocol := range []model.Protocol{model.ProtocolResponses, model.ProtocolChatCompletions} {
		connection := model.Connection{
			Protocol: protocol, Endpoint: "https://example.com/v1", Model: "test-model",
			ContextWindowTokens: 100_000, AuthMode: "no_auth",
		}
		_, err := model.BuildRequestDraft(connection, model.CompiledPromptRequest{
			Shape: model.ModelRequestShape{
				Lane: model.PromptLaneRespond, Model: connection.Model,
				Instructions: "test", MaxOutputTokens: 64,
			},
			Input: items,
		})
		if err != nil {
			t.Fatalf("BuildRequestDraft(%s) error = %v", protocol, err)
		}
	}
}

func TestLaneUsageKeepsMissingCacheObservationForL1(t *testing.T) {
	input := uint64(100)
	usage := []model.LaneModelUsage{{
		Lane: string(model.PromptLaneRespond),
		Usage: model.LaneUsage{
			InputTokens:       &input,
			CachedInputTokens: model.CacheMissing(),
			CacheWriteTokens:  model.CacheUnsupported(),
		},
	}}
	state := agentLoopState{}
	state.lastInputTokens = *usage[0].Usage.InputTokens
	state.lastCacheObservation = usage[0].Usage.CachedInputTokens
	state.lastCacheWriteObservation = usage[0].Usage.CacheWriteTokens
	if state.lastCacheObservation.Status != "missing" ||
		state.lastCacheWriteObservation.Status != "unsupported" {
		t.Fatalf("cache observations = %#v / %#v", state.lastCacheObservation, state.lastCacheWriteObservation)
	}
}
