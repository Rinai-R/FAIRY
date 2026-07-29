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
	versionID          *string
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
		statement := action.Content
		confidence := action.ConfidenceBasisPoints
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
			if _, ok := supplied[action.MemoryID]; !ok || statement != "" || confidence != 0 {
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
			statementRunes := utf8.RuneCountInString(statement)
			if strings.TrimSpace(statement) != statement ||
				statementRunes < 8 || statementRunes > maxKnowledgeDocumentActionContentRunes ||
				ContainsDisallowedControl(statement) ||
				confidence == 0 || confidence > 10000 {
				return 0, fmt.Errorf("knowledge document action[%d] content is invalid", index)
			}
			if _, duplicate := seenContents[statement]; duplicate {
				return 0, errors.New("knowledge document action content is duplicated")
			}
			seenContents[statement] = struct{}{}
			embeddingIndexes = append(embeddingIndexes, index)
			embeddingContents = append(embeddingContents, topic+"\n"+statement)
		}
		prepared[index] = preparedKnowledgeDocumentAction{
			action: action,
			topic:  topic,
		}
	}
	return s.commitPreparedKnowledgeDocumentActionsPostgres(
		ctx, jobID, taskID,
		document,
		prepared,
		targetIDs,
		embeddingIndexes,
		embeddingContents,
	)
}

