package reply

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"fairy/context/character"
)

const (
	MaxReplyChains      = 12
	MaxModelReplyChains = 5
)

type VisualState = character.VisualState

type CompiledReply struct {
	DisplayText string       `json:"displayText"`
	VisualState string       `json:"visualState"`
	Chains      []ReplyChain `json:"chains"`
}

type CompileOptions struct {
	StickerAllowed    bool
	StickerCandidates map[string]StickerReference
}

func CompileReply(draft string, availableVisualStates []VisualState) (CompiledReply, error) {
	return CompileReplyWithOptions(draft, availableVisualStates, CompileOptions{})
}

func CompileReplyWithOptions(draft string, availableVisualStates []VisualState, options CompileOptions) (CompiledReply, error) {
	if err := ValidateAvailableVisualStates(availableVisualStates); err != nil {
		return CompiledReply{}, err
	}
	if err := validateDraft(draft); err != nil {
		return CompiledReply{}, err
	}
	return compileJSONReplyChains(draft, availableVisualStates, options)
}

type jsonReplyChains struct {
	Chains []jsonReplyChain `json:"chains"`
}

type jsonReplyChain struct {
	Kind        optionalJSONString `json:"kind"`
	VisualState string             `json:"visualState"`
	Text        optionalJSONString `json:"text"`
	StickerID   optionalJSONString `json:"stickerId"`
}

type optionalJSONString struct {
	set   bool
	value string
}

func (value *optionalJSONString) UnmarshalJSON(data []byte) error {
	value.set = true
	if string(data) == "null" {
		return errors.New("field must be a string")
	}
	return json.Unmarshal(data, &value.value)
}

func compileJSONReplyChains(draft string, availableVisualStates []VisualState, options CompileOptions) (CompiledReply, error) {
	decoder := json.NewDecoder(strings.NewReader(draft))
	decoder.DisallowUnknownFields()
	var parsed jsonReplyChains
	if err := decoder.Decode(&parsed); err != nil {
		return CompiledReply{}, errors.New("model reply must be strict reply chains JSON")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return CompiledReply{}, errors.New("model reply must contain exactly one JSON object")
	}
	if len(parsed.Chains) == 0 || len(parsed.Chains) > MaxModelReplyChains {
		return CompiledReply{}, fmt.Errorf("model reply chains count must be 1-%d", MaxModelReplyChains)
	}
	chains := make([]ReplyChain, 0, len(parsed.Chains))
	stickerCount := 0
	for _, chain := range parsed.Chains {
		compiled, err := compileJSONReplyChain(chain, availableVisualStates, options)
		if err != nil {
			return CompiledReply{}, err
		}
		if compiled.Kind == ChainSticker {
			stickerCount++
			if stickerCount > 1 {
				return CompiledReply{}, errors.New("model reply may contain at most one sticker chain")
			}
		}
		chains = append(chains, compiled)
	}
	return CompiledReplyFromChains(chains)
}

func CompiledReplyFromChains(chains []ReplyChain) (CompiledReply, error) {
	if err := ValidateReplyChains(chains); err != nil {
		return CompiledReply{}, err
	}
	parts := make([]string, 0, len(chains))
	for _, chain := range chains {
		if chain.Kind == ChainSticker {
			continue
		}
		parts = append(parts, chain.Text)
	}
	return CompiledReply{
		DisplayText: strings.Join(parts, "\n"),
		VisualState: chains[len(chains)-1].VisualState,
		Chains:      chains,
	}, nil
}

