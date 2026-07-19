package coreclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"time"
)

func (c *Client) OpenSession(ctx context.Context, request OpenSessionRequest) (OpenSessionResponse, error) {
	body, _ := json.Marshal(request)
	var result OpenSessionResponse
	err := c.doJSON(ctx, "open session", http.MethodPost, "/v1/sessions", body, &result)
	if err == nil && (result.ConversationID == "" || result.CharacterID == "" || result.Surface == "") {
		err = errors.New("open session response is missing required fields")
	}
	return result, err
}

func (c *Client) SubmitTurn(ctx context.Context, conversationID string, request SubmitTurnRequest) (SubmitTurnResponse, error) {
	body, _ := json.Marshal(request)
	path := "/v1/sessions/" + url.PathEscape(conversationID) + "/turns"
	var result SubmitTurnResponse
	// Turn execution is synchronous and intentionally follows the caller context,
	// not the finite client timeout.
	err := c.doJSONWithoutTimeout(ctx, "submit turn", http.MethodPost, path, body, &result)
	if err == nil && (result.Outcome.ConversationID == "" || result.Outcome.TurnID == "" || result.Surface == "") {
		err = errors.New("submit turn response is missing required fields")
	}
	return result, err
}

func (c *Client) CancelTurn(ctx context.Context, conversationID, turnID string) error {
	path := "/v1/sessions/" + url.PathEscape(conversationID) + "/turns/" + url.PathEscape(turnID) + "/cancel"
	var result struct {
		OK bool `json:"ok"`
	}
	return c.doJSON(ctx, "cancel turn", http.MethodPost, path, nil, &result)
}

func (c *Client) OpenEvents(ctx context.Context, conversationID string, readyTimeout time.Duration) (EventStream, error) {
	path := "/v1/sessions/" + url.PathEscape(conversationID) + "/events"
	return c.openReadyStream(ctx, "follow session events", path, readyTimeout)
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
