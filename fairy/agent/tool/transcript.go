package tool

import (
	"encoding/json"
	"errors"
	"time"
	"unicode/utf8"

	history "fairy/context/history/transcript"
	"fairy/runtime/model"
)

const (
	maxTranscriptProjectionTurns        = 5
	maxTranscriptProjectionMessages     = 10
	maxTranscriptProjectionMessageRunes = 800
	maxTranscriptProjectionTotalRunes   = 4000
)

type TranscriptMessage struct {
	MessageID string `json:"messageId,omitempty"`
	Role      string `json:"role"`
	Content   string `json:"content"`
	Sequence  uint64 `json:"sequence"`
}

type TranscriptTurn struct {
	TurnID   string              `json:"turnId"`
	Messages []TranscriptMessage `json:"messages"`
}

type TranscriptContext struct {
	ContextType string           `json:"contextType"`
	Turns       []TranscriptTurn `json:"turns"`
	Truncated   bool             `json:"truncated"`
}

type TranscriptReceipt struct {
	Status       string `json:"status"`
	Empty        bool   `json:"empty"`
	TurnCount    int    `json:"turnCount"`
	MessageCount int    `json:"messageCount"`
	Truncated    bool   `json:"truncated"`
}

type TranscriptRuntimeDetail struct {
	Version   string             `json:"version"`
	Arguments RuntimeArguments   `json:"arguments"`
	Receipt   TranscriptReceipt  `json:"receipt"`
	Result    *TranscriptContext `json:"result,omitempty"`
}

func ProjectTranscriptRecall(result history.CompactedTranscriptRecall) TranscriptContext {
	projected := TranscriptContext{ContextType: "compacted_transcript_recall", Turns: []TranscriptTurn{}, Truncated: result.Truncated}
	totalRunes := 0
	messageCount := 0
	for _, turn := range result.Turns {
		if len(projected.Turns) >= maxTranscriptProjectionTurns || messageCount >= maxTranscriptProjectionMessages {
			projected.Truncated = true
			break
		}
		projectedTurn := TranscriptTurn{TurnID: runtimeInspectionText(turn.TurnID, 128), Messages: []TranscriptMessage{}}
		for _, message := range turn.Messages {
			if messageCount >= maxTranscriptProjectionMessages || totalRunes >= maxTranscriptProjectionTotalRunes {
				projected.Truncated = true
				break
			}
			remaining := maxTranscriptProjectionTotalRunes - totalRunes
			limit := maxTranscriptProjectionMessageRunes
			if remaining < limit {
				limit = remaining
			}
			content, truncated := truncateTranscriptText(message.Content, limit)
			if truncated {
				projected.Truncated = true
			}
			projectedTurn.Messages = append(projectedTurn.Messages, TranscriptMessage{
				MessageID: runtimeInspectionText(message.MessageID, 128),
				Role:      runtimeInspectionText(message.Role, 32), Content: content, Sequence: message.Sequence,
			})
			totalRunes += utf8.RuneCountInString(content)
			messageCount++
		}
		if len(projectedTurn.Messages) > 0 {
			projected.Turns = append(projected.Turns, projectedTurn)
		}
	}
	return projected
}

func TranscriptPromptItems(call model.FunctionCall, status string, result history.CompactedTranscriptRecall) []model.PromptItem {
	projection := ProjectTranscriptRecall(result)
	receipt := transcriptReceipt(status, projection)
	receiptJSON, err := json.Marshal(receipt)
	if err != nil {
		receiptJSON = []byte(`{"status":"failed","empty":true,"turnCount":0,"messageCount":0,"truncated":false}`)
	}
	parts := model.PromptContentParts{{Type: model.PromptContentText, Text: string(receiptJSON)}}
	items := []model.PromptItem{
		{Type: model.PromptItemToolCall, ToolCallID: call.CallID, ToolName: call.Name, ToolArguments: call.Arguments},
		{Type: model.PromptItemToolResult, ToolCallID: call.CallID, Parts: &parts},
	}
	if receipt.Status == "ok" {
		contextJSON, err := json.Marshal(projection)
		if err == nil {
			items = append(items, model.PromptItem{Type: model.PromptItemContextData, Content: string(contextJSON)})
		}
	}
	return items
}

func TranscriptRuntimeProjection(query, status string, result history.CompactedTranscriptRecall) TranscriptRuntimeDetail {
	projection := ProjectTranscriptRecall(result)
	detail := TranscriptRuntimeDetail{
		Version: "v1", Arguments: RuntimeArguments{Query: runtimeInspectionText(query, maxToolQueryRunes)},
		Receipt: transcriptReceipt(status, projection),
	}
	if detail.Receipt.Status == "ok" {
		detail.Result = &projection
	}
	return detail
}

func transcriptReceipt(status string, projection TranscriptContext) TranscriptReceipt {
	status = stableRetrievalToolStatus(status)
	messageCount := 0
	for _, turn := range projection.Turns {
		messageCount += len(turn.Messages)
	}
	return TranscriptReceipt{
		Status: status, Empty: len(projection.Turns) == 0, TurnCount: len(projection.Turns),
		MessageCount: messageCount, Truncated: projection.Truncated,
	}
}

func truncateTranscriptText(value string, maxRunes int) (string, bool) {
	if maxRunes <= 0 {
		return "", value != ""
	}
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value, false
	}
	return string(runes[:maxRunes]), true
}

func TranscriptContextSegments(items []model.PromptItem, createdAt time.Time) ([]model.ContextSegment, error) {
	if len(items) != 2 && len(items) != 3 {
		return nil, errors.New("transcript tool context requires a call, result, and optional context projection")
	}
	if len(items) == 3 && items[2].Type != model.PromptItemContextData {
		return nil, errors.New("transcript tool projection must be context data")
	}
	if len(items) == 2 {
		return ContextSegments(items, createdAt)
	}
	base, err := ContextSegments(items[:2], createdAt)
	if err != nil {
		return nil, err
	}
	contextItem := items[2]
	contextID := "tool:" + items[0].ToolCallID + ":context"
	expiresAtMS := createdAt.Add(ResultSegmentTTL).UnixMilli()
	base = append(base, model.ContextSegment{
		ID: contextID, Ordinal: 3, Kind: model.ContextSegmentContextData, Item: &contextItem,
		CreatedAtUnixMS: createdAt.UnixMilli(), ExpiresAtUnixMS: &expiresAtMS,
		RetentionPolicy: model.ContextRetentionTTL, TokenCount: PromptItemTokenCount(contextItem),
		Recoverability: model.ContextRecoverabilityTranscript,
		Dependencies:   []string{base[1].ID}, ProjectionState: model.ContextProjectionActive,
	})
	if err := model.ValidateContextSegments(base); err != nil {
		return nil, err
	}
	return base, nil
}