func validateKnowledgeDocument(task KnowledgeIngestTask, document KnowledgeDocument) error {
	source := task.Source
	if document.SourceID != source.ID || document.CanonicalURL != source.URL {
		return errors.New("knowledge document does not match the claimed task source")
	}
	parsed, err := url.Parse(document.CanonicalURL)
	if err != nil || parsed.User != nil || parsed.Hostname() == "" || parsed.Fragment != "" || parsed.String() != document.CanonicalURL || parsed.Scheme != "http" && parsed.Scheme != "https" {
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
SELECT d.id, d.current_version_id,
       COALESCE(v.content_hash, ''), COALESCE(v.fetched_at_ms, 0),
       COALESCE(v.reconciler_revision, '')
FROM knowledge_documents d
LEFT JOIN knowledge_document_versions v ON v.id = d.current_version_id
WHERE d.canonical_url = $1
FOR UPDATE OF d`, canonicalURL).Scan(
		&current.documentID,
		&current.versionID,
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

func advanceKnowledgeDocumentVersionMetadata(
	ctx context.Context,
	tx pgx.Tx,
	versionID string,
	document KnowledgeDocument,
) error {
	changed, err := tx.Exec(ctx, `
UPDATE knowledge_document_versions
SET fetched_at_ms = GREATEST(fetched_at_ms, $2),
    etag = CASE WHEN $2 > fetched_at_ms THEN $3 ELSE etag END,
    last_modified = CASE WHEN $2 > fetched_at_ms THEN $4 ELSE last_modified END
WHERE id = $1`,
		versionID,
		document.FetchedAtUnixMS,
		document.ETag,
		document.LastModified,
	)
	if err != nil {
		return fmt.Errorf("advancing knowledge document version metadata: %w", err)
	}
	if changed.RowsAffected() != 1 {
		return errors.New("knowledge document version no longer exists")
	}
	return nil
}

func refreshSameHashKnowledgeDocumentMetadata(
	ctx context.Context,
	tx pgx.Tx,
	task KnowledgeIngestTask,
	current currentKnowledgeDocument,
	document KnowledgeDocument,
	now int64,
) error {
	if current.versionID == nil {
		return errors.New("knowledge document current version is unavailable")
	}
	versionID := *current.versionID
	if err := advanceKnowledgeDocumentVersionMetadata(ctx, tx, versionID, document); err != nil {
		return err
	}
	if document.FetchedAtUnixMS > current.fetchedAtUnixMS {
		changed, err := tx.Exec(ctx, `
UPDATE knowledge_documents
SET title = $2, updated_at_ms = $3
WHERE id = $1 AND current_version_id = $4`,
			current.documentID,
			document.Title,
			now,
			versionID,
		)
		if err != nil {
			return fmt.Errorf("refreshing same-hash knowledge document metadata: %w", err)
		}
		if changed.RowsAffected() != 1 {
			return errors.New("knowledge document current version changed")
		}
	}
	source := task.Source
	if _, err := tx.Exec(ctx, `
UPDATE knowledge_sources AS s
SET title = $2,
    snippet = $3,
    rank = $4,
    fetched_at_ms = $5
WHERE (s.canonical_url = $1 OR s.url = $1)
  AND s.fetched_at_ms < $5
  AND EXISTS (
    SELECT 1
    FROM knowledge_evidence AS e
    WHERE e.knowledge_id = s.knowledge_id
      AND e.version_id = $6
      AND e.active
  )`,
		document.CanonicalURL,
		document.Title,
		source.Snippet,
		source.Rank,
		document.FetchedAtUnixMS,
		versionID,
	); err != nil {
		return fmt.Errorf("refreshing same-hash knowledge sources: %w", err)
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
	if !exists || current.versionID == nil {
		return false, nil
	}
	if current.contentHash == document.ContentHash {
		if !knowledgeDocumentReconcilerIsCurrent(current, document) {
			return false, nil
		}
		if err := refreshSameHashKnowledgeDocumentMetadata(
			queryCtx,
			tx,
			task,
			current,
			document,
			nowUnixMS(),
		); err != nil {
			return false, err
		}
	} else if current.fetchedAtUnixMS >= document.FetchedAtUnixMS {
		if err := FinishKnowledgeIngestJob(queryCtx, tx, jobID, s.workerID, "succeeded", "", nowUnixMS()); err != nil {
			return false, err
		}
		if err := tx.Commit(queryCtx); err != nil {
			return false, fmt.Errorf("committing stale knowledge document job: %w", err)
		}
		return true, nil
	} else {
		return false, nil
	}
	if err := FinishKnowledgeIngestJob(queryCtx, tx, jobID, s.workerID, "succeeded", "", nowUnixMS()); err != nil {
		return false, err
	}
	if err := tx.Commit(queryCtx); err != nil {
		return false, fmt.Errorf("committing unchanged knowledge document job: %w", err)
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
	settled, err := s.settleKnowledgeDocumentWithoutExtraction(
		ctx,
		jobID,
		taskID,
		document,
	)
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
		return 0, fmt.Errorf("beginning knowledge document transaction: %w", err)
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
	var documentID string
	var previousVersionID *string
	refreshDocumentTitle := true
	if !exists {
		documentID = newID()
		if _, err := tx.Exec(queryCtx, `
INSERT INTO knowledge_documents(id, canonical_url, title, created_at_ms, updated_at_ms)
VALUES ($1, $2, $3, $4, $4)`, documentID, document.CanonicalURL, document.Title, now); err != nil {
			return 0, fmt.Errorf("inserting knowledge document: %w", err)
		}
		previousVersionID = nil
	} else {
		documentID = current.documentID
		previousVersionID = current.versionID
		sameContent := current.versionID != nil && current.contentHash == document.ContentHash
		if sameContent && knowledgeDocumentReconcilerIsCurrent(current, document) {
			if err := refreshSameHashKnowledgeDocumentMetadata(
				queryCtx,
				tx,
				lockedTask,
				current,
				document,
				now,
			); err != nil {
				return 0, err
			}
			if err := FinishKnowledgeIngestJob(queryCtx, tx, jobID, s.workerID, "succeeded", "", now); err != nil {
				return 0, err
			}
			if err := tx.Commit(queryCtx); err != nil {
				return 0, fmt.Errorf("committing stale knowledge document job: %w", err)
			}
			return 0, nil
		}
		if sameContent {
			refreshDocumentTitle = document.FetchedAtUnixMS > current.fetchedAtUnixMS
			if err := refreshSameHashKnowledgeDocumentMetadata(
				queryCtx,
				tx,
				lockedTask,
				current,
				document,
				now,
			); err != nil {
				return 0, err
			}
		} else if current.versionID != nil &&
			current.fetchedAtUnixMS >= document.FetchedAtUnixMS {
			if err := FinishKnowledgeIngestJob(queryCtx, tx, jobID, s.workerID, "succeeded", "", now); err != nil {
				return 0, err
			}
			if err := tx.Commit(queryCtx); err != nil {
				return 0, fmt.Errorf("committing stale knowledge document job: %w", err)
			}
			return 0, nil
		}
	}
	var versionID string
	err = tx.QueryRow(queryCtx, `
SELECT id FROM knowledge_document_versions
WHERE document_id = $1 AND content_hash = $2`, documentID, document.ContentHash).Scan(&versionID)
	if errors.Is(err, pgx.ErrNoRows) {
		versionID = newID()
		if _, err := tx.Exec(queryCtx, `
INSERT INTO knowledge_document_versions(
  id, document_id, content_hash, content_type, status, fetched_at_ms, etag, last_modified, created_at_ms
) VALUES ($1, $2, $3, $4, 'staged', $5, $6, $7, $8)`,
			versionID, documentID, document.ContentHash, document.ContentType,
			document.FetchedAtUnixMS, document.ETag, document.LastModified, now,
		); err != nil {
			return 0, fmt.Errorf("inserting knowledge document version: %w", err)
		}
		if _, err := tx.Exec(queryCtx, `
INSERT INTO knowledge_chunks(id, version_id, ordinal, text, text_hash, created_at_ms)
VALUES ($1, $2, $3, $4, $5, $6)`,
			document.EvidenceID, versionID, 0, document.Content, document.ContentHash, now,
		); err != nil {
			return 0, fmt.Errorf("inserting complete knowledge document evidence: %w", err)
		}
	} else if err != nil {
		return 0, fmt.Errorf("loading knowledge document version: %w", err)
	} else if document.Content != "" {
		if err := ensureCompleteKnowledgeDocumentEvidence(queryCtx, tx, versionID, document, now); err != nil {
			return 0, err
		}
		if err := advanceKnowledgeDocumentVersionMetadata(queryCtx, tx, versionID, document); err != nil {
			return 0, err
		}
	}
	if previousVersionID != nil && *previousVersionID != versionID {
		if _, err := tx.Exec(queryCtx, "UPDATE knowledge_document_versions SET status = 'superseded' WHERE id = $1", *previousVersionID); err != nil {
			return 0, err
		}
	}
	if _, err := tx.Exec(queryCtx, "UPDATE knowledge_document_versions SET status = 'current' WHERE id = $1", versionID); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(queryCtx, `
UPDATE knowledge_documents
SET title = CASE WHEN $6 THEN $2 ELSE title END,
    current_version_id = $3,
    current_content_hash = $4,
    updated_at_ms = $5
WHERE id = $1`,
		documentID,
		document.Title,
		versionID,
		document.ContentHash,
		now,
		refreshDocumentTitle,
	); err != nil {
		return 0, err
	}

	for _, targetID := range targetIDs {
		var status string
		if err := tx.QueryRow(queryCtx, `
SELECT status
FROM knowledge_entries
WHERE id = $1
FOR UPDATE`, targetID).Scan(&status); err != nil {
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
		knowledgeID := ""
		switch item.action.Operation {
		case KnowledgeMutationNone:
			knowledgeID = item.action.MemoryID
		case KnowledgeMutationDelete:
			knowledgeID = item.action.MemoryID
			result, err := tx.Exec(queryCtx, `
UPDATE knowledge_entries
SET status = 'tombstone', updated_at_ms = $2
WHERE id = $1 AND status = 'verified'`, item.action.MemoryID, now)
			if err != nil {
				return 0, fmt.Errorf("deleting recalled knowledge: %w", err)
			}
			if result.RowsAffected() != 1 {
				return 0, errors.New("knowledge ingest DELETE lost its target")
			}
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
		case KnowledgeMutationAdd:
		default:
			return 0, errors.New("knowledge ingest mutation operation is invalid")
		}

		if item.action.Operation == KnowledgeMutationAdd || item.action.Operation == KnowledgeMutationUpdate {
			knowledgeID = newID()
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
  source_conversation_id, source_turn_id, supersedes_id,
  subject, predicate, value, fact_key,
  embedding_model_id, embedding_content_hash, embedding,
  created_at_ms, updated_at_ms
) VALUES (
  $1, $2, $3, 'verified', 'retrieval_ingest', $4,
  $5, $6, $7, $8, $9, $10, NULL,
  $11, $12, $13::public.vector, $14, $14
)`,
				knowledgeID, item.topic, item.action.Content, item.action.ConfidenceBasisPoints,
				lockedTask.ConversationID, lockedTask.TurnID, supersedesID,
				nil, nil, nil,
				modelID, contentHash, vector, now,
			); err != nil {
				return 0, fmt.Errorf("inserting reconciled knowledge action[%d]: %w", index, err)
			}
		}
		if item.action.MemoryID != "" {
			if _, err := tx.Exec(queryCtx, `
UPDATE knowledge_evidence AS e
SET active = false, invalidated_at_ms = $3
FROM knowledge_document_versions AS v
JOIN knowledge_documents AS d ON d.id = v.document_id
WHERE e.knowledge_id = $1
  AND e.version_id = v.id
  AND d.canonical_url = $2
  AND e.chunk_id <> $4
  AND e.active`,
				item.action.MemoryID,
				document.CanonicalURL,
				now,
				document.EvidenceID,
			); err != nil {
				return 0, fmt.Errorf("invalidating reconciled knowledge evidence: %w", err)
			}
		}
		if _, err := tx.Exec(queryCtx, `
INSERT INTO knowledge_evidence(knowledge_id, chunk_id, version_id, active, created_at_ms)
VALUES ($1, $2, $3, true, $4)
ON CONFLICT (knowledge_id, chunk_id) DO UPDATE
SET active = true, invalidated_at_ms = NULL`,
			knowledgeID, document.EvidenceID, versionID, now,
		); err != nil {
			return 0, fmt.Errorf("merging knowledge evidence: %w", err)
		}
		source := lockedTask.Source
		if err := InsertCanonicalKnowledgeSource(queryCtx, tx, knowledgeID, newID(), AssistantSource{
			Title: document.Title, URL: document.CanonicalURL, Snippet: source.Snippet,
			Rank: source.Rank, FetchedAtUnixMS: document.FetchedAtUnixMS,
		}); err != nil {
			return 0, err
		}
		if item.action.Operation != KnowledgeMutationNone {
			changed++
		}
	}
	if document.ReconcilerRevision != "" {
		updated, err := tx.Exec(queryCtx, `
UPDATE knowledge_document_versions
SET reconciler_revision = $2
WHERE id = $1`,
			versionID,
			document.ReconcilerRevision,
		)
		if err != nil {
			return 0, fmt.Errorf("updating knowledge document reconciler revision: %w", err)
		}
		if updated.RowsAffected() != 1 {
			return 0, errors.New("knowledge document version no longer exists")
		}
	}
	if err := FinishKnowledgeIngestJob(queryCtx, tx, jobID, s.workerID, "succeeded", "", now); err != nil {
		return 0, err
	}
	if err := tx.Commit(queryCtx); err != nil {
		return 0, fmt.Errorf("committing knowledge document actions: %w", err)
	}
	return changed, nil
}

