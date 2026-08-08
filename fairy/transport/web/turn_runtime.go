package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"

	historyruntime "fairy/context/history/runtime"

	"github.com/cloudwego/hertz/pkg/app"
)

type turnRuntimeEventProjection struct {
	Sequence        uint64         `json:"sequence"`
	EventType       string         `json:"eventType"`
	State           *string        `json:"state,omitempty"`
	Code            *string        `json:"code,omitempty"`
	Metadata        map[string]any `json:"metadata"`
	CreatedAtUnixMS int64          `json:"createdAtUnixMs"`
}

func (s *Server) handleTurnRuntime(ctx context.Context, c *app.RequestContext) {
	conversationID := strings.TrimSpace(c.Param("conversationId"))
	turnID := strings.TrimSpace(c.Param("turnId"))
	if conversationID == "" || turnID == "" {
		writeErr(c, http.StatusBadRequest, errors.New("conversationId and turnId are required"))
		return
	}
	records, err := s.rt.RuntimeStore.ListTurnRuntimeEventsContext(ctx, conversationID, turnID)
	if err != nil {
		if errors.Is(err, historyruntime.ErrTurnNotFound) {
			writeErr(c, http.StatusNotFound, err)
			return
		}
		writeErr(c, http.StatusBadRequest, err)
		return
	}
	events := make([]turnRuntimeEventProjection, 0, len(records))
	for _, record := range records {
		metadata := map[string]any{}
		var raw map[string]any
		if err := json.Unmarshal([]byte(record.MetadataJSON), &raw); err != nil {
			writeErr(c, http.StatusInternalServerError, errors.New("stored runtime metadata is invalid"))
			return
		}
		metadata = projectTurnRuntimeMetadata(record.EventType, raw)
		events = append(events, turnRuntimeEventProjection{
			Sequence: record.Sequence, EventType: record.EventType, State: record.State, Code: record.Code,
			Metadata: metadata, CreatedAtUnixMS: record.CreatedAtUnixMS,
		})
	}
	c.JSON(http.StatusOK, map[string]any{
		"conversationId": conversationID,
		"turnId":         turnID,
		"events":         events,
	})
}

func projectTurnRuntimeMetadata(eventType string, raw map[string]any) map[string]any {
	allowed := runtimeMetadataAllowlist[eventType]
	projected := make(map[string]any)
	for key := range allowed {
		value, ok := raw[key]
		if !ok {
			continue
		}
		if key == "usage" {
			if usage := projectRuntimeUsage(value); usage != nil {
				projected[key] = usage
			}
			continue
		}
		if key == "detail" {
			if detail := projectRuntimeToolDetail(value); detail != nil {
				projected[key] = detail
			}
			continue
		}
		if safe, ok := safeRuntimeScalar(value); ok {
			projected[key] = safe
		}
	}
	return projected
}

func projectRuntimeToolDetail(value any) map[string]any {
	raw, ok := value.(map[string]any)
	if !ok || raw["version"] != "v1" {
		return nil
	}
	projected := map[string]any{"version": "v1"}
	if arguments, ok := raw["arguments"].(map[string]any); ok {
		clean := map[string]any{}
		if query, ok := safeRuntimeText(arguments["query"], 200); ok {
			clean["query"] = query
		}
		projected["arguments"] = clean
	}
	if receipt, ok := raw["receipt"].(map[string]any); ok {
		projected["receipt"] = projectRuntimeToolReceipt(receipt)
	}
	if result := projectRuntimeRetrievalContext(raw["result"]); result != nil {
		projected["result"] = result
	}
	if merged := projectRuntimeRetrievalContext(raw["mergedContext"]); merged != nil {
		projected["mergedContext"] = merged
	}
	return projected
}

func projectRuntimeToolReceipt(raw map[string]any) map[string]any {
	projected := map[string]any{}
	for _, key := range []string{"status", "empty", "personalCount", "knowledgeCount", "socialCount", "semanticStatus"} {
		if value, ok := safeRuntimeScalar(raw[key]); ok {
			projected[key] = value
		}
	}
	return projected
}

