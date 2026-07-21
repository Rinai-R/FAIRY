package bridge

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"fairy-surfaces/turnclient"
	"fairy/coreclient"
	zero "github.com/wdvxdr1123/ZeroBot"
	"github.com/wdvxdr1123/ZeroBot/message"
)

type ActionSender interface {
	SendGroupText(context.Context, int64, string) error
}
type TurnRunner interface {
	Run(context.Context, turnclient.Request, turnclient.Callback) (turnclient.Result, error)
}
type SessionOpener interface {
	OpenSession(context.Context, coreclient.OpenSessionRequest) (coreclient.OpenSessionResponse, error)
}

type Runner struct {
	Core     TurnRunner
	Sessions SessionOpener
}

func (r *Runner) Handle(ctx context.Context, groupID int64, input string, sender ActionSender) error {
	if r == nil || r.Core == nil || r.Sessions == nil || sender == nil {
		return errors.New("bridge runner is not configured")
	}
	if groupID == 0 || input == "" {
		return errors.New("group ID and input are required")
	}
	session, err := r.Sessions.OpenSession(ctx, coreclient.OpenSessionRequest{Surface: "im_group", SurfaceKey: "onebot-group:" + strconv.FormatInt(groupID, 10)})
	if err != nil {
		return fmt.Errorf("opening group session: %w", err)
	}
	_, err = r.Core.Run(ctx, turnclient.Request{ConversationID: session.ConversationID, Input: input, Surface: "im_group"}, func(event turnclient.Event) error {
		if event.Beat == nil || event.Beat.Kind != "final" {
			return nil
		}
		if err := sender.SendGroupText(ctx, groupID, event.Beat.DisplayText); err != nil {
			return fmt.Errorf("sending group beat: %w", err)
		}
		return nil
	})
	return err
}

type ZeroActions struct {
	Context *zero.Ctx
}

func (a ZeroActions) SendGroupText(ctx context.Context, groupID int64, text string) error {
	_, err := a.call(ctx, "send_group_msg", zero.Params{
		"group_id": groupID,
		"message":  message.Message{message.Text(text)},
	})
	return err
}

func (a ZeroActions) IsSelfMessage(ctx context.Context, messageID string) (bool, error) {
	response, err := a.call(ctx, "get_msg", zero.Params{"message_id": messageID})
	if err != nil {
		return false, err
	}
	senderID := response.Data.Get("sender.user_id")
	if !senderID.Exists() {
		return false, errors.New("get_msg response is missing sender.user_id")
	}
	return senderID.Int() == a.Context.Event.SelfID, nil
}

func (a ZeroActions) call(ctx context.Context, action string, params zero.Params) (zero.APIResponse, error) {
	if a.Context == nil {
		return zero.APIResponse{}, errors.New("ZeroBot context is required")
	}
	response := a.Context.CallActionWithContext(ctx, action, params)
	if response.Status != "ok" || response.RetCode != 0 {
		return zero.APIResponse{}, fmt.Errorf("ZeroBot action %s failed with retcode %d", action, response.RetCode)
	}
	return response, nil
}
