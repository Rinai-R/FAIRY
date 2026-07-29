package companion

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"unicode/utf8"

	"fairy/memory"
	"fairy/model"
)

const (
	knowledgeSearchToolName              = "knowledge_search"
	maxKnowledgeAgentActions             = 8
	maxKnowledgeAgentToolCalls           = 8
	maxKnowledgeSearchQueryRunes         = 1200
	minKnowledgeSearchQueryRunes         = 8
	maxKnowledgeActionContentRunes       = 2400
	minKnowledgeActionContentRunes       = 8
	minKnowledgeActionEvidenceRunes      = 8
	maxKnowledgeActionEvidenceRunes      = 1200
	knowledgeAgentInputBudgetPercent     = 60
	knowledgeAgentPromptReserveTokens    = 1024
	knowledgeAgentMinimumContextTokens   = 4096
	knowledgeAgentMaximumCallsPerRound   = 8
	knowledgeAgentMaximumCandidateResult = memory.MaxKnowledgeSearchCandidates
	knowledgeAgentContractRevision       = "whole-document-task-actions-v2"
)

var knowledgeSearchToolParameters = json.RawMessage(`{
  "type":"object",
  "additionalProperties":false,
  "properties":{"query":{"type":"string","minLength":8,"maxLength":1200}},
  "required":["query"]
}`)

func knowledgeReconcilerRevision(instructions, contractRevision string) string {
	material := instructions + "\x00" + contractRevision
	return fmt.Sprintf("%x", sha256.Sum256([]byte(material)))
}

func knowledgeSearchToolSpec() model.ToolSpec {
	return model.ToolSpec{
		Name:        knowledgeSearchToolName,
		Description: "Search only existing verified public knowledge before deciding whether the current complete document should ADD, UPDATE, DELETE, or keep it unchanged. Use a self-contained query describing the knowledge to compare.",
		Parameters:  append(json.RawMessage(nil), knowledgeSearchToolParameters...),
	}
}

type knowledgeAgentPromptDocument struct {
	SourceID     string `json:"sourceId"`
	CanonicalURL string `json:"canonicalUrl"`
	Title        string `json:"title"`
	Content      string `json:"content"`
}

type knowledgeAgentPromptPayload struct {
	TaskID   string                       `json:"taskId"`
	Document knowledgeAgentPromptDocument `json:"document"`
}