func compileJSONReplyChain(chain jsonReplyChain, availableVisualStates []VisualState, options CompileOptions) (ReplyChain, error) {
	if !hasVisualState(availableVisualStates, chain.VisualState) {
		return ReplyChain{}, errors.New("model reply returned undeclared visual state")
	}
	kind := ChainUtterance
	if chain.Kind.set {
		kind = ChainKind(chain.Kind.value)
	}
	switch kind {
	case ChainUtterance:
		if !chain.Text.set || chain.StickerID.set {
			return ReplyChain{}, errors.New("utterance chain must contain text and must not contain stickerId")
		}
		display := SanitizeDisplayText(chain.Text.value)
		if display == "" {
			return ReplyChain{}, errors.New("model did not return usable reply text")
		}
		return ReplyChain{Kind: ChainUtterance, Text: display, VisualState: chain.VisualState}, nil
	case ChainSticker:
		if chain.Text.set || !chain.StickerID.set || strings.TrimSpace(chain.StickerID.value) == "" {
			return ReplyChain{}, errors.New("sticker chain must contain stickerId and must not contain text")
		}
		if !options.StickerAllowed {
			return ReplyChain{}, errors.New("sticker output is unavailable for this session")
		}
		candidate, ok := options.StickerCandidates[chain.StickerID.value]
		if !ok || candidate.ID != chain.StickerID.value {
			return ReplyChain{}, errors.New("stickerId was not returned in this turn")
		}
		candidateCopy := candidate
		return ReplyChain{Kind: ChainSticker, VisualState: chain.VisualState, Sticker: &candidateCopy}, nil
	default:
		return ReplyChain{}, fmt.Errorf("model reply chain kind %q is invalid", kind)
	}
}

func SanitizeDisplayText(value string) string {
	lines := strings.Split(value, "\n")
	cleaned := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(stripLeadingBracketedClauses(strings.TrimSpace(line)))
		if line == "" || isBracketedClause(line) {
			continue
		}
		cleaned = append(cleaned, line)
	}
	return strings.TrimSpace(strings.Join(cleaned, "\n"))
}

func stripLeadingBracketedClauses(value string) string {
	for {
		rest, ok := stripOneLeadingBracketedClause(value)
		if !ok {
			return value
		}
		value = strings.TrimLeft(rest, " \t")
	}
}

func stripOneLeadingBracketedClause(value string) (string, bool) {
	open, size := utf8.DecodeRuneInString(value)
	if open == utf8.RuneError && size == 0 {
		return "", false
	}
	close, ok := matchingCloseBracket(open)
	if !ok {
		return "", false
	}
	for index, character := range value[size:] {
		if character == close {
			end := size + index + utf8.RuneLen(character)
			return value[end:], true
		}
	}
	return "", false
}

func isBracketedClause(value string) bool {
	open, _ := utf8.DecodeRuneInString(value)
	close, ok := matchingCloseBracket(open)
	return ok && strings.HasSuffix(value, string(close))
}

func matchingCloseBracket(open rune) (rune, bool) {
	switch open {
	case '（':
		return '）', true
	case '(':
		return ')', true
	case '【':
		return '】', true
	case '[':
		return ']', true
	default:
		return 0, false
	}
}

func ValidateAvailableVisualStates(states []VisualState) error {
	if len(states) == 0 || len(states) > 16 {
		return errors.New("available visual states must contain 1-16 states")
	}
	seen := make(map[string]struct{}, len(states))
	hasIdle := false
	for _, state := range states {
		if !validVisualStateID(state.ID) {
			return fmt.Errorf("available visual state %q is invalid", state.ID)
		}
		if _, exists := seen[state.ID]; exists {
			return errors.New("available visual states contain duplicate state")
		}
		seen[state.ID] = struct{}{}
		if state.Description == "" || len([]rune(state.Description)) > 96 || strings.TrimSpace(state.Description) != state.Description || containsDisallowedControl(state.Description) {
			return fmt.Errorf("available visual state %q description is invalid", state.ID)
		}
		if state.ID == "idle" {
			hasIdle = true
		}
	}
	if !hasIdle {
		return errors.New("available visual states must contain idle")
	}
	return nil
}

func validateDraft(draft string) error {
	if draft == "" {
		return errors.New("model did not return usable reply text")
	}
	if containsDisallowedControl(draft) {
		return errors.New("model reply contains disallowed control characters")
	}
	return nil
}

func hasVisualState(states []VisualState, id string) bool {
	for _, state := range states {
		if state.ID == id {
			return true
		}
	}
	return false
}

func validVisualStateID(value string) bool {
	if value == "" || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}

func containsDisallowedControl(value string) bool {
	for _, character := range value {
		if character == 0 || character < 32 && character != '\n' && character != '\r' && character != '\t' {
			return true
		}
	}
	return false
}
