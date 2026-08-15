package qqonebot

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

type Mention struct {
	UserID      string `json:"userId"`
	DisplayName string `json:"displayName"`
}

type Event struct {
	Kind            string    `json:"kind"`
	MessageID       string    `json:"messageId"`
	UserID          string    `json:"userId"`
	GroupID         string    `json:"groupId,omitempty"`
	SenderName      string    `json:"senderName"`
	Text            string    `json:"text"`
	Mentions        []Mention `json:"mentions"`
	DirectedToBot   bool      `json:"directedToBot"`
	TimestampUnixMS int64     `json:"timestampUnixMs"`
	EndpointKey     string    `json:"endpointKey"`
}

type Segment struct {
	Type string
	Data map[string]string
}

func ParseEvent(raw json.RawMessage, selfID string) (Event, error) {
	var payload struct {
		PostType    string          `json:"post_type"`
		MessageType string          `json:"message_type"`
		Time        int64           `json:"time"`
		MessageID   json.RawMessage `json:"message_id"`
		UserID      int64           `json:"user_id"`
		GroupID     int64           `json:"group_id"`
		SelfID      int64           `json:"self_id"`
		RawMessage  string          `json:"raw_message"`
		Message     json.RawMessage `json:"message"`
		IsToMe      bool            `json:"to_me"`
		Sender      struct {
			Card     string `json:"card"`
			Nickname string `json:"nickname"`
		} `json:"sender"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return Event{}, errors.New("OneBot event is invalid")
	}
	if payload.PostType != "message" || (payload.MessageType != "group" && payload.MessageType != "private") {
		return Event{}, errors.New("OneBot event is not a message")
	}
	if payload.UserID <= 0 || payload.Time <= 0 {
		return Event{}, errors.New("sender ID and timestamp are required")
	}
	messageID, err := decodeMessageID(payload.MessageID)
	if err != nil {
		return Event{}, err
	}
	segments := parseMessage(payload.Message, payload.RawMessage)
	text, mentions, err := projectMessage(segments)
	if err != nil {
		return Event{}, err
	}
	if text == "" {
		return Event{}, errors.New("readable message text is empty")
	}
	senderName := strings.TrimSpace(payload.Sender.Card)
	if senderName == "" {
		senderName = strings.TrimSpace(payload.Sender.Nickname)
	}
	if senderName == "" {
		return Event{}, errors.New("sender name, sender ID, and timestamp are required")
	}
	self := strings.TrimSpace(selfID)
	if self == "" && payload.SelfID > 0 {
		self = strconv.FormatInt(payload.SelfID, 10)
	}
	directed := payload.IsToMe
	if !directed && self != "" {
		for _, mention := range mentions {
			if mention.UserID == self {
				directed = true
				break
			}
		}
	}
	event := Event{
		Kind:            payload.MessageType,
		MessageID:       messageID,
		UserID:          strconv.FormatInt(payload.UserID, 10),
		SenderName:      senderName,
		Text:            text,
		Mentions:        mentions,
		DirectedToBot:   directed,
		TimestampUnixMS: payload.Time * 1000,
	}
	if payload.MessageType == "group" {
		if payload.GroupID <= 0 {
			return Event{}, errors.New("group ID is required")
		}
		event.GroupID = strconv.FormatInt(payload.GroupID, 10)
		event.EndpointKey = "onebot-group:" + event.GroupID
	} else {
		event.EndpointKey = "onebot-private:" + event.UserID
	}
	return event, nil
}

func decodeMessageID(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", errors.New("message ID is required")
	}
	var asString string
	if err := json.Unmarshal(raw, &asString); err == nil {
		if strings.TrimSpace(asString) == "" {
			return "", errors.New("message ID is required")
		}
		return asString, nil
	}
	var asNumber json.Number
	if err := json.Unmarshal(raw, &asNumber); err == nil {
		return asNumber.String(), nil
	}
	return "", errors.New("message ID is required")
}

func parseMessage(message json.RawMessage, raw string) []Segment {
	if len(message) > 0 && string(message) != "null" {
		var asString string
		if err := json.Unmarshal(message, &asString); err == nil && strings.TrimSpace(asString) != "" {
			if segments := ParseCQ(asString); len(segments) > 0 {
				return segments
			}
		}
		var asArray []map[string]any
		if err := json.Unmarshal(message, &asArray); err == nil && len(asArray) > 0 {
			segments := make([]Segment, 0, len(asArray))
			for _, item := range asArray {
				typ, _ := item["type"].(string)
				data := map[string]string{}
				switch rawData := item["data"].(type) {
				case map[string]any:
					for key, value := range rawData {
						data[key] = fmt.Sprint(value)
					}
				}
				segments = append(segments, Segment{Type: typ, Data: data})
			}
			if len(segments) > 0 {
				return segments
			}
		}
	}
	if strings.TrimSpace(raw) != "" {
		return ParseCQ(raw)
	}
	return nil
}

func ParseCQ(raw string) []Segment {
	segments := make([]Segment, 0)
	rest := raw
	for rest != "" {
		start := strings.Index(rest, "[CQ:")
		if start < 0 {
			if text := rest; text != "" {
				segments = append(segments, Segment{Type: "text", Data: map[string]string{"text": text}})
			}
			break
		}
		if start > 0 {
			segments = append(segments, Segment{Type: "text", Data: map[string]string{"text": rest[:start]}})
		}
		end := strings.Index(rest[start:], "]")
		if end < 0 {
			segments = append(segments, Segment{Type: "text", Data: map[string]string{"text": rest[start:]}})
			break
		}
		end += start
		body := rest[start+4 : end]
		comma := strings.Index(body, ",")
		typ := body
		data := map[string]string{}
		if comma >= 0 {
			typ = body[:comma]
			for _, field := range strings.Split(body[comma+1:], ",") {
				key, value, ok := strings.Cut(field, "=")
				if !ok {
					continue
				}
				data[key] = value
			}
		}
		segments = append(segments, Segment{Type: typ, Data: data})
		rest = rest[end+1:]
	}
	return segments
}

func projectMessage(elements []Segment) (string, []Mention, error) {
	parts := make([]string, 0, len(elements))
	mentions := make([]Mention, 0)
	seen := make(map[string]struct{})
	for _, element := range elements {
		switch element.Type {
		case "text":
			if text := strings.TrimSpace(element.Data["text"]); text != "" {
				parts = append(parts, text)
			}
		case "at":
			userID := strings.TrimSpace(element.Data["qq"])
			if userID == "all" {
				parts = append(parts, "@全体成员")
				continue
			}
			parsed, err := strconv.ParseInt(userID, 10, 64)
			if err != nil || parsed <= 0 || strconv.FormatInt(parsed, 10) != userID {
				return "", nil, fmt.Errorf("OneBot at segment contains invalid user ID %q", userID)
			}
			displayName := strings.TrimSpace(element.Data["name"])
			if displayName == "" {
				displayName = userID
			}
			parts = append(parts, "@"+displayName)
			if _, exists := seen[userID]; exists {
				continue
			}
			seen[userID] = struct{}{}
			mentions = append(mentions, Mention{UserID: userID, DisplayName: displayName})
		}
	}
	return strings.Join(parts, " "), mentions, nil
}
