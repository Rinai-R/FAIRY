package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/jackc/pgx/v5"
)

const PromptProjectionVersion uint32 = 1

var ErrPromptWindowRevisionChanged = errors.New("prompt window revision changed")

func EmptyPromptProjection() PromptProjectionState {
	return PromptProjectionState{
		Version:   PromptProjectionVersion,
		Omissions: []PromptProjectionOmission{},
	}
}

func EncodePromptProjection(state PromptProjectionState) ([]byte, error) {
	if err := ValidatePromptProjection(state); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		return nil, fmt.Errorf("encoding prompt projection: %w", err)
	}
	return encoded, nil
}

func DecodePromptProjection(encoded []byte) (PromptProjectionState, error) {
	if len(bytes.TrimSpace(encoded)) == 0 {
		return PromptProjectionState{}, errors.New("prompt projection is empty")
	}
	var state PromptProjectionState
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return PromptProjectionState{}, fmt.Errorf("decoding prompt projection: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return PromptProjectionState{}, errors.New("prompt projection contains trailing data")
	}
	if err := ValidatePromptProjection(state); err != nil {
		return PromptProjectionState{}, err
	}
	return state, nil
}

func ValidatePromptProjection(state PromptProjectionState) error {
	if state.Version != PromptProjectionVersion {
		return fmt.Errorf("prompt projection version %d is unsupported", state.Version)
	}
	if state.Omissions == nil {
		return errors.New("prompt projection omissions must be an array")
	}
	var previousEnd uint64
	segmentIDs := make(map[string]struct{}, len(state.Omissions))
	for index, omission := range state.Omissions {
		hasSegment := strings.TrimSpace(omission.SegmentID) != ""
		hasRange := omission.StartMessageSequence > 0 || omission.EndMessageSequence > 0
		if hasSegment == hasRange {
			return fmt.Errorf("prompt projection omission %d must identify exactly one segment or message range", index)
		}
		switch omission.Reason {
		case "l1_tool_result":
			if !hasSegment {
				return fmt.Errorf("prompt projection omission %d l1 reason requires segment id", index)
			}
		case "memory_committed":
			if !hasRange || strings.TrimSpace(omission.MemoryID) == "" {
				return fmt.Errorf("prompt projection omission %d memory reason requires range and memory id", index)
			}
		case "full_compact":
			if !hasRange || omission.CompactRevision == 0 {
				return fmt.Errorf("prompt projection omission %d compact reason requires range and revision", index)
			}
		default:
			return fmt.Errorf("prompt projection omission %d reason %q is invalid", index, omission.Reason)
		}
		if hasSegment {
			if _, exists := segmentIDs[omission.SegmentID]; exists {
				return fmt.Errorf("prompt projection segment %q is duplicated", omission.SegmentID)
			}
			segmentIDs[omission.SegmentID] = struct{}{}
			continue
		}
		if omission.StartMessageSequence == 0 ||
			omission.EndMessageSequence < omission.StartMessageSequence {
			return fmt.Errorf("prompt projection omission %d message range is invalid", index)
		}
		if previousEnd > 0 && omission.StartMessageSequence <= previousEnd {
			return fmt.Errorf("prompt projection omission %d message ranges overlap or are unordered", index)
		}
		if state.RecentTailStartSequence > 0 &&
			omission.EndMessageSequence >= state.RecentTailStartSequence {
			return fmt.Errorf("prompt projection omission %d crosses recent tail", index)
		}
		previousEnd = omission.EndMessageSequence
	}
	return nil
}

func applyPromptProjection(messages []MessageRecord, projection PromptProjectionState) []MessageRecord {
	if len(messages) == 0 || len(projection.Omissions) == 0 {
		return messages
	}
	filtered := make([]MessageRecord, 0, len(messages))
	for _, message := range messages {
		omitted := false
		for _, omission := range projection.Omissions {
			if omission.StartMessageSequence > 0 &&
				message.Sequence >= omission.StartMessageSequence &&
				message.Sequence <= omission.EndMessageSequence {
				omitted = true
				break
			}
		}
		if !omitted {
			filtered = append(filtered, message)
		}
	}
	return filtered
}

func updatePromptProjection(
	ctx context.Context,
	tx pgx.Tx,
	conversationID string,
	expectedWindowRevision, expectedProjectionRevision, nextWindowRevision, nextProjectionRevision int64,
	state []byte,
	now int64,
) error {
	changed, err := tx.Exec(ctx, `
UPDATE prompt_windows
SET revision = $4, projection_revision = $5, projection_state = $6, updated_at_ms = $7
WHERE conversation_id = $1 AND revision = $2 AND projection_revision = $3`,
		conversationID, expectedWindowRevision, expectedProjectionRevision,
		nextWindowRevision, nextProjectionRevision, state, now)
	if err != nil {
		return fmt.Errorf("updating prompt projection: %w", err)
	}
	if changed.RowsAffected() != 1 {
		return ErrPromptWindowRevisionChanged
	}
	return nil
}
