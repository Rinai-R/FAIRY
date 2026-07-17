package companion

import (
	"encoding/json"
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
		Text:        "（轻轻歪头）哎呀，你先休息一会儿吧。后面不该显示。",
	}), visualStates("idle"))
	if err != nil {
		t.Fatalf("CompileReply() error = %v", err)
	}
	if reply.DisplayText != "哎呀，你先休息一会儿吧。后面不该显示。" {
		t.Fatalf("DisplayText = %q", reply.DisplayText)
	}
	if reply.SpeechText != "哎呀，你先休息一会儿吧。" {
		t.Fatalf("SpeechText = %q", reply.SpeechText)
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
	if reply.SpeechText != "先检查网络。" {
		t.Fatalf("SpeechText = %q", reply.SpeechText)
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
	if reply.DisplayText != "嗯，我懂。\n先这样改。" || reply.SpeechText != "嗯，我懂。" || reply.VisualState != "happy" {
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
		{name: "emoji", draft: testRespondEnvelope(testReplyChain{VisualState: "idle", Text: "我在🙂。"}), states: visualStates("idle")},
		{name: "url speech", draft: testRespondEnvelope(testReplyChain{VisualState: "idle", Text: "看看 https://example.test。"}), states: visualStates("idle")},
		{name: "pure bracketed action", draft: testRespondEnvelope(testReplyChain{VisualState: "idle", Text: "（安静地看着你）"}), states: visualStates("idle")},
		{name: "undeclared state", draft: testRespondEnvelope(testReplyChain{VisualState: "angry", Text: "我在。"}), states: visualStates("idle", "happy")},
		{name: "bad visual state", draft: testRespondEnvelope(testReplyChain{VisualState: "Bad", Text: "我在。"}), states: visualStates("idle")},
		{name: "empty chains", draft: testRespondEnvelope(), states: visualStates("idle")},
		{name: "missing idle", draft: testRespondEnvelope(testReplyChain{VisualState: "idle", Text: "我在。"}), states: []VisualState{{ID: "happy", Description: "happy 状态说明"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := CompileReply(tt.draft, tt.states); err == nil {
				t.Fatal("CompileReply() error = nil, want error")
			}
		})
	}
}

func TestCompileReplyRejectsUnknownTrailingAndLegacyOutput(t *testing.T) {
	validChains := `[{"visualState":"idle","text":"我在。"}]`
	valid := `{"chains":` + validChains + `}`
	tests := []struct {
		name  string
		draft string
	}{
		{name: "missing chains", draft: `{}`},
		{name: "unknown decision field", draft: `{"decision":{"stance":"先回应"},"chains":` + validChains + `}`},
		{name: "unknown top level field", draft: `{"chains":` + validChains + `,"reasoning":"no"}`},
		{name: "unknown chain field", draft: `{"chains":[{"visualState":"idle","text":"我在。","gesture":"wave"}]}`},
		{name: "second json value", draft: valid + `{}`},
		{name: "trailing text", draft: valid + ` trailing`},
		{name: "legacy visual header", draft: "VISUAL_STATE: idle\n我在。"},
		{name: "legacy plain text", draft: "我在。"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := CompileReply(tt.draft, visualStates("idle")); err == nil {
				t.Fatalf("CompileReply(%q) error = nil, want error", tt.draft)
			}
		})
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
