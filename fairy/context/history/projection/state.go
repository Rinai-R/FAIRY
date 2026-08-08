package projection

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

const Version uint32 = 1

type Omission struct {
	SegmentID            string `json:"segmentId,omitempty"`
	StartMessageSequence uint64 `json:"startMessageSequence,omitempty"`
	EndMessageSequence   uint64 `json:"endMessageSequence,omitempty"`
	Reason               string `json:"reason"`
	MemoryID             string `json:"memoryId,omitempty"`
	CompactRevision      uint64 `json:"compactRevision,omitempty"`
}

type State struct {
	Version                 uint32     `json:"version"`
	Omissions               []Omission `json:"omissions"`
	RecentTailStartSequence uint64     `json:"recentTailStartSequence,omitempty"`
}

func Empty() State {
	return State{Version: Version, Omissions: []Omission{}}
}

func Encode(state State) ([]byte, error) {
	if err := Validate(state); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		return nil, fmt.Errorf("encoding prompt projection: %w", err)
	}
	return encoded, nil
}

func Decode(encoded []byte) (State, error) {
	if len(bytes.TrimSpace(encoded)) == 0 {
		return State{}, errors.New("prompt projection is empty")
	}
	var state State
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return State{}, fmt.Errorf("decoding prompt projection: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return State{}, errors.New("prompt projection contains trailing data")
	}
	if err := Validate(state); err != nil {
		return State{}, err
	}
	return state, nil
}

func Validate(state State) error {
	if state.Version != Version {
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
		if omission.StartMessageSequence == 0 || omission.EndMessageSequence < omission.StartMessageSequence {
			return fmt.Errorf("prompt projection omission %d message range is invalid", index)
		}
		if previousEnd > 0 && omission.StartMessageSequence <= previousEnd {
			return fmt.Errorf("prompt projection omission %d message ranges overlap or are unordered", index)
		}
		if state.RecentTailStartSequence > 0 && omission.EndMessageSequence >= state.RecentTailStartSequence {
			return fmt.Errorf("prompt projection omission %d crosses recent tail", index)
		}
		previousEnd = omission.EndMessageSequence
	}
	return nil
}
