package edge

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"fairy/plugin"
	"fairy/plugin/qqonebot"
	"fairy/plugin/sdk"
	"fairy/plugin/testhost"
	"fairy/runtime/wasm"
	"fairy/transport/session"
)

const (
	qqPollLimit        = 32
	qqIngressMaxBytes  = 64 << 10
	qqCredentialHandle = "onebot"
)

var (
	ErrQQPluginNotInstalled = errors.New("QQ plugin instance is not installed")
	ErrQQIngressBindInvalid = errors.New("QQ ingress bind must be a loopback address")
)

type qqSession interface {
	OpenSession(context.Context, session.OpenSessionRequest) (session.OpenSessionResponse, error)
	Watch(context.Context, string) (<-chan session.TurnEvent, error)
	ObserveAmbient(context.Context, string, session.AmbientObservation) error
	SubmitTurn(context.Context, string, session.SubmitTurnRequest) (session.SubmitTurnResponse, error)
	ReportExpressionDelivery(context.Context, session.ExpressionDeliveryResult) error
}

type qqStickerReader func(context.Context, string) (session.StickerContent, error)

type sendTarget struct {
	Kind    string
	GroupID string
	UserID  string
}

type QQBridge struct {
	host       *testhost.Host
	queue      *wasm.EventQueue
	session    qqSession
	stickers   qqStickerReader
	instanceID string
	config     qqonebot.InstanceConfig
	selfID     string

	mu             sync.Mutex
	cursor         uint64
	conversations  map[string]string
	targets        map[string]sendTarget
	replyPositions map[string]*qqonebot.ReplyDistanceTracker
}

func newQQBridge(session qqSession, stickers qqStickerReader, instanceID string, config qqonebot.InstanceConfig, grant wasm.Grant, httpCall testhost.HostCall) (*QQBridge, error) {
	if session == nil {
		return nil, errors.New("QQ session facade is required")
	}
	queue := &wasm.EventQueue{}
	bridge := &QQBridge{
		queue:          queue,
		session:        session,
		stickers:       stickers,
		instanceID:     instanceID,
		config:         config,
		conversations:  make(map[string]string),
		targets:        make(map[string]sendTarget),
		replyPositions: make(map[string]*qqonebot.ReplyDistanceTracker),
	}
	var host *testhost.Host
	host, err := testhost.New(qqonebot.NewHandler(func(ctx context.Context, capability string, payload json.RawMessage) ([]byte, error) {
		return host.Call(ctx, capability, payload)
	}), testhost.Options{
		MaxInputBytes: testhost.DefaultOptions().MaxInputBytes,
		MaxCalls:      testhost.DefaultOptions().MaxCalls,
		Capabilities:  []string{"http.request", "http.ingress", "event.emit", "action.complete"},
		HostCall: func(ctx context.Context, capability string, payload json.RawMessage) ([]byte, error) {
			switch capability {
			case "event.emit":
				queue.Push(payload)
				return json.Marshal(map[string]any{"ok": true})
			case "action.complete":
				return json.Marshal(map[string]any{"ok": true})
			case "http.request":
				if httpCall == nil {
					return nil, &plugin.CodedError{Code: plugin.CodeCapabilityDenied, Message: "http.request: not granted"}
				}
				return httpCall(ctx, capability, payload)
			default:
				return nil, &plugin.CodedError{Code: plugin.CodeCapabilityDenied, Message: capability + ": not granted"}
			}
		},
	})
	if err != nil {
		return nil, err
	}
	bridge.host = host
	_ = grant
	return bridge, nil
}