func ensureCompleteKnowledgeDocumentEvidence(
	ctx context.Context,
	tx pgx.Tx,
	versionID string,
	document KnowledgeDocument,
	now int64,
) error {
	var existingVersionID, existingText, existingTextHash string
	err := tx.QueryRow(ctx, `
SELECT version_id, text, text_hash
FROM knowledge_chunks
WHERE id = $1`, document.EvidenceID).Scan(&existingVersionID, &existingText, &existingTextHash)
	if err == nil {
		if existingVersionID != versionID || existingText != document.Content || existingTextHash != document.ContentHash {
			return errors.New("complete knowledge document evidence changed")
		}
		return nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("loading complete knowledge document evidence: %w", err)
	}
	var ordinal int
	if err := tx.QueryRow(ctx, `
SELECT COALESCE(MAX(ordinal), -1) + 1
FROM knowledge_chunks
WHERE version_id = $1`, versionID).Scan(&ordinal); err != nil {
		return fmt.Errorf("selecting complete knowledge document evidence ordinal: %w", err)
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO knowledge_chunks(id, version_id, ordinal, text, text_hash, created_at_ms)
VALUES ($1, $2, $3, $4, $5, $6)`,
		document.EvidenceID, versionID, ordinal, document.Content, document.ContentHash, now,
	); err != nil {
		return fmt.Errorf("inserting complete knowledge document evidence: %w", err)
	}
	return nil
}
