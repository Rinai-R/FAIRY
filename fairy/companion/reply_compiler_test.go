package companion

import (
	"encoding/json"
	"strings"
	"testing"
)

type testReplyChain struct {
	VisualState string `json:"visualState"`
	Text        string `json:"text"`
}

func testRespondEnvelope(chains ...testReplyChain) string {
	payload := struct {
		Chains []testReplyChain `json:"chains"`
	}{
		Chains: chains,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

func visualStates(ids ...string) []VisualState {
	states := make([]VisualState, 0, len(ids))
	for _, id := range ids {
		states = append(states, VisualState{ID: id, Description: id + " 状态说明"})
	}
	return states
}

func TestCompileReplyStripsLeadingActionBrackets(t *testing.T) {
	reply, err := CompileReply(testRespondEnvelope(testReplyChain{
		VisualState: "idle",
		Text:        "（轻轻歪头）你先休息一下吧，我在这里。",
	}), visualStates("idle"))
	if err != nil {
		t.Fatalf("CompileReply() error = %v", err)
	}
	if reply.DisplayText != "你先休息一下吧，我在这里。" {
		t.Fatalf("DisplayText = %q", reply.DisplayText)
	}
	if reply.SpeechText != "" {
		t.Fatalf("SpeechText = %q, want empty before translate fill", reply.SpeechText)
	}
}

func TestCompileReplyKeepsInlineBracketsAndRemovesStandaloneActions(t *testing.T) {
	reply, err := CompileReply(testRespondEnvelope(testReplyChain{
		VisualState: "idle",
		Text:        "先检查网络。\n（轻轻点头）\n然后确认配置（不要猜测）。",
	}), visualStates("idle"))
	if err != nil {
		t.Fatalf("CompileReply() error = %v", err)
	}
	if reply.DisplayText != "先检查网络。\n然后确认配置（不要猜测）。" {
		t.Fatalf("DisplayText = %q", reply.DisplayText)
	}
}

func TestCompileReplyCompilesJSONChains(t *testing.T) {
	reply, err := CompileReply(testRespondEnvelope(
		testReplyChain{VisualState: "thinking", Text: "嗯，我懂。"},
		testReplyChain{VisualState: "happy", Text: "先这样改。"},
	), visualStates("idle", "thinking", "happy"))
	if err != nil {
		t.Fatalf("CompileReply() error = %v", err)
	}
	if len(reply.Chains) != 2 {
		t.Fatalf("chains = %#v", reply.Chains)
	}
	if reply.DisplayText != "嗯，我懂。\n先这样改。" || reply.SpeechText != "" || reply.VisualState != "happy" {
		t.Fatalf("reply = %#v", reply)
	}
}

func TestCompileReplyRejectsInvalidOutput(t *testing.T) {
	tests := []struct {
		name   string
		draft  string
		states []VisualState
	}{
		{name: "empty", draft: "", states: visualStates("idle")},
		{name: "pure bracketed action", draft: testRespondEnvelope(testReplyChain{VisualState: "idle", Text: "（安静地看着你）"}), states: visualStates("idle")},
		{name: "undeclared state", draft: testRespondEnvelope(testReplyChain{VisualState: "angry", Text: "我在。"}), states: visualStates("idle", "happy")},
		{name: "bad visual state", draft: testRespondEnvelope(testReplyChain{VisualState: "Bad", Text: "我在。"}), states: visualStates("idle")},
		{name: "empty chains", draft: testRespondEnvelope(), states: visualStates("idle")},
		{name: "missing idle", draft: testRespondEnvelope(testReplyChain{VisualState: "idle", Text: "我在。"}), states: []VisualState{{ID: "happy", Description: "happy 状态说明"}}},
		{name: "speechText unknown field", draft: `{"chains":[{"visualState":"idle","text":"我在。","speechText":"いるよ。"}]}`, states: visualStates("idle")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := CompileReply(tt.draft, tt.states); err == nil {
				t.Fatal("CompileReply() error = nil, want error")
			}
		})
	}
}

func TestCompileReplyAllowsEmojiWithoutRetryTax(t *testing.T) {
	reply, err := CompileReply(testRespondEnvelope(testReplyChain{VisualState: "idle", Text: "摸摸，辛苦了🙂"}), visualStates("idle"))
	if err != nil {
		t.Fatal(err)
	}
	if reply.DisplayText != "摸摸，辛苦了🙂" {
		t.Fatalf("reply = %#v", reply)
	}
}

func TestCompileReplyRejectsUnknownTrailingAndLegacyOutput(t *testing.T) {
	validChains := `[{"visualState":"idle","text":"我在。"}]`
	valid := `{"chains":` + validChains + `}`
	tests := []struct {
		name       string
		draft      string
		wantErrSub string
	}{
		{name: "missing chains", draft: `{}`, wantErrSub: "chains count"},
		{name: "unknown decision field", draft: `{"decision":{"stance":"先回应"},"chains":` + validChains + `}`, wantErrSub: "strict reply chains JSON"},
		{name: "unknown top level field", draft: `{"chains":` + validChains + `,"reasoning":"no"}`, wantErrSub: "strict reply chains JSON"},
		{name: "unknown chain field", draft: `{"chains":[{"visualState":"idle","text":"我在。","gesture":"wave"}]}`, wantErrSub: "strict reply chains JSON"},
		{name: "second json value", draft: valid + `{}`, wantErrSub: "exactly one JSON object"},
		{name: "trailing text", draft: valid + ` trailing`, wantErrSub: "exactly one JSON object"},
		{name: "legacy visual header", draft: "VISUAL_STATE: idle\n我在。", wantErrSub: "strict reply chains JSON"},
		{name: "legacy plain text", draft: "我在。", wantErrSub: "strict reply chains JSON"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := CompileReply(tt.draft, visualStates("idle"))
			if err == nil {
				t.Fatalf("CompileReply(%q) error = nil, want error", tt.draft)
			}
			if !strings.Contains(err.Error(), tt.wantErrSub) {
				t.Fatalf("CompileReply() error = %q, want substring %q", err.Error(), tt.wantErrSub)
			}
		})
	}
}