func (b *QQBridge) Ingest(ctx context.Context, raw json.RawMessage) error {
	if b == nil || b.host == nil {
		return ErrQQPluginNotInstalled
	}
	payload, err := json.Marshal(map[string]any{
		"op":             "parse",
		"raw":            raw,
		"selfId":         b.selfID,
		"groupAllowlist": b.config.GroupAllowlist,
	})
	if err != nil {
		return err
	}
	envelope, err := sdk.Encode(plugin.Envelope{
		ABIVersion:  plugin.ABIVersion,
		Kind:        "handle",
		Correlation: plugin.Correlation{PluginInstanceID: b.instanceID},
		Payload:     payload,
	})
	if err != nil {
		return err
	}
	out, err := b.host.Invoke(ctx, envelope)
	if err != nil {
		return err
	}
	parsed, err := sdk.Decode(out)
	if err != nil {
		return err
	}
	if parsed.Kind == "error" {
		if parsed.Error == nil {
			return errors.New("qq-onebot parse failed")
		}
		return parsed.Error
	}
	return nil
}

func (b *QQBridge) Poll(ctx context.Context) error {
	if b == nil {
		return ErrQQPluginNotInstalled
	}
	b.mu.Lock()
	after := b.cursor
	b.mu.Unlock()
	events := b.queue.Poll(after, qqPollLimit)
	for _, item := range events {
		if err := b.dispatch(ctx, item.Payload); err != nil {
			return err
		}
		b.mu.Lock()
		b.cursor = item.Sequence
		b.mu.Unlock()
	}
	return nil
}

func (b *QQBridge) dispatch(ctx context.Context, payload []byte) error {
	var body struct {
		Event             qqonebot.Event `json:"event"`
		ExternalMessageID string         `json:"externalMessageId"`
		TraceID           string         `json:"traceId"`
	}
	if err := json.Unmarshal(payload, &body); err != nil {
		return err
	}
	event := body.Event
	if event.MessageID == "" {
		return errors.New("queued QQ event is missing messageId")
	}
	switch event.Kind {
	case "private":
		conversationID, err := b.ensureConversation(ctx, event)
		if err != nil {
			return err
		}
		_, err = b.session.SubmitTurn(ctx, conversationID, session.SubmitTurnRequest{Input: event.Text, MessageID: event.MessageID})
		return err
	case "group":
		conversationID, err := b.ensureConversation(ctx, event)
		if err != nil {
			return err
		}
		b.observeReplyPosition(conversationID, event.MessageID, time.UnixMilli(event.TimestampUnixMS))
		return b.session.ObserveAmbient(ctx, conversationID, session.AmbientObservation{
			MessageID:       event.MessageID,
			SenderID:        event.UserID,
			SenderName:      event.SenderName,
			Text:            event.Text,
			Mentions:        mentionsOf(event.Mentions),
			DirectedToBot:   event.DirectedToBot,
			TimestampUnixMS: event.TimestampUnixMS,
		})
	default:
		return fmt.Errorf("queued QQ event kind %q is unsupported", event.Kind)
	}
}

func (b *QQBridge) ensureConversation(ctx context.Context, event qqonebot.Event) (string, error) {
	b.mu.Lock()
	if conversationID, ok := b.conversations[event.EndpointKey]; ok {
		b.mu.Unlock()
		return conversationID, nil
	}
	b.mu.Unlock()

	request := session.OpenSessionRequest{
		Endpoint:           session.EndpointIM,
		EndpointKey:        event.EndpointKey,
		OutputCapabilities: session.OutputCapabilities{Sticker: true},
	}
	target := sendTarget{Kind: event.Kind, GroupID: event.GroupID, UserID: event.UserID}
	if event.Kind == "group" {
		request.Interaction = session.Context{Audience: session.AudienceMulti, Initiation: session.InitiationAmbient, Presentation: session.PresentationChat}
	} else {
		request.Interaction = session.Context{
			Audience: session.AudienceSingle, Initiation: session.InitiationDirect, Presentation: session.PresentationChat,
			Principal: &session.PrincipalRef{Namespace: "qq.onebot", Subject: event.UserID},
		}
	}
	opened, err := b.session.OpenSession(ctx, request)
	if err != nil {
		return "", err
	}
	stream, err := b.session.Watch(ctx, opened.ConversationID)
	if err != nil {
		return "", err
	}
	b.mu.Lock()
	b.conversations[event.EndpointKey] = opened.ConversationID
	b.targets[opened.ConversationID] = target
	b.mu.Unlock()
	go b.consumeTurnEvents(ctx, opened.ConversationID, stream)
	return opened.ConversationID, nil
}

