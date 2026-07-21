package bridge

import (
	"context"
	"errors"
	"testing"

	"fairy-surfaces/turnclient"
	"fairy/coreclient"
)

type fakeSender struct {
	calls  int
	failAt int
}

func (f *fakeSender) SendGroupText(context.Context, int64, string) error {
	f.calls++
	if f.failAt > 0 && f.calls == f.failAt {
		return errors.New("action failed")
	}
	return nil
}

type fakeSessions struct{ request coreclient.OpenSessionRequest }

func (f *fakeSessions) OpenSession(_ context.Context, request coreclient.OpenSessionRequest) (coreclient.OpenSessionResponse, error) {
	f.request = request
	return coreclient.OpenSessionResponse{ConversationID: "group-conversation", Surface: "im_group"}, nil
}

type fakeTurnRunner struct{ events []turnclient.Event }

func (f fakeTurnRunner) Run(_ context.Context, _ turnclient.Request, callback turnclient.Callback) (turnclient.Result, error) {
	for _, event := range f.events {
		if err := callback(event); err != nil {
			return turnclient.Result{}, err
		}
	}
	return turnclient.Result{}, nil
}

func TestRunnerSendsOnlyFinalBeatsAndStopsOnActionFailure(t *testing.T) {
	sender := &fakeSender{failAt: 2}
	sessions := &fakeSessions{}
	events := []turnclient.Event{{Type: "beat.ready", Beat: &turnclient.BeatReady{Kind: "utterance", DisplayText: "等待"}}, {Type: "beat.ready", Beat: &turnclient.BeatReady{Kind: "final", DisplayText: "第一拍"}}, {Type: "beat.ready", Beat: &turnclient.BeatReady{Kind: "final", DisplayText: "第二拍"}}, {Type: "beat.ready", Beat: &turnclient.BeatReady{Kind: "final", DisplayText: "第三拍"}}, {Type: "completed"}}
	runner := &Runner{Core: fakeTurnRunner{events: events}, Sessions: sessions}
	if err := runner.Handle(context.Background(), 20001, "小明：你好", sender); err == nil {
		t.Fatal("action failure was ignored")
	}
	if sender.calls != 2 {
		t.Fatalf("send calls=%d, want 2 and no retry/third beat", sender.calls)
	}
	if sessions.request.Surface != "im_group" || sessions.request.SurfaceKey != "onebot-group:20001" {
		t.Fatalf("session request=%#v", sessions.request)
	}
}

func TestRunnerRejectsMissingDependencies(t *testing.T) {
	r := &Runner{}
	if err := r.Handle(context.Background(), 20001, "hi", &fakeSender{}); err == nil {
		t.Fatal("unconfigured runner accepted")
	}
}
