package companion

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

const (
	maxReplyChains = 12
	// maxSpeechChars is a SOFT warning threshold, not a hard cap. A chain's speech
	// is always synthesized as one request (stable timbre per semantic unit); when
	// it exceeds this many runes the runtime only logs a warning. 20 runes is the
	// soft target, 40 is the tolerance before warning.
	maxSpeechChars = 40
)

// speechExceedsSoftLimit reports whether a speech line is longer than the soft
// warning threshold. It never rejects — callers only log a warning.
func speechExceedsSoftLimit(value string) bool {
	return len([]rune(value)) > maxSpeechChars
}

type VisualState struct {
	ID          string `json:"id"`
	Description string `json:"description"`
}

type CompiledReply struct {
	DisplayText string       `json:"displayText"`
	SpeechText  string       `json:"speechText"`
	VisualState string       `json:"visualState"`
	Chains      []ReplyChain `json:"chains"`
}

func CompileReply(draft string, availableVisualStates []VisualState) (CompiledReply, error) {
	if err := validateAvailableVisualStates(availableVisualStates); err != nil {
		return CompiledReply{}, err
	}
	if err := validateDraft(draft); err != nil {
		return CompiledReply{}, err
	}
	return compileJSONReplyChains(draft, availableVisualStates)
}

type jsonReplyChains struct {
	Chains []jsonReplyChain `json:"chains"`
}

type jsonReplyChain struct {
	VisualState string `json:"visualState"`
	Text        string `json:"text"`
}

func compileJSONReplyChains(draft string, availableVisualStates []VisualState) (CompiledReply, error) {
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
	if len(parsed.Chains) == 0 || len(parsed.Chains) > maxReplyChains {
		return CompiledReply{}, fmt.Errorf("model reply chains count must be 1-%d", maxReplyChains)
	}
	chains := make([]ReplyChain, 0, len(parsed.Chains))
	for _, chain := range parsed.Chains {
		compiled, err := compileChain(chain.VisualState, chain.Text, availableVisualStates)
		if err != nil {
			return CompiledReply{}, err
		}
		chains = append(chains, compiled)
	}
	return compiledReplyFromChains(chains)
}

func compiledReplyFromChains(chains []ReplyChain) (CompiledReply, error) {
	if err := ValidateReplyChains(chains); err != nil {
		return CompiledReply{}, err
	}
	parts := make([]string, 0, len(chains))
	speechParts := make([]string, 0, len(chains))
	for _, chain := range chains {
		parts = append(parts, chain.Text)
		if chain.SpeechText != "" {
			speechParts = append(speechParts, chain.SpeechText)
		}
	}
	return CompiledReply{
		DisplayText: strings.Join(parts, "\n"),
		SpeechText:  strings.Join(speechParts, " "),
		VisualState: chains[len(chains)-1].VisualState,
		Chains:      chains,
	}, nil
}

func compileChain(visualState string, rawText string, availableVisualStates []VisualState) (ReplyChain, error) {
	if !hasVisualState(availableVisualStates, visualState) {
		return ReplyChain{}, errors.New("model reply returned undeclared visual state")
	}
	display := sanitizeDisplayText(rawText)
	if display == "" {
		return ReplyChain{}, errors.New("model did not return usable reply text")
	}
	return ReplyChain{Text: display, SpeechText: "", VisualState: visualState}, nil
}

func sanitizeSpeechText(value string) string {
	cleaned := sanitizeDisplayText(value)
	if cleaned == "" {
		return ""
	}
	// Keep the full speakable line. Do not truncate to the first sentence —
	// companion replies are often multi-clause and TTS should match speechText.
	collapsed := strings.Join(strings.FieldsFunc(cleaned, func(r rune) bool {
		return r == '\n' || r == '\r'
	}), " ")
	return strings.TrimSpace(collapsed)
}

func sanitizeDisplayText(value string) string {
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

func validateSpeech(value string) error {
	if value == "" {
		return errors.New("model reply chain speechText is required")
	}
	// Length is NOT validated here: an over-length line is still a single valid
	// TTS unit. Callers warn on speechExceedsSoftLimit but never reject.
	if strings.ContainsAny(value, "\r\n") {
		return errors.New("speech text must not contain line breaks")
	}
	lower := strings.ToLower(value)
	if strings.Contains(lower, "https://") || strings.Contains(lower, "http://") || strings.Contains(lower, "www.") {
		return errors.New("speech text must not contain URL")
	}
	if strings.Contains(value, "`") || strings.HasPrefix(value, "#") || strings.HasPrefix(value, "- ") || strings.HasPrefix(value, "*") || strings.HasPrefix(value, "> ") {
		return errors.New("speech text must not contain Markdown or list markers")
	}
	if strings.HasPrefix(value, "{") || strings.HasPrefix(value, "[") {
		return errors.New("speech text must not contain JSON artifacts")
	}
	return nil
}

func validateAvailableVisualStates(states []VisualState) error {
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

func fillSameLanguageSpeech(reply CompiledReply) (CompiledReply, error) {
	chains := make([]ReplyChain, len(reply.Chains))
	copy(chains, reply.Chains)
	for index := range chains {
		candidate := sanitizeSpeechText(chains[index].Text)
		if candidate == "" || validateSpeech(candidate) != nil {
			chains[index].SpeechText = ""
			continue
		}
		chains[index].SpeechText = candidate
	}
	return compiledReplyFromChains(chains)
}

func applyTranslatedSpeech(reply CompiledReply, rawSpeech string) (CompiledReply, error) {
	speech := sanitizeSpeechText(rawSpeech)
	if speech == "" || validateSpeech(speech) != nil {
		return reply, errors.New("translated speech text is unusable")
	}
	chains := make([]ReplyChain, len(reply.Chains))
	copy(chains, reply.Chains)
	for index := range chains {
		chains[index].SpeechText = speech
	}
	compiled, err := compiledReplyFromChains(chains)
	if err != nil {
		return reply, err
	}
	compiled.SpeechText = speech
	return compiled, nil
}
