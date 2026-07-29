package memory

import (
	"context"
	"errors"
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
	action    KnowledgeDocumentAction
	topic     string
	embedding EmbeddingValue
}

type currentKnowledgeDocument struct {
	documentID         string
	contentHash        string
	fetchedAtUnixMS    int64
	reconcilerRevision string
}

func (s *Store) commitKnowledgeDocumentActionsPostgres(
	ctx context.Context,
	jobID, taskID string,
	document KnowledgeDocument,
	suppliedKnowledgeIDs []string,
	actions []KnowledgeDocumentAction,
) (int, error) {
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
		case KnowledgeMutationAdd:
			if action.MemoryID != "" {
				return 0, fmt.Errorf("knowledge document action[%d] ADD is invalid", index)
			}
		case KnowledgeMutationUpdate:
			if _, ok := supplied[action.MemoryID]; !ok {
				return 0, fmt.Errorf("knowledge document action[%d] UPDATE is invalid", index)
			}
		case KnowledgeMutationDelete, KnowledgeMutationNone:
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
		if action.Operation == KnowledgeMutationAdd || action.Operation == KnowledgeMutationUpdate {
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
	return s.commitPreparedKnowledgeDocumentActionsPostgres(
		ctx, jobID, taskID, document, prepared, targetIDs,
		embeddingIndexes, embeddingContents,
	)
}

func validateKnowledgeDocument(task KnowledgeIngestTask, document KnowledgeDocument) error {
	source := task.Source
	if document.SourceID != source.ID || document.CanonicalURL != source.URL {
		return errors.New("knowledge document does not match the claimed task source")
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
		semanticContentHash(document.Content) != document.ContentHash {
		return errors.New("knowledge document complete content is invalid")
	}
	return ValidateID("knowledge_evidence_id", document.EvidenceID)
}

func lockCurrentKnowledgeDocument(
	ctx context.Context,
	tx pgx.Tx,
	canonicalURL string,
) (currentKnowledgeDocument, bool, error) {
	var current currentKnowledgeDocument
	err := tx.QueryRow(ctx, `
SELECT id, content_hash, fetched_at_ms, reconciler_revision
FROM knowledge_documents
WHERE canonical_url = $1
FOR UPDATE`, canonicalURL).Scan(
		&current.documentID,
		&current.contentHash,
		&current.fetchedAtUnixMS,
		&current.reconcilerRevision,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return currentKnowledgeDocument{}, false, nil
	}
	if err != nil {
		return currentKnowledgeDocument{}, false, fmt.Errorf("locking current knowledge document: %w", err)
	}
	return current, true, nil
}

func knowledgeDocumentReconcilerIsCurrent(current currentKnowledgeDocument, document KnowledgeDocument) bool {
	return document.ReconcilerRevision == "" ||
		current.reconcilerRevision == document.ReconcilerRevision
}

func refreshSameHashKnowledgeDocumentMetadata(
	ctx context.Context,
	tx pgx.Tx,
	current currentKnowledgeDocument,
	document KnowledgeDocument,
	now int64,
) error {
	changed, err := tx.Exec(ctx, `
UPDATE knowledge_documents
SET title = CASE WHEN $2 > fetched_at_ms THEN $3 ELSE title END,
    fetched_at_ms = GREATEST(fetched_at_ms, $2),
    etag = CASE WHEN $2 > fetched_at_ms THEN $4 ELSE etag END,
    last_modified = CASE WHEN $2 > fetched_at_ms THEN $5 ELSE last_modified END,
    updated_at_ms = $6
WHERE id = $1 AND content_hash = $7`,
		current.documentID, document.FetchedAtUnixMS, document.Title,
		document.ETag, document.LastModified, now, current.contentHash)
	if err != nil {
		return fmt.Errorf("refreshing direct knowledge document metadata: %w", err)
	}
	if changed.RowsAffected() != 1 {
		return errors.New("knowledge document changed during refresh")
	}
	return nil
}

func (s *Store) settleKnowledgeDocumentWithoutExtraction(
	ctx context.Context,
	jobID string,
	taskID string,
	document KnowledgeDocument,
) (bool, error) {
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	tx, err := s.pool.Raw().Begin(queryCtx)
	if err != nil {
		return false, fmt.Errorf("beginning knowledge document freshness transaction: %w", err)
	}
	defer tx.Rollback(queryCtx)
	job, err := loadOwnedKnowledgeIngestJob(queryCtx, tx, jobID, s.workerID, true)
	if err != nil {
		return false, err
	}
	task, err := knowledgeIngestTaskFromJob(job)
	if err != nil || task.ID != taskID {
		return false, errors.New("knowledge document task does not match claimed job")
	}
	if err := validateKnowledgeDocument(task, document); err != nil {
		return false, err
	}
	current, exists, err := lockCurrentKnowledgeDocument(queryCtx, tx, document.CanonicalURL)
	if err != nil {
		return false, err
	}
	if !exists {
		return false, nil
	}
	settled := false
	now := nowUnixMS()
	switch {
	case current.contentHash == document.ContentHash && knowledgeDocumentReconcilerIsCurrent(current, document):
		if err := refreshSameHashKnowledgeDocumentMetadata(queryCtx, tx, current, document, now); err != nil {
			return false, err
		}
		settled = true
	case current.contentHash != document.ContentHash && current.fetchedAtUnixMS >= document.FetchedAtUnixMS:
		settled = true
	}
	if !settled {
		return false, nil
	}
	if err := FinishKnowledgeIngestJob(queryCtx, tx, jobID, s.workerID, "succeeded", "", now); err != nil {
		return false, err
	}
	if err := tx.Commit(queryCtx); err != nil {
		return false, fmt.Errorf("committing settled knowledge document feedback: %w", err)
	}
	return true, nil
}

func (s *Store) knowledgeDocumentNeedsExtractionPostgres(ctx context.Context, jobID, taskID string, document KnowledgeDocument) (bool, error) {
	settled, err := s.settleKnowledgeDocumentWithoutExtraction(ctx, jobID, taskID, document)
	return !settled, err
}

func (s *Store) commitPreparedKnowledgeDocumentActionsPostgres(
	ctx context.Context,
	jobID, taskID string,
	document KnowledgeDocument,
	prepared []preparedKnowledgeDocumentAction,
	targetIDs []string,
	embeddingIndexes []int,
	embeddingContents []string,
) (int, error) {
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	job, err := loadOwnedKnowledgeIngestJob(queryCtx, s.pool.Raw(), jobID, s.workerID, false)
	if err != nil {
		return 0, err
	}
	task, err := knowledgeIngestTaskFromJob(job)
	if err != nil || task.ID != taskID {
		return 0, errors.New("knowledge document task does not match claimed job")
	}
	if err := validateKnowledgeDocument(task, document); err != nil {
		return 0, err
	}
	settled, err := s.settleKnowledgeDocumentWithoutExtraction(ctx, jobID, taskID, document)
	if err != nil {
		return 0, err
	}
	if settled {
		return 0, nil
	}
	embeddings, err := embeddingsForContents(s.semanticEmbedder, embeddingContents)
	if err != nil {
		return 0, err
	}
	for index, actionIndex := range embeddingIndexes {
		prepared[actionIndex].embedding = embeddings[index]
	}
	sort.Strings(targetIDs)

	tx, err := s.pool.Raw().Begin(queryCtx)
	if err != nil {
		return 0, fmt.Errorf("beginning direct knowledge document transaction: %w", err)
	}
	defer tx.Rollback(queryCtx)
	lockedJob, err := loadOwnedKnowledgeIngestJob(queryCtx, tx, jobID, s.workerID, true)
	if err != nil {
		return 0, err
	}
	lockedTask, err := knowledgeIngestTaskFromJob(lockedJob)
	if err != nil || lockedTask.ID != taskID {
		return 0, errors.New("knowledge document task changed before commit")
	}
	now := nowUnixMS()
	current, exists, err := lockCurrentKnowledgeDocument(queryCtx, tx, document.CanonicalURL)
	if err != nil {
		return 0, err
	}
	if exists {
		if current.contentHash == document.ContentHash && knowledgeDocumentReconcilerIsCurrent(current, document) {
			if err := refreshSameHashKnowledgeDocumentMetadata(queryCtx, tx, current, document, now); err != nil {
				return 0, err
			}
			if err := FinishKnowledgeIngestJob(queryCtx, tx, jobID, s.workerID, "succeeded", "", now); err != nil {
				return 0, err
			}
			if err := tx.Commit(queryCtx); err != nil {
				return 0, fmt.Errorf("committing unchanged direct knowledge document: %w", err)
			}
			return 0, nil
		}
		if current.contentHash != document.ContentHash && current.fetchedAtUnixMS >= document.FetchedAtUnixMS {
			if err := FinishKnowledgeIngestJob(queryCtx, tx, jobID, s.workerID, "succeeded", "", now); err != nil {
				return 0, err
			}
			if err := tx.Commit(queryCtx); err != nil {
				return 0, fmt.Errorf("committing stale direct knowledge document: %w", err)
			}
			return 0, nil
		}
	}
	documentID := newID()
	if exists {
		documentID = current.documentID
		_, err = tx.Exec(queryCtx, `
UPDATE knowledge_documents
SET title = $2, content = $3, content_hash = $4, content_type = $5,
    fetched_at_ms = $6, etag = $7, last_modified = $8,
    reconciler_revision = $9, updated_at_ms = $10
WHERE id = $1`,
			documentID, document.Title, document.Content, document.ContentHash,
			document.ContentType, document.FetchedAtUnixMS, document.ETag,
			document.LastModified, document.ReconcilerRevision, now)
	} else {
		_, err = tx.Exec(queryCtx, `
INSERT INTO knowledge_documents(
  id, canonical_url, title, content, content_hash, content_type,
  fetched_at_ms, etag, last_modified, reconciler_revision,
  created_at_ms, updated_at_ms
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $11)`,
			documentID, document.CanonicalURL, document.Title, document.Content,
			document.ContentHash, document.ContentType, document.FetchedAtUnixMS,
			document.ETag, document.LastModified, document.ReconcilerRevision, now)
	}
	if err != nil {
		return 0, fmt.Errorf("storing direct knowledge document: %w", err)
	}

	for _, targetID := range targetIDs {
		var status string
		if err := tx.QueryRow(queryCtx, `
SELECT status FROM knowledge_entries WHERE id = $1 FOR UPDATE`,
			targetID).Scan(&status); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return 0, errors.New("knowledge ingest mutation target no longer exists")
			}
			return 0, fmt.Errorf("locking knowledge mutation target: %w", err)
		}
		if status != "verified" {
			return 0, errors.New("knowledge ingest mutation target is no longer verified")
		}
	}
	changed := 0
	for index, item := range prepared {
		switch item.action.Operation {
		case KnowledgeMutationNone:
			// NONE deliberately leaves the supplied entry and its source
			// ownership untouched. The current document is not appended as a
			// hidden second source.
		case KnowledgeMutationDelete:
			result, err := tx.Exec(queryCtx, `
UPDATE knowledge_entries
SET status = 'tombstone', document_id = $2, evidence_text = $3, updated_at_ms = $4
WHERE id = $1 AND status = 'verified'`,
				item.action.MemoryID, documentID, item.action.Evidence, now)
			if err != nil {
				return 0, fmt.Errorf("deleting recalled knowledge: %w", err)
			}
			if result.RowsAffected() != 1 {
				return 0, errors.New("knowledge ingest DELETE lost its target")
			}
			changed++
		case KnowledgeMutationUpdate:
			result, err := tx.Exec(queryCtx, `
UPDATE knowledge_entries
SET status = 'superseded', updated_at_ms = $2
WHERE id = $1 AND status = 'verified'`, item.action.MemoryID, now)
			if err != nil {
				return 0, fmt.Errorf("superseding recalled knowledge: %w", err)
			}
			if result.RowsAffected() != 1 {
				return 0, errors.New("knowledge ingest UPDATE lost its target")
			}
			fallthrough
		case KnowledgeMutationAdd:
			knowledgeID := newID()
			var supersedesID any
			if item.action.Operation == KnowledgeMutationUpdate {
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
  source_conversation_id, source_turn_id, document_id, evidence_text,
  supersedes_id, embedding_model_id, embedding_content_hash, embedding,
  created_at_ms, updated_at_ms
) VALUES (
  $1, $2, $3, 'verified', 'retrieval_ingest', $4,
  $5, $6, $7, $8, $9, $10, $11, $12::public.vector, $13, $13
)`,
				knowledgeID, item.topic, item.action.Content,
				item.action.ConfidenceBasisPoints,
				lockedTask.ConversationID, lockedTask.TurnID,
				documentID, item.action.Evidence, supersedesID,
				modelID, contentHash, vector, now,
			); err != nil {
				return 0, fmt.Errorf("inserting direct knowledge action[%d]: %w", index, err)
			}
			changed++
		default:
			return 0, errors.New("knowledge ingest mutation operation is invalid")
		}
	}
	if err := FinishKnowledgeIngestJob(queryCtx, tx, jobID, s.workerID, "succeeded", "", now); err != nil {
		return 0, err
	}
	if err := tx.Commit(queryCtx); err != nil {
		return 0, fmt.Errorf("committing direct knowledge document actions: %w", err)
	}
	return changed, nil
}
