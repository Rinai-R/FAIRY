package knowledge

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"unicode/utf8"
)

func (s *Store) commitKnowledgeDocumentActionsSeekDB(
	ctx context.Context,
	task IngestTask,
	document Document,
	suppliedKnowledgeIDs []string,
	actions []DocumentAction,
	requireContext bool,
) (int, error) {
	if _, err := validateKnowledgeIngestTask(task); err != nil {
		return 0, err
	}
	if err := validateKnowledgeDocument(task, document); err != nil {
		return 0, err
	}
	if err := validateSeekDBKnowledgeDocumentPersistence(task, document); err != nil {
		return 0, err
	}
	documentHash, err := decodeKnowledgeHash("knowledge content hash", document.ContentHash)
	if err != nil {
		return 0, err
	}
	if len(actions) > maxKnowledgeDocumentActions {
		return 0, errors.New("knowledge document action count is invalid")
	}
	supplied := make(map[string]struct{}, len(suppliedKnowledgeIDs))
	for _, id := range suppliedKnowledgeIDs {
		if err := validateSeekDBKnowledgeID("knowledge_id", id); err != nil {
			return 0, err
		}
		if _, duplicate := supplied[id]; duplicate {
			return 0, errors.New("supplied knowledge ID is duplicated")
		}
		supplied[id] = struct{}{}
	}
	topic := strings.TrimSpace(document.Title)
	if topic == "" {
		parsed, err := url.Parse(document.CanonicalURL)
		if err != nil || parsed.Hostname() == "" {
			return 0, errors.New("knowledge document topic is unavailable")
		}
		topic = parsed.Hostname()
	}
	if err := validateSeekDBKnowledgeTopic(topic); err != nil {
		return 0, errors.New("knowledge document topic is invalid")
	}
	prepared := make([]preparedKnowledgeDocumentAction, len(actions))
	targetIDs := make([]string, 0, len(actions))
	embeddingIndexes := make([]int, 0, len(actions))
	embeddingContents := make([]string, 0, len(actions))
	seenTargets := make(map[string]struct{}, len(actions))
	seenContents := make(map[string]struct{}, len(actions))
	for index, action := range actions {
		if strings.TrimSpace(action.Evidence) != action.Evidence ||
			utf8.RuneCountInString(action.Evidence) < 8 ||
			utf8.RuneCountInString(action.Evidence) > 1200 ||
			ContainsDisallowedControl(action.Evidence) ||
			!strings.Contains(document.Content, action.Evidence) {
			return 0, fmt.Errorf("knowledge document action[%d] evidence is invalid", index)
		}
		switch action.Operation {
		case MutationAdd:
			if action.MemoryID != "" {
				return 0, fmt.Errorf("knowledge document action[%d] ADD is invalid", index)
			}
		case MutationReplace:
			if _, ok := supplied[action.MemoryID]; !ok {
				return 0, fmt.Errorf("knowledge document action[%d] REPLACE is invalid", index)
			}
		case MutationDelete, MutationNone:
			if _, ok := supplied[action.MemoryID]; !ok || action.Content != "" || action.ConfidenceBasisPoints != 0 {
				return 0, fmt.Errorf("knowledge document action[%d] target is invalid", index)
			}
		default:
			return 0, fmt.Errorf("knowledge document action[%d] operation is invalid", index)
		}
		if action.MemoryID != "" {
			if _, duplicate := seenTargets[action.MemoryID]; duplicate {
				return 0, errors.New("knowledge document action target is duplicated")
			}
			seenTargets[action.MemoryID] = struct{}{}
			targetIDs = append(targetIDs, action.MemoryID)
		}
		if action.Operation == MutationAdd || action.Operation == MutationReplace {
			statementRunes := utf8.RuneCountInString(action.Content)
			if strings.TrimSpace(action.Content) != action.Content ||
				statementRunes < 8 || statementRunes > maxKnowledgeDocumentActionContentRunes ||
				ContainsDisallowedControl(action.Content) ||
				action.ConfidenceBasisPoints == 0 || action.ConfidenceBasisPoints > 10000 {
				return 0, fmt.Errorf("knowledge document action[%d] content is invalid", index)
			}
			if _, duplicate := seenContents[action.Content]; duplicate {
				return 0, errors.New("knowledge document action content is duplicated")
			}
			seenContents[action.Content] = struct{}{}
			embeddingIndexes = append(embeddingIndexes, index)
			embeddingContents = append(embeddingContents, topic+"\n"+action.Content)
		}
		prepared[index] = preparedKnowledgeDocumentAction{action: action, topic: topic}
	}
	embeddings, err := prepareKnowledgeEmbeddings(
		ctx, s.semanticEmbedderSnapshot(), embeddingContents, requireContext,
	)
	if err != nil {
		return 0, err
	}
	for index, actionIndex := range embeddingIndexes {
		prepared[actionIndex].embedding = embeddings[index]
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	sort.Strings(targetIDs)
	queryCtx, cancel := s.seekDBQueryContext(context.WithoutCancel(ctx))
	defer cancel()
	tx, err := s.seekDB.BeginTx(queryCtx, nil)
	if err != nil {
		return 0, fmt.Errorf("beginning SeekDB direct knowledge action transaction: %w", err)
	}
	defer tx.Rollback()
	if err := lockSeekDBKnowledgeSource(queryCtx, tx, task.ConversationID, task.TurnID); err != nil {
		return 0, err
	}
	locked, err := lockSeekDBKnowledgeTargets(queryCtx, tx, targetIDs)
	if err != nil {
		return 0, err
	}
	for _, targetID := range targetIDs {
		if locked[targetID].Status != "verified" {
			return 0, errors.New("knowledge action target is no longer verified")
		}
	}
	now := s.currentUnixMS()
	changed := 0
	for index, item := range prepared {
		action := item.action
		switch action.Operation {
		case MutationNone:
		case MutationDelete:
			effectiveNow := max(now, locked[action.MemoryID].UpdatedAtUnixMS)
			result, err := tx.ExecContext(queryCtx, `
UPDATE knowledge_entries
SET status = 'tombstone',
    source_url = ?, source_title = ?, source_content_hash = ?,
    source_content_type = ?, source_fetched_at_ms = ?,
    source_etag = ?, source_last_modified = ?, reconciler_revision = ?,
    evidence_text = ?, updated_at_ms = ?
WHERE id = ? AND status = 'verified'`,
				document.CanonicalURL, nullIfEmpty(document.Title), documentHash,
				document.ContentType, document.FetchedAtUnixMS, nullIfEmpty(document.ETag),
				nullIfEmpty(document.LastModified), nullIfEmpty(document.ReconcilerRevision),
				action.Evidence, effectiveNow, action.MemoryID,
			)
			if err != nil {
				return 0, fmt.Errorf("deleting recalled SeekDB knowledge: %w", err)
			}
			if err := requireOneSeekDBKnowledgeRow(result, "knowledge DELETE lost its target"); err != nil {
				return 0, err
			}
			changed++
		case MutationReplace:
			effectiveNow := max(now, locked[action.MemoryID].UpdatedAtUnixMS)
			result, err := tx.ExecContext(queryCtx, `
UPDATE knowledge_entries
SET status = 'superseded', updated_at_ms = ?
WHERE id = ? AND status = 'verified'`, effectiveNow, action.MemoryID)
			if err != nil {
				return 0, fmt.Errorf("superseding recalled SeekDB knowledge: %w", err)
			}
			if err := requireOneSeekDBKnowledgeRow(result, "knowledge REPLACE lost its target"); err != nil {
				return 0, err
			}
			fallthrough
		case MutationAdd:
			var supersedesID any
			if action.Operation == MutationReplace {
				supersedesID = action.MemoryID
			}
			content := item.topic + "\n" + action.Content
			spaceID, contentHash, vector, err := seekDBKnowledgeEmbeddingTuple(content, item.embedding)
			if err != nil {
				return 0, err
			}
			knowledgeID := newID()
			_, err = tx.ExecContext(queryCtx, `
INSERT INTO knowledge_entries(
  id, topic, statement, status, verification_basis, confidence_basis_points,
  source_conversation_id, source_turn_id,
  source_url, source_title, source_content_hash, source_content_type,
  source_fetched_at_ms, source_etag, source_last_modified, reconciler_revision,
  evidence_text, supersedes_id,
  embedding_space_id, embedding_content_hash, embedding,
  created_at_ms, updated_at_ms
) VALUES (
  ?, ?, ?, 'verified', 'retrieval_ingest', ?, ?, ?,
  ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
)`,
				knowledgeID, item.topic, action.Content, int(action.ConfidenceBasisPoints),
				task.ConversationID, task.TurnID, document.CanonicalURL,
				nullIfEmpty(document.Title), documentHash, document.ContentType,
				document.FetchedAtUnixMS, nullIfEmpty(document.ETag), nullIfEmpty(document.LastModified),
				nullIfEmpty(document.ReconcilerRevision), action.Evidence, supersedesID,
				spaceID, contentHash, vector, now, now,
			)
			if err != nil {
				return 0, fmt.Errorf("inserting direct SeekDB knowledge action[%d]: %w", index, err)
			}
			changed++
		default:
			return 0, errors.New("knowledge mutation operation is invalid")
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("committing SeekDB direct knowledge actions: %w", err)
	}
	return changed, nil
}

func requireOneSeekDBKnowledgeRow(result sql.Result, lostMessage string) error {
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("reading SeekDB knowledge mutation count: %w", err)
	}
	if rows != 1 {
		return errors.New(lostMessage)
	}
	return nil
}
