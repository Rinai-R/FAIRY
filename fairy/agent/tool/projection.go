package tool

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	knowledgectx "fairy/context/knowledge"
	"fairy/context/memory/personal"
	"fairy/context/recall"
	"fairy/context/social"
	"fairy/runtime/model"
)

type RetrievalResultSummary struct {
	Status         string `json:"status"`
	Empty          bool   `json:"empty"`
	PersonalCount  int    `json:"personalCount"`
	KnowledgeCount int    `json:"knowledgeCount"`
	SocialCount    int    `json:"socialCount"`
	SemanticStatus string `json:"semanticStatus,omitempty"`
}

type RuntimeDetail struct {
	Version       string                       `json:"version"`
	Arguments     RuntimeArguments             `json:"arguments"`
	Receipt       RetrievalResultSummary       `json:"receipt"`
	Result        *runtimeToolRetrievalContext `json:"result,omitempty"`
	MergedContext *runtimeToolRetrievalContext `json:"mergedContext,omitempty"`
}

type RuntimeArguments struct {
	Query string `json:"query,omitempty"`
}

type runtimeToolRetrievalContext struct {
	PersonalMemories []runtimeToolPersonalMemory `json:"personalMemories"`
	Knowledge        []runtimeToolKnowledge      `json:"knowledge"`
	SocialMemories   runtimeToolSocialContext    `json:"socialMemories,omitempty"`
	SemanticStatus   string                      `json:"semanticStatus,omitempty"`
}

type runtimeToolPersonalMemory struct {
	ID                    string         `json:"id"`
	Kind                  string         `json:"kind"`
	Layer                 string         `json:"layer"`
	Scope                 personal.Scope `json:"scope"`
	Content               string         `json:"content"`
	ConfidenceBasisPoints uint16         `json:"confidenceBasisPoints"`
	UpdatedAtUnixMS       int64          `json:"updatedAtUnixMs"`
}

type runtimeToolKnowledge struct {
	ID                    string              `json:"id"`
	Layer                 string              `json:"layer"`
	Topic                 string              `json:"topic"`
	Statement             string              `json:"statement"`
	VerificationBasis     string              `json:"verificationBasis"`
	ConfidenceBasisPoints uint16              `json:"confidenceBasisPoints"`
	Sources               []runtimeToolSource `json:"sources"`
	UpdatedAtUnixMS       int64               `json:"updatedAtUnixMs"`
}

type runtimeToolSource struct {
	Title           string `json:"title"`
	URL             string `json:"url"`
	Snippet         string `json:"snippet"`
	Rank            uint8  `json:"rank"`
	FetchedAtUnixMS int64  `json:"fetchedAtUnixMs"`
}

type runtimeToolSocialContext struct {
	Entries []runtimeToolSocialMemory `json:"entries"`
}

type runtimeToolSocialMemory struct {
	ID              string `json:"id"`
	Kind            string `json:"kind"`
	Situation       string `json:"situation"`
	Content         string `json:"content"`
	RecallCue       string `json:"recallCue"`
	UpdatedAtUnixMS int64  `json:"updatedAtUnixMs"`
}

func RetrievalSummary(status string, result recall.Context) RetrievalResultSummary {
	summary := RetrievalResultSummary{
		Status:         stableRetrievalToolStatus(status),
		PersonalCount:  len(result.PersonalMemories),
		KnowledgeCount: len(result.Knowledge),
		SocialCount:    len(result.SocialMemories.Entries),
		SemanticStatus: strings.TrimSpace(result.SemanticStatus),
	}
	summary.Empty = summary.PersonalCount == 0 && summary.KnowledgeCount == 0 && summary.SocialCount == 0
	return summary
}

// runtimeRetrievalToolDetail captures the validated model-visible retrieval
// projection. Provider responses, request headers, binary payloads and the
// complete model prompt never cross this boundary.
func RetrievalRuntimeDetail(query, status string, result, merged recall.Context) RuntimeDetail {
	detail := RuntimeDetail{
		Version:   "v1",
		Arguments: RuntimeArguments{Query: runtimeInspectionText(query, maxToolQueryRunes)},
		Receipt:   RetrievalSummary(status, result),
	}
	// Failure causes may contain upstream response bodies. The stable receipt is
	// enough to diagnose those paths without persisting arbitrary provider text.
	if stableRetrievalToolStatus(status) == "ok" {
		resultCopy := runtimeToolRetrievalProjection(result)
		mergedCopy := runtimeToolRetrievalProjection(merged)
		detail.Result = &resultCopy
		detail.MergedContext = &mergedCopy
	}
	return detail
}

