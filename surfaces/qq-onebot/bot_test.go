package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"fairy-surfaces/turnclient"
	"fairy/coreclient"
)

type fakeSessions struct {
	request              coreclient.OpenSessionRequest
	participationRequest coreclient.GroupParticipationRequest
	participation        coreclient.GroupParticipationResponse
	sessionErr           error
	participationErr     error
}

func (f *fakeSessions) OpenSession(_ context.Context, request coreclient.OpenSessionRequest) (coreclient.OpenSessionResponse, error) {
	f.request = request
	return coreclient.OpenSessionResponse{ConversationID: "conversation-1", Surface: "im_group"}, f.sessionErr
}

func (f *fakeSessions) DecideGroupParticipation(_ context.Context, _ string, request coreclient.GroupParticipationRequest) (coreclient.GroupParticipationResponse, error) {
	f.participationRequest = request
	return f.participation, f.participationErr
}

type fakeTurns struct {
	request turnclient.Request
	events  []turnclient.Event
	err     error
}

func (f *fakeTurns) Run(_ context.Context, request turnclient.Request, callback turnclient.Callback) (turnclient.Result, error) {
	f.request = request
	for _, event := range f.events {
		if err := callback(event); err != nil {
			return turnclient.Result{}, err
		}
	}
	return turnclient.Result{}, f.err
}

func TestGroupWindowCallbacksDecideAndReplyWithFinalBeats(t *testing.T) {
	target := "2"
	core := &fakeSessions{participation: coreclient.GroupParticipationResponse{Action: "reply", TargetMessageID: &target}}
	turns := &fakeTurns{events: []turnclient.Event{
		{Beat: &turnclient.BeatReady{Kind: "utterance", DisplayText: "skip"}},
		{Beat: &turnclient.BeatReady{Kind: "final", DisplayText: "第一拍"}},
		{Beat: &turnclient.BeatReady{Kind: "final", DisplayText: "第二拍"}},
	}}
	b := &bot{core: core, turns: turns}
	var sent []string
	messages := []coreclient.GroupObservation{
		{MessageID: "1", SenderID: "40001", SenderName: "甲", Text: "你好", TimestampUnixMS: 1000},
		{MessageID: "2", SenderID: "40002", SenderName: "乙", Text: "在吗", TimestampUnixMS: 2000},
	}
	batch := groupWindowBatch{groupID: 20001, evaluationReason: "message", messages: messages, send: func(text string) error {
		sent = append(sent, text)
		return nil
	}}
	decision, err := b.decideGroupParticipation(t.Context(), batch)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.replyToGroupWindow(t.Context(), batch, decision); err != nil {
		t.Fatal(err)
	}
	if core.request.Surface != "im_group" || core.request.SurfaceKey != "onebot-group:20001" {
		t.Fatalf("session request = %#v", core.request)
	}
	if core.participationRequest.EvaluationReason != "message" || len(core.participationRequest.Messages) != 2 {
		t.Fatalf("participation request = %#v", core.participationRequest)
	}
	if turns.request.Input != "[甲/40001] 你好\n[reply-target][乙/40002] 在吗" || turns.request.Surface != "im_group" {
		t.Fatalf("turn request = %#v", turns.request)
	}
	if strings.Join(sent, ",") != "第一拍,第二拍" {
		t.Fatalf("sent = %#v", sent)
	}
}

func TestFormatGroupTurnInputRequiresExactlyOneTarget(t *testing.T) {
	messages := []coreclient.GroupObservation{{MessageID: "1", SenderID: "2", SenderName: "甲", Text: "你好"}}
	if _, err := formatGroupTurnInput(messages, "missing"); err == nil {
		t.Fatal("missing target accepted")
	}
	messages = append(messages, messages[0])
	if _, err := formatGroupTurnInput(messages, "1"); err == nil {
		t.Fatal("duplicate target accepted")
	}
}