func (b *QQBridge) consumeTurnEvents(ctx context.Context, conversationID string, stream <-chan session.TurnEvent) {
	claims := qqonebot.NewTurnReplyClaims()
	for {
		var event session.TurnEvent
		select {
		case <-ctx.Done():
			return
		case received, ok := <-stream:
			if !ok {
				return
			}
			event = received
		}
		if qqonebot.TerminalTurnState(event.State) {
			claims.Release(event.TurnID)
		}
		beat, ok := finalExpressionBeat(event)
		if !ok {
			continue
		}
		replyTarget := ""
		messageGap, first := claims.Claim(event.TurnID)
		if first && b.shouldQuote(conversationID, beat.ReplyTargetMessageID, time.Now(), messageGap) {
			replyTarget = beat.ReplyTargetMessageID
		}
		switch beat.Part.Kind {
		case session.ExpressionUtterance:
			b.deliverText(ctx, conversationID, event, beat, replyTarget)
		case session.ExpressionSticker:
			b.deliverSticker(ctx, conversationID, event, beat, replyTarget)
		}
	}
}

func (b *QQBridge) shouldQuote(conversationID, messageID string, now time.Time, gap uint64) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.replyPositions[conversationID].ShouldQuote(messageID, now, gap)
}

func (b *QQBridge) observeReplyPosition(conversationID, messageID string, at time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	tracker := b.replyPositions[conversationID]
	if tracker == nil {
		tracker = &qqonebot.ReplyDistanceTracker{}
		b.replyPositions[conversationID] = tracker
	}
	tracker.Observe(messageID, at)
}

func (b *QQBridge) deliverText(ctx context.Context, conversationID string, event session.TurnEvent, beat expressionBeat, replyTarget string) {
	result := session.ExpressionDeliveryResult{
		ConversationID: conversationID, TurnID: event.TurnID, BeatID: beat.BeatID,
		Status: session.ExpressionDeliveryFailed, ErrorMessage: "QQ 文字发送失败",
	}
	receipt, err := b.send(ctx, conversationID, event.TurnID, qqonebot.SendRequest{
		Op: "send", Text: beat.Part.Text, ReplyMessageID: replyTarget,
	})
	if err == nil && receipt.Status == "succeeded" {
		result.Status = session.ExpressionDeliverySucceeded
		result.ExternalMessageID = receipt.ExternalMessageID
		result.ErrorMessage = ""
	}
	_ = b.session.ReportExpressionDelivery(ctx, result)
}

func (b *QQBridge) deliverSticker(ctx context.Context, conversationID string, event session.TurnEvent, beat expressionBeat, replyTarget string) {
	result := session.ExpressionDeliveryResult{
		ConversationID: conversationID, TurnID: event.TurnID, BeatID: beat.BeatID,
		Status: session.ExpressionDeliveryFailed, ErrorMessage: "QQ 图片发送失败",
	}
	fail := func(message string) {
		result.ErrorMessage = message
		_ = b.session.ReportExpressionDelivery(ctx, result)
	}
	if beat.Part.Sticker == nil || b.stickers == nil {
		fail("QQ 表情包引用缺失")
		return
	}
	content, err := b.stickers(ctx, beat.Part.Sticker.ID)
	if err != nil {
		fail("从 Core 读取表情包失败")
		return
	}
	if content.MIMEType != beat.Part.Sticker.MIMEType {
		fail("Core 表情包 MIME 与回复快照不一致")
		return
	}
	receipt, err := b.send(ctx, conversationID, event.TurnID, qqonebot.SendRequest{
		Op: "send", ImageBase64: base64.StdEncoding.EncodeToString(content.Bytes), ReplyMessageID: replyTarget,
	})
	if err != nil || receipt.Status != "succeeded" {
		fail("QQ 图片发送失败")
		return
	}
	result.Status = session.ExpressionDeliverySucceeded
	result.ExternalMessageID = receipt.ExternalMessageID
	result.ErrorMessage = ""
	_ = b.session.ReportExpressionDelivery(ctx, result)
}

