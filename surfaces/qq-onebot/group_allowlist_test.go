package main

import (
	"context"
	"encoding/json"
	"errors"
	"fairy/transport/session"
	"reflect"
	"testing"
	"time"

	zero "github.com/wdvxdr1123/ZeroBot"
	"github.com/wdvxdr1123/ZeroBot/message"
)

type fakeCoreConfigReader struct {
	raw     json.RawMessage
	err     error
	section string
}

func (reader *fakeCoreConfigReader) GetConfig(context.Context, string) (json.RawMessage, error) {
	return reader.raw, reader.err
}

func TestCoreGroupAuthorizerUsesCurrentStrictAllowlist(t *testing.T) {
	reader := &fakeCoreConfigReader{}
	authorizer := coreGroupAuthorizer{config: reader}

	reader.raw = json.RawMessage(`{"schemaVersion":1,"groupAllowlist":[]}`)
	allowed, err := authorizer.GroupAllowed(t.Context(), 20001)
	if err != nil || allowed {
		t.Fatalf("empty allowlist allowed=%v err=%v", allowed, err)
	}

	reader.raw = json.RawMessage(`{"schemaVersion":1,"groupAllowlist":["20001","20002"]}`)
	allowed, err = authorizer.GroupAllowed(t.Context(), 20001)
	if err != nil || !allowed {
		t.Fatalf("matching allowlist allowed=%v err=%v", allowed, err)
	}

	for _, raw := range []string{
		`{"schemaVersion":1}`,
		`{"schemaVersion":2,"groupAllowlist":[]}`,
		`{"schemaVersion":1,"groupAllowlist":["020001"]}`,
		`not-json`,
	} {
		reader.raw = json.RawMessage(raw)
		if allowed, err = authorizer.GroupAllowed(t.Context(), 20001); err == nil || allowed {
			t.Fatalf("invalid response %q allowed=%v err=%v", raw, allowed, err)
		}
	}

	reader.err = errors.New("Core unavailable")
	if allowed, err = authorizer.GroupAllowed(t.Context(), 20001); err == nil || allowed {
		t.Fatalf("reader failure allowed=%v err=%v", allowed, err)
	}
}

type mutableGroupAuthorizer struct {
	allowed map[int64]bool
	err     error
	calls   []int64
}

func (authorizer *mutableGroupAuthorizer) GroupAllowed(_ context.Context, groupID int64) (bool, error) {
	authorizer.calls = append(authorizer.calls, groupID)
	return authorizer.allowed[groupID], authorizer.err
}

func TestBotReadsCurrentAllowlistBeforeAnySessionWork(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	socket := &fakeSessionSocket{
		openResponse: session.OpenSessionResponse{
			ConversationID: "conversation-1",
			CharacterID:    "character-1",
			Endpoint:       session.EndpointIM,
		},
		stream: make(chan session.TurnEvent),
	}
	authorizer := &mutableGroupAuthorizer{allowed: map[int64]bool{}}
	bot, err := newBot(ctx, socket, fakeStickerReader{}, authorizer)
	if err != nil {
		t.Fatal(err)
	}

	bot.handle(groupMessageContext(20001, 1, "初始拒绝"))
	if socket.openCalls != 0 || socket.observeCalls != 0 {
		t.Fatalf("empty allowlist reached session: opens=%d observes=%d", socket.openCalls, socket.observeCalls)
	}

	authorizer.allowed[20001] = true
	bot.handle(groupMessageContext(20001, 2, "新增后允许"))
	if socket.openCalls != 1 || socket.observeCalls != 1 {
		t.Fatalf("added group calls: opens=%d observes=%d", socket.openCalls, socket.observeCalls)
	}

	delete(authorizer.allowed, 20001)
	bot.handle(groupMessageContext(20001, 3, "删除后拒绝"))
	if socket.openCalls != 1 || socket.observeCalls != 1 {
		t.Fatalf("removed group reached session: opens=%d observes=%d", socket.openCalls, socket.observeCalls)
	}

	authorizer.err = errors.New("config unavailable")
	bot.handle(groupMessageContext(20001, 4, "读取失败拒绝"))
	if socket.openCalls != 1 || socket.observeCalls != 1 {
		t.Fatalf("failed authorization reached session: opens=%d observes=%d", socket.openCalls, socket.observeCalls)
	}
	if !reflect.DeepEqual(authorizer.calls, []int64{20001, 20001, 20001, 20001}) {
		t.Fatalf("authorization calls = %#v", authorizer.calls)
	}
}

func groupMessageContext(groupID, messageID int64, text string) *zero.Ctx {
	return &zero.Ctx{Event: &zero.Event{
		Time:      time.Now().Unix(),
		GroupID:   groupID,
		UserID:    40001,
		MessageID: messageID,
		Message:   message.Message{message.Text(text)},
		Sender:    &zero.User{ID: 40001, NickName: "测试成员"},
	}}
}