func projectRuntimeRetrievalContext(value any) map[string]any {
	raw, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	projected := map[string]any{}
	if items := projectRuntimePersonalMemories(raw["personalMemories"]); items != nil {
		projected["personalMemories"] = items
	}
	if items := projectRuntimeKnowledge(raw["knowledge"]); items != nil {
		projected["knowledge"] = items
	}
	if social, ok := raw["socialMemories"].(map[string]any); ok {
		if entries := projectRuntimeSocialMemories(firstRuntimeValue(social, "entries", "Entries")); entries != nil {
			projected["socialMemories"] = map[string]any{"entries": entries}
		}
	}
	if status, ok := safeRuntimeText(raw["semanticStatus"], 64); ok {
		projected["semanticStatus"] = status
	}
	return projected
}

func projectRuntimePersonalMemories(value any) []map[string]any {
	return projectRuntimeRecords(value, 16, func(raw map[string]any) map[string]any {
		projected := projectRuntimeTextFields(raw, map[string]int{
			"id": 128, "kind": 64, "layer": 64, "content": 2400,
		})
		if scope, ok := raw["scope"].(map[string]any); ok {
			projected["scope"] = projectRuntimeTextFields(scope, map[string]int{"type": 64, "characterId": 128})
		}
		projectRuntimeNumberFields(projected, raw, "confidenceBasisPoints", "updatedAtUnixMs")
		return projected
	})
}

func projectRuntimeKnowledge(value any) []map[string]any {
	return projectRuntimeRecords(value, 16, func(raw map[string]any) map[string]any {
		projected := projectRuntimeTextFields(raw, map[string]int{
			"id": 128, "layer": 64, "topic": 300, "statement": 7200, "verificationBasis": 128,
		})
		projectRuntimeNumberFields(projected, raw, "confidenceBasisPoints", "updatedAtUnixMs")
		if sources := projectRuntimeSources(raw["sources"]); sources != nil {
			projected["sources"] = sources
		}
		return projected
	})
}

func projectRuntimeSources(value any) []map[string]any {
	return projectRuntimeRecords(value, 5, func(raw map[string]any) map[string]any {
		projected := projectRuntimeTextFields(raw, map[string]int{"title": 300, "url": 2048, "snippet": 1200})
		projectRuntimeNumberFields(projected, raw, "rank", "fetchedAtUnixMs")
		return projected
	})
}

func projectRuntimeSocialMemories(value any) []map[string]any {
	return projectRuntimeRecords(value, 12, func(raw map[string]any) map[string]any {
		projected := map[string]any{}
		fields := []struct {
			key, legacy string
			limit       int
		}{
			{"id", "ID", 128}, {"kind", "Kind", 64}, {"situation", "Situation", 240},
			{"content", "Content", 800}, {"recallCue", "RecallCue", 400},
		}
		for _, field := range fields {
			if text, ok := safeRuntimeText(firstRuntimeValue(raw, field.key, field.legacy), field.limit); ok {
				projected[field.key] = text
			}
		}
		if updated, ok := safeRuntimeScalar(firstRuntimeValue(raw, "updatedAtUnixMs", "UpdatedAtUnixMS")); ok {
			projected["updatedAtUnixMs"] = updated
		}
		return projected
	})
}

func projectRuntimeRecords(value any, limit int, project func(map[string]any) map[string]any) []map[string]any {
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	projected := make([]map[string]any, 0, min(len(items), limit))
	for _, item := range items {
		raw, ok := item.(map[string]any)
		if !ok {
			continue
		}
		projected = append(projected, project(raw))
		if len(projected) == limit {
			break
		}
	}
	return projected
}

func projectRuntimeTextFields(raw map[string]any, limits map[string]int) map[string]any {
	projected := map[string]any{}
	for key, limit := range limits {
		if value, ok := safeRuntimeText(raw[key], limit); ok {
			projected[key] = value
		}
	}
	return projected
}

func projectRuntimeNumberFields(projected, raw map[string]any, keys ...string) {
	for _, key := range keys {
		if value, ok := safeRuntimeScalar(raw[key]); ok {
			projected[key] = value
		}
	}
}

func firstRuntimeValue(raw map[string]any, keys ...string) any {
	for _, key := range keys {
		if value, ok := raw[key]; ok {
			return value
		}
	}
	return nil
}

func safeRuntimeText(value any, maxRunes int) (string, bool) {
	text, ok := value.(string)
	if !ok || utf8.RuneCountInString(text) > maxRunes {
		return "", false
	}
	for _, character := range text {
		if unicode.IsControl(character) && character != '\n' && character != '\r' && character != '\t' {
			return "", false
		}
	}
	lower := strings.ToLower(text)
	if strings.Contains(lower, "authorization:") || strings.Contains(lower, "bearer ") {
		return "", false
	}
	return text, true
}

