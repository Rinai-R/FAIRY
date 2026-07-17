package companion

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"fairy/memory"
	"fairy/model"
	"fairy/search"
)

const (
	toolMemorySearch         = "memory_search"
	toolWebSearch            = "web_search"
	maxModelDrivenToolCalls  = 2
	maxToolQueryRunes        = 200
	runtimeLedgerEventGather = "gather"
	runtimeLedgerEventTool   = "tool"
)

// RespondInstructionsAllowTools extends reply rules with native function tools.
const RespondInstructionsAllowTools = RespondInstructions + ` When personal memories or public facts in context are insufficient, call function tools instead of guessing. Available tools: memory_search for personal/relationship facts; web_search (when provided) for timely public topics such as anime, games, versions, or news. After tool results appear in retrieved context, output chains only. Never narrate tools, search, or system settings to the user. Never output a gather JSON object.`

type toolQueryArgs struct {
	Query string `json:"query"`
}

func RespondToolSpecs(webSearchEnabled bool) []model.ToolSpec {
	querySchema := json.RawMessage(`{"type":"object","properties":{"query":{"type":"string","description":"Short search query"}},"required":["query"],"additionalProperties":false}`)
	tools := []model.ToolSpec{{
		Name:        toolMemorySearch,
		Description: "Search local personal memories and confirmed knowledge for this user/character.",
		Parameters:  querySchema,
	}}
	if webSearchEnabled {
		tools = append(tools, model.ToolSpec{
			Name:        toolWebSearch,
			Description: "Search the public web via local OpenSERP for timely public facts (anime, games, versions, news).",
			Parameters:  querySchema,
		})
	}
	return tools
}

func RespondInstructionsForTools(toolsEnabled bool) string {
	if toolsEnabled {
		return RespondInstructionsAllowTools
	}
	return RespondInstructions
}

func parseToolQuery(arguments string) (string, error) {
	trimmed := strings.TrimSpace(arguments)
	if trimmed == "" {
		return "", fmt.Errorf("tool arguments are empty")
	}
	var parsed toolQueryArgs
	if err := json.Unmarshal([]byte(trimmed), &parsed); err != nil {
		return "", fmt.Errorf("tool arguments must be JSON object with query")
	}
	query := strings.TrimSpace(parsed.Query)
	if query == "" {
		return "", fmt.Errorf("tool query is required")
	}
	if utf8.RuneCountInString(query) > maxToolQueryRunes {
		return "", fmt.Errorf("tool query is too long")
	}
	return query, nil
}

func mergeRetrievalContext(base memory.RetrievalContext, extra memory.RetrievalContext) memory.RetrievalContext {
	return memory.RetrievalContext{
		PersonalMemories: mergePersonalMemories(base.PersonalMemories, extra.PersonalMemories),
		Knowledge:        mergeKnowledge(base.Knowledge, extra.Knowledge),
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

func retrievalFromWebHits(hits []search.Hit) memory.RetrievalContext {
	if len(hits) == 0 {
		return memory.RetrievalContext{}
	}
	now := time.Now().UnixMilli()
	knowledge := make([]memory.RetrievedKnowledge, 0, len(hits))
	for index, hit := range hits {
		statement := strings.TrimSpace(hit.Title)
		if hit.Snippet != "" {
			if statement == "" {
				statement = hit.Snippet
			} else {
				statement = statement + " — " + hit.Snippet
			}
		}
		knowledge = append(knowledge, memory.RetrievedKnowledge{
			ID:                    fmt.Sprintf("web-search-%d", index+1),
			Topic:                 "web_search",
			Statement:             statement,
			VerificationBasis:     "web_search",
			ConfidenceBasisPoints: 5000,
			Sources: []memory.AssistantSource{{
				Title:           hit.Title,
				URL:             hit.URL,
				Snippet:         hit.Snippet,
				Rank:            uint8(index + 1),
				FetchedAtUnixMS: now,
			}},
			UpdatedAtUnixMS: now,
		})
	}
	return memory.RetrievalContext{Knowledge: knowledge}
}

func retrievalFromToolError(toolName string, err error) memory.RetrievalContext {
	now := time.Now().UnixMilli()
	return memory.RetrievalContext{
		Knowledge: []memory.RetrievedKnowledge{{
			ID:                    fmt.Sprintf("tool-error-%s", toolName),
			Topic:                 "tool_error",
			Statement:             fmt.Sprintf("%s failed: %s", toolName, err.Error()),
			VerificationBasis:     "tool_error",
			ConfidenceBasisPoints: 0,
			UpdatedAtUnixMS:       now,
		}},
	}
}
