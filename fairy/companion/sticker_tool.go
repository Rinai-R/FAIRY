package companion

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"fairy/model"
	replyapp "fairy/reply"
	"fairy/session"
	"fairy/sticker"
)

const (
	toolStickerSearch      = "sticker_search"
	maxStickerCandidates   = 12
	stickerCandidatesLimit = 6
)

type StickerSearchPort interface {
	HasActive(context.Context) (bool, error)
	Search(context.Context, string, int) ([]sticker.Candidate, error)
}

var _ StickerSearchPort = (*sticker.Store)(nil)

func AttachStickerSearch(service *CompanionService, search StickerSearchPort) {
	if service == nil {
		return
	}
	service.stickers = search
}

func stickerToolAvailable(ctx context.Context, capabilities session.OutputCapabilities, search StickerSearchPort) (bool, error) {
	if !capabilities.Sticker || search == nil {
		return false, nil
	}
	return search.HasActive(ctx)
}

func stickerToolSpec() model.ToolSpec {
	return model.ToolSpec{
		Name:        toolStickerSearch,
		Description: "Search the managed sticker library by intended emotion or conversational meaning. Results contain only human-authored descriptions and tags. A returned ID is selectable only in this turn's final sticker chain; never invent or reuse another ID.",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"query":{"type":"string","description":"Short intended emotion, reaction, or conversational meaning"}},"required":["query"],"additionalProperties":false}`),
	}
}

type stickerCandidateSet map[string]sticker.Candidate

func (set stickerCandidateSet) add(candidates []sticker.Candidate) []sticker.Candidate {
	remaining := maxStickerCandidates - len(set)
	if remaining <= 0 {
		return []sticker.Candidate{}
	}
	added := make([]sticker.Candidate, 0, min(len(candidates), remaining))
	for _, candidate := range candidates {
		if _, exists := set[candidate.ID]; exists {
			continue
		}
		set[candidate.ID] = candidate
		added = append(added, candidate)
		if len(added) == remaining {
			break
		}
	}
	return added
}

func (set stickerCandidateSet) contains(id string) bool {
	_, ok := set[id]
	return ok
}

func (set stickerCandidateSet) compileOptions(allowed bool) replyapp.CompileOptions {
	candidates := make(map[string]replyapp.StickerReference, len(set))
	for id, candidate := range set {
		candidates[id] = replyapp.StickerReference{
			ID: candidate.ID, Description: candidate.Description, MIMEType: candidate.MIMEType,
		}
	}
	return replyapp.CompileOptions{StickerAllowed: allowed, StickerCandidates: candidates}
}

func stickerExpressionInstructions(instructions string, enabled bool) string {
	if !enabled {
		return instructions
	}
	const legacy = `Exact schema: {"chains":[{"visualState":"<one id from available_visual_states>","text":"the character's spoken line"}]}. The top level may contain only chains; each chain only visualState/text; chains length is 1-5.`
	const expressions = `Exact schema: {"chains":[{"kind":"utterance","visualState":"<one id from available_visual_states>","text":"the character's spoken line"}]} or a chain may instead be {"kind":"sticker","visualState":"<one id from available_visual_states>","stickerId":"<one id returned by sticker_search in this turn>"}. The top level may contain only chains; each chain is exactly one closed utterance/sticker variant; chains length is 1-5 and at most one chain may be sticker.`
	return strings.Replace(instructions, legacy, expressions, 1)
}

func stickerToolPromptItems(callID, arguments string, candidates []sticker.Candidate, toolErr error) []model.PromptItem {
	result := struct {
		Candidates []sticker.Candidate `json:"candidates"`
		Error      string              `json:"error,omitempty"`
	}{Candidates: candidates}
	if result.Candidates == nil {
		result.Candidates = []sticker.Candidate{}
	}
	if toolErr != nil {
		result.Error = "sticker_search unavailable"
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		encoded = []byte(`{"candidates":[],"error":"sticker_search unavailable"}`)
	}
	parts := model.PromptContentParts{{Type: model.PromptContentText, Text: string(encoded)}}
	return []model.PromptItem{
		{Type: model.PromptItemToolCall, ToolCallID: callID, ToolName: toolStickerSearch, ToolArguments: arguments},
		{Type: model.PromptItemToolResult, ToolCallID: callID, Parts: &parts},
	}
}

func searchStickerCandidates(ctx context.Context, search StickerSearchPort, set stickerCandidateSet, query string) ([]sticker.Candidate, error) {
	if search == nil {
		return nil, errors.New("sticker search is unavailable")
	}
	remaining := maxStickerCandidates - len(set)
	if remaining <= 0 {
		return []sticker.Candidate{}, nil
	}
	limit := min(stickerCandidatesLimit, remaining)
	candidates, err := search.Search(ctx, query, limit)
	if err != nil {
		return nil, err
	}
	return set.add(candidates), nil
}