func safeRuntimeScalar(value any) (any, bool) {
	switch typed := value.(type) {
	case string:
		if len(typed) > 256 {
			return nil, false
		}
		return typed, true
	case bool, float64:
		return typed, true
	default:
		return nil, false
	}
}

func projectRuntimeUsage(value any) []map[string]any {
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	projected := make([]map[string]any, 0, len(items))
	for _, item := range items {
		record, ok := item.(map[string]any)
		if !ok {
			continue
		}
		entry := map[string]any{}
		if lane, ok := safeRuntimeScalar(record["lane"]); ok {
			entry["lane"] = lane
		}
		if window, ok := safeRuntimeScalar(record["historyWindow"]); ok {
			entry["historyWindow"] = window
		}
		usage, ok := record["usage"].(map[string]any)
		if ok {
			entry["usage"] = projectRuntimeUsageFields(usage)
		}
		projected = append(projected, entry)
		if len(projected) == 8 {
			break
		}
	}
	return projected
}

func projectRuntimeUsageFields(raw map[string]any) map[string]any {
	result := map[string]any{}
	for _, key := range []string{"inputTokens", "outputTokens"} {
		if value, ok := safeRuntimeScalar(raw[key]); ok {
			result[key] = value
		}
	}
	for _, key := range []string{"cachedInputTokens", "cacheWriteTokens"} {
		observation, ok := raw[key].(map[string]any)
		if !ok {
			continue
		}
		clean := map[string]any{}
		if status, ok := safeRuntimeScalar(observation["status"]); ok {
			clean["status"] = status
		}
		if tokens, ok := safeRuntimeScalar(observation["tokens"]); ok {
			clean["tokens"] = tokens
		}
		result[key] = clean
	}
	return result
}

func stringSet(values ...string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}

var runtimeMetadataAllowlist = map[string]map[string]struct{}{
	"transition": stringSet("source"),
	"prompt": stringSet(
		"promptInputHash", "promptItemCount", "promptWindowRevision", "promptWindowSummaryPresent",
		"dialogueMessageCount", "availableVisualStateCount", "retrievedPersonalCount", "retrievedKnowledgeCount",
	),
	"continuation": stringSet(
		"cacheRetentionSupported", "incremental", "fullReason", "previousStatePresent", "previousStateSource",
		"requestShapeHash", "fullInputHash", "fullInputItemCount", "executeInputItemCount", "newItemCount",
		"newItemsHash", "previousResponseIDPresent", "previousResponseIDHash", "storedWindowRevision",
		"storedRequestShapeHash", "storedInputPrefixHash", "storedResponseItemHash", "modelDrivenTools",
	),
	"model": stringSet(
		"phase", "firstByteMs", "previewMs", "completedMs", "streamEventCount", "responseIDPresent",
		"responseIDHash", "usage", "providerCacheObservation",
	),
	"tool": stringSet(
		"tool", "phase", "status", "modelDrivenIndex", "queryHash", "personalCount", "knowledgeCount",
		"mergedPersonal", "mergedKnowledge", "semanticStatus", "webHitCount", "webSourceCount",
		"candidateCount", "candidateSetSize", "mediaType", "width", "height", "detail",
	),
	"context_window": stringSet(
		"windowNumber", "windowIDHash", "previousWindowIDHash", "observedPrefillTokens", "estimatedPrefillTokens",
		"lastTrigger", "failureCount", "promptWindowRevision",
	),
	"context_compaction": stringSet(
		"layer", "trigger", "watermark", "candidateCount", "omittedCount", "releasedTokens",
		"estimatedInvalidatedCacheTokens", "beforeTokens", "afterTokens", "projectionRevision",
	),
	"compile": stringSet("status", "chainCount", "visualState", "errorCode", "retryable", "messageHash"),
	"beat_delivery": stringSet(
		"status", "kind", "chainIndex", "playIndex", "targetIntervalMs", "paceWaitMs", "publishedPrefixCount",
	),
	"terminal": stringSet(
		"status", "visualState", "chainCount", "displayTextHash", "usage", "errorCode", "retryable",
		"messageHash", "plannedChainCount", "publishedChainCount", "publishedPrefixHash",
	),
}
