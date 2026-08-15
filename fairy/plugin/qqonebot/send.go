package qqonebot

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"strings"

	"fairy/plugin"
)

type SendRequest struct {
	Op             string `json:"op"`
	Kind           string `json:"kind"`
	APIBaseURL     string `json:"apiBaseURL"`
	Credential     string `json:"credential"`
	GroupID        string `json:"groupId"`
	UserID         string `json:"userId"`
	Text           string `json:"text"`
	ImageBase64    string `json:"imageBase64,omitempty"`
	ReplyMessageID string `json:"replyMessageId"`
}

type Receipt struct {
	Status            string `json:"status"`
	ExternalMessageID string `json:"externalMessageId,omitempty"`
	ErrorCode         string `json:"errorCode,omitempty"`
}

func Send(ctx context.Context, call func(context.Context, string, json.RawMessage) ([]byte, error), req SendRequest) (Receipt, error) {
	base := strings.TrimRight(strings.TrimSpace(req.APIBaseURL), "/")
	if base == "" {
		return Receipt{}, errors.New("OneBot API base URL is required")
	}
	path, body, err := encodeSendBody(req)
	if err != nil {
		return Receipt{}, err
	}
	target, err := url.JoinPath(base, path)
	if err != nil {
		return Receipt{}, errors.New("OneBot API URL is invalid")
	}
	payload, err := json.Marshal(map[string]string{
		"method":     "POST",
		"url":        target,
		"body":       string(body),
		"credential": req.Credential,
	})
	if err != nil {
		return Receipt{}, err
	}
	raw, err := call(ctx, "http.request", payload)
	if err != nil {
		return Receipt{}, err
	}
	var result struct {
		OK      bool            `json:"ok"`
		Code    string          `json:"code"`
		Message string          `json:"message"`
		Body    json.RawMessage `json:"body"`
	}
	if err := json.Unmarshal(raw, &result); err != nil || !result.OK {
		code := result.Code
		if code == "" {
			code = plugin.CodeModuleTrap
		}
		message := result.Message
		if message == "" {
			message = "http.request failed"
		}
		return Receipt{}, &plugin.CodedError{Code: code, Message: message}
	}
	var httpBody struct {
		Status int    `json:"status"`
		Body   string `json:"body"`
	}
	if err := json.Unmarshal(result.Body, &httpBody); err != nil {
		return Receipt{}, &plugin.CodedError{Code: plugin.CodeModuleTrap, Message: "http.request body is invalid"}
	}
	if httpBody.Status < 200 || httpBody.Status >= 300 {
		return Receipt{Status: "failed", ErrorCode: plugin.CodeModuleTrap}, nil
	}
	var api struct {
		Status  string `json:"status"`
		Retcode int    `json:"retcode"`
		Data    struct {
			MessageID json.RawMessage `json:"message_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(httpBody.Body), &api); err != nil {
		return Receipt{Status: "failed", ErrorCode: plugin.CodeModuleTrap}, nil
	}
	messageID, err := decodeMessageID(api.Data.MessageID)
	if err != nil || api.Status != "ok" || api.Retcode != 0 || numericMessageIDZero(messageID) {
		return Receipt{Status: "failed", ErrorCode: plugin.CodeModuleTrap}, nil
	}
	return Receipt{Status: "succeeded", ExternalMessageID: messageID}, nil
}

func numericMessageIDZero(messageID string) bool {
	id, err := strconv.ParseInt(messageID, 10, 64)
	return err == nil && id == 0
}

func encodeSendBody(req SendRequest) (string, []byte, error) {
	message, err := encodeOneBotMessage(req)
	if err != nil {
		return "", nil, err
	}
	if strings.TrimSpace(req.ReplyMessageID) != "" {
		message = "[CQ:reply,id=" + req.ReplyMessageID + "]" + message
	}
	switch {
	case strings.TrimSpace(req.GroupID) != "":
		id, err := strconv.ParseInt(req.GroupID, 10, 64)
		if err != nil || id <= 0 {
			return "", nil, errors.New("group ID is invalid")
		}
		body, err := json.Marshal(map[string]any{"group_id": id, "message": message})
		return "send_group_msg", body, err
	case strings.TrimSpace(req.UserID) != "":
		id, err := strconv.ParseInt(req.UserID, 10, 64)
		if err != nil || id <= 0 {
			return "", nil, errors.New("user ID is invalid")
		}
		body, err := json.Marshal(map[string]any{"user_id": id, "message": message})
		return "send_private_msg", body, err
	default:
		return "", nil, errors.New("send target is required")
	}
}

func encodeOneBotMessage(req SendRequest) (string, error) {
	if req.ImageBase64 != "" {
		if strings.ContainsAny(req.ImageBase64, "[]=\r\n,") {
			return "", errors.New("sticker payload is invalid")
		}
		return "[CQ:image,file=base64://" + req.ImageBase64 + "]", nil
	}
	if strings.TrimSpace(req.Text) == "" {
		return "", errors.New("send text is required")
	}
	return req.Text, nil
}
