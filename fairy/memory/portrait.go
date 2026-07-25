package memory

import (
	"cmp"
	"slices"
	"strings"
	"unicode/utf8"

	domainmemory "fairy/internal/domain/memory"
	"fairy/memory/semantic"
)

const (
	maxCompanionPortraitMemories   = 6
	maxCompanionPortraitPerKind    = 2
	maxCompanionPortraitRunes      = 1200
	maxCompanionPortraitCandidates = 16
)

func buildCompanionPortrait(characterID string, records []PersonalMemoryRecord) (RetrievalContext, error) {
	slices.SortFunc(records, func(left, right PersonalMemoryRecord) int {
		if order := cmp.Compare(portraitKindPriority(left.Kind), portraitKindPriority(right.Kind)); order != 0 {
			return order
		}
		if order := cmp.Compare(right.ConfidenceBasisPoints, left.ConfidenceBasisPoints); order != 0 {
			return order
		}
		if order := cmp.Compare(right.UpdatedAtUnixMS, left.UpdatedAtUnixMS); order != 0 {
			return order
		}
		return cmp.Compare(left.ID, right.ID)
	})

	type candidate struct {
		record  PersonalMemoryRecord
		content string
		tier    int
	}
	candidates := make([]candidate, 0, min(len(records), maxCompanionPortraitMemories*2))
	perKindCandidate := make(map[string]int, 4)
	for _, record := range records {
		if !portraitRecordAllowed(characterID, record) {
			continue
		}
		if err := domainmemory.ValidatePersistedPersonalMemoryContent(record.ID, record.Content); err != nil {
			return RetrievalContext{}, err
		}
		content := strings.TrimSpace(record.Content)
		if record.ID == "" || content == "" {
			continue
		}
		tier := perKindCandidate[record.Kind]
		perKindCandidate[record.Kind]++
		if tier >= maxCompanionPortraitPerKind {
			continue
		}
		candidates = append(candidates, candidate{record: record, content: content, tier: tier})
	}
	slices.SortFunc(candidates, func(left, right candidate) int {
		if order := cmp.Compare(left.tier, right.tier); order != 0 {
			return order
		}
		if order := cmp.Compare(portraitKindPriority(left.record.Kind), portraitKindPriority(right.record.Kind)); order != 0 {
			return order
		}
		if order := cmp.Compare(right.record.ConfidenceBasisPoints, left.record.ConfidenceBasisPoints); order != 0 {
			return order
		}
		if order := cmp.Compare(right.record.UpdatedAtUnixMS, left.record.UpdatedAtUnixMS); order != 0 {
			return order
		}
		return cmp.Compare(left.record.ID, right.record.ID)
	})

	result := RetrievalContext{
		PersonalMemories: make([]RetrievedPersonalMemory, 0, min(len(records), maxCompanionPortraitMemories)),
		Knowledge:        []RetrievedKnowledge{},
		SemanticStatus:   string(semantic.StatusUnavailable),
	}
	perKind := make(map[string]int, 4)
	remaining := maxCompanionPortraitRunes
	for _, candidate := range candidates {
		record := candidate.record
		content := candidate.content
		length := utf8.RuneCountInString(content)
		if perKind[record.Kind] >= maxCompanionPortraitPerKind || length > remaining {
			continue
		}
		result.PersonalMemories = append(result.PersonalMemories, RetrievedPersonalMemory{
			ID: record.ID, Kind: record.Kind, Layer: record.Kind, Scope: record.Scope,
			Content: content, ConfidenceBasisPoints: record.ConfidenceBasisPoints, UpdatedAtUnixMS: record.UpdatedAtUnixMS,
		})
		perKind[record.Kind]++
		remaining -= length
		if len(result.PersonalMemories) == maxCompanionPortraitMemories {
			break
		}
	}
	return result, nil
}

func portraitRecordAllowed(characterID string, record PersonalMemoryRecord) bool {
	if record.Scope.Type == "global" {
		return record.Kind == "profile" || record.Kind == "preference" || record.Kind == "experience"
	}
	return record.Kind == "relationship" && record.Scope.Type == "character" && record.Scope.CharacterID == characterID
}

func portraitKindPriority(kind string) int {
	switch kind {
	case "profile":
		return 0
	case "preference":
		return 1
	case "relationship":
		return 2
	case "experience":
		return 3
	default:
		return 4
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
