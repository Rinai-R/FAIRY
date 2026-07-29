package companion

import (
	"fmt"
	"strings"
	"time"

	"fairy/memory"
)

func mergeRetrievalContext(base, extra memory.RetrievalContext) memory.RetrievalContext {
	return memory.RetrievalContext{
		PersonalMemories: mergePersonalMemories(base.PersonalMemories, extra.PersonalMemories),
		Knowledge:        mergeKnowledge(base.Knowledge, extra.Knowledge),
		SocialMemories:   mergeSocialMemory(base.SocialMemories, extra.SocialMemories),
		SemanticStatus:   mergeSemanticStatus(base.SemanticStatus, extra.SemanticStatus),
	}
}

func mergeSocialMemory(base, extra memory.SocialMemoryContext) memory.SocialMemoryContext {
	if base.Empty() {
		return extra
	}
	if extra.Empty() {
		return base
	}
	seen := make(map[string]struct{}, len(base.Entries)+len(extra.Entries))
	entries := make([]memory.SocialMemoryEntry, 0, len(base.Entries)+len(extra.Entries))
	for _, entry := range append(append([]memory.SocialMemoryEntry{}, base.Entries...), extra.Entries...) {
		if _, ok := seen[entry.ID]; ok {
			continue
		}
		seen[entry.ID] = struct{}{}
		entries = append(entries, entry)
	}
	return memory.SocialMemoryContext{Entries: entries}
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

func mergePersonalMemories(base, extra []memory.RetrievedPersonalMemory) []memory.RetrievedPersonalMemory {
	if len(extra) == 0 {
		return base
	}
	seen := make(map[string]struct{}, len(base)+len(extra))
	out := make([]memory.RetrievedPersonalMemory, 0, len(base)+len(extra))
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

func mergeKnowledge(base, extra []memory.RetrievedKnowledge) []memory.RetrievedKnowledge {
	if len(extra) == 0 {
		return base
	}
	seen := make(map[string]struct{}, len(base)+len(extra))
	out := make([]memory.RetrievedKnowledge, 0, len(base)+len(extra))
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

func retrievalFromWebSearchBatch(batch webSearchBatch) memory.RetrievalContext {
	if len(batch.Sources) == 0 {
		return memory.RetrievalContext{}
	}
	now := time.Now().UnixMilli()
	knowledge := make([]memory.RetrievedKnowledge, 0, len(batch.Sources))
	for _, source := range batch.Sources {
		statement := strings.TrimSpace(source.Title)
		if source.Snippet != "" {
			if statement == "" {
				statement = source.Snippet
			} else {
				statement = statement + " — " + source.Snippet
			}
		}
		knowledge = append(knowledge, memory.RetrievedKnowledge{
			ID:                    source.ID,
			Layer:                 "knowledge",
			Topic:                 "web_search",
			Statement:             statement,
			VerificationBasis:     "web_search",
			ConfidenceBasisPoints: 5000,
			Sources: []memory.AssistantSource{{
				Title:           source.Title,
				URL:             source.URL,
				Snippet:         source.Snippet,
				Rank:            source.Rank,
				FetchedAtUnixMS: source.FetchedAtUnixMS,
			}},
			UpdatedAtUnixMS: now,
		})
	}
	return memory.RetrievalContext{Knowledge: knowledge, SemanticStatus: "unavailable"}
}

func retrievalFromToolError(toolName string, err error) memory.RetrievalContext {
	now := time.Now().UnixMilli()
	return memory.RetrievalContext{
		Knowledge: []memory.RetrievedKnowledge{{
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
