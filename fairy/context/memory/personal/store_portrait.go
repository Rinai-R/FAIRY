package personal

import (
	"cmp"
	"context"
	"fairy/runtime/embedding"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"
)

func (s *Store) CompanionPortraitContext(ctx context.Context, characterID string) (Retrieval, error) {
	if s.usesSeekDB() {
		return s.companionPortraitSeekDB(ctx, characterID)
	}
	if !s.usesPostgres() {
		return Retrieval{}, ErrStoreBackendUnavailable
	}
	return s.companionPortraitPostgres(ctx, characterID)
}

func (s *Store) companionPortraitPostgres(ctx context.Context, characterID string) (Retrieval, error) {
	if err := validateID("character_id", characterID); err != nil {
		return Retrieval{}, err
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	rows, err := s.pool.Raw().Query(queryCtx, `
WITH ranked AS (
    SELECT id, kind, scope_kind, character_id, review_status, content, status,
           confidence_basis_points, source_conversation_id, source_turn_id,
           supersedes_id, created_at_ms, updated_at_ms,
           ROW_NUMBER() OVER (
               PARTITION BY kind
               ORDER BY confidence_basis_points DESC, updated_at_ms DESC, id ASC
           ) AS kind_rank
    FROM personal_memories
    WHERE status = 'active' AND review_status = 'ready'
      AND (
        (scope_kind = 'global' AND character_id IS NULL AND kind IN ('profile', 'preference', 'experience'))
        OR (scope_kind = 'character' AND character_id = $1 AND kind = 'relationship')
      )
)
SELECT id, kind, scope_kind, character_id, review_status, content, status,
       confidence_basis_points, source_conversation_id, source_turn_id,
       supersedes_id, created_at_ms, updated_at_ms
FROM ranked
WHERE kind_rank <= 4
ORDER BY CASE kind WHEN 'profile' THEN 0 WHEN 'preference' THEN 1 WHEN 'relationship' THEN 2 ELSE 3 END,
         confidence_basis_points DESC, updated_at_ms DESC, id ASC
LIMIT $2`, characterID, maxPortraitCandidates)
	if err != nil {
		return Retrieval{}, fmt.Errorf("querying companion portrait: %w", err)
	}
	defer rows.Close()
	records := make([]Record, 0, maxPortraitCandidates)
	for rows.Next() {
		record, err := ScanRecord(rows)
		if err != nil {
			return Retrieval{}, err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return Retrieval{}, fmt.Errorf("iterating companion portrait: %w", err)
	}
	return buildCompanionPortrait(characterID, records)
}

func buildCompanionPortrait(characterID string, records []Record) (Retrieval, error) {
	slices.SortFunc(records, func(left, right Record) int {
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
		record  Record
		content string
		tier    int
	}
	candidates := make([]candidate, 0, min(len(records), maxPortraitMemories*2))
	perKindCandidate := make(map[string]int, 4)
	for _, record := range records {
		if !portraitRecordAllowed(characterID, record) {
			continue
		}
		if err := ValidatePersistedContent(record.ID, record.Content); err != nil {
			return Retrieval{}, err
		}
		content := strings.TrimSpace(record.Content)
		if record.ID == "" || content == "" {
			continue
		}
		tier := perKindCandidate[record.Kind]
		perKindCandidate[record.Kind]++
		if tier >= maxPortraitPerKind {
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

	result := Retrieval{
		PersonalMemories: make([]Retrieved, 0, min(len(records), maxPortraitMemories)),
		SemanticStatus:   string(embedding.SemanticStatusUnavailable),
	}
	perKind := make(map[string]int, 4)
	remaining := maxPortraitRunes
	for _, candidate := range candidates {
		record := candidate.record
		content := candidate.content
		length := utf8.RuneCountInString(content)
		if perKind[record.Kind] >= maxPortraitPerKind || length > remaining {
			continue
		}
		result.PersonalMemories = append(result.PersonalMemories, Retrieved{
			ID: record.ID, Kind: record.Kind, Layer: record.Kind, Scope: record.Scope,
			Content: content, ConfidenceBasisPoints: record.ConfidenceBasisPoints, UpdatedAtUnixMS: record.UpdatedAtUnixMS,
		})
		perKind[record.Kind]++
		remaining -= length
		if len(result.PersonalMemories) == maxPortraitMemories {
			break
		}
	}
	return result, nil
}

func portraitRecordAllowed(characterID string, record Record) bool {
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
