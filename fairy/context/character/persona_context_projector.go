package character

import (
	"fmt"
	"unicode/utf8"

	"fairy/runtime/model"
)

// ContextProjector is the single conversion boundary from typed context
// lifecycle data to provider PromptItems.
type ContextProjector struct{}

func (ContextProjector) SegmentsFromSlots(slots []ContextSlot) []model.ContextSegment {
	total := 0
	for _, slot := range slots {
		if slot.Present {
			total += len(slot.Items)
		}
	}
	segments := make([]model.ContextSegment, 0, total)
	ordinal := uint64(1)
	for _, slot := range slots {
		if !slot.Present {
			continue
		}
		for index := range slot.Items {
			item := slot.Items[index]
			segments = append(segments, model.ContextSegment{
				ID:              fmt.Sprintf("%s:%s:%d", slot.ID, slot.RevisionHash, index),
				Ordinal:         ordinal,
				Kind:            segmentKindForSlotItem(slot.ID, item.Type),
				Item:            &item,
				RetentionPolicy: segmentRetentionPolicy(slot.CachePolicy),
				TokenCount:      estimatedSegmentTokens(item),
				Recoverability:  segmentRecoverability(slot),
				SourceRefs: []model.ContextSourceRef{{
					Kind: "context_slot", ID: slot.ID,
				}},
				ProjectionState: model.ContextProjectionActive,
			})
			ordinal++
		}
	}
	return segments
}

func (ContextProjector) Project(segments []model.ContextSegment) ([]model.PromptItem, error) {
	if err := model.ValidateContextSegments(segments); err != nil {
		return nil, err
	}
	items := make([]model.PromptItem, 0, len(segments))
	for _, segment := range segments {
		item, included, err := segment.PromptItem()
		if err != nil {
			return nil, err
		}
		if included {
			items = append(items, item)
		}
	}
	return items, nil
}

func (projector ContextProjector) ProjectSlots(slots []ContextSlot) ([]model.PromptItem, error) {
	segments := projector.SegmentsFromSlots(slots)
	if len(segments) == 0 {
		return []model.PromptItem{}, nil
	}
	return projector.Project(segments)
}

func (projector ContextProjector) ProjectSlotsWithTail(slots []ContextSlot, tail []model.ContextSegment) ([]model.PromptItem, error) {
	segments := projector.SegmentsFromSlots(slots)
	combined := make([]model.ContextSegment, 0, len(segments)+len(tail))
	combined = append(combined, segments...)
	for _, segment := range tail {
		segment.Ordinal = uint64(len(combined) + 1)
		combined = append(combined, segment)
	}
	if len(combined) == 0 {
		return []model.PromptItem{}, nil
	}
	return projector.Project(combined)
}

func segmentKindForSlotItem(slotID string, itemType model.PromptItemType) model.ContextSegmentKind {
	switch itemType {
	case model.PromptItemUserMessage:
		return model.ContextSegmentUserMessage
	case model.PromptItemAssistantMessage:
		return model.ContextSegmentAssistantMessage
	case model.PromptItemToolCall:
		return model.ContextSegmentToolCall
	case model.PromptItemToolResult:
		return model.ContextSegmentToolResult
	}
	switch slotID {
	case "compaction_summary":
		return model.ContextSegmentCompactSummary
	case "retrieved_context", "social_memory", "person_notes":
		return model.ContextSegmentMemoryRef
	default:
		return model.ContextSegmentContextData
	}
}

func segmentRetentionPolicy(cachePolicy string) model.ContextRetentionPolicy {
	switch cachePolicy {
	case "stable":
		return model.ContextRetentionStable
	case "suffix":
		return model.ContextRetentionRecent
	case "tail":
		return model.ContextRetentionCurrentTurn
	default:
		return model.ContextRetentionWindow
	}
}

func segmentRecoverability(slot ContextSlot) model.ContextRecoverability {
	switch slot.ID {
	case "dialogue":
		return model.ContextRecoverabilityTranscript
	case "retrieved_context", "social_memory", "person_notes":
		return model.ContextRecoverabilityMemory
	}
	if slot.Required {
		return model.ContextRecoverabilityRequired
	}
	return model.ContextRecoverabilityEphemeral
}

func estimatedSegmentTokens(item model.PromptItem) uint64 {
	runes := utf8.RuneCountInString(item.Content) +
		utf8.RuneCountInString(item.ToolArguments) +
		utf8.RuneCountInString(item.ToolName) + 12
	if item.Parts != nil {
		for _, part := range *item.Parts {
			runes += utf8.RuneCountInString(part.Text)
			if part.ImageDataURL != "" {
				runes += len(part.ImageDataURL)
			}
		}
	}
	return uint64((runes + 3) / 4)
}
