package tool

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"fairy/runtime/model"
)

const ResultSegmentTTL = 2 * time.Minute

func ContextSegments(items []model.PromptItem, createdAt time.Time) ([]model.ContextSegment, error) {
	if len(items) != 2 || items[0].Type != model.PromptItemToolCall || items[1].Type != model.PromptItemToolResult {
		return nil, errors.New("tool context requires one call followed by one result")
	}
	call := items[0]
	result := items[1]
	if strings.TrimSpace(call.ToolCallID) == "" || call.ToolCallID != result.ToolCallID {
		return nil, errors.New("tool context call and result ids must match")
	}
	callSegmentID := fmt.Sprintf("tool:%s:call", call.ToolCallID)
	resultSegmentID := fmt.Sprintf("tool:%s:result", call.ToolCallID)
	createdAtMS := createdAt.UnixMilli()
	expiresAtMS := createdAt.Add(ResultSegmentTTL).UnixMilli()
	recoverability := model.ContextRecoverabilityRefetchable
	if call.ToolName == DesktopObserve {
		recoverability = model.ContextRecoverabilityEphemeral
	}
	segments := []model.ContextSegment{
		{
			ID: callSegmentID, Ordinal: 1, Kind: model.ContextSegmentToolCall, Item: &call,
			CreatedAtUnixMS: createdAtMS, RetentionPolicy: model.ContextRetentionCurrentTurn,
			TokenCount: PromptItemTokenCount(call), Recoverability: model.ContextRecoverabilityRequired,
			ProjectionState: model.ContextProjectionActive,
		},
		{
			ID: resultSegmentID, Ordinal: 2, Kind: model.ContextSegmentToolResult, Item: &result,
			CreatedAtUnixMS: createdAtMS, ExpiresAtUnixMS: &expiresAtMS, RetentionPolicy: model.ContextRetentionTTL,
			TokenCount: PromptItemTokenCount(result), Recoverability: recoverability, Dependencies: []string{callSegmentID},
			ProjectionState: model.ContextProjectionActive,
		},
	}
	if err := model.ValidateContextSegments(segments); err != nil {
		return nil, err
	}
	return segments, nil
}

func PromptItemTokenCount(item model.PromptItem) uint64 {
	runes := utf8.RuneCountInString(item.Content) +
		utf8.RuneCountInString(item.ToolName) +
		utf8.RuneCountInString(item.ToolArguments) + 12
	if item.Parts != nil {
		for _, part := range *item.Parts {
			runes += utf8.RuneCountInString(part.Text)
			runes += utf8.RuneCountInString(part.ImageDataURL)
		}
	}
	return uint64((runes + 3) / 4)
}
