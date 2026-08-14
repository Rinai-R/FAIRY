package social

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

const seekDBSocialMemoryEntryColumns = `
id, character_id, conversation_id, kind, situation, content, recall_cue, status,
source_start_ms, source_end_ms,
feedback_evaluation_count, feedback_adopted_count,
feedback_positive_count, feedback_partial_count, feedback_negative_count,
feedback_score_basis_points, feedback_quarantined_until_ms,
created_at_ms, updated_at_ms`

type seekDBTx interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func sqlPlaceholders(count int) string {
	if count <= 0 {
		return ""
	}
	return strings.Repeat("?,", count-1) + "?"
}

func scanSeekDBSocialMemoryEntry(row scanner) (SocialMemoryEntry, error) {
	var (
		entry      SocialMemoryEntry
		quarantine sql.NullInt64
	)
	if err := row.Scan(
		&entry.ID, &entry.CharacterID, &entry.ConversationID, &entry.Kind,
		&entry.Situation, &entry.Content, &entry.RecallCue, &entry.Status,
		&entry.SourceStartUnixMS, &entry.SourceEndUnixMS,
		&entry.FeedbackEvaluationCount, &entry.FeedbackAdoptedCount,
		&entry.FeedbackPositiveCount, &entry.FeedbackPartialCount, &entry.FeedbackNegativeCount,
		&entry.FeedbackScoreBasisPoints, &quarantine,
		&entry.CreatedAtUnixMS, &entry.UpdatedAtUnixMS,
	); err != nil {
		return SocialMemoryEntry{}, fmt.Errorf("scanning SeekDB social memory entry: %w", err)
	}
	if !ValidSocialMemoryKind(entry.Kind) || (entry.Status != "active" && entry.Status != "suppressed") {
		return SocialMemoryEntry{}, errors.New("stored social memory entry is invalid")
	}
	if quarantine.Valid {
		until := quarantine.Int64
		entry.FeedbackQuarantinedUntilUnixMS = &until
	}
	return entry, nil
}

func lockSeekDBSocialConversation(ctx context.Context, tx seekDBTx, characterID, conversationID string) error {
	return verifySeekDBSocialConversation(ctx, tx, characterID, conversationID, true)
}

func verifySeekDBSocialConversation(ctx context.Context, db seekDBTx, characterID, conversationID string, forUpdate bool) error {
	query := "SELECT character_id FROM conversations WHERE id = ?"
	if forUpdate {
		query += " FOR UPDATE"
	}
	var storedCharacterID string
	err := db.QueryRowContext(ctx, query, conversationID).Scan(&storedCharacterID)
	if errors.Is(err, sql.ErrNoRows) {
		return errors.New("social memory conversation does not exist")
	}
	if err != nil {
		return fmt.Errorf("checking social memory conversation: %w", err)
	}
	if storedCharacterID != characterID {
		return errors.New("social memory character does not own the conversation")
	}
	return nil
}

func collectSeekDBSocialMemoryContext(rows *sql.Rows, capacity, runeBudget int) (SocialMemoryContext, error) {
	entries := make([]SocialMemoryEntry, 0, capacity)
	perKind := make(map[string]int, 3)
	remaining := runeBudget
	for rows.Next() {
		entry, scanErr := scanSeekDBSocialMemoryEntry(rows)
		if scanErr != nil {
			return SocialMemoryContext{}, scanErr
		}
		if perKind[entry.Kind] >= 3 {
			continue
		}
		length := len([]rune(entry.Situation)) + len([]rune(entry.Content)) + len([]rune(entry.RecallCue))
		if length > remaining {
			continue
		}
		remaining -= length
		perKind[entry.Kind]++
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return SocialMemoryContext{}, fmt.Errorf("iterating SeekDB social memory: %w", err)
	}
	return SocialMemoryContext{Entries: entries}, nil
}
