package memory

import (
	"context"
	"unicode"
	"unicode/utf8"
)

func (s *Store) StoreSocialMemoryEntries(ctx context.Context, input SocialMemoryBatchInput) ([]SocialMemoryEntry, error) {
	if s == nil || s.pool == nil {
		return nil, ErrDatabasePoolEmpty
	}
	if err := ValidateSocialMemoryBatch(input); err != nil {
		return nil, err
	}
	return s.storeSocialMemoryEntriesPostgres(ctx, input)
}

func (s *Store) RetrieveSocialMemoryContext(ctx context.Context, characterID, conversationID, query string) (SocialMemoryContext, error) {
	if s == nil || s.pool == nil {
		return SocialMemoryContext{}, ErrDatabasePoolEmpty
	}
	if err := ValidateID("character_id", characterID); err != nil {
		return SocialMemoryContext{}, err
	}
	if err := ValidateID("conversation_id", conversationID); err != nil {
		return SocialMemoryContext{}, err
	}
	normalized, err := normalizePostgresSearchQuery(query)
	if err != nil {
		return SocialMemoryContext{}, err
	}
	if normalized == "" {
		return SocialMemoryContext{Entries: []SocialMemoryEntry{}}, nil
	}
	fragments := buildSocialRetrievalProjection(normalized)
	if len(fragments) == 0 {
		return SocialMemoryContext{Entries: []SocialMemoryEntry{}}, nil
	}
	return s.retrieveSocialMemoryContextPostgres(ctx, characterID, conversationID, fragments)
}

func (s *Store) RetrieveCharacterSocialMemoryContext(ctx context.Context, characterID, query string) (SocialMemoryContext, error) {
	if s == nil || s.pool == nil {
		return SocialMemoryContext{}, ErrDatabasePoolEmpty
	}
	if err := ValidateID("character_id", characterID); err != nil {
		return SocialMemoryContext{}, err
	}
	normalized, err := normalizePostgresSearchQuery(query)
	if err != nil {
		return SocialMemoryContext{}, err
	}
	if normalized == "" {
		return SocialMemoryContext{Entries: []SocialMemoryEntry{}}, nil
	}
	fragments := buildSocialRetrievalProjection(normalized)
	if len(fragments) == 0 {
		return SocialMemoryContext{Entries: []SocialMemoryEntry{}}, nil
	}
	return s.retrieveCharacterSocialMemoryContextPostgres(ctx, characterID, fragments)
}

func (s *Store) RecordSocialReplyFeedback(ctx context.Context, input SocialReplyFeedbackInput) (SocialReplyFeedback, error) {
	if s == nil || s.pool == nil {
		return SocialReplyFeedback{}, ErrDatabasePoolEmpty
	}
	if err := ValidateSocialReplyFeedback(input); err != nil {
		return SocialReplyFeedback{}, err
	}
	return s.recordSocialReplyFeedbackPostgres(ctx, input)
}

func (s *Store) RecentSocialFeedbackSummary(ctx context.Context, characterID, conversationID string) (RecentSocialFeedbackSummary, error) {
	if s == nil || s.pool == nil {
		return RecentSocialFeedbackSummary{}, ErrDatabasePoolEmpty
	}
	if err := ValidateID("character_id", characterID); err != nil {
		return RecentSocialFeedbackSummary{}, err
	}
	if err := ValidateID("conversation_id", conversationID); err != nil {
		return RecentSocialFeedbackSummary{}, err
	}
	return s.recentSocialFeedbackSummaryPostgres(ctx, characterID, conversationID)
}

func buildSocialRetrievalProjection(query string) []string {
	runes := []rune(query)
	candidates := make([]string, 0, len(runes))
	if len(runes) <= 64 {
		candidates = append(candidates, query)
	}
	run := make([]rune, 0, len(runes))
	flush := func() {
		if len(run) < 3 {
			run = run[:0]
			return
		}
		if len(run) <= socialQueryWindowRunes {
			candidates = append(candidates, string(run))
		} else {
			for start := 0; start+socialQueryWindowRunes <= len(run); start++ {
				candidates = append(candidates, string(run[start:start+socialQueryWindowRunes]))
			}
		}
		run = run[:0]
	}
	for _, character := range runes {
		if unicode.IsLetter(character) || unicode.IsDigit(character) {
			run = append(run, character)
			continue
		}
		flush()
	}
	flush()
	if len(candidates) == 0 {
		return []string{}
	}

	selected := candidates
	if len(candidates) > maxSocialQueryFragments {
		selected = make([]string, 0, maxSocialQueryFragments)
		for index := range maxSocialQueryFragments {
			candidateIndex := index * (len(candidates) - 1) / (maxSocialQueryFragments - 1)
			selected = append(selected, candidates[candidateIndex])
		}
	}
	projection := make([]string, 0, len(selected))
	seen := make(map[string]struct{}, len(selected))
	remaining := maxSocialQueryRunes
	for _, candidate := range selected {
		if _, exists := seen[candidate]; exists {
			continue
		}
		length := utf8.RuneCountInString(candidate)
		if length < 3 || length > remaining {
			continue
		}
		seen[candidate] = struct{}{}
		projection = append(projection, candidate)
		remaining -= length
	}
	return projection
}
