package session

import (
	"errors"
	"strings"
)

const (
	DesktopCaptureMaxDecodedBytes = 768 << 10
	DesktopCaptureMaxFrameBytes   = 2 << 20
	DesktopCaptureMaxDimension    = 8192
	DesktopCaptureMaxPixels       = 16_000_000
)

type OpenRequest struct {
	Endpoint    EndpointKind `json:"endpoint"`
	EndpointKey string       `json:"endpointKey"`
	Interaction Context      `json:"interaction"`
}

type OpenResponse struct {
	ConversationID string       `json:"conversationId"`
	CharacterID    string       `json:"characterId"`
	MessageCount   int          `json:"messageCount"`
	Endpoint       EndpointKind `json:"endpoint"`
}

type MessageRecord struct {
	ID              string `json:"id"`
	ConversationID  string `json:"conversationId"`
	TurnID          string `json:"turnId"`
	Sequence        uint64 `json:"sequence"`
	Role            string `json:"role"`
	Content         string `json:"content"`
	CreatedAtUnixMS int64  `json:"createdAtUnixMs"`
}

type MessagePage struct {
	Messages           []MessageRecord `json:"messages"`
	NextBeforeSequence *uint64         `json:"nextBeforeSequence,omitempty"`
}

type AmbientObservation struct {
	MessageID       string `json:"messageId"`
	SenderID        string `json:"senderId"`
	SenderName      string `json:"senderName"`
	Text            string `json:"text"`
	DirectedToBot   bool   `json:"directedToBot"`
	IsNew           bool   `json:"isNew"`
	TimestampUnixMS int64  `json:"timestampUnixMs"`
}

type ParticipationRequest struct {
	EvaluationReason string               `json:"evaluationReason"`
	Messages         []AmbientObservation `json:"messages"`
}

type ParticipationResponse struct {
	Action          string  `json:"action"`
	TargetMessageID *string `json:"targetMessageId,omitempty"`
	WaitSeconds     *int    `json:"waitSeconds,omitempty"`
}

type DesktopCaptureRequest struct {
	ExecutionID      string   `json:"executionId"`
	ConversationID   string   `json:"conversationId"`
	TurnID           string   `json:"turnId"`
	CallID           string   `json:"callId"`
	DeadlineUnixMS   int64    `json:"deadlineUnixMs"`
	MaxDecodedBytes  int      `json:"maxDecodedBytes"`
	MaxDimension     int      `json:"maxDimension"`
	AllowedMIMETypes []string `json:"allowedMimeTypes"`
}

func (request DesktopCaptureRequest) Validate() error {
	if strings.TrimSpace(request.ExecutionID) == "" || strings.TrimSpace(request.ConversationID) == "" || strings.TrimSpace(request.TurnID) == "" || strings.TrimSpace(request.CallID) == "" {
		return errors.New("desktop capture request is missing correlation fields")
	}
	if request.DeadlineUnixMS <= 0 {
		return errors.New("desktop capture request deadline is required")
	}
	if request.MaxDecodedBytes <= 0 || request.MaxDecodedBytes > DesktopCaptureMaxDecodedBytes {
		return errors.New("desktop capture request byte limit is invalid")
	}
	if request.MaxDimension <= 0 || request.MaxDimension > DesktopCaptureMaxDimension {
		return errors.New("desktop capture request dimension limit is invalid")
	}
	if len(request.AllowedMIMETypes) == 0 || len(request.AllowedMIMETypes) > 2 {
		return errors.New("desktop capture request MIME types are invalid")
	}
	seen := make(map[string]struct{}, len(request.AllowedMIMETypes))
	for _, mediaType := range request.AllowedMIMETypes {
		if mediaType != "image/png" && mediaType != "image/jpeg" {
			return errors.New("desktop capture request MIME type is unsupported")
		}
		if _, ok := seen[mediaType]; ok {
			return errors.New("desktop capture request MIME type is duplicated")
		}
		seen[mediaType] = struct{}{}
	}
	return nil
}

type DesktopCaptureResult struct {
	ExecutionID    string `json:"executionId"`
	ConversationID string `json:"conversationId"`
	TurnID         string `json:"turnId"`
	CallID         string `json:"callId"`
	Status         string `json:"status"`
	MediaType      string `json:"mediaType,omitempty"`
	Width          int    `json:"width,omitempty"`
	Height         int    `json:"height,omitempty"`
	ByteCount      int    `json:"byteCount,omitempty"`
	SHA256         string `json:"sha256,omitempty"`
	DataURL        string `json:"dataUrl,omitempty"`
	ErrorCode      string `json:"errorCode,omitempty"`
	ErrorMessage   string `json:"errorMessage,omitempty"`
}

func (result DesktopCaptureResult) ValidateShape() error {
	if strings.TrimSpace(result.ExecutionID) == "" || strings.TrimSpace(result.ConversationID) == "" || strings.TrimSpace(result.TurnID) == "" || strings.TrimSpace(result.CallID) == "" {
		return errors.New("desktop capture result is missing correlation fields")
	}
	switch result.Status {
	case "completed":
		if result.MediaType == "" || result.Width <= 0 || result.Height <= 0 || result.ByteCount <= 0 || result.SHA256 == "" || result.DataURL == "" || result.ErrorCode != "" || result.ErrorMessage != "" {
			return errors.New("completed desktop capture result is invalid")
		}
	case "failed":
		if strings.TrimSpace(result.ErrorCode) == "" || result.MediaType != "" || result.Width != 0 || result.Height != 0 || result.ByteCount != 0 || result.SHA256 != "" || result.DataURL != "" {
			return errors.New("failed desktop capture result is invalid")
		}
	default:
		return errors.New("desktop capture result status is invalid")
	}
	return nil
}
