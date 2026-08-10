package main

import (
	"context"
	"encoding/json"
	"errors"
	"fairy/transport/session"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	zero "github.com/wdvxdr1123/ZeroBot"
	"github.com/wdvxdr1123/ZeroBot/driver"
	"github.com/wdvxdr1123/ZeroBot/message"
)

type bot struct {
	ctx        context.Context
	authorizer groupAuthorizer
	socket     sessionSocket
	stickers   stickerContentReader

	mu             sync.Mutex
	ensureMu       sync.Mutex
	conversations  map[string]string
	senders        map[string]expressionSender
	replyPositions map[string]*replyDistanceTracker
}

type sessionSocket interface {
	OpenSession(context.Context, session.OpenSessionRequest) (session.OpenSessionResponse, error)
	Watch(context.Context, string) (<-chan session.TurnEvent, error)
	ObserveAmbient(context.Context, string, session.AmbientObservation) error
	SubmitTurn(context.Context, string, session.SubmitTurnRequest) (session.SubmitTurnResponse, error)
	ReportExpressionDelivery(context.Context, session.ExpressionDeliveryResult) error
}

type stickerContentReader interface {
	ReadStickerContent(context.Context, string) (session.StickerContent, error)
}

type groupAuthorizer interface {
	GroupAllowed(context.Context, int64) (bool, error)
}

type expressionSender struct {
	text       func(string) (string, error)
	image      func([]byte) (string, error)
	replyText  func(string, string) (string, error)
	replyImage func(string, []byte) (string, error)
}

func newBot(ctx context.Context, socket sessionSocket, stickers stickerContentReader, authorizer groupAuthorizer) (*bot, error) {
	if ctx == nil || socket == nil || stickers == nil || authorizer == nil {
		return nil, errors.New("bot context, session socket, sticker reader, and group authorizer are required")
	}
	return &bot{
		ctx: ctx, authorizer: authorizer, socket: socket, stickers: stickers,
		conversations:  make(map[string]string),
		senders:        make(map[string]expressionSender),
		replyPositions: make(map[string]*replyDistanceTracker),
	}, nil
}

func (b *bot) register(engine *zero.Engine) {
	engine.OnMessage(zero.OnlyGroup).Handle(b.handle)
	engine.OnMessage(zero.OnlyPrivate).Handle(b.handlePrivate)
}

func (b *bot) handle(ctx *zero.Ctx) {
	if b == nil || ctx == nil || b.ctx.Err() != nil || ctx.Event == nil {
		return
	}
	groupID := ctx.Event.GroupID
	authorizeCtx, cancel := context.WithTimeout(b.ctx, 2*time.Second)
	allowed, err := b.authorizer.GroupAllowed(authorizeCtx, groupID)
	cancel()
	if err != nil {
		log.Printf("group message %d authorization failed: %v", groupID, err)
		return
	}
	if !allowed {
		return
	}
	observation, err := ambientObservationFromEvent(ctx)
	if err != nil {
		log.Printf("group message %d ignored: %v", groupID, err)
		return
	}
	send := expressionSender{
		text: func(text string) (string, error) {
			return sendChain(ctx, message.Text(text))
		},
		image: func(content []byte) (string, error) {
			return sendChain(ctx, message.ImageBytes(content))
		},
		replyText: func(targetMessageID, text string) (string, error) {
			return sendChain(ctx, message.Reply(targetMessageID), message.Text(text))
		},
		replyImage: func(targetMessageID string, content []byte) (string, error) {
			return sendChain(ctx, message.Reply(targetMessageID), message.ImageBytes(content))
		},
	}
	conversationID, err := b.ensureConversation(groupID, send)
	if err != nil {
		log.Printf("group message %d open session failed: %v", groupID, err)
		return
	}
	b.observeReplyPosition(conversationID, observation.MessageID, time.Now())
	if err := b.socket.ObserveAmbient(b.ctx, conversationID, observation); err != nil {
		log.Printf("group message %d observe failed: %v", groupID, err)
	}
}

func (b *bot) handlePrivate(ctx *zero.Ctx) {
	if b == nil || ctx == nil || b.ctx.Err() != nil || ctx.Event == nil {
		return
	}
	input, messageID, userID, err := privateTurnFromEvent(ctx)
	if err != nil {
		log.Printf("private message ignored: %v", err)
		return
	}
	send := expressionSender{
		text: func(text string) (string, error) {
			return sendChain(ctx, message.Text(text))
		},
		image: func(content []byte) (string, error) {
			return sendChain(ctx, message.ImageBytes(content))
		},
	}
	conversationID, err := b.ensureEndpointConversation(
		"onebot-private:"+userID,
		session.Context{
			Audience: session.AudienceSingle, Initiation: session.InitiationDirect, Presentation: session.PresentationChat,
			Principal: &session.PrincipalRef{Namespace: "qq.onebot", Subject: userID},
		},
		send,
	)
	if err != nil {
		log.Printf("private message open session failed: %v", err)
		return
	}
	if _, err := b.socket.SubmitTurn(b.ctx, conversationID, session.SubmitTurnRequest{Input: input, MessageID: messageID}); err != nil {
		log.Printf("private message submit turn failed: %v", err)
	}
}

