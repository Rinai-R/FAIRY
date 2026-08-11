package knowledge

import (
	"context"
	"errors"
	"fairy/runtime/embedding"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

const (
	maxKnowledgeDocumentActions            = 8
	maxKnowledgeDocumentActionContentRunes = 2400
)

type preparedKnowledgeDocumentAction struct {
	action    DocumentAction
	topic     string
	embedding embedding.EmbeddingValue
}

func (s *Store) commitKnowledgeDocumentActionsPostgres(
	ctx context.Context,
	task IngestTask,
	document Document,
	suppliedKnowledgeIDs []string,
	actions []DocumentAction,
) (int, error) {
	if _, err := validateKnowledgeIngestTask(task); err != nil {
		return 0, err
	}
	if err := validateKnowledgeDocument(task, document); err != nil {
		return 0, err
	}
	if len(actions) > maxKnowledgeDocumentActions {
		return 0, errors.New("knowledge document action count is invalid")
	}
	supplied := make(map[string]struct{}, len(suppliedKnowledgeIDs))
	for _, id := range suppliedKnowledgeIDs {
		if err := ValidateID("knowledge_id", id); err != nil {
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
	embeddings, err := embedding.ForContents(s.embedder.Snapshot(), embeddingContents)
	if err != nil {
		return 0, err
	}
	for index, actionIndex := range embeddingIndexes {
		prepared[actionIndex].embedding = embeddings[index]
	}
	sort.Strings(targetIDs)

	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	tx, err := s.pool.Raw().Begin(queryCtx)
	if err != nil {
		return 0, fmt.Errorf("beginning direct knowledge action transaction: %w", err)
	}
	defer tx.Rollback(queryCtx)
	for _, targetID := range targetIDs {
		var status string
		if err := tx.QueryRow(queryCtx, "SELECT status FROM knowledge_entries WHERE id = $1 FOR UPDATE", targetID).Scan(&status); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return 0, errors.New("knowledge action target no longer exists")
			}
			return 0, fmt.Errorf("locking knowledge action target: %w", err)
		}
		if status != "verified" {
			return 0, errors.New("knowledge action target is no longer verified")
		}
	}
	now := nowUnixMS()
	changed := 0
	for index, item := range prepared {
		switch item.action.Operation {
		case MutationNone:
		case MutationDelete:
			result, err := tx.Exec(queryCtx, `
UPDATE knowledge_entries
SET status = 'tombstone',
    source_url = $2, source_title = $3, source_content_hash = $4,
    source_content_type = $5, source_fetched_at_ms = $6,
    source_etag = $7, source_last_modified = $8, reconciler_revision = $9,
    evidence_text = $10, updated_at_ms = $11
WHERE id = $1 AND status = 'verified'`,
				item.action.MemoryID, document.CanonicalURL, document.Title, document.ContentHash,
				document.ContentType, document.FetchedAtUnixMS, document.ETag,
				document.LastModified, document.ReconcilerRevision, item.action.Evidence, now)
			if err != nil {
				return 0, fmt.Errorf("deleting recalled knowledge: %w", err)
			}
			if result.RowsAffected() != 1 {
				return 0, errors.New("knowledge DELETE lost its target")
			}
			changed++
		case MutationReplace:
			result, err := tx.Exec(queryCtx, "UPDATE knowledge_entries SET status = 'superseded', updated_at_ms = $2 WHERE id = $1 AND status = 'verified'", item.action.MemoryID, now)
			if err != nil {
				return 0, fmt.Errorf("superseding recalled knowledge: %w", err)
			}
			if result.RowsAffected() != 1 {
				return 0, errors.New("knowledge REPLACE lost its target")
			}
			fallthrough
		case MutationAdd:
			knowledgeID := newID()
			var supersedesID any
			if item.action.Operation == MutationReplace {
				supersedesID = item.action.MemoryID
			}
			var modelID, contentHash, vector any
			if item.embedding.Enabled() {
				modelID = item.embedding.ModelID
				contentHash = item.embedding.ContentHash
				vector = item.embedding.Vector.String()
			}
			if _, err := tx.Exec(queryCtx, `
INSERT INTO knowledge_entries(
  id, topic, statement, status, verification_basis, confidence_basis_points,
  source_conversation_id, source_turn_id,
  source_url, source_title, source_content_hash, source_content_type,
  source_fetched_at_ms, source_etag, source_last_modified, reconciler_revision,
  evidence_text, supersedes_id,
  embedding_model_id_v2, embedding_content_hash_v2, embedding_v2,
  created_at_ms, updated_at_ms
) VALUES (
  $1, $2, $3, 'verified', 'retrieval_ingest', $4,
  $5, $6, $7, $8, $9, $10, $11, $12, $13, $14,
  $15, $16, $17, $18, $19::public.vector, $20, $20
)`,
				knowledgeID, item.topic, item.action.Content, item.action.ConfidenceBasisPoints,
				task.ConversationID, task.TurnID,
				document.CanonicalURL, document.Title, document.ContentHash, document.ContentType,
				document.FetchedAtUnixMS, document.ETag, document.LastModified, document.ReconcilerRevision,
				item.action.Evidence, supersedesID,
				modelID, contentHash, vector, now); err != nil {
				return 0, fmt.Errorf("inserting direct knowledge action[%d]: %w", index, err)
			}
			changed++
		default:
			return 0, errors.New("knowledge mutation operation is invalid")
		}
	}
	if err := tx.Commit(queryCtx); err != nil {
		return 0, fmt.Errorf("committing direct knowledge actions: %w", err)
	}
	return changed, nil
}

func validateKnowledgeDocument(task IngestTask, document Document) error {
	source := task.Source
	if document.SourceID != source.ID || document.CanonicalURL != source.URL {
		return errors.New("knowledge document does not match the task source")
	}
	parsed, err := url.Parse(document.CanonicalURL)
	if err != nil || parsed.User != nil || parsed.Hostname() == "" || parsed.Fragment != "" ||
		parsed.String() != document.CanonicalURL ||
		parsed.Scheme != "http" && parsed.Scheme != "https" {
		return errors.New("knowledge document URL is invalid")
	}
	if !validContentHash(document.ContentHash) ||
		document.ReconcilerRevision != "" && !validContentHash(document.ReconcilerRevision) ||
		document.ContentType != "text/html" && document.ContentType != "text/plain" ||
		document.FetchedAtUnixMS < 0 {
		return errors.New("knowledge document metadata is invalid")
	}
	if strings.TrimSpace(document.Content) != document.Content || document.Content == "" ||
		ContainsDisallowedControl(document.Content) ||
		embedding.ContentHash(document.Content) != document.ContentHash {
		return errors.New("knowledge document complete content is invalid")
	}
	return ValidateID("knowledge_evidence_id", document.EvidenceID)
}