func TestGroupParticipationSilentCreatesNoTurnOrAction(t *testing.T) {
	core := &fakeSessions{participation: coreclient.GroupParticipationResponse{Action: "silent"}}
	turns := &fakeTurns{}
	b := &bot{core: core, turns: turns}
	sends := 0
	decision, err := b.decideGroupParticipation(t.Context(), groupWindowBatch{groupID: 20001, evaluationReason: "message", messages: []coreclient.GroupObservation{{MessageID: "1", SenderID: "2", SenderName: "甲", Text: "聊完了", TimestampUnixMS: 1000}}, send: func(string) error {
		sends++
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Action != "silent" || turns.request.ConversationID != "" || sends != 0 {
		t.Fatalf("silent started turn=%#v sends=%d", turns.request, sends)
	}
}

func TestGroupWindowCallbacksReturnCoreAndActionFailuresWithoutFallback(t *testing.T) {
	target := "1"
	core := &fakeSessions{participation: coreclient.GroupParticipationResponse{Action: "reply", TargetMessageID: &target}, sessionErr: errors.New("core unavailable")}
	b := &bot{core: core, turns: &fakeTurns{}}
	sends := 0
	batch := groupWindowBatch{groupID: 20001, messages: []coreclient.GroupObservation{{MessageID: "1", SenderID: "2", SenderName: "甲", Text: "你好", TimestampUnixMS: 1000}}, send: func(string) error { sends++; return nil }}
	_, err := b.decideGroupParticipation(t.Context(), batch)
	if err == nil || !strings.Contains(err.Error(), "core unavailable") || sends != 0 {
		t.Fatalf("core error = %v sends=%d", err, sends)
	}

	core.sessionErr = nil
	b.turns = &fakeTurns{events: []turnclient.Event{{Beat: &turnclient.BeatReady{Kind: "final", DisplayText: "真实回复"}}}}
	batch.send = func(string) error {
		sends++
		return errors.New("action failed")
	}
	decision, err := b.decideGroupParticipation(t.Context(), batch)
	if err != nil {
		t.Fatal(err)
	}
	err = b.replyToGroupWindow(t.Context(), batch, decision)
	if err == nil || !strings.Contains(err.Error(), "action failed") || sends != 1 {
		t.Fatalf("action error = %v sends=%d", err, sends)
	}
}

func TestConfigValidationAndExactTokens(t *testing.T) {
	valid := Config{
		CoreEndpoint: "http://127.0.0.1:8787", CoreToken: " core-token ",
		OneBotWebhookEndpoint: "http://127.0.0.1:3002", OneBotAPIEndpoint: "http://127.0.0.1:3001",
		OneBotToken: " onebot-token ", GroupAllowlist: []string{"20001"},
	}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	invalid := []Config{
		{},
		{CoreEndpoint: "http://core.example.com", CoreToken: "x", OneBotWebhookEndpoint: "http://127.0.0.1:2", OneBotAPIEndpoint: "http://127.0.0.1:1", OneBotToken: "x", GroupAllowlist: []string{"2"}},
		{CoreEndpoint: "http://127.0.0.1:1", CoreToken: "x", OneBotWebhookEndpoint: "ws://127.0.0.1:2", OneBotAPIEndpoint: "http://127.0.0.1:1", OneBotToken: "x", GroupAllowlist: []string{"2"}},
		{CoreEndpoint: "http://127.0.0.1:1", CoreToken: "x", OneBotWebhookEndpoint: "http://example.com:2", OneBotAPIEndpoint: "http://127.0.0.1:1", OneBotToken: "x", GroupAllowlist: []string{"2"}},
		{CoreEndpoint: "http://127.0.0.1:1", CoreToken: "x", OneBotWebhookEndpoint: "http://127.0.0.1:2", OneBotAPIEndpoint: "http://127.0.0.1:1", OneBotToken: "x"},
	}
	for i, cfg := range invalid {
		if err := cfg.Validate(); err == nil {
			t.Fatalf("invalid config %d accepted", i)
		}
	}
}

func TestConfigFromEnvPreservesExactTokens(t *testing.T) {
	t.Setenv("FAIRY_CORE_ENDPOINT", "http://127.0.0.1:8787")
	t.Setenv("FAIRY_CORE_TOKEN", " core-token ")
	t.Setenv("FAIRY_ONEBOT_WEBHOOK_ENDPOINT", "http://127.0.0.1:3002")
	t.Setenv("FAIRY_ONEBOT_API_ENDPOINT", "http://127.0.0.1:3001")
	t.Setenv("FAIRY_ONEBOT_TOKEN", " onebot-token ")
	t.Setenv("FAIRY_ONEBOT_GROUP_ALLOWLIST", "20001,20002")
	cfg, err := configFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CoreToken != " core-token " || cfg.OneBotToken != " onebot-token " {
		t.Fatalf("tokens were changed: Core=%q OneBot=%q", cfg.CoreToken, cfg.OneBotToken)
	}
	if len(cfg.GroupAllowlist) != 2 {
		t.Fatalf("allowlist = %#v", cfg.GroupAllowlist)
	}
}
