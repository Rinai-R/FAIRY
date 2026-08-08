package main

import (
	"context"
	"encoding/json"
	"errors"
	"fairy/transport/session"
	"sync"
	"testing"
	"time"

	zero "github.com/wdvxdr1123/ZeroBot"
	"github.com/wdvxdr1123/ZeroBot/message"
)

func TestAmbientObservationPreservesOtherUserMentions(t *testing.T) {
	ctx := &zero.Ctx{Event: &zero.Event{
		Time: 1, MessageID: int64(7), UserID: 10001,
		Sender:     &zero.User{Card: "白色季节"},
		RawMessage: "是吗[CQ:at,qq=718249954,name=秋] 快看新同学[CQ:at,qq=718249954,name=秋]",
		Message:    message.Message{message.Text("是吗"), message.Text(" 快看新同学")},
	}}
	observation, err := ambientObservationFromEvent(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if observation.MessageID != "7" || observation.Text != "是吗 @秋 快看新同学 @秋" || observation.DirectedToBot {
		t.Fatalf("observation = %#v", observation)
	}
	if len(observation.Mentions) != 1 || observation.Mentions[0] != (session.MessageMention{UserID: "718249954", DisplayName: "秋"}) {
		t.Fatalf("mentions = %#v", observation.Mentions)
	}
}

func TestAmbientObservationPreservesStringMessageID(t *testing.T) {
	ctx := &zero.Ctx{Event: &zero.Event{
		Time: 1, MessageID: "guild-message-7", UserID: 10001,
		Sender:  &zero.User{NickName: "群友"},
		Message: message.Message{message.Text("你好")},
	}}
	observation, err := ambientObservationFromEvent(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if observation.MessageID != "guild-message-7" {
		t.Fatalf("message id = %q", observation.MessageID)
	}
}

func TestAmbientObservationPreservesDirectedToBotWithoutSelfAtSegment(t *testing.T) {
	ctx := &zero.Ctx{Event: &zero.Event{
		Time: 1, MessageID: int64(8), UserID: 10001, IsToMe: true,
		Sender:     &zero.User{NickName: "群友"},
		RawMessage: "[CQ:at,qq=527338184,name=亚托莉] 你好",
		Message:    message.Message{message.Text("你好")},
	}}
	observation, err := ambientObservationFromEvent(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !observation.DirectedToBot || observation.Text != "@亚托莉 你好" || len(observation.Mentions) != 1 || observation.Mentions[0].DisplayName != "亚托莉" {
		t.Fatalf("observation = %#v", observation)
	}
}

func TestAmbientObservationRejectsInvalidMentionUserID(t *testing.T) {
	ctx := &zero.Ctx{Event: &zero.Event{
		Time: 1, MessageID: int64(9), UserID: 10001,
		Sender:     &zero.User{NickName: "群友"},
		RawMessage: "你好[CQ:at,qq=invalid,name=坏数据]",
		Message:    message.Message{message.Text("你好")},
	}}
	if _, err := ambientObservationFromEvent(ctx); err == nil {
		t.Fatal("invalid mention accepted")
	}
}

func TestAmbientObservationFallsBackToMentionUserIDWhenNameMissing(t *testing.T) {
	ctx := &zero.Ctx{Event: &zero.Event{
		Time: 1, MessageID: int64(10), UserID: 10001,
		Sender:     &zero.User{NickName: "群友"},
		RawMessage: "看这里[CQ:at,qq=718249954]",
		Message:    message.Message{message.Text("看这里")},
	}}
	observation, err := ambientObservationFromEvent(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if observation.Text != "看这里 @718249954" || len(observation.Mentions) != 1 || observation.Mentions[0].DisplayName != "718249954" {
		t.Fatalf("observation = %#v", observation)
	}
}

func TestConsumeTurnEventsDeliversConversationStreamInOrder(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	delivered := make(chan string, 2)
	bot := &bot{
		ctx: ctx,
		senders: map[string]expressionSender{
			"c1": {text: func(text string) error {
				delivered <- text
				return nil
			}},
		},
		conversations: make(map[string]string),
	}
	stream := make(chan session.TurnEvent, 2)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		bot.consumeTurnEvents("c1", stream)
	}()
	for _, text := range []string{"第一拍", "第二拍"} {
		payload, _ := json.Marshal(map[string]any{"type": "beat.ready", "kind": "final", "displayText": text})
		stream <- session.TurnEvent{ConversationID: "c1", Payload: payload}
	}
	for _, want := range []string{"第一拍", "第二拍"} {
		select {
		case got := <-delivered:
			if got != want {
				t.Fatalf("delivered = %q, want %q", got, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for %q", want)
		}
	}
	cancel()
	wg.Wait()
}

func TestFinalExpressionBeatRequiresFinalKind(t *testing.T) {
	payload, _ := json.Marshal(map[string]any{"type": "beat.ready", "kind": "utterance", "displayText": "skip"})
	if _, ok := finalExpressionBeat(session.TurnEvent{Payload: payload}); ok {
		t.Fatal("utterance accepted as final")
	}
	payload, _ = json.Marshal(map[string]any{"type": "beat.ready", "kind": "final", "displayText": "你好"})
	beat, ok := finalExpressionBeat(session.TurnEvent{Payload: payload})
	if !ok || beat.Part.Kind != session.ExpressionUtterance || beat.Part.Text != "你好" {
		t.Fatalf("beat=%#v ok=%v", beat, ok)
	}
}

func TestConsumeTurnEventsDeliversStickerAndReportsSuccess(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	socket := &fakeSessionSocket{reports: make(chan session.ExpressionDeliveryResult, 1)}
	reader := fakeStickerReader{content: session.StickerContent{
		MIMEType: "image/gif",
		Bytes:    []byte("GIF89a-content"),
	}}
	delivered := make(chan []byte, 1)
	bot := &bot{
		ctx:      ctx,
		socket:   socket,
		stickers: reader,
		senders: map[string]expressionSender{
			"c1": {image: func(content []byte) error {
				delivered <- append([]byte(nil), content...)
				return nil
			}},
		},
		conversations: make(map[string]string),
	}
	stream := make(chan session.TurnEvent, 1)
	done := make(chan struct{})
	go func() {
		bot.consumeTurnEvents("c1", stream)
		close(done)
	}()
	stream <- stickerBeatEvent("c1", "t1", "b1", "sticker-1", "image/gif")

	select {
	case got := <-delivered:
		if string(got) != "GIF89a-content" {
			t.Fatalf("image content = %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for image delivery")
	}
	select {
	case report := <-socket.reports:
		if report.Status != session.ExpressionDeliverySucceeded || report.ConversationID != "c1" ||
			report.TurnID != "t1" || report.BeatID != "b1" || report.ErrorMessage != "" {
			t.Fatalf("report = %#v", report)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for delivery report")
	}
	cancel()
	<-done
}

func TestConsumeTurnEventsReportsStickerFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	socket := &fakeSessionSocket{reports: make(chan session.ExpressionDeliveryResult, 1)}
	bot := &bot{
		ctx:      ctx,
		socket:   socket,
		stickers: fakeStickerReader{err: errors.New("content unavailable")},
		senders: map[string]expressionSender{
			"c1": {image: func([]byte) error {
				t.Fatal("image sender called after content failure")
				return nil
			}},
		},
		conversations: make(map[string]string),
	}
	stream := make(chan session.TurnEvent, 1)
	done := make(chan struct{})
	go func() {
		bot.consumeTurnEvents("c1", stream)
		close(done)
	}()
	stream <- stickerBeatEvent("c1", "t1", "b1", "sticker-1", "image/png")

	select {
	case report := <-socket.reports:
		if report.Status != session.ExpressionDeliveryFailed || report.ErrorMessage != "从 Core 读取表情包失败" {
			t.Fatalf("report = %#v", report)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for failed delivery report")
	}
	cancel()
	<-done
}

func TestEnsureConversationDeclaresStickerCapability(t *testing.T) {
	socket := &fakeSessionSocket{
		stream: make(chan session.TurnEvent),
		openResponse: session.OpenSessionResponse{
			ConversationID: "c1",
			CharacterID:    "character-1",
			Endpoint:       session.EndpointIM,
		},
	}
	bot := &bot{
		ctx:           t.Context(),
		socket:        socket,
		stickers:      fakeStickerReader{},
		conversations: make(map[string]string),
		senders:       make(map[string]expressionSender),
	}
	if _, err := bot.ensureConversation(20001, expressionSender{}); err != nil {
		t.Fatal(err)
	}
	if !socket.openRequest.OutputCapabilities.Sticker {
		t.Fatalf("output capabilities = %#v", socket.openRequest.OutputCapabilities)
	}
}

func TestPrivateMessageUsesDirectSessionAndPreservesMessageID(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	socket := &fakeSessionSocket{
		stream: make(chan session.TurnEvent),
		openResponse: session.OpenSessionResponse{
			ConversationID: "private-conversation",
			CharacterID:    "character-1",
			Endpoint:       session.EndpointIM,
		},
	}
	authorizer := &mutableGroupAuthorizer{allowed: map[int64]bool{40001: false}}
	bot, err := newBot(ctx, socket, fakeStickerReader{}, authorizer)
	if err != nil {
		t.Fatal(err)
	}
	private := privateMessageContext(40001, 70001, "你好")
	bot.handlePrivate(private)
	bot.handlePrivate(privateMessageContext(40001, 70002, "还记得我吗"))

	if len(authorizer.calls) != 0 {
		t.Fatalf("private message called group authorizer: %#v", authorizer.calls)
	}
	if socket.openCalls != 1 || socket.submitCalls != 2 {
		t.Fatalf("opens=%d submits=%d", socket.openCalls, socket.submitCalls)
	}
	request := socket.openRequest
	if request.EndpointKey != "onebot-private:40001" || request.Endpoint != session.EndpointIM ||
		request.Interaction.Audience != session.AudienceSingle || request.Interaction.Initiation != session.InitiationDirect ||
		request.Interaction.Presentation != session.PresentationChat || request.Interaction.Principal == nil ||
		request.Interaction.Principal.Namespace != "qq.onebot" || request.Interaction.Principal.Subject != "40001" ||
		!request.OutputCapabilities.Sticker {
		t.Fatalf("private open request = %#v", request)
	}
	if socket.submitID != "private-conversation" || socket.submit.Input != "还记得我吗" || socket.submit.MessageID != "70002" {
		t.Fatalf("private submit = conversation %q, request %#v", socket.submitID, socket.submit)
	}
}

func TestPrivateAndGroupWithSameNumericIDRemainIsolated(t *testing.T) {
	socket := &fakeSessionSocket{stream: make(chan session.TurnEvent)}
	socket.open = func(request session.OpenSessionRequest) (session.OpenSessionResponse, error) {
		return session.OpenSessionResponse{
			ConversationID: request.EndpointKey + "-conversation",
			CharacterID:    "character-1",
			Endpoint:       session.EndpointIM,
		}, nil
	}
	bot := &bot{
		ctx: t.Context(), socket: socket, stickers: fakeStickerReader{},
		conversations: make(map[string]string), senders: make(map[string]expressionSender),
	}
	privateID, err := bot.ensureEndpointConversation("onebot-private:40001", session.Context{
		Audience: session.AudienceSingle, Initiation: session.InitiationDirect, Presentation: session.PresentationChat,
		Principal: &session.PrincipalRef{Namespace: "qq.onebot", Subject: "40001"},
	}, expressionSender{})
	if err != nil {
		t.Fatal(err)
	}
	groupID, err := bot.ensureConversation(40001, expressionSender{})
	if err != nil {
		t.Fatal(err)
	}
	if privateID == groupID || socket.openCalls != 2 || len(bot.conversations) != 2 {
		t.Fatalf("private=%q group=%q opens=%d conversations=%#v", privateID, groupID, socket.openCalls, bot.conversations)
	}
	if socket.openRequests[0].EndpointKey != "onebot-private:40001" || socket.openRequests[1].EndpointKey != "onebot-group:40001" {
		t.Fatalf("open requests = %#v", socket.openRequests)
	}
}

func TestPrivateMessageRejectsIncompleteEventBeforeSessionWork(t *testing.T) {
	socket := &fakeSessionSocket{}
	bot := &bot{ctx: t.Context(), socket: socket, conversations: make(map[string]string), senders: make(map[string]expressionSender)}
	bot.handlePrivate(&zero.Ctx{Event: &zero.Event{UserID: 40001, Time: time.Now().Unix(), Sender: &zero.User{ID: 40001}}})
	if socket.openCalls != 0 || socket.submitCalls != 0 {
		t.Fatalf("invalid private event reached session: opens=%d submits=%d", socket.openCalls, socket.submitCalls)
	}
}

func privateMessageContext(userID, messageID int64, text string) *zero.Ctx {
	return &zero.Ctx{Event: &zero.Event{
		PostType: "message", DetailType: "private", Time: time.Now().Unix(),
		UserID: userID, MessageID: messageID, Message: message.Message{message.Text(text)},
		Sender: &zero.User{ID: userID, NickName: "测试用户"},
	}}
}

func stickerBeatEvent(conversationID, turnID, beatID, stickerID, mimeType string) session.TurnEvent {
	payload, _ := json.Marshal(map[string]any{
		"type":   "beat.ready",
		"kind":   "final",
		"beatId": beatID,
		"part": map[string]any{
			"kind": "sticker",
			"sticker": map[string]any{
				"id": stickerID, "description": "开心", "mimeType": mimeType,
			},
		},
	})
	return session.TurnEvent{
		ConversationID: conversationID,
		TurnID:         turnID,
		Payload:        payload,
	}
}

type fakeSessionSocket struct {
	openRequest  session.OpenSessionRequest
	openResponse session.OpenSessionResponse
	stream       chan session.TurnEvent
	reports      chan session.ExpressionDeliveryResult
	openCalls    int
	observeCalls int
	submitCalls  int
	submitID     string
	submit       session.SubmitTurnRequest
	openRequests []session.OpenSessionRequest
	open         func(session.OpenSessionRequest) (session.OpenSessionResponse, error)
}

func (socket *fakeSessionSocket) OpenSession(_ context.Context, request session.OpenSessionRequest) (session.OpenSessionResponse, error) {
	socket.openCalls++
	socket.openRequest = request
	socket.openRequests = append(socket.openRequests, request)
	if socket.open != nil {
		return socket.open(request)
	}
	return socket.openResponse, nil
}

func (socket *fakeSessionSocket) Watch(context.Context, string) (<-chan session.TurnEvent, error) {
	return socket.stream, nil
}

func (socket *fakeSessionSocket) ObserveAmbient(context.Context, string, session.AmbientObservation) error {
	socket.observeCalls++
	return nil
}

func (socket *fakeSessionSocket) SubmitTurn(_ context.Context, conversationID string, request session.SubmitTurnRequest) (session.SubmitTurnResponse, error) {
	socket.submitCalls++
	socket.submitID = conversationID
	socket.submit = request
	return session.SubmitTurnResponse{Outcome: session.TurnOutcome{ConversationID: conversationID, TurnID: "turn-1"}}, nil
}

func (socket *fakeSessionSocket) ReportExpressionDelivery(_ context.Context, result session.ExpressionDeliveryResult) error {
	socket.reports <- result
	return nil
}

type fakeStickerReader struct {
	content session.StickerContent
	err     error
}

func (reader fakeStickerReader) ReadStickerContent(context.Context, string) (session.StickerContent, error) {
	return reader.content, reader.err
}

func TestConfigValidationAndExactTokens(t *testing.T) {
	valid := Config{
		CoreEndpoint: "http://127.0.0.1:8787", CoreToken: " core-token ",
		OneBotWebhookEndpoint: "http://127.0.0.1:3002", OneBotAPIEndpoint: "http://127.0.0.1:3001",
		OneBotToken: " onebot-token ",
	}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	invalid := []Config{
		{},
		{CoreEndpoint: "http://core.example.com", CoreToken: "x", OneBotWebhookEndpoint: "http://127.0.0.1:2", OneBotAPIEndpoint: "http://127.0.0.1:1", OneBotToken: "x"},
		{CoreEndpoint: "http://127.0.0.1:1", CoreToken: "x", OneBotWebhookEndpoint: "ws://127.0.0.1:2", OneBotAPIEndpoint: "http://127.0.0.1:1", OneBotToken: "x"},
		{CoreEndpoint: "http://127.0.0.1:1", CoreToken: "x", OneBotWebhookEndpoint: "http://example.com:2", OneBotAPIEndpoint: "http://127.0.0.1:1", OneBotToken: "x"},
	}
	for i, cfg := range invalid {
		if err := cfg.Validate(); err == nil {
			t.Fatalf("invalid config %d accepted", i)
		}
	}
}

func TestConfigValidationAllowsExplicitContainerNetwork(t *testing.T) {
	valid := Config{
		CoreEndpoint: "http://fairy:8787", CoreToken: "core-token",
		OneBotWebhookEndpoint: "http://0.0.0.0:3002", OneBotAPIEndpoint: "http://llonebot:3000",
		OneBotToken: "onebot-token", ContainerNetwork: true,
	}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	invalid := []Config{
		{CoreEndpoint: "http://203.0.113.2:8787", CoreToken: "x", OneBotWebhookEndpoint: "http://0.0.0.0:3002", OneBotAPIEndpoint: "http://llonebot:3000", OneBotToken: "x", ContainerNetwork: true},
		{CoreEndpoint: "http://fairy.example.com:8787", CoreToken: "x", OneBotWebhookEndpoint: "http://0.0.0.0:3002", OneBotAPIEndpoint: "http://llonebot:3000", OneBotToken: "x", ContainerNetwork: true},
		{CoreEndpoint: "http://fairy:8787", CoreToken: "x", OneBotWebhookEndpoint: "http://0.0.0.0:3002/callback", OneBotAPIEndpoint: "http://llonebot:3000", OneBotToken: "x", ContainerNetwork: true},
		{CoreEndpoint: "http://fairy:8787", CoreToken: "x", OneBotWebhookEndpoint: "http://0.0.0.0:3002", OneBotAPIEndpoint: "http://user@llonebot:3000", OneBotToken: "x", ContainerNetwork: true},
		{CoreEndpoint: "http://fairy:8787", CoreToken: "x", OneBotWebhookEndpoint: "http://0.0.0.0:3002", OneBotAPIEndpoint: "http://203.0.113.3:3000", OneBotToken: "x", ContainerNetwork: true},
	}
	for i, cfg := range invalid {
		if err := cfg.Validate(); err == nil {
			t.Fatalf("invalid container config %d accepted", i)
		}
	}

	valid.ContainerNetwork = false
	if err := valid.Validate(); err == nil {
		t.Fatal("container endpoints accepted without explicit opt-in")
	}
}

func TestConfigFromEnvPreservesExactTokens(t *testing.T) {
	t.Setenv("FAIRY_CORE_ENDPOINT", "http://127.0.0.1:8787")
	t.Setenv("FAIRY_CORE_TOKEN", " core-token ")
	t.Setenv("FAIRY_ONEBOT_WEBHOOK_ENDPOINT", "http://127.0.0.1:3002")
	t.Setenv("FAIRY_ONEBOT_API_ENDPOINT", "http://127.0.0.1:3001")
	t.Setenv("FAIRY_ONEBOT_TOKEN", " onebot-token ")
	t.Setenv("FAIRY_ONEBOT_CONTAINER_NETWORK", "true")
	cfg, err := configFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CoreToken != " core-token " || cfg.OneBotToken != " onebot-token " {
		t.Fatalf("tokens were changed: Core=%q OneBot=%q", cfg.CoreToken, cfg.OneBotToken)
	}
	if !cfg.ContainerNetwork {
		t.Fatal("container network opt-in was not loaded")
	}
}

func TestConfigFromEnvRejectsInvalidContainerNetwork(t *testing.T) {
	t.Setenv("FAIRY_ONEBOT_CONTAINER_NETWORK", "yes")
	if _, err := configFromEnv(); err == nil {
		t.Fatal("invalid container network boolean accepted")
	}
}