func sendChain(ctx *zero.Ctx, segments ...message.Segment) (string, error) {
	id := ctx.SendChain(segments...)
	if id.ID() == 0 {
		return "", errors.New("OneBot send action returned empty message ID")
	}
	return strconv.FormatInt(id.ID(), 10), nil
}

func (b *bot) ensureConversation(groupID int64, send expressionSender) (string, error) {
	return b.ensureEndpointConversation(
		"onebot-group:"+strconv.FormatInt(groupID, 10),
		session.Context{Audience: session.AudienceMulti, Initiation: session.InitiationAmbient, Presentation: session.PresentationChat},
		send,
	)
}

func (b *bot) ensureEndpointConversation(endpointKey string, interaction session.Context, send expressionSender) (string, error) {
	b.ensureMu.Lock()
	defer b.ensureMu.Unlock()
	b.mu.Lock()
	if conversationID, ok := b.conversations[endpointKey]; ok {
		b.senders[conversationID] = send
		b.mu.Unlock()
		return conversationID, nil
	}
	b.mu.Unlock()

	session, err := b.socket.OpenSession(b.ctx, session.OpenSessionRequest{
		Endpoint: session.EndpointIM, EndpointKey: endpointKey,
		Interaction:        interaction,
		OutputCapabilities: session.OutputCapabilities{Sticker: true},
	})
	if err != nil {
		return "", err
	}
	stream, err := b.socket.Watch(b.ctx, session.ConversationID)
	if err != nil {
		return "", err
	}
	b.mu.Lock()
	b.conversations[endpointKey] = session.ConversationID
	b.senders[session.ConversationID] = send
	b.mu.Unlock()
	go b.consumeTurnEvents(session.ConversationID, stream)
	return session.ConversationID, nil
}

func privateTurnFromEvent(ctx *zero.Ctx) (input, messageID, userID string, err error) {
	if ctx == nil || ctx.Event == nil || ctx.Event.Sender == nil {
		return "", "", "", errors.New("OneBot event sender is required")
	}
	if ctx.Event.UserID <= 0 || ctx.Event.Time <= 0 {
		return "", "", "", errors.New("sender ID and timestamp are required")
	}
	if ctx.Event.MessageID == nil {
		return "", "", "", errors.New("message ID is required")
	}
	input, _, err = projectOneBotMessage(sourceOneBotMessage(ctx.Event))
	if err != nil {
		return "", "", "", err
	}
	if input == "" {
		return "", "", "", errors.New("readable message text is empty")
	}
	return input, fmt.Sprint(ctx.Event.MessageID), strconv.FormatInt(ctx.Event.UserID, 10), nil
}

func (b *bot) consumeTurnEvents(conversationID string, stream <-chan session.TurnEvent) {
	claims := newTurnReplyClaims()
	for {
		var event session.TurnEvent
		select {
		case <-b.ctx.Done():
			return
		case received, ok := <-stream:
			if !ok {
				if b.ctx.Err() == nil {
					log.Printf("session turn-event stream closed: conversation=%s", conversationID)
				}
				return
			}
			event = received
		}
		if terminalTurnState(event.State) {
			claims.Release(event.TurnID)
		}
		beat, ok := finalExpressionBeat(event)
		if !ok {
			continue
		}
		replyTargetMessageID := ""
		messageGap, firstFinalBeat := claims.Claim(event.TurnID)
		if firstFinalBeat && b.shouldQuoteReply(conversationID, beat.ReplyTargetMessageID, time.Now(), messageGap) {
			replyTargetMessageID = beat.ReplyTargetMessageID
		}
		b.mu.Lock()
		send := b.senders[conversationID]
		b.mu.Unlock()
		switch beat.Part.Kind {
		case session.ExpressionUtterance:
			b.deliverUtterance(conversationID, event, beat, send, replyTargetMessageID)
		case session.ExpressionSticker:
			b.deliverSticker(conversationID, event, beat, send, replyTargetMessageID)
		}
	}
}

