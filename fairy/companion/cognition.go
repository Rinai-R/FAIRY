package companion

import (
	"context"
	"encoding/json"
	"errors"
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
const RespondInstructionsAllowTools = RespondInstructions + ` When personal memories or public facts in context are insufficient, call function tools instead of guessing. Available tools: memory_search for profile, preference, experience, current-character relationship, and confirmed local knowledge; web_search (when provided) for timely public topics such as anime, games, versions, or news. When you call a tool, you MAY put one short in-character line for the user in the assistant content (plain text in textLanguage, not JSON) so you stay present while the tool runs—keep it to a single natural sentence in this character's voice, and never mention tool names, searches, retrieval, reasoning, or system internals. If you have nothing natural to say, leave content empty; never invent filler. After tool results appear in retrieved context, output chains only. Never output a gather JSON object.`

type toolQueryArgs struct {
	Query string `json:"query"`
}

func RespondToolSpecs(webSearchEnabled bool) []model.ToolSpec {
	return RespondToolSpecsForSurface(webSearchEnabled, SurfaceDesktop)
}

// RespondToolSpecsForSurface applies the hard privacy boundary for group IM.
// Group turns may use current-turn web search, but never receive personal memory tooling.
func RespondToolSpecsForSurface(webSearchEnabled bool, surface SurfaceKind) []model.ToolSpec {
	querySchema := json.RawMessage(`{"type":"object","properties":{"query":{"type":"string","description":"Short search query"}},"required":["query"],"additionalProperties":false}`)
	tools := make([]model.ToolSpec, 0, 2)
	if mustNormalizeSurface(surface) != SurfaceIMGroup {
		tools = append(tools, model.ToolSpec{
			Name:        toolMemorySearch,
			Description: "Search layered local memory for user profile, preferences, experiences, current-character relationship facts, and confirmed local knowledge. Results include semanticStatus metadata; unavailable means FTS-only recall.",
			Parameters:  querySchema,
		})
	}
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

func RespondInstructionsForSurface(toolsEnabled bool, surface SurfaceKind) string {
	if mustNormalizeSurface(surface) == SurfaceIMGroup {
		if toolsEnabled {
			return strings.ReplaceAll(RespondInstructionsAllowTools, "memory_search for profile, preference, experience, current-character relationship, and confirmed local knowledge; ", "")
		}
		return RespondInstructions
	}
	return RespondInstructionsForTools(toolsEnabled)
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

func (s *CompanionService) retrieveMemoryForTool(characterID string, query string) (memory.RetrievalContext, error) {
	if s == nil || s.memory == nil {
		return memory.RetrievalContext{}, errors.New("memory store is unavailable")
	}
	if s.semanticEmbedder != nil && s.semanticEmbedder.Ready() {
		return s.memory.RetrieveWithSemanticVectorIndex(context.Background(), characterID, query, s.semanticEmbedder, s.vectorIndex)
	}
	return s.memory.Retrieve(characterID, query)
}

func mergeRetrievalContext(base memory.RetrievalContext, extra memory.RetrievalContext) memory.RetrievalContext {
	return memory.RetrievalContext{
		PersonalMemories: mergePersonalMemories(base.PersonalMemories, extra.PersonalMemories),
		Knowledge:        mergeKnowledge(base.Knowledge, extra.Knowledge),
		SemanticStatus:   mergeSemanticStatus(base.SemanticStatus, extra.SemanticStatus),
	}
}

func mergeSemanticStatus(base string, extra string) string {
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
			Layer:                 "knowledge",
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