func buildKnowledgeAgentInput(task memory.KnowledgeIngestTask, document memory.KnowledgeDocument) ([]model.PromptItem, error) {
	if task.ID == "" || document.SourceID != task.Source.ID ||
		document.CanonicalURL != task.Source.URL || strings.TrimSpace(document.Content) != document.Content ||
		document.Content == "" || memory.ContainsDisallowedControl(document.Content) {
		return nil, errors.New("knowledge agent document input is invalid")
	}
	payload, err := json.Marshal(struct {
		FairyContextData knowledgeAgentPromptPayload `json:"fairy_context_data"`
	}{
		FairyContextData: knowledgeAgentPromptPayload{
			TaskID: task.ID,
			Document: knowledgeAgentPromptDocument{
				SourceID: document.SourceID, CanonicalURL: document.CanonicalURL,
				Title: document.Title, Content: document.Content,
			},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("serializing knowledge agent document: %w", err)
	}
	return []model.PromptItem{{Type: model.PromptItemContextData, Content: string(payload)}}, nil
}

func validateInitialKnowledgeAgentBudget(document memory.KnowledgeDocument, contextWindowTokens uint64, outputTokens uint32) error {
	if contextWindowTokens < knowledgeAgentMinimumContextTokens {
		return errors.New("knowledge agent model context window is too small")
	}
	estimated := estimateKnowledgeTokens(document.Content)
	available := contextWindowTokens - uint64(outputTokens)
	if available <= knowledgeAgentPromptReserveTokens {
		return errors.New("knowledge agent model context window has no input budget")
	}
	available -= knowledgeAgentPromptReserveTokens
	if estimated > available*knowledgeAgentInputBudgetPercent/100 {
		return fmt.Errorf("complete knowledge document exceeds model context budget: estimated=%d available=%d", estimated, available)
	}
	return nil
}

func validateKnowledgeAgentPromptBudget(items []model.PromptItem, instructions string, contextWindowTokens uint64, outputTokens uint32) error {
	if contextWindowTokens < knowledgeAgentMinimumContextTokens {
		return errors.New("knowledge agent model context window is too small")
	}
	var builder strings.Builder
	builder.WriteString(instructions)
	for _, item := range items {
		builder.WriteString(item.Content)
		builder.WriteString(item.ToolCallID)
		builder.WriteString(item.ToolName)
		builder.WriteString(item.ToolArguments)
		if item.Parts != nil {
			for _, part := range *item.Parts {
				builder.WriteString(part.Text)
			}
		}
	}
	estimated := estimateKnowledgeTokens(builder.String())
	available := contextWindowTokens - uint64(outputTokens)
	if available <= knowledgeAgentPromptReserveTokens || estimated > available-knowledgeAgentPromptReserveTokens {
		return fmt.Errorf("knowledge agent prompt exceeds model context budget: estimated=%d available=%d", estimated, available)
	}
	return nil
}

func estimateKnowledgeTokens(value string) uint64 {
	runes := utf8.RuneCountInString(value)
	bytesEstimate := len(value) / 4
	if bytesEstimate > runes {
		runes = bytesEstimate
	}
	return uint64(runes)
}

type knowledgeSearchArguments struct {
	Query string `json:"query"`
}

func parseKnowledgeSearchArguments(raw string) (string, error) {
	var arguments knowledgeSearchArguments
	if err := decodeStrictKnowledgeJSON(raw, &arguments); err != nil {
		return "", errors.New("knowledge search arguments are not strict JSON")
	}
	if strings.TrimSpace(arguments.Query) != arguments.Query ||
		utf8.RuneCountInString(arguments.Query) < minKnowledgeSearchQueryRunes ||
		utf8.RuneCountInString(arguments.Query) > maxKnowledgeSearchQueryRunes ||
		memory.ContainsDisallowedControl(arguments.Query) {
		return "", errors.New("knowledge search query is invalid")
	}
	return arguments.Query, nil
}

type knowledgeAliasSet struct {
	realToAlias map[string]string
	aliasToReal map[string]string
}

func newKnowledgeAliasSet() *knowledgeAliasSet {
	return &knowledgeAliasSet{
		realToAlias: make(map[string]string),
		aliasToReal: make(map[string]string),
	}
}

func (s *knowledgeAliasSet) aliasFor(realID string) (string, error) {
	if s == nil {
		return "", errors.New("knowledge alias set is unavailable")
	}
	if alias := s.realToAlias[realID]; alias != "" {
		return alias, nil
	}
	if err := memory.ValidateID("knowledge_id", realID); err != nil {
		return "", err
	}
	alias := fmt.Sprintf("k%d", len(s.realToAlias))
	s.realToAlias[realID] = alias
	s.aliasToReal[alias] = realID
	return alias, nil
}

func (s *knowledgeAliasSet) realID(alias string) (string, bool) {
	if s == nil {
		return "", false
	}
	realID, ok := s.aliasToReal[alias]
	return realID, ok
}

func (s *knowledgeAliasSet) suppliedIDs() []string {
	if s == nil {
		return nil
	}
	out := make([]string, 0, len(s.aliasToReal))
	for _, realID := range s.aliasToReal {
		out = append(out, realID)
	}
	sort.Strings(out)
	return out
}

type knowledgeSearchPromptCandidate struct {
	ID                    string `json:"id"`
	Topic                 string `json:"topic"`
	Statement             string `json:"statement"`
	ConfidenceBasisPoints uint16 `json:"confidenceBasisPoints"`
	UpdatedAtUnixMS       int64  `json:"updatedAtUnixMs"`
}

func buildKnowledgeSearchToolItems(call model.FunctionCall, candidates []memory.RetrievedKnowledge, aliases *knowledgeAliasSet) ([]model.PromptItem, error) {
	if call.CallID == "" || call.Name != knowledgeSearchToolName ||
		len(candidates) > knowledgeAgentMaximumCandidateResult {
		return nil, errors.New("knowledge search tool result is invalid")
	}
	projected := make([]knowledgeSearchPromptCandidate, 0, len(candidates))
	seen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		if _, duplicate := seen[candidate.ID]; duplicate {
			return nil, errors.New("knowledge search candidate is duplicated")
		}
		seen[candidate.ID] = struct{}{}
		if strings.TrimSpace(candidate.Statement) == "" {
			return nil, errors.New("knowledge search candidate statement is invalid")
		}
		alias, err := aliases.aliasFor(candidate.ID)
		if err != nil {
			return nil, err
		}
		projected = append(projected, knowledgeSearchPromptCandidate{
			ID: alias, Topic: candidate.Topic, Statement: candidate.Statement,
			ConfidenceBasisPoints: candidate.ConfidenceBasisPoints,
			UpdatedAtUnixMS:       candidate.UpdatedAtUnixMS,
		})
	}
	result, err := json.Marshal(struct {
		Candidates []knowledgeSearchPromptCandidate `json:"candidates"`
	}{Candidates: projected})
	if err != nil {
		return nil, fmt.Errorf("serializing knowledge search result: %w", err)
	}
	parts := model.PromptContentParts{{Type: model.PromptContentText, Text: string(result)}}
	return []model.PromptItem{
		{
			Type: model.PromptItemToolCall, ToolCallID: call.CallID,
			ToolName: call.Name, ToolArguments: call.Arguments,
		},
		{Type: model.PromptItemToolResult, ToolCallID: call.CallID, Parts: &parts},
	}, nil
}

type knowledgeAgentAction struct {
	Operation             string  `json:"operation"`
	MemoryID              *string `json:"memoryId,omitempty"`
	Content               *string `json:"content,omitempty"`
	ConfidenceBasisPoints *uint16 `json:"confidenceBasisPoints,omitempty"`
	Evidence              string  `json:"evidence"`
}

type knowledgeAgentOutput struct {
	Actions []knowledgeAgentAction `json:"actions"`
}

func parseKnowledgeAgentOutput(raw string, document memory.KnowledgeDocument, aliases *knowledgeAliasSet) ([]memory.KnowledgeDocumentAction, error) {
	var output knowledgeAgentOutput
	if err := decodeStrictKnowledgeJSON(raw, &output); err != nil {
		return nil, errors.New("knowledge agent did not return strict JSON")
	}
	if output.Actions == nil {
		return nil, errors.New("knowledge agent actions are required")
	}
	if len(output.Actions) > maxKnowledgeAgentActions {
		return nil, errors.New("knowledge agent action limit exceeded")
	}
	actions := make([]memory.KnowledgeDocumentAction, 0, len(output.Actions))
	seenTargets := make(map[string]struct{})
	seenContents := make(map[string]struct{})
	for index, action := range output.Actions {
		if strings.TrimSpace(action.Evidence) != action.Evidence || action.Evidence == "" ||
			utf8.RuneCountInString(action.Evidence) < minKnowledgeActionEvidenceRunes ||
			utf8.RuneCountInString(action.Evidence) > maxKnowledgeActionEvidenceRunes ||
			memory.ContainsDisallowedControl(action.Evidence) ||
			!strings.Contains(document.Content, action.Evidence) {
			return nil, fmt.Errorf("knowledge action[%d] evidence is invalid", index)
		}
		operation := memory.KnowledgeMutationOperation(action.Operation)
		item := memory.KnowledgeDocumentAction{Operation: operation, Evidence: action.Evidence}
		switch operation {
		case memory.KnowledgeMutationAdd:
			if action.MemoryID != nil || action.Content == nil || action.ConfidenceBasisPoints == nil {
				return nil, fmt.Errorf("knowledge action[%d] ADD fields are invalid", index)
			}
		case memory.KnowledgeMutationUpdate:
			if action.MemoryID == nil || action.Content == nil || action.ConfidenceBasisPoints == nil {
				return nil, fmt.Errorf("knowledge action[%d] UPDATE fields are invalid", index)
			}
			realID, ok := aliases.realID(*action.MemoryID)
			if !ok {
				return nil, fmt.Errorf("knowledge action[%d] UPDATE fields are invalid", index)
			}
			item.MemoryID = realID
		case memory.KnowledgeMutationDelete, memory.KnowledgeMutationNone:
			if action.MemoryID == nil || action.Content != nil || action.ConfidenceBasisPoints != nil {
				return nil, fmt.Errorf("knowledge action[%d] target fields are invalid", index)
			}
			realID, ok := aliases.realID(*action.MemoryID)
			if !ok {
				return nil, fmt.Errorf("knowledge action[%d] target fields are invalid", index)
			}
			item.MemoryID = realID
		default:
			return nil, fmt.Errorf("knowledge action[%d] operation is invalid", index)
		}
		if item.MemoryID != "" {
			if _, duplicate := seenTargets[item.MemoryID]; duplicate {
				return nil, errors.New("knowledge action target is duplicated")
			}
			seenTargets[item.MemoryID] = struct{}{}
		}
		if operation == memory.KnowledgeMutationAdd || operation == memory.KnowledgeMutationUpdate {
			content := *action.Content
			if strings.TrimSpace(content) != content ||
				utf8.RuneCountInString(content) < minKnowledgeActionContentRunes ||
				utf8.RuneCountInString(content) > maxKnowledgeActionContentRunes ||
				memory.ContainsDisallowedControl(content) ||
				action.ConfidenceBasisPoints == nil || *action.ConfidenceBasisPoints == 0 ||
				*action.ConfidenceBasisPoints > 10000 {
				return nil, fmt.Errorf("knowledge action[%d] content is invalid", index)
			}
			if _, duplicate := seenContents[content]; duplicate {
				return nil, errors.New("knowledge action content is duplicated")
			}
			seenContents[content] = struct{}{}
			item.Content = content
			item.ConfidenceBasisPoints = *action.ConfidenceBasisPoints
		}
		actions = append(actions, item)
	}
	return actions, nil
}

func decodeStrictKnowledgeJSON(raw string, target any) error {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return errors.New("JSON is empty")
	}
	if err := rejectDuplicateKnowledgeJSONFields(trimmed); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader([]byte(trimmed)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("JSON has trailing content")
	}
	return nil
}

func rejectDuplicateKnowledgeJSONFields(raw string) error {
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	if err := walkKnowledgeJSONValue(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err != nil {
			return err
		}
		return errors.New("JSON has trailing content")
	}
	return nil
}

func walkKnowledgeJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("JSON object key is invalid")
			}
			if _, duplicate := seen[key]; duplicate {
				return fmt.Errorf("JSON field %q is duplicated", key)
			}
			seen[key] = struct{}{}
			if err := walkKnowledgeJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return err
		}
		if closing != json.Delim('}') {
			return errors.New("JSON object is not closed")
		}
	case '[':
		for decoder.More() {
			if err := walkKnowledgeJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return err
		}
		if closing != json.Delim(']') {
			return errors.New("JSON array is not closed")
		}
	default:
		return errors.New("JSON contains an unexpected closing delimiter")
	}
	return nil
}
