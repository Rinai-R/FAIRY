package coreclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func (c *Client) OpenSession(ctx context.Context, request OpenSessionRequest) (OpenSessionResponse, error) {
	var result OpenSessionResponse
	err := c.sessionRPC(ctx, c.timeout, func(ctx context.Context, socket *SessionSocket) error {
		opened, openErr := socket.OpenSession(ctx, request)
		if openErr != nil {
			return openErr
		}
		result = opened
		return nil
	})
	return result, err
}

func (c *Client) DecideGroupParticipation(ctx context.Context, conversationID string, request GroupParticipationRequest) (GroupParticipationResponse, error) {
	var result GroupParticipationResponse
	err := c.sessionRPC(ctx, c.timeout, func(ctx context.Context, socket *SessionSocket) error {
		decision, decideErr := socket.DecideGroupParticipation(ctx, conversationID, request)
		if decideErr != nil {
			return decideErr
		}
		result = decision
		return nil
	})
	return result, err
}

func validateGroupParticipationResponse(result GroupParticipationResponse) error {
	switch result.Action {
	case "reply":
		if result.TargetMessageID == nil || strings.TrimSpace(*result.TargetMessageID) == "" || result.WaitSeconds != nil {
			return errors.New("group participation reply response is invalid")
		}
	case "wait":
		if result.TargetMessageID != nil || result.WaitSeconds == nil || *result.WaitSeconds < 1 || *result.WaitSeconds > 300 {
			return errors.New("group participation wait response is invalid")
		}
	case "silent":
		if result.TargetMessageID != nil || result.WaitSeconds != nil {
			return errors.New("group participation silent response is invalid")
		}
	default:
		return errors.New("group participation response has invalid action")
	}
	return nil
}

func (c *Client) ListMessages(ctx context.Context, conversationID string, beforeSequence uint64, limit int) (MessagePage, error) {
	values := url.Values{}
	if beforeSequence != 0 {
		values.Set("beforeSequence", strconv.FormatUint(beforeSequence, 10))
	}
	if limit != 0 {
		values.Set("limit", strconv.Itoa(limit))
	}
	path := "/v1/sessions/" + url.PathEscape(conversationID) + "/messages"
	if query := values.Encode(); query != "" {
		path += "?" + query
	}
	var result MessagePage
	err := c.doJSON(ctx, "list session messages", http.MethodGet, path, nil, &result)
	if err == nil && result.Messages == nil {
		err = errors.New("message page response is missing messages")
	}
	return result, err
}

func (c *Client) SubmitTurn(ctx context.Context, conversationID string, request SubmitTurnRequest) (SubmitTurnResponse, error) {
	socket, err := c.DialSession(ctx)
	if err != nil {
		return SubmitTurnResponse{}, err
	}
	defer socket.Close()
	return socket.SubmitTurn(ctx, conversationID, request)
}

func (c *Client) CancelTurn(ctx context.Context, conversationID, turnID string) error {
	return c.sessionRPC(ctx, c.timeout, func(ctx context.Context, socket *SessionSocket) error {
		return socket.CancelTurn(ctx, conversationID, turnID)
	})
}

func (c *Client) OpenEvents(ctx context.Context, conversationID string, readyTimeout time.Duration) (EventStream, error) {
	_ = readyTimeout
	socket, err := c.DialSession(ctx)
	if err != nil {
		return nil, err
	}
	ch, err := socket.Watch(ctx, conversationID)
	if err != nil {
		_ = socket.Close()
		return nil, err
	}
	return &wsEventStream{ctx: ctx, socket: socket, ch: ch}, nil
}

func (c *Client) ObserveGroup(ctx context.Context, conversationID string, message GroupObservation) error {
	return c.sessionRPC(ctx, c.timeout, func(ctx context.Context, socket *SessionSocket) error {
		return socket.ObserveGroup(ctx, conversationID, message)
	})
}

func DecodeHarnessEvent(event SSEEvent) (HarnessEvent, error) {
	var result HarnessEvent
	if err := json.Unmarshal(event.Data, &result); err != nil {
		return HarnessEvent{}, err
	}
	if result.ConversationID == "" || result.TurnID == "" || result.Sequence == 0 || result.State == "" || len(result.Payload) == 0 {
		return HarnessEvent{}, errors.New("invalid harness event")
	}
	return result, nil
}
