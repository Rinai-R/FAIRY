package companion

import "testing"

func visualStates(ids ...string) []VisualState {
	states := make([]VisualState, 0, len(ids))
	for _, id := range ids {
		states = append(states, VisualState{ID: id, Description: id + " 状态说明"})
	}
	return states
}

func TestCompileReplyStripsLeadingActionBrackets(t *testing.T) {
	reply, err := CompileReply("VISUAL_STATE: idle\n（轻轻歪头）哎呀，你先休息一会儿吧。后面不该显示。", visualStates("idle"))
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
	reply, err := CompileReply("VISUAL_STATE: idle\n先检查网络。\n（轻轻点头）\n然后确认配置（不要猜测）。", visualStates("idle"))
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
	reply, err := CompileReply(`{"chains":[{"visualState":"thinking","text":"嗯，我懂。"},{"visualState":"happy","text":"先这样改。"}]}`, visualStates("idle", "thinking", "happy"))
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

func TestCompileReplyFallsBackToIdleWhenHeaderMissing(t *testing.T) {
	reply, err := CompileReply("（开心地蹦跳）在的在的！", visualStates("idle", "happy"))
	if err != nil {
		t.Fatalf("CompileReply() error = %v", err)
	}
	if reply.VisualState != "idle" || reply.DisplayText != "在的在的！" {
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
		{name: "emoji", draft: "VISUAL_STATE: idle\n我在🙂。", states: visualStates("idle")},
		{name: "url speech", draft: "VISUAL_STATE: idle\n看看 https://example.test。", states: visualStates("idle")},
		{name: "pure bracketed action", draft: "VISUAL_STATE: idle\n（安静地看着你）", states: visualStates("idle")},
		{name: "undeclared state", draft: "VISUAL_STATE: angry\n我在。", states: visualStates("idle", "happy")},
		{name: "bad visual state", draft: "VISUAL_STATE: Bad\n我在。", states: visualStates("idle")},
		{name: "empty chains", draft: `{"chains":[]}`, states: visualStates("idle")},
		{name: "missing idle", draft: "VISUAL_STATE: idle\n我在。", states: []VisualState{{ID: "happy", Description: "happy 状态说明"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := CompileReply(tt.draft, tt.states); err == nil {
				t.Fatal("CompileReply() error = nil, want error")
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