func (b *bot) deliverUtterance(conversationID string, event session.TurnEvent, beat expressionBeat, send expressionSender, replyTargetMessageID string) {
	result := session.ExpressionDeliveryResult{
		ConversationID: conversationID,
		TurnID:         event.TurnID,
		BeatID:         beat.BeatID,
		Status:         session.ExpressionDeliveryFailed,
	}
	if replyTargetMessageID != "" && send.replyText != nil {
		if externalMessageID, err := send.replyText(replyTargetMessageID, beat.Part.Text); err != nil {
			result.ErrorMessage = "QQ 引用文字发送失败"
			log.Printf("QQ 引用文字投递失败")
		} else {
			result.Status = session.ExpressionDeliverySucceeded
			result.ExternalMessageID = externalMessageID
		}
	} else if send.text == nil {
		result.ErrorMessage = "QQ 文字发送器不可用"
		log.Printf("QQ 文字投递失败")
	} else if externalMessageID, err := send.text(beat.Part.Text); err != nil {
		result.ErrorMessage = "QQ 文字发送失败"
		log.Printf("QQ 文字投递失败")
	} else {
		result.Status = session.ExpressionDeliverySucceeded
		result.ExternalMessageID = externalMessageID
	}
	b.reportExpressionDelivery(result)
}

func (b *bot) reportExpressionDelivery(result session.ExpressionDeliveryResult) {
	if err := b.socket.ReportExpressionDelivery(b.ctx, result); err != nil {
		log.Printf("QQ 投递回执上报失败")
	}
}

type expressionBeat struct {
	BeatID               string
	ReplyTargetMessageID string
	Part                 session.ExpressionPart
}

func (b *bot) deliverSticker(conversationID string, event session.TurnEvent, beat expressionBeat, send expressionSender, replyTargetMessageID string) {
	result := session.ExpressionDeliveryResult{
		ConversationID: conversationID,
		TurnID:         event.TurnID,
		BeatID:         beat.BeatID,
		Status:         session.ExpressionDeliveryFailed,
	}
	fail := func(message string) {
		result.ErrorMessage = message
		log.Printf("QQ 图片投递失败")
		b.reportExpressionDelivery(result)
	}
	if beat.Part.Sticker == nil {
		fail("QQ 表情包引用缺失")
		return
	}
	if replyTargetMessageID != "" && send.replyImage == nil && send.image == nil {
		fail("QQ 图片发送器不可用")
		return
	}
	if replyTargetMessageID == "" && send.image == nil {
		fail("QQ 图片发送器不可用")
		return
	}
	content, err := b.stickers.ReadStickerContent(b.ctx, beat.Part.Sticker.ID)
	if err != nil {
		fail("从 Core 读取表情包失败")
		return
	}
	if content.MIMEType != beat.Part.Sticker.MIMEType {
		fail("Core 表情包 MIME 与回复快照不一致")
		return
	}
	var externalMessageID string
	if replyTargetMessageID != "" && send.replyImage != nil {
		externalMessageID, err = send.replyImage(replyTargetMessageID, content.Bytes)
	} else {
		externalMessageID, err = send.image(content.Bytes)
	}
	if err != nil {
		fail("QQ 图片发送失败")
		return
	}
	result.Status = session.ExpressionDeliverySucceeded
	result.ExternalMessageID = externalMessageID
	result.ErrorMessage = ""
	b.reportExpressionDelivery(result)
}

func ambientObservationFromEvent(ctx *zero.Ctx) (session.AmbientObservation, error) {
	if ctx == nil || ctx.Event == nil || ctx.Event.Sender == nil {
		return session.AmbientObservation{}, errors.New("OneBot event sender is required")
	}
	text, mentions, err := projectOneBotMessage(sourceOneBotMessage(ctx.Event))
	if err != nil {
		return session.AmbientObservation{}, err
	}
	if text == "" {
		return session.AmbientObservation{}, errors.New("readable message text is empty")
	}
	if ctx.Event.MessageID == nil {
		return session.AmbientObservation{}, errors.New("message ID is required")
	}
	senderName := strings.TrimSpace(ctx.Event.Sender.Card)
	if senderName == "" {
		senderName = strings.TrimSpace(ctx.Event.Sender.NickName)
	}
	if senderName == "" || ctx.Event.UserID <= 0 || ctx.Event.Time <= 0 {
		return session.AmbientObservation{}, errors.New("sender name, sender ID, and timestamp are required")
	}
	return session.AmbientObservation{
		MessageID: fmt.Sprint(ctx.Event.MessageID), SenderID: strconv.FormatInt(ctx.Event.UserID, 10), SenderName: senderName,
		Text: text, Mentions: mentions,
		DirectedToBot: ctx.Event.IsToMe, TimestampUnixMS: ctx.Event.Time * 1000,
	}, nil
}

