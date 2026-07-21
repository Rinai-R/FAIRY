package bridge

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	zero "github.com/wdvxdr1123/ZeroBot"
	"github.com/wdvxdr1123/ZeroBot/message"
)

type GroupEvent struct {
	ID         string
	GroupID    int64
	UserID     int64
	SenderName string
	Message    message.Message
	ToMe       bool
}
type ReplyVerifier interface {
	IsSelfMessage(context.Context, string) (bool, error)
}

func TriggerInput(ctx context.Context, event GroupEvent, selfID int64, allowlist map[int64]struct{}, verifier ReplyVerifier) (string, bool, error) {
	if event.ID == "" || event.GroupID == 0 || event.UserID == 0 {
		return "", false, errors.New("group event identity is incomplete")
	}
	if _, ok := allowlist[event.GroupID]; !ok {
		return "", false, nil
	}
	if event.UserID == selfID {
		return "", false, nil
	}
	text, mentions, replyID := textAndTriggers(event.Message)
	mentioned := event.ToMe
	for _, mention := range mentions {
		if mention == strconv.FormatInt(selfID, 10) {
			mentioned = true
			break
		}
	}
	if !mentioned && replyID != nil {
		if verifier == nil {
			return "", false, errors.New("reply trigger requires verifier")
		}
		var err error
		mentioned, err = verifier.IsSelfMessage(ctx, *replyID)
		if err != nil {
			return "", false, err
		}
	}
	if !mentioned || text == "" {
		return "", false, nil
	}
	name := strings.TrimSpace(event.SenderName)
	if name == "" {
		name = "群成员"
	}
	return fmt.Sprintf("%s：%s", name, text), true, nil
}

func textAndTriggers(segments message.Message) (string, []string, *string) {
	var text strings.Builder
	mentions := make([]string, 0, 1)
	var replyID *string
	for _, segment := range segments {
		switch segment.Type {
		case "text":
			text.WriteString(segment.Data["text"])
		case "at":
			mentions = append(mentions, segment.Data["qq"])
		case "reply":
			id := segment.Data["id"]
			if id != "" && replyID == nil {
				replyID = &id
			}
		}
	}
	return strings.TrimSpace(text.String()), mentions, replyID
}

func groupEventFromZero(event *zero.Event) (GroupEvent, bool) {
	if event == nil || event.PostType != "message" || event.MessageType != "group" {
		return GroupEvent{}, false
	}
	id := messageIDString(event.MessageID)
	if id == "" && len(event.RawMessageID) > 0 {
		id = strings.Trim(string(event.RawMessageID), `"`)
	}
	name := ""
	if event.Sender != nil {
		name = event.Sender.Name()
	}
	return GroupEvent{ID: id, GroupID: event.GroupID, UserID: event.UserID, SenderName: name, Message: event.Message, ToMe: event.IsToMe}, true
}

func messageIDString(id any) string {
	switch value := id.(type) {
	case int64:
		return strconv.FormatInt(value, 10)
	case int:
		return strconv.Itoa(value)
	case string:
		return value
	default:
		return ""
	}
}
