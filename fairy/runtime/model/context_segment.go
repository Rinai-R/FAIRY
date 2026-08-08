package model

import (
	"errors"
	"fmt"
	"strings"
)

type ContextSegmentKind string

const (
	ContextSegmentUserMessage      ContextSegmentKind = "user_message"
	ContextSegmentAssistantMessage ContextSegmentKind = "assistant_message"
	ContextSegmentContextData      ContextSegmentKind = "context_data"
	ContextSegmentToolCall         ContextSegmentKind = "tool_call"
	ContextSegmentToolResult       ContextSegmentKind = "tool_result"
	ContextSegmentArtifactRef      ContextSegmentKind = "artifact_ref"
	ContextSegmentMemoryRef        ContextSegmentKind = "memory_ref"
	ContextSegmentCompactSummary   ContextSegmentKind = "compact_summary"
)

type ContextRetentionPolicy string

const (
	ContextRetentionStable        ContextRetentionPolicy = "stable"
	ContextRetentionWindow        ContextRetentionPolicy = "window"
	ContextRetentionRecent        ContextRetentionPolicy = "recent"
	ContextRetentionTTL           ContextRetentionPolicy = "ttl"
	ContextRetentionCurrentTurn   ContextRetentionPolicy = "current_turn"
	ContextRetentionMemoryCovered ContextRetentionPolicy = "memory_covered"
)

type ContextRecoverability string

const (
	ContextRecoverabilityRequired    ContextRecoverability = "required"
	ContextRecoverabilityTranscript  ContextRecoverability = "transcript"
	ContextRecoverabilityRefetchable ContextRecoverability = "refetchable"
	ContextRecoverabilityMemory      ContextRecoverability = "memory"
	ContextRecoverabilityEphemeral   ContextRecoverability = "ephemeral"
)

type ContextProjectionState string

const (
	ContextProjectionActive        ContextProjectionState = "active"
	ContextProjectionOmittedL1     ContextProjectionState = "omitted_l1"
	ContextProjectionOmittedMemory ContextProjectionState = "omitted_memory"
	ContextProjectionCompacted     ContextProjectionState = "compacted"
)

type ContextSourceRef struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

// ContextSegment is the provider-neutral lifecycle contract for one ordered
// piece of active model context. Item is process-local prompt content;
// ContentRef is a non-sensitive durable reference and never a copy of Item.
type ContextSegment struct {
	ID              string                 `json:"id"`
	TurnID          string                 `json:"turnId,omitempty"`
	Ordinal         uint64                 `json:"ordinal"`
	Kind            ContextSegmentKind     `json:"kind"`
	Item            *PromptItem            `json:"item,omitempty"`
	ContentRef      string                 `json:"contentRef,omitempty"`
	CreatedAtUnixMS int64                  `json:"createdAtUnixMs"`
	ExpiresAtUnixMS *int64                 `json:"expiresAtUnixMs,omitempty"`
	RetentionPolicy ContextRetentionPolicy `json:"retentionPolicy"`
	TokenCount      uint64                 `json:"tokenCount"`
	Recoverability  ContextRecoverability  `json:"recoverability"`
	Dependencies    []string               `json:"dependencies,omitempty"`
	SourceRefs      []ContextSourceRef     `json:"sourceRefs,omitempty"`
	ProjectionState ContextProjectionState `json:"projectionState"`
}

func (s ContextSegment) Validate() error {
	if strings.TrimSpace(s.ID) == "" {
		return errors.New("context segment id is required")
	}
	if s.Ordinal == 0 {
		return errors.New("context segment ordinal is required")
	}
	if !validContextSegmentKind(s.Kind) {
		return fmt.Errorf("context segment kind %q is invalid", s.Kind)
	}
	if !validContextRetentionPolicy(s.RetentionPolicy) {
		return fmt.Errorf("context segment retention policy %q is invalid", s.RetentionPolicy)
	}
	if !validContextRecoverability(s.Recoverability) {
		return fmt.Errorf("context segment recoverability %q is invalid", s.Recoverability)
	}
	if !validContextProjectionState(s.ProjectionState) {
		return fmt.Errorf("context segment projection state %q is invalid", s.ProjectionState)
	}
	if s.CreatedAtUnixMS < 0 {
		return errors.New("context segment created time cannot be negative")
	}
	if s.ExpiresAtUnixMS != nil && *s.ExpiresAtUnixMS < s.CreatedAtUnixMS {
		return errors.New("context segment expiry precedes creation")
	}
	if s.RetentionPolicy == ContextRetentionTTL && s.ExpiresAtUnixMS == nil {
		return errors.New("ttl context segment requires expiry")
	}
	if s.Item == nil && strings.TrimSpace(s.ContentRef) == "" {
		return errors.New("context segment requires prompt content or content reference")
	}
	if s.Item != nil && strings.TrimSpace(s.ContentRef) != "" {
		return errors.New("context segment prompt content and content reference are mutually exclusive")
	}
	if s.Item != nil {
		if err := validateSegmentPromptItem(s.Kind, *s.Item); err != nil {
			return err
		}
	}
	seenDependencies := make(map[string]struct{}, len(s.Dependencies))
	for _, dependency := range s.Dependencies {
		if strings.TrimSpace(dependency) == "" || dependency == s.ID {
			return errors.New("context segment dependency is invalid")
		}
		if _, exists := seenDependencies[dependency]; exists {
			return errors.New("context segment dependency is duplicated")
		}
		seenDependencies[dependency] = struct{}{}
	}
	for _, source := range s.SourceRefs {
		if strings.TrimSpace(source.Kind) == "" || strings.TrimSpace(source.ID) == "" {
			return errors.New("context segment source reference is invalid")
		}
	}
	return nil
}

