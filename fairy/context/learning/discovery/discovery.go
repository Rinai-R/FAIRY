// Package discovery owns the side-effect-free learning classification contract.
package discovery

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"fairy/runtime/model"
)

const MaxCandidates = 16

type Space string

const (
	Personal  Space = "personal"
	Knowledge Space = "knowledge"
	Social    Space = "social"
	Ignore    Space = "ignore"
)

type Evidence struct {
	Ref     string `json:"ref"`
	Role    string `json:"role"`
	Content string `json:"content"`
}

type Envelope struct {
	Type           string     `json:"type"`
	ConversationID string     `json:"conversationId"`
	CharacterID    string     `json:"characterId"`
	AllowedSpaces  []Space    `json:"allowedSpaces"`
	Evidence       []Evidence `json:"evidence"`
}

type Candidate struct {
	Space        Space    `json:"space"`
	Statement    string   `json:"statement"`
	Query        string   `json:"query"`
	EvidenceRefs []string `json:"evidenceRefs"`
}

type Output struct {
	Candidates []Candidate `json:"candidates"`
}

func BuildInput(envelope Envelope) ([]model.PromptItem, error) {
	payload, err := json.Marshal(struct {
		FairyContextData Envelope `json:"fairy_context_data"`
	}{FairyContextData: envelope})
	if err != nil {
		return nil, fmt.Errorf("serializing learning discovery envelope: %w", err)
	}
	return []model.PromptItem{{Type: model.PromptItemContextData, Content: string(payload)}}, nil
}

func ParseOutput(raw string, envelope Envelope) (Output, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return Output{}, errors.New("learning discovery returned empty output")
	}
	decoder := json.NewDecoder(bytes.NewReader([]byte(trimmed)))
	decoder.DisallowUnknownFields()
	var output Output
	if err := decoder.Decode(&output); err != nil {
		return Output{}, errors.New("learning discovery did not return strict JSON")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Output{}, errors.New("learning discovery returned trailing JSON")
	}
	if len(output.Candidates) > MaxCandidates {
		return Output{}, errors.New("learning discovery candidate limit exceeded")
	}
	allowedSpaces := make(map[Space]struct{}, len(envelope.AllowedSpaces))
	for _, space := range envelope.AllowedSpaces {
		allowedSpaces[space] = struct{}{}
	}
	allowedRefs := make(map[string]Evidence, len(envelope.Evidence))
	for _, item := range envelope.Evidence {
		allowedRefs[item.Ref] = item
	}
	seen := make(map[string]struct{}, len(output.Candidates))
	for _, candidate := range output.Candidates {
		if _, ok := allowedSpaces[candidate.Space]; !ok {
			return Output{}, fmt.Errorf("learning discovery space %q is not allowed", candidate.Space)
		}
		if strings.TrimSpace(candidate.Statement) != candidate.Statement || candidate.Statement == "" || utf8.RuneCountInString(candidate.Statement) > 2400 {
			return Output{}, errors.New("learning discovery statement is invalid")
		}
		if strings.TrimSpace(candidate.Query) != candidate.Query || candidate.Query == "" || utf8.RuneCountInString(candidate.Query) > 800 {
			return Output{}, errors.New("learning discovery query is invalid")
		}
		if len(candidate.EvidenceRefs) == 0 {
			return Output{}, errors.New("learning discovery evidenceRefs is required")
		}
		for _, ref := range candidate.EvidenceRefs {
			evidence, ok := allowedRefs[ref]
			if !ok {
				return Output{}, fmt.Errorf("learning discovery evidence ref %q was not supplied", ref)
			}
			if candidate.Space == Personal && evidence.Role != "user" {
				return Output{}, errors.New("personal candidate must cite user evidence only")
			}
		}
		key := string(candidate.Space) + "\x00" + candidate.Statement
		if _, ok := seen[key]; ok {
			return Output{}, errors.New("learning discovery contains duplicate candidate")
		}
		seen[key] = struct{}{}
	}
	return output, nil
}