func runtimeToolRetrievalProjection(context recall.Context) runtimeToolRetrievalContext {
	projected := runtimeToolRetrievalContext{
		PersonalMemories: make([]runtimeToolPersonalMemory, 0, len(context.PersonalMemories)),
		Knowledge:        make([]runtimeToolKnowledge, 0, len(context.Knowledge)),
		SocialMemories:   runtimeToolSocialContext{Entries: make([]runtimeToolSocialMemory, 0, len(context.SocialMemories.Entries))},
		SemanticStatus:   runtimeInspectionText(context.SemanticStatus, 64),
	}
	for _, item := range context.PersonalMemories {
		projected.PersonalMemories = append(projected.PersonalMemories, runtimeToolPersonalMemory{
			ID: runtimeInspectionText(item.ID, 128), Kind: runtimeInspectionText(item.Kind, 64),
			Layer: runtimeInspectionText(item.Layer, 64), Scope: item.Scope,
			Content:               runtimeInspectionText(item.Content, personal.MaxContentRunes),
			ConfidenceBasisPoints: item.ConfidenceBasisPoints, UpdatedAtUnixMS: item.UpdatedAtUnixMS,
		})
	}
	for _, item := range context.Knowledge {
		knowledge := runtimeToolKnowledge{
			ID: runtimeInspectionText(item.ID, 128), Layer: runtimeInspectionText(item.Layer, 64),
			Topic: runtimeInspectionText(item.Topic, 300), Statement: runtimeInspectionText(item.Statement, 7200),
			VerificationBasis:     runtimeInspectionText(item.VerificationBasis, 128),
			ConfidenceBasisPoints: item.ConfidenceBasisPoints, UpdatedAtUnixMS: item.UpdatedAtUnixMS,
			Sources: make([]runtimeToolSource, 0, len(item.Sources)),
		}
		for _, source := range item.Sources {
			knowledge.Sources = append(knowledge.Sources, runtimeToolSource{
				Title: runtimeInspectionText(source.Title, 300), URL: runtimeInspectionText(source.URL, 2048),
				Snippet: runtimeInspectionText(source.Snippet, knowledgectx.MaxIngestSnippetRunes),
				Rank:    source.Rank, FetchedAtUnixMS: source.FetchedAtUnixMS,
			})
		}
		projected.Knowledge = append(projected.Knowledge, knowledge)
	}
	for _, item := range context.SocialMemories.Entries {
		projected.SocialMemories.Entries = append(projected.SocialMemories.Entries, runtimeToolSocialMemory{
			ID: runtimeInspectionText(item.ID, 128), Kind: runtimeInspectionText(item.Kind, 64),
			Situation:       runtimeInspectionText(item.Situation, social.MaxSocialSituationRunes),
			Content:         runtimeInspectionText(item.Content, social.MaxSocialContentRunes),
			RecallCue:       runtimeInspectionText(item.RecallCue, social.MaxSocialRecallRunes),
			UpdatedAtUnixMS: item.UpdatedAtUnixMS,
		})
	}
	return projected
}

func runtimeInspectionText(value string, maxRunes int) string {
	value = strings.TrimSpace(value)
	lower := strings.ToLower(value)
	if strings.Contains(lower, "authorization:") || strings.Contains(lower, "bearer ") {
		return "[已脱敏]"
	}
	runes := []rune(value)
	if len(runes) > maxRunes {
		return string(runes[:maxRunes]) + "…"
	}
	return value
}

func RetrievalPromptItems(call model.FunctionCall, status string, result recall.Context) []model.PromptItem {
	summary := RetrievalSummary(status, result)
	encoded, err := json.Marshal(summary)
	if err != nil {
		encoded = []byte(`{"status":"failed","empty":true,"personalCount":0,"knowledgeCount":0,"socialCount":0}`)
	}
	parts := model.PromptContentParts{{Type: model.PromptContentText, Text: string(encoded)}}
	return []model.PromptItem{
		{Type: model.PromptItemToolCall, ToolCallID: call.CallID, ToolName: call.Name, ToolArguments: call.Arguments},
		{Type: model.PromptItemToolResult, ToolCallID: call.CallID, Parts: &parts},
	}
}

func stableRetrievalToolStatus(status string) string {
	switch status {
	case "ok", "failed", "args_invalid", "disabled", "endpoint_missing", "not_whitelisted":
		return status
	default:
		return "failed"
	}
}

func MergeRetrieval(base, extra recall.Context) recall.Context {
	return recall.Context{
		PersonalMemories: mergePersonalMemories(base.PersonalMemories, extra.PersonalMemories),
		Knowledge:        mergeKnowledge(base.Knowledge, extra.Knowledge),
		SocialMemories:   MergeSocialMemory(base.SocialMemories, extra.SocialMemories),
		SemanticStatus:   mergeSemanticStatus(base.SemanticStatus, extra.SemanticStatus),
	}
}

func MergeSocialMemory(base, extra social.SocialMemoryContext) social.SocialMemoryContext {
	if base.Empty() {
		return extra
	}
	if extra.Empty() {
		return base
	}
	seen := make(map[string]struct{}, len(base.Entries)+len(extra.Entries))
	entries := make([]social.SocialMemoryEntry, 0, len(base.Entries)+len(extra.Entries))
	for _, entry := range append(append([]social.SocialMemoryEntry{}, base.Entries...), extra.Entries...) {
		if _, ok := seen[entry.ID]; ok {
			continue
		}
		seen[entry.ID] = struct{}{}
		entries = append(entries, entry)
	}
	return social.SocialMemoryContext{Entries: entries}
}