func (s ContextSegment) PromptItem() (PromptItem, bool, error) {
	if err := s.Validate(); err != nil {
		return PromptItem{}, false, err
	}
	if s.ProjectionState != ContextProjectionActive || s.Item == nil {
		return PromptItem{}, false, nil
	}
	return *s.Item, true, nil
}

func ValidateContextSegments(segments []ContextSegment) error {
	ids := make(map[string]uint64, len(segments))
	var previousOrdinal uint64
	for i, segment := range segments {
		if err := segment.Validate(); err != nil {
			return fmt.Errorf("context segment %d: %w", i, err)
		}
		if i > 0 && segment.Ordinal <= previousOrdinal {
			return errors.New("context segments are not in strictly increasing order")
		}
		if _, exists := ids[segment.ID]; exists {
			return fmt.Errorf("context segment id %q is duplicated", segment.ID)
		}
		ids[segment.ID] = segment.Ordinal
		previousOrdinal = segment.Ordinal
	}
	for _, segment := range segments {
		for _, dependency := range segment.Dependencies {
			ordinal, exists := ids[dependency]
			if !exists {
				return fmt.Errorf("context segment %q has unknown dependency %q", segment.ID, dependency)
			}
			if ordinal >= segment.Ordinal {
				return fmt.Errorf("context segment %q dependency %q is not earlier", segment.ID, dependency)
			}
		}
	}
	return nil
}

func validContextSegmentKind(kind ContextSegmentKind) bool {
	switch kind {
	case ContextSegmentUserMessage, ContextSegmentAssistantMessage, ContextSegmentContextData,
		ContextSegmentToolCall, ContextSegmentToolResult, ContextSegmentArtifactRef,
		ContextSegmentMemoryRef, ContextSegmentCompactSummary:
		return true
	default:
		return false
	}
}

func validContextRetentionPolicy(policy ContextRetentionPolicy) bool {
	switch policy {
	case ContextRetentionStable, ContextRetentionWindow, ContextRetentionRecent,
		ContextRetentionTTL, ContextRetentionCurrentTurn, ContextRetentionMemoryCovered:
		return true
	default:
		return false
	}
}

func validContextRecoverability(value ContextRecoverability) bool {
	switch value {
	case ContextRecoverabilityRequired, ContextRecoverabilityTranscript,
		ContextRecoverabilityRefetchable, ContextRecoverabilityMemory,
		ContextRecoverabilityEphemeral:
		return true
	default:
		return false
	}
}

func validContextProjectionState(state ContextProjectionState) bool {
	switch state {
	case ContextProjectionActive, ContextProjectionOmittedL1,
		ContextProjectionOmittedMemory, ContextProjectionCompacted:
		return true
	default:
		return false
	}
}

func validateSegmentPromptItem(kind ContextSegmentKind, item PromptItem) error {
	expected := PromptItemContextData
	switch kind {
	case ContextSegmentUserMessage:
		expected = PromptItemUserMessage
	case ContextSegmentAssistantMessage:
		expected = PromptItemAssistantMessage
	case ContextSegmentToolCall:
		expected = PromptItemToolCall
	case ContextSegmentToolResult:
		expected = PromptItemToolResult
	case ContextSegmentContextData, ContextSegmentArtifactRef, ContextSegmentMemoryRef, ContextSegmentCompactSummary:
		expected = PromptItemContextData
	}
	if item.Type != expected {
		return fmt.Errorf("context segment kind %q requires prompt item type %q", kind, expected)
	}
	switch item.Type {
	case PromptItemToolCall:
		if strings.TrimSpace(item.ToolCallID) == "" || strings.TrimSpace(item.ToolName) == "" {
			return errors.New("tool call context segment requires call id and tool name")
		}
	case PromptItemToolResult:
		if strings.TrimSpace(item.ToolCallID) == "" {
			return errors.New("tool result context segment requires call id")
		}
	}
	return nil
}
