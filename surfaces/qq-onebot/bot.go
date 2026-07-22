package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"

	"fairy/coreclient"
	"fairy/interaction"
	zero "github.com/wdvxdr1123/ZeroBot"
	"github.com/wdvxdr1123/ZeroBot/driver"
	"github.com/wdvxdr1123/ZeroBot/message"
)

type bot struct {
	ctx       context.Context
	allowlist map[int64]struct{}
	socket    *coreclient.SessionSocket

	mu            sync.Mutex
	ensureMu      sync.Mutex
	conversations map[int64]string
	senders       map[string]func(string) error
}

func newBot(ctx context.Context, cfg Config, socket *coreclient.SessionSocket) (*bot, error) {
	if ctx == nil || socket == nil {
		return nil, errors.New("bot context and session socket are required")
	}
	allowlist := make(map[int64]struct{}, len(cfg.GroupAllowlist))
	for _, raw := range cfg.GroupAllowlist {
		id, err := positiveID(raw, "OneBot group allowlist entry")
		if err != nil {
			return nil, err
		}
		allowlist[id] = struct{}{}
	}
	return &bot{
		ctx: ctx, allowlist: allowlist, socket: socket,
		conversations: make(map[int64]string),
		senders:       make(map[string]func(string) error),
	}, nil
}

func (b *bot) register(engine *zero.Engine) {
	engine.OnMessage(zero.OnlyGroup).Handle(b.handle)
}

func (b *bot) handle(ctx *zero.Ctx) {
	if b == nil || ctx == nil || b.ctx.Err() != nil || ctx.Event == nil {
		return
	}
	groupID := ctx.Event.GroupID
	if _, ok := b.allowlist[groupID]; !ok {
		return
	}
	observation, err := ambientObservationFromEvent(ctx)
	if err != nil {
		log.Printf("group message %d ignored: %v", groupID, err)
		return
	}
	send := func(text string) error {
		id := ctx.SendChain(message.Text(text))
		if id.ID() == 0 {
			return errors.New("ZeroBot send_group_msg returned empty message ID")
		}
		return nil
	}
	conversationID, err := b.ensureConversation(groupID, send)
	if err != nil {
		log.Printf("group message %d open session failed: %v", groupID, err)
		return
	}
	if err := b.socket.ObserveAmbient(b.ctx, conversationID, observation); err != nil {
		log.Printf("group message %d observe failed: %v", groupID, err)
	}
}

func (b *bot) ensureConversation(groupID int64, send func(string) error) (string, error) {
	b.ensureMu.Lock()
	defer b.ensureMu.Unlock()
	b.mu.Lock()
	if conversationID, ok := b.conversations[groupID]; ok {
		b.senders[conversationID] = send
		b.mu.Unlock()
		return conversationID, nil
	}
	b.mu.Unlock()

	session, err := b.socket.OpenSession(b.ctx, coreclient.OpenSessionRequest{
		Endpoint: interaction.EndpointIM, EndpointKey: "onebot-group:" + strconv.FormatInt(groupID, 10),
		Interaction: interaction.Context{Audience: interaction.AudienceMulti, Initiation: interaction.InitiationAmbient, Presentation: interaction.PresentationChat},
	})
	if err != nil {
		return "", err
	}
	stream, err := b.socket.Watch(b.ctx, session.ConversationID)
	if err != nil {
		return "", err
	}
	b.mu.Lock()
	b.conversations[groupID] = session.ConversationID
	b.senders[session.ConversationID] = send
	b.mu.Unlock()
	go b.consumeHarness(session.ConversationID, stream)
	return session.ConversationID, nil
}

func (b *bot) consumeHarness(conversationID string, stream <-chan coreclient.HarnessEvent) {
	for {
		var event coreclient.HarnessEvent
		select {
		case <-b.ctx.Done():
			return
		case received, ok := <-stream:
			if !ok {
				if b.ctx.Err() == nil {
					log.Printf("session harness stream closed: conversation=%s", conversationID)
				}
				return
			}
			event = received
		}
		text, ok := finalBeatText(event)
		if !ok {
			continue
		}
		b.mu.Lock()
		send := b.senders[conversationID]
		b.mu.Unlock()
		if send == nil {
			continue
		}
		if err := send(text); err != nil {
			log.Printf("deliver final beat failed: %v", err)
		}
	}
}

func ambientObservationFromEvent(ctx *zero.Ctx) (coreclient.AmbientObservation, error) {
	if ctx == nil || ctx.Event == nil || ctx.Event.Sender == nil {
		return coreclient.AmbientObservation{}, errors.New("OneBot event sender is required")
	}
	text := strings.TrimSpace(ctx.ExtractPlainText())
	if text == "" {
		return coreclient.AmbientObservation{}, errors.New("plain text is empty")
	}
	if ctx.Event.MessageID == nil {
		return coreclient.AmbientObservation{}, errors.New("message ID is required")
	}
	senderName := strings.TrimSpace(ctx.Event.Sender.Card)
	if senderName == "" {
		senderName = strings.TrimSpace(ctx.Event.Sender.NickName)
	}
	if senderName == "" || ctx.Event.UserID <= 0 || ctx.Event.Time <= 0 {
		return coreclient.AmbientObservation{}, errors.New("sender name, sender ID, and timestamp are required")
	}
	return coreclient.AmbientObservation{
		MessageID: fmt.Sprint(ctx.Event.MessageID), SenderID: strconv.FormatInt(ctx.Event.UserID, 10), SenderName: senderName,
		Text: text, DirectedToBot: ctx.Event.IsToMe, TimestampUnixMS: ctx.Event.Time * 1000,
	}, nil
}

func finalBeatText(event coreclient.HarnessEvent) (string, bool) {
	var envelope struct {
		Type        string `json:"type"`
		Kind        string `json:"kind"`
		DisplayText string `json:"displayText"`
	}
	if err := json.Unmarshal(event.Payload, &envelope); err != nil {
		return "", false
	}
	if envelope.Type != "beat.ready" || envelope.Kind != "final" || strings.TrimSpace(envelope.DisplayText) == "" {
		return "", false
	}
	return envelope.DisplayText, true
}

func runBot(ctx context.Context, cfg Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	core, err := coreclient.New(coreclient.Options{Endpoint: cfg.CoreEndpoint, Token: cfg.CoreToken})
	if err != nil {
		return err
	}
	socket, err := core.DialSession(ctx)
	if err != nil {
		return err
	}
	defer socket.Close()
	b, err := newBot(ctx, cfg, socket)
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
	<-ctx.Done()
	return nil
}