func TestFillSameLanguageSpeechCopiesDisplay(t *testing.T) {
	reply, err := CompileReply(testRespondEnvelope(testReplyChain{VisualState: "idle", Text: "我在。"}), visualStates("idle"))
	if err != nil {
		t.Fatalf("CompileReply() error = %v", err)
	}
	filled, err := fillSameLanguageSpeech(reply)
	if err != nil {
		t.Fatalf("fillSameLanguageSpeech() error = %v", err)
	}
	if filled.SpeechText != "我在。" {
		t.Fatalf("SpeechText = %q", filled.SpeechText)
	}
}

func TestApplyTranslatedSpeechSetsAllChains(t *testing.T) {
	reply, err := CompileReply(testRespondEnvelope(
		testReplyChain{VisualState: "thinking", Text: "嗯，我懂。"},
		testReplyChain{VisualState: "happy", Text: "先这样改。"},
	), visualStates("idle", "thinking", "happy"))
	if err != nil {
		t.Fatalf("CompileReply() error = %v", err)
	}
	filled, err := applyTranslatedSpeech(reply, "うん、わかった。まずこう直そう。")
	if err != nil {
		t.Fatalf("applyTranslatedSpeech() error = %v", err)
	}
	if filled.SpeechText != "うん、わかった。まずこう直そう。" {
		t.Fatalf("SpeechText = %q", filled.SpeechText)
	}
	for _, chain := range filled.Chains {
		if chain.SpeechText != "うん、わかった。まずこう直そう。" {
			t.Fatalf("chain speech = %#v", chain)
		}
	}
}

func TestSanitizeSpeechTextKeepsMultiClauseUnderLimit(t *testing.T) {
	raw := "うん、わかったよ。まずこう直そう。"
	got := sanitizeSpeechText(raw)
	if got != raw {
		t.Fatalf("sanitizeSpeechText truncated: %q", got)
	}
	if err := validateSpeech(got); err != nil {
		t.Fatalf("validateSpeech() error = %v", err)
	}
	// Length is a SOFT limit now: overlong speech is still a valid single TTS
	// unit (stable timbre). validateSpeech must NOT reject it; only the soft-limit
	// helper flags it so callers can warn.
	long := strings.Repeat("あ", maxSpeechChars+1)
	if err := validateSpeech(long); err != nil {
		t.Fatalf("validateSpeech() error = %v for overlong speech, want nil (soft limit)", err)
	}
	if !speechExceedsSoftLimit(long) {
		t.Fatal("speechExceedsSoftLimit() = false for overlong speech, want true")
	}
	if speechExceedsSoftLimit(strings.Repeat("あ", maxSpeechChars)) {
		t.Fatal("speechExceedsSoftLimit() = true at threshold, want false")
	}
}

func TestValidateAvailableVisualStatesRejectsInvalidStateList(t *testing.T) {
	tests := []struct {
		name   string
		states []VisualState
	}{
		{name: "empty", states: nil},
		{name: "duplicate", states: []VisualState{{ID: "idle", Description: "idle 状态说明"}, {ID: "idle", Description: "idle 状态说明"}}},
		{name: "bad id", states: []VisualState{{ID: "idle", Description: "idle 状态说明"}, {ID: "Bad", Description: "bad 状态说明"}}},
		{name: "blank description", states: []VisualState{{ID: "idle", Description: ""}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := validateAvailableVisualStates(tt.states); err == nil {
				t.Fatal("validateAvailableVisualStates() error = nil, want error")
			}
		})
	}
}
