package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"fairy-surfaces/turnclient"
	"fairy/coreclient"
	zero "github.com/wdvxdr1123/ZeroBot"
	"github.com/wdvxdr1123/ZeroBot/driver"
	"github.com/wdvxdr1123/ZeroBot/message"
)

type bot struct {
	ctx       context.Context
	allowlist map[int64]struct{}
	windows   *groupWindow
	turns     interface {
		Run(context.Context, turnclient.Request, turnclient.Callback) (turnclient.Result, error)
	}
	core interface {
		OpenSession(context.Context, coreclient.OpenSessionRequest) (coreclient.OpenSessionResponse, error)
		DecideGroupParticipation(context.Context, string, coreclient.GroupParticipationRequest) (coreclient.GroupParticipationResponse, error)
	}
}

func newBot(ctx context.Context, cfg Config, core *coreclient.Client) (*bot, error) {
	if ctx == nil || core == nil {
		return nil, errors.New("bot context and Core client are required")
	}
	turns, err := turnclient.New(core, 15*time.Second)
	if err != nil {
		return nil, err
	}
	allowlist := make(map[int64]struct{}, len(cfg.GroupAllowlist))
	for _, raw := range cfg.GroupAllowlist {
		id, err := positiveID(raw, "OneBot group allowlist entry")
		if err != nil {
			return nil, err
		}
		allowlist[id] = struct{}{}
	}
	b := &bot{ctx: ctx, allowlist: allowlist, turns: turns, core: core}
	b.windows, err = newGroupWindow(ctx, b.decideGroupParticipation, b.replyToGroupWindow, func(groupID int64, err error) {
		log.Printf("group window %d failed: %v", groupID, err)
	})
	if err != nil {
		return nil, err
	}
	return b, nil
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
	observation, err := groupObservationFromEvent(ctx)
	if err != nil {
		log.Printf("group message %d ignored: %v", groupID, err)
		return
	}
	if err := b.windows.Add(groupID, observation, func(text string) error {
		id := ctx.SendChain(message.Text(text))
		if id.ID() == 0 {
			return errors.New("ZeroBot send_group_msg returned empty message ID")
		}
		return nil
	}); err != nil {
		log.Printf("group message %d enqueue failed: %v", groupID, err)
	}
}

func groupObservationFromEvent(ctx *zero.Ctx) (coreclient.GroupObservation, error) {
	if ctx == nil || ctx.Event == nil || ctx.Event.Sender == nil {
		return coreclient.GroupObservation{}, errors.New("OneBot event sender is required")
	}
	text := strings.TrimSpace(ctx.ExtractPlainText())
	if text == "" {
		return coreclient.GroupObservation{}, errors.New("plain text is empty")
	}
	if ctx.Event.MessageID == nil {
		return coreclient.GroupObservation{}, errors.New("message ID is required")
	}
	senderName := strings.TrimSpace(ctx.Event.Sender.Card)
	if senderName == "" {
		senderName = strings.TrimSpace(ctx.Event.Sender.NickName)
	}
	if senderName == "" || ctx.Event.UserID <= 0 || ctx.Event.Time <= 0 {
		return coreclient.GroupObservation{}, errors.New("sender name, sender ID, and timestamp are required")
	}
	return coreclient.GroupObservation{
		MessageID: fmt.Sprint(ctx.Event.MessageID), SenderID: strconv.FormatInt(ctx.Event.UserID, 10), SenderName: senderName,
		Text: text, DirectedToBot: ctx.Event.IsToMe, TimestampUnixMS: ctx.Event.Time * 1000,
	}, nil
}

func (b *bot) decideGroupParticipation(ctx context.Context, batch groupWindowBatch) (groupWindowDecision, error) {
	if b == nil || ctx == nil || batch.send == nil || len(batch.messages) == 0 {
		return groupWindowDecision{}, errors.New("bot group participation processor is not configured")
	}
	session, err := b.core.OpenSession(ctx, coreclient.OpenSessionRequest{
		Surface: "im_group", SurfaceKey: "onebot-group:" + strconv.FormatInt(batch.groupID, 10),
	})
	if err != nil {
		return groupWindowDecision{}, fmt.Errorf("open group session: %w", err)
	}
	participation, err := b.core.DecideGroupParticipation(ctx, session.ConversationID, coreclient.GroupParticipationRequest{
		EvaluationReason: batch.evaluationReason, Messages: batch.messages,
	})
	if err != nil {
		return groupWindowDecision{}, fmt.Errorf("decide group participation: %w", err)
	}
	return groupWindowDecision{GroupParticipationResponse: participation, conversationID: session.ConversationID}, nil
}

func (b *bot) replyToGroupWindow(ctx context.Context, batch groupWindowBatch, decision groupWindowDecision) error {
	if b == nil || ctx == nil || batch.send == nil || len(batch.messages) == 0 || decision.conversationID == "" || decision.TargetMessageID == nil {
		return errors.New("bot group reply processor is not configured")
	}
	input, err := formatGroupTurnInput(batch.messages, *decision.TargetMessageID)
	if err != nil {
		return err
	}
	_, err = b.turns.Run(ctx, turnclient.Request{
		ConversationID: decision.conversationID, Input: input, Surface: "im_group",
	}, func(event turnclient.Event) error {
		if event.Beat == nil || event.Beat.Kind != "final" {
			return nil
		}
		return batch.send(event.Beat.DisplayText)
	})
	return err
}

func formatGroupTurnInput(messages []coreclient.GroupObservation, targetMessageID string) (string, error) {
	var builder strings.Builder
	targets := 0
	for index, observation := range messages {
		if index > 0 {
			builder.WriteByte('\n')
		}
		if observation.MessageID == targetMessageID {
			builder.WriteString("[reply-target]")
			targets++
		}
		fmt.Fprintf(&builder, "[%s/%s] %s", observation.SenderName, observation.SenderID, observation.Text)
	}
	if targets != 1 {
		return "", errors.New("group reply target must match exactly one observation")
	}
	return builder.String(), nil
}

func runBot(ctx context.Context, cfg Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	core, err := coreclient.New(coreclient.Options{Endpoint: cfg.CoreEndpoint, Token: cfg.CoreToken})
	if err != nil {
		return err
	}
	b, err := newBot(ctx, cfg, core)
	if err != nil {
		return err
	}
	defer b.windows.Close()
	engine := zero.New()
	b.register(engine)
	defer engine.Delete()
	go zero.Run(&zero.Config{
		RingLen: 16, Latency: time.Millisecond,
		Driver: []zero.Driver{driver.NewHTTPClient(
			cfg.OneBotWebhookEndpoint, cfg.OneBotToken,
			cfg.OneBotAPIEndpoint, cfg.OneBotToken,
		)},
	})
	<-ctx.Done()
	return nil
}