func sourceOneBotMessage(event *zero.Event) message.Message {
	if len(event.NativeMessage) > 0 {
		if elements := message.ParseMessage(event.NativeMessage); len(elements) > 0 {
			return elements
		}
	}
	if strings.TrimSpace(event.RawMessage) != "" {
		if elements := message.ParseMessageFromString(event.RawMessage); len(elements) > 0 {
			return elements
		}
	}
	return event.Message
}

func projectOneBotMessage(elements message.Message) (string, []session.MessageMention, error) {
	parts := make([]string, 0, len(elements))
	mentions := make([]session.MessageMention, 0)
	seen := make(map[string]struct{})
	for _, element := range elements {
		switch element.Type {
		case "text":
			if text := strings.TrimSpace(element.Data["text"]); text != "" {
				parts = append(parts, text)
			}
		case "at":
			userID := strings.TrimSpace(element.Data["qq"])
			if userID == "all" {
				parts = append(parts, "@全体成员")
				continue
			}
			parsed, err := strconv.ParseInt(userID, 10, 64)
			if err != nil || parsed <= 0 || strconv.FormatInt(parsed, 10) != userID {
				return "", nil, fmt.Errorf("OneBot at segment contains invalid user ID %q", userID)
			}
			displayName := strings.TrimSpace(element.Data["name"])
			if displayName == "" {
				displayName = userID
			}
			parts = append(parts, "@"+displayName)
			if _, exists := seen[userID]; exists {
				continue
			}
			seen[userID] = struct{}{}
			mentions = append(mentions, session.MessageMention{UserID: userID, DisplayName: displayName})
		}
	}
	return strings.Join(parts, " "), mentions, nil
}

func finalExpressionBeat(event session.TurnEvent) (expressionBeat, bool) {
	var envelope struct {
		Type                 string                 `json:"type"`
		BeatID               string                 `json:"beatId"`
		Kind                 string                 `json:"kind"`
		DisplayText          string                 `json:"displayText"`
		ReplyTargetMessageID string                 `json:"replyTargetMessageId"`
		Part                 session.ExpressionPart `json:"part"`
	}
	if err := json.Unmarshal(event.Payload, &envelope); err != nil {
		return expressionBeat{}, false
	}
	if envelope.Type != "beat.ready" || envelope.Kind != "final" {
		return expressionBeat{}, false
	}
	envelope.BeatID = strings.TrimSpace(envelope.BeatID)
	if envelope.BeatID == "" {
		return expressionBeat{}, false
	}
	if envelope.Part.Kind == "" && strings.TrimSpace(envelope.DisplayText) != "" {
		envelope.Part = session.ExpressionPart{
			Kind: session.ExpressionUtterance,
			Text: envelope.DisplayText,
		}
	}
	switch envelope.Part.Kind {
	case session.ExpressionUtterance:
		envelope.Part.Text = strings.TrimSpace(envelope.Part.Text)
		if envelope.Part.Text == "" {
			return expressionBeat{}, false
		}
	case session.ExpressionSticker:
		if envelope.Part.Sticker == nil ||
			strings.TrimSpace(envelope.Part.Sticker.ID) == "" || strings.TrimSpace(envelope.Part.Sticker.MIMEType) == "" {
			return expressionBeat{}, false
		}
	default:
		return expressionBeat{}, false
	}
	if !validReplyMessageID(envelope.ReplyTargetMessageID) {
		envelope.ReplyTargetMessageID = ""
	}
	return expressionBeat{BeatID: envelope.BeatID, ReplyTargetMessageID: envelope.ReplyTargetMessageID, Part: envelope.Part}, true
}

func runBot(ctx context.Context, cfg Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	core, err := session.New(session.Options{Endpoint: cfg.CoreEndpoint, Token: cfg.CoreToken})
	if err != nil {
		return err
	}
	socket, err := core.DialSession(ctx)
	if err != nil {
		return err
	}
	defer socket.Close()
	b, err := newBot(ctx, socket, core, coreGroupAuthorizer{config: core})
	if err != nil {
		return err
	}
	engine := zero.New()
	b.register(engine)
	defer engine.Delete()
	go zero.Run(&zero.Config{
		RingLen: 16, Latency: 0,
		Driver: []zero.Driver{driver.NewHTTPClient(
			cfg.OneBotWebhookEndpoint, cfg.OneBotToken,
			cfg.OneBotAPIEndpoint, cfg.OneBotToken,
		)},
	})
	select {
	case <-ctx.Done():
		return nil
	case <-socket.Done():
		return fmt.Errorf("Core session websocket closed: %w", socket.Err())
	}
}