func BoundSocialRetrieval(base social.SocialMemoryContext, extra recall.Context, limit int) recall.Context {
	if limit <= 0 || len(extra.SocialMemories.Entries) == 0 {
		extra.Knowledge = nil
		extra.SocialMemories = social.SocialMemoryContext{}
		return extra
	}
	seen := make(map[string]struct{}, len(base.Entries)+len(extra.SocialMemories.Entries))
	for _, entry := range base.Entries {
		seen[entry.ID] = struct{}{}
	}
	allowed := make(map[string]struct{}, len(extra.SocialMemories.Entries))
	entries := make([]social.SocialMemoryEntry, 0, len(extra.SocialMemories.Entries))
	for _, entry := range extra.SocialMemories.Entries {
		if _, exists := seen[entry.ID]; !exists {
			if len(seen) >= limit {
				continue
			}
			seen[entry.ID] = struct{}{}
		}
		allowed[entry.ID] = struct{}{}
		entries = append(entries, entry)
	}
	knowledge := make([]knowledgectx.Retrieved, 0, len(entries))
	for _, item := range extra.Knowledge {
		if _, exists := allowed[item.ID]; exists {
			knowledge = append(knowledge, item)
		}
	}
	extra.Knowledge = knowledge
	extra.SocialMemories = social.SocialMemoryContext{Entries: entries}
	return extra
}

func mergeSemanticStatus(base, extra string) string {
	switch {
	case extra == "used" || base == "used":
		return "used"
	case extra == "ready" || base == "ready":
		return "ready"
	case extra != "":
		return extra
	default:
		return base
	}
}

func mergePersonalMemories(base, extra []personal.Retrieved) []personal.Retrieved {
	if len(extra) == 0 {
		return base
	}
	seen := make(map[string]struct{}, len(base)+len(extra))
	out := make([]personal.Retrieved, 0, len(base)+len(extra))
	for _, item := range base {
		if item.ID == "" {
			out = append(out, item)
			continue
		}
		if _, ok := seen[item.ID]; ok {
			continue
		}
		seen[item.ID] = struct{}{}
		out = append(out, item)
	}
	for _, item := range extra {
		if item.ID != "" {
			if _, ok := seen[item.ID]; ok {
				continue
			}
			seen[item.ID] = struct{}{}
		}
		out = append(out, item)
	}
	return out
}

func mergeKnowledge(base, extra []knowledgectx.Retrieved) []knowledgectx.Retrieved {
	if len(extra) == 0 {
		return base
	}
	seen := make(map[string]struct{}, len(base)+len(extra))
	out := make([]knowledgectx.Retrieved, 0, len(base)+len(extra))
	for _, item := range base {
		if item.ID == "" {
			out = append(out, item)
			continue
		}
		if _, ok := seen[item.ID]; ok {
			continue
		}
		seen[item.ID] = struct{}{}
		out = append(out, item)
	}
	for _, item := range extra {
		if item.ID != "" {
			if _, ok := seen[item.ID]; ok {
				continue
			}
			seen[item.ID] = struct{}{}
		}
		out = append(out, item)
	}
	return out
}

func FromSearchBatch(batch knowledgectx.SearchBatch) recall.Context {
	if len(batch.Sources) == 0 {
		return recall.Context{}
	}
	now := time.Now().UnixMilli()
	knowledge := make([]knowledgectx.Retrieved, 0, len(batch.Sources))
	for _, source := range batch.Sources {
		statement := strings.TrimSpace(source.Title)
		if source.Snippet != "" {
			if statement == "" {
				statement = source.Snippet
			} else {
				statement = statement + " — " + source.Snippet
			}
		}
		knowledge = append(knowledge, knowledgectx.Retrieved{
			ID:                    source.ID,
			Layer:                 "knowledge",
			Topic:                 "web_search",
			Statement:             statement,
			VerificationBasis:     "web_search",
			ConfidenceBasisPoints: 5000,
			Sources: []knowledgectx.AssistantSource{{
				Title:           source.Title,
				URL:             source.URL,
				Snippet:         source.Snippet,
				Rank:            source.Rank,
				FetchedAtUnixMS: source.FetchedAtUnixMS,
			}},
			UpdatedAtUnixMS: now,
		})
	}
	return recall.Context{Knowledge: knowledge, SemanticStatus: "unavailable"}
}

func FromError(toolName string, err error) recall.Context {
	now := time.Now().UnixMilli()
	return recall.Context{
		Knowledge: []knowledgectx.Retrieved{{
			ID:                    fmt.Sprintf("tool-error-%s", toolName),
			Layer:                 "knowledge",
			Topic:                 "tool_error",
			Statement:             fmt.Sprintf("%s failed: %s", toolName, err.Error()),
			VerificationBasis:     "tool_error",
			ConfidenceBasisPoints: 0,
			UpdatedAtUnixMS:       now,
		}},
		SemanticStatus: "unavailable",
	}
}
