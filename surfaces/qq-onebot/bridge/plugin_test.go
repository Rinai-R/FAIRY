package bridge

import (
	"context"
	"testing"
	"time"

	"fairy-surfaces/turnclient"
	zero "github.com/wdvxdr1123/ZeroBot"
	"github.com/wdvxdr1123/ZeroBot/message"
)

type pluginActions struct {
	sent chan string
}

type blockingTurnRunner struct {
	started  chan struct{}
	canceled chan struct{}
}

func (r *blockingTurnRunner) Run(ctx context.Context, _ turnclient.Request, _ turnclient.Callback) (turnclient.Result, error) {
	close(r.started)
	<-ctx.Done()
	close(r.canceled)
	return turnclient.Result{}, ctx.Err()
}

func (a *pluginActions) SendGroupText(_ context.Context, _ int64, text string) error {
	a.sent <- text
	return nil
}

func (a *pluginActions) IsSelfMessage(context.Context, string) (bool, error) {
	return true, nil
}

func TestPluginHandlesZeroBotGroupEventOnce(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sessions := &fakeSessions{}
	runner := &Runner{
		Core:     fakeTurnRunner{events: []turnclient.Event{{Type: "beat.ready", Beat: &turnclient.BeatReady{Kind: "final", DisplayText: "第一拍"}}}},
		Sessions: sessions,
	}
	plugin, err := NewPlugin(ctx, Config{SelfID: "10001", GroupAllowlist: []string{"20001"}}, runner)
	if err != nil {
		t.Fatal(err)
	}
	defer plugin.Close()
	actions := &pluginActions{sent: make(chan string, 2)}
	plugin.actions = func(*zero.Ctx) Actions { return actions }

	event := &zero.Ctx{Event: &zero.Event{
		PostType:    "message",
		MessageType: "group",
		MessageID:   int64(30001),
		GroupID:     20001,
		UserID:      40001,
		SelfID:      10001,
		Message:     message.ParseMessage([]byte(`"[CQ:at,qq=10001] 你好"`)),
		Sender:      &zero.User{Card: "测试成员"},
	}}
	plugin.Handle(event)
	plugin.Handle(event)

	select {
	case text := <-actions.sent:
		if text != "第一拍" {
			t.Fatalf("sent text = %q", text)
		}
	case <-time.After(time.Second):
		t.Fatal("ZeroBot event was not delivered")
	}
	select {
	case duplicate := <-actions.sent:
		t.Fatalf("duplicate event sent %q", duplicate)
	case <-time.After(50 * time.Millisecond):
	}
	if sessions.request.SurfaceKey != "onebot-group:20001" {
		t.Fatalf("surface key = %q", sessions.request.SurfaceKey)
	}
}

func TestPluginContextCancellationStopsActiveCoreTurn(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	turns := &blockingTurnRunner{started: make(chan struct{}), canceled: make(chan struct{})}
	plugin, err := NewPlugin(ctx, Config{SelfID: "10001", GroupAllowlist: []string{"20001"}}, &Runner{Core: turns, Sessions: &fakeSessions{}})
	if err != nil {
		t.Fatal(err)
	}
	actions := &pluginActions{sent: make(chan string, 1)}
	plugin.actions = func(*zero.Ctx) Actions { return actions }
	plugin.Handle(&zero.Ctx{Event: &zero.Event{
		PostType: "message", MessageType: "group", MessageID: int64(30002),
		GroupID: 20001, UserID: 40001, SelfID: 10001, IsToMe: true,
		Message: message.Message{message.Text("等待")}, Sender: &zero.User{Card: "测试成员"},
	}})
	select {
	case <-turns.started:
	case <-time.After(time.Second):
		t.Fatal("Core turn did not start")
	}
	cancel()
	plugin.Close()
	select {
	case <-turns.canceled:
	case <-time.After(time.Second):
		t.Fatal("Core turn was not canceled")
	}
}
