package companion

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"fairy/memory"
	"fairy/search"
)

const (
	gatherToolMemorySearch      = "memory.search"
	gatherToolWebSearch         = "web.search"
	maxModelDrivenMemoryGathers = 1
	maxGatherQueryRunes         = 200
	runtimeLedgerEventGather    = "gather"
)

// RespondInstructionsAllowGather extends reply rules with optional memory.search gather.
const RespondInstructionsAllowGather = RespondInstructions + ` If personal memories in context are insufficient to understand the user's intent, you MAY instead output exactly {"gather":{"tool":"memory.search","query":"<short search query>"}} with no other top-level fields. Use gather at most when a different memory query would materially help; otherwise output chains. Never request tools other than memory.search. Never narrate gather or tools to the user.`

// RespondInstructionsAllowGatherWithWeb also permits web.search when OpenSERP sidecar search is enabled.
const RespondInstructionsAllowGatherWithWeb = RespondInstructions + ` If personal memories or fresh public facts are insufficient, you MAY instead output exactly {"gather":{"tool":"memory.search"|"web.search","query":"<short search query>"}} with no other top-level fields. Use memory.search for personal/relationship facts and web.search for timely public topics (anime, games, news). Otherwise output chains. Never request tools other than memory.search or web.search. Never narrate gather or tools to the user.`

func RespondGatherInstructions(webSearchEnabled bool) string {
	if webSearchEnabled {
		return RespondInstructionsAllowGatherWithWeb
	}
	return RespondInstructionsAllowGather
}

type CognitionKind int

const (
	CognitionReply CognitionKind = iota
	CognitionGather
)

type GatherRequest struct {
	Tool  string
	Query string
}

type gatherEnvelope struct {
	Gather *gatherPayload `json:"gather"`
}

type gatherPayload struct {
	Tool  string `json:"tool"`
	Query string `json:"query"`
}

// ParseCognitionOutput accepts either strict reply chains or a gather envelope.
func ParseCognitionOutput(draft string) (CognitionKind, GatherRequest, error) {
	trimmed := strings.TrimSpace(draft)
	if trimmed == "" {
		return 0, GatherRequest{}, errors.New("model cognition output is empty")
	}
	if strings.Contains(trimmed, `"gather"`) {
		decoder := json.NewDecoder(strings.NewReader(trimmed))
		decoder.DisallowUnknownFields()
		var parsed gatherEnvelope
		if err := decoder.Decode(&parsed); err != nil {
			return 0, GatherRequest{}, errors.New("model gather output must be strict gather JSON")
		}
		var trailing any
		if err := decoder.Decode(&trailing); err != io.EOF {
			return 0, GatherRequest{}, errors.New("model gather output must contain exactly one JSON object")
		}
		if parsed.Gather == nil {
			return 0, GatherRequest{}, errors.New("model gather output missing gather object")
		}
		req, err := normalizeGatherRequest(*parsed.Gather)
		if err != nil {
			return 0, GatherRequest{}, err
		}
		return CognitionGather, req, nil
	}
	return CognitionReply, GatherRequest{}, nil
}

func normalizeGatherRequest(payload gatherPayload) (GatherRequest, error) {
	tool := strings.TrimSpace(payload.Tool)
	query := strings.TrimSpace(payload.Query)
	if tool == "" || query == "" {
		return GatherRequest{}, errors.New("gather tool and query are required")
	}
	if utf8.RuneCountInString(query) > maxGatherQueryRunes {
		return GatherRequest{}, errors.New("gather query is too long")
	}
	if tool != gatherToolMemorySearch && tool != gatherToolWebSearch {
		return GatherRequest{}, errors.New("gather tool is not whitelisted")
	}
	return GatherRequest{Tool: tool, Query: query}, nil
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
