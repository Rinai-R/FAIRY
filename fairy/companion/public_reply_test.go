package companion

import (
	"errors"
	"strings"
	"testing"
)

func TestCompileReplyForInteractionEnforcesOnlyPublicIdentityBoundary(t *testing.T) {
	draft := testRespondEnvelope(testReplyChain{VisualState: "happy", Text: "哼哼，我可是高性能机器人！"})
	states := visualStates("idle", "happy")
	if _, err := compileReplyForInteraction(draft, states, publicAmbientResolved(), nil); err == nil {
		t.Fatal("compileReplyForInteraction() public error = nil, want identity boundary error")
	}
	if _, err := compileReplyForInteraction(draft, states, desktopResolved(), nil); err != nil {
		t.Fatalf("compileReplyForInteraction() private error = %v", err)
	}
}

func TestValidateReplyForInteractionRejectsPublicMachineSelfIdentity(t *testing.T) {
	tests := []string{
		"虽然我是机器人，但今晚也算个倒霉机器人吧！",
		"作为高性能机器人，我可以自由切换模式哦。",
		"我的判断模块觉得对方在回避问题。",
		"高性能可不是白叫的！",
		"这个比喻我要记到核心存储器里。",
		"我不能把说过的话回收进数据库。",
		"高性能发呆模式启动。",
		"I'm an AI assistant, so I can help.",
		"私は高性能ロボットです。",
	}
	for _, text := range tests {
		reply := CompiledReply{Chains: []ReplyChain{{Text: text, VisualState: "idle"}}}
		if err := validateReplyForInteraction(reply, publicAmbientResolved(), nil); err == nil {
			t.Fatalf("validateReplyForInteraction(%q) error = nil, want identity boundary error", text)
		}
	}
}

func TestValidateReplyForInteractionAllowsPublicTopicDiscussion(t *testing.T) {
	tests := []string{
		"现在的 AI 模型训练成本又涨了。",
		"我觉得那个机器人设计得挺可爱的。",
		"群聊协作果然高性能。",
		"你电脑内存是不是不够了？",
	}
	for _, text := range tests {
		reply := CompiledReply{Chains: []ReplyChain{{Text: text, VisualState: "idle"}}}
		if err := validateReplyForInteraction(reply, publicAmbientResolved(), nil); err != nil {
			t.Fatalf("validateReplyForInteraction(%q) error = %v", text, err)
		}
	}
}

func TestValidateReplyForInteractionPreservesPrivateCharacterIdentity(t *testing.T) {
	reply := CompiledReply{Chains: []ReplyChain{{Text: "哼哼，我可是高性能机器人！", VisualState: "happy"}}}
	if err := validateReplyForInteraction(reply, desktopResolved(), nil); err != nil {
		t.Fatalf("validateReplyForInteraction() private error = %v", err)
	}
}

func TestPublicReplyShapeContract(t *testing.T) {
	tests := []struct {
		name  string
		mode  string
		count int
		valid bool
	}{
		{name: "brief one", mode: "brief", count: 1, valid: true},
		{name: "brief two", mode: "brief", count: 2, valid: false},
		{name: "normal three", mode: "normal", count: 3, valid: true},
		{name: "normal four", mode: "normal", count: 4, valid: false},
		{name: "expanded five", mode: "expanded", count: 5, valid: true},
		{name: "expanded six", mode: "expanded", count: 6, valid: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chains := make([]ReplyChain, 0, tt.count)
			for i := 0; i < tt.count; i++ {
				chains = append(chains, ReplyChain{VisualState: "idle", Text: "接一句"})
			}
			err := validateReplyForInteraction(CompiledReply{Chains: chains}, publicAmbientResolved(), &ReplyIntent{ReplyMode: tt.mode})
			if (err == nil) != tt.valid {
				t.Fatalf("validateReplyForInteraction() error = %v, valid = %v", err, tt.valid)
			}
		})
	}
}

func TestPrivateReplyDoesNotApplyPublicShapeContract(t *testing.T) {
	chains := make([]ReplyChain, 0, 6)
	for i := 0; i < 6; i++ {
		chains = append(chains, ReplyChain{VisualState: "idle", Text: "接一句"})
	}
	if err := validateReplyForInteraction(CompiledReply{Chains: chains}, desktopResolved(), &ReplyIntent{ReplyMode: "brief"}); err != nil {
		t.Fatalf("private reply unexpectedly applied public shape: %v", err)
	}
}

func TestReplyCompileRetryCorrectionIncludesPublicShape(t *testing.T) {
	err := &publicReplyShapeError{mode: "brief", actual: 2, want: publicReplyShape{minChains: 1, maxChains: 1}}
	correction := replyCompileRetryCorrection(err)
	if !strings.Contains(correction, `replyMode "brief"`) || !strings.Contains(correction, "1-1 chains") || !strings.Contains(correction, "one conversational hook") {
		t.Fatalf("correction = %q", correction)
	}
}

func TestAllowReplyPreviewForInteractionKeepsUncompiledDraftPrivate(t *testing.T) {
	if allowReplyPreviewForInteraction(publicAmbientResolved()) {
		t.Fatal("public interaction allowed uncompiled reply preview")
	}
	if !allowReplyPreviewForInteraction(desktopResolved()) {
		t.Fatal("private interaction lost reply preview")
	}
}

func TestReplyCompileRetryCorrectionDistinguishesIdentityBoundary(t *testing.T) {
	identity := replyCompileRetryCorrection(errPublicPeerIdentity)
	if !strings.Contains(identity, "public peer identity") || !strings.Contains(identity, "strict reply JSON") {
		t.Fatalf("identity correction = %q", identity)
	}
	protocol := replyCompileRetryCorrection(errors.New("bad JSON"))
	if strings.Contains(protocol, "public peer identity") || !strings.Contains(protocol, "strict reply protocol") {
		t.Fatalf("protocol correction = %q", protocol)
	}
}