func (b *QQBridge) send(ctx context.Context, conversationID, turnID string, req qqonebot.SendRequest) (qqonebot.Receipt, error) {
	b.mu.Lock()
	target := b.targets[conversationID]
	b.mu.Unlock()
	req.APIBaseURL = b.config.APIBaseURL
	req.Credential = qqCredentialHandle
	req.GroupID = target.GroupID
	req.UserID = target.UserID
	payload, err := json.Marshal(req)
	if err != nil {
		return qqonebot.Receipt{}, err
	}
	raw, err := sdk.Encode(plugin.Envelope{
		ABIVersion: plugin.ABIVersion, Kind: "handle",
		Correlation: plugin.Correlation{PluginInstanceID: b.instanceID, TurnID: turnID},
		Payload:     payload,
	})
	if err != nil {
		return qqonebot.Receipt{}, err
	}
	out, err := b.host.Invoke(ctx, raw)
	if err != nil {
		return qqonebot.Receipt{}, err
	}
	parsed, err := sdk.Decode(out)
	if err != nil {
		return qqonebot.Receipt{}, err
	}
	if parsed.Kind == "error" {
		if parsed.Error == nil {
			return qqonebot.Receipt{}, errors.New("qq-onebot send failed")
		}
		return qqonebot.Receipt{}, parsed.Error
	}
	var receipt qqonebot.Receipt
	if err := json.Unmarshal(parsed.Payload, &receipt); err != nil {
		return qqonebot.Receipt{}, err
	}
	return receipt, nil
}

func (b *QQBridge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, qqIngressMaxBytes+1))
	if err != nil || len(body) > qqIngressMaxBytes {
		http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
		return
	}
	if err := b.Ingest(r.Context(), body); err != nil {
		http.Error(w, "invalid event", http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func mentionsOf(values []qqonebot.Mention) []session.MessageMention {
	out := make([]session.MessageMention, 0, len(values))
	for _, item := range values {
		out = append(out, session.MessageMention{UserID: item.UserID, DisplayName: item.DisplayName})
	}
	return out
}

type expressionBeat struct {
	BeatID               string
	ReplyTargetMessageID string
	Part                 session.ExpressionPart
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
		envelope.Part = session.ExpressionPart{Kind: session.ExpressionUtterance, Text: envelope.DisplayText}
	}
	switch envelope.Part.Kind {
	case session.ExpressionUtterance:
		envelope.Part.Text = strings.TrimSpace(envelope.Part.Text)
		if envelope.Part.Text == "" {
			return expressionBeat{}, false
		}
	case session.ExpressionSticker:
		if envelope.Part.Sticker == nil || strings.TrimSpace(envelope.Part.Sticker.ID) == "" || strings.TrimSpace(envelope.Part.Sticker.MIMEType) == "" {
			return expressionBeat{}, false
		}
	default:
		return expressionBeat{}, false
	}
	if !qqonebot.ValidReplyMessageID(envelope.ReplyTargetMessageID) {
		envelope.ReplyTargetMessageID = ""
	}
	return expressionBeat{BeatID: envelope.BeatID, ReplyTargetMessageID: envelope.ReplyTargetMessageID, Part: envelope.Part}, true
}

func loopbackBind(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", nil
	}
	host, port, err := net.SplitHostPort(value)
	if err != nil {
		return "", ErrQQIngressBindInvalid
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() || port == "" {
		return "", ErrQQIngressBindInvalid
	}
	return net.JoinHostPort(ip.String(), port), nil
}
