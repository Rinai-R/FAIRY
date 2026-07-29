package memory

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

type preparedDocumentFact struct {
	fact      KnowledgeIngestFact
	slotKey   string
	embedding EmbeddingValue
}

func validateKnowledgeDocuments(batch KnowledgeIngestBatch, documents []KnowledgeDocument) (map[string]KnowledgeDocument, map[string]KnowledgeDocumentChunk, error) {
	if len(documents) == 0 || len(documents) > MaxKnowledgeIngestSources {
		return nil, nil, errors.New("knowledge document count is invalid")
	}
	sourceByID := make(map[string]KnowledgeIngestSource, len(batch.Sources))
	for _, source := range batch.Sources {
		sourceByID[source.ID] = source
	}
	documentByURL := make(map[string]KnowledgeDocument, len(documents))
	chunkByID := make(map[string]KnowledgeDocumentChunk)
	totalChunks := 0
	for index, document := range documents {
		source, exists := sourceByID[document.SourceID]
		if !exists || document.CanonicalURL != source.URL {
			return nil, nil, fmt.Errorf("knowledge document[%d] does not match a batch source", index)
		}
		parsed, err := url.Parse(document.CanonicalURL)
		if err != nil || parsed.User != nil || parsed.Hostname() == "" || parsed.Fragment != "" || parsed.String() != document.CanonicalURL || parsed.Scheme != "http" && parsed.Scheme != "https" {
			return nil, nil, fmt.Errorf("knowledge document[%d] URL is invalid", index)
		}
		if _, duplicate := documentByURL[document.CanonicalURL]; duplicate {
			return nil, nil, errors.New("knowledge document URL is duplicated")
		}
		if !validContentHash(document.ContentHash) || document.ContentType != "text/html" && document.ContentType != "text/plain" || document.FetchedAtUnixMS < 0 {
			return nil, nil, fmt.Errorf("knowledge document[%d] metadata is invalid", index)
		}
		if len(document.Chunks) == 0 || len(document.Chunks) > 32 {
			return nil, nil, fmt.Errorf("knowledge document[%d] chunk count is invalid", index)
		}
		for chunkIndex, chunk := range document.Chunks {
			if err := ValidateID("knowledge_chunk_id", chunk.ID); err != nil {
				return nil, nil, err
			}
			if chunk.Ordinal != chunkIndex || strings.TrimSpace(chunk.Text) != chunk.Text || chunk.Text == "" || utf8.RuneCountInString(chunk.Text) > 1200 {
				return nil, nil, fmt.Errorf("knowledge document[%d] chunk[%d] is invalid", index, chunkIndex)
			}
			if semanticContentHash(chunk.Text) != chunk.TextHash {
				return nil, nil, fmt.Errorf("knowledge document[%d] chunk[%d] hash is invalid", index, chunkIndex)
			}
			if _, duplicate := chunkByID[chunk.ID]; duplicate {
				return nil, nil, errors.New("knowledge chunk ID is duplicated")
			}
			chunkByID[chunk.ID] = chunk
			totalChunks++
		}
		documentByURL[document.CanonicalURL] = document
	}
	if totalChunks > 128 {
		return nil, nil, errors.New("knowledge batch chunk limit exceeded")
	}
	return documentByURL, chunkByID, nil
}

func (s *Store) knowledgeDocumentsNeedExtractionPostgres(ctx context.Context, jobID, batchID string, documents []KnowledgeDocument) (bool, error) {
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	job, err := loadOwnedKnowledgeIngestJob(queryCtx, s.pool.Raw(), jobID, s.workerID, false)
	if err != nil {
		return false, err
	}
	batch, err := knowledgeIngestBatchFromJob(job)
	if err != nil || batch.ID != batchID {
		return false, errors.New("knowledge document batch does not match claimed job")
	}
	if _, _, err := validateKnowledgeDocuments(batch, documents); err != nil {
		return false, err
	}
	for _, document := range documents {
		var currentHash string
		err := s.pool.Raw().QueryRow(queryCtx, `
SELECT COALESCE(current_content_hash, '')
FROM knowledge_documents
WHERE canonical_url = $1`, document.CanonicalURL).Scan(&currentHash)
		if errors.Is(err, pgx.ErrNoRows) || currentHash != document.ContentHash {
			return true, nil
		}
		if err != nil {
			return false, fmt.Errorf("checking knowledge document content hash: %w", err)
		}
	}
	if err := s.finishKnowledgeIngestJobPostgres(queryCtx, jobID, "succeeded", ""); err != nil {
		return false, err
	}
	return false, nil
}

func (s *Store) commitKnowledgeDocumentBatchPostgres(ctx context.Context, jobID, batchID string, documents []KnowledgeDocument, facts []KnowledgeIngestFact) (int, error) {
	if len(facts) > maxKnowledgeIngestFacts {
		return 0, errors.New("knowledge ingest fact count is invalid")
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	job, err := loadOwnedKnowledgeIngestJob(queryCtx, s.pool.Raw(), jobID, s.workerID, false)
	if err != nil {
		return 0, err
	}
	batch, err := knowledgeIngestBatchFromJob(job)
	if err != nil || batch.ID != batchID {
		return 0, errors.New("knowledge document batch does not match claimed job")
	}
	documentByURL, chunkByID, err := validateKnowledgeDocuments(batch, documents)
	if err != nil {
		return 0, err
	}
	prepared := make([]preparedDocumentFact, len(facts))
	contents := make([]string, len(facts))
	seenSlots := make(map[string]struct{}, len(facts))
	for index, fact := range facts {
		fact.Subject = strings.TrimSpace(fact.Subject)
		fact.Predicate = strings.TrimSpace(fact.Predicate)
		fact.Value = strings.TrimSpace(fact.Value)
		fact.Statement = strings.TrimSpace(fact.Statement)
		if fact.Subject == "" || utf8.RuneCountInString(fact.Subject) > 300 ||
			fact.Predicate == "" || utf8.RuneCountInString(fact.Predicate) > 160 ||
			fact.Value == "" || utf8.RuneCountInString(fact.Value) > 600 ||
			utf8.RuneCountInString(fact.Statement) < 8 || utf8.RuneCountInString(fact.Statement) > maxKnowledgeIngestStatementRunes ||
			fact.ConfidenceBasisPoints == 0 || fact.ConfidenceBasisPoints > 10000 {
			return 0, fmt.Errorf("knowledge ingest fact[%d] is invalid", index)
		}
		slotSum := sha256.Sum256([]byte(normalizeFactPart(fact.Subject) + "\x00" + normalizeFactPart(fact.Predicate)))
		slotKey := fmt.Sprintf("%x", slotSum[:])
		if _, duplicate := seenSlots[slotKey]; duplicate {
			return 0, errors.New("knowledge ingest fact slots are duplicated")
		}
		seenSlots[slotKey] = struct{}{}
		if len(fact.EvidenceChunkIDs) == 0 || len(fact.EvidenceChunkIDs) > 128 {
			return 0, fmt.Errorf("knowledge ingest fact[%d] evidence is invalid", index)
		}
		seenEvidence := make(map[string]struct{}, len(fact.EvidenceChunkIDs))
		for _, chunkID := range fact.EvidenceChunkIDs {
			if _, exists := chunkByID[chunkID]; !exists {
				return 0, fmt.Errorf("knowledge ingest fact[%d] references an unknown chunk", index)
			}
			if _, duplicate := seenEvidence[chunkID]; duplicate {
				return 0, fmt.Errorf("knowledge ingest fact[%d] repeats evidence", index)
			}
			seenEvidence[chunkID] = struct{}{}
		}
		prepared[index] = preparedDocumentFact{fact: fact, slotKey: slotKey}
		contents[index] = fact.Subject + "\n" + fact.Statement
	}
	embeddings, err := embeddingsForContents(s.semanticEmbedder, contents)
	if err != nil {
		return 0, err
	}
	for index := range prepared {
		prepared[index].embedding = embeddings[index]
	}

	tx, err := s.pool.Raw().Begin(queryCtx)
	if err != nil {
		return 0, fmt.Errorf("beginning knowledge document transaction: %w", err)
	}
	defer tx.Rollback(queryCtx)
	lockedJob, err := loadOwnedKnowledgeIngestJob(queryCtx, tx, jobID, s.workerID, true)
	if err != nil {
		return 0, err
	}
	lockedBatch, err := knowledgeIngestBatchFromJob(lockedJob)
	if err != nil || lockedBatch.ID != batchID {
		return 0, errors.New("knowledge document batch changed before commit")
	}
	now := nowUnixMS()
	chunkVersion := make(map[string]string, len(chunkByID))
	chunkDocument := make(map[string]KnowledgeDocument, len(chunkByID))
	for _, document := range documents {
		var documentID string
		var previousVersionID *string
		err := tx.QueryRow(queryCtx, `
SELECT id, current_version_id
FROM knowledge_documents
WHERE canonical_url = $1
FOR UPDATE`, document.CanonicalURL).Scan(&documentID, &previousVersionID)
		if errors.Is(err, pgx.ErrNoRows) {
			documentID = newID()
			if _, err := tx.Exec(queryCtx, `
INSERT INTO knowledge_documents(id, canonical_url, title, created_at_ms, updated_at_ms)
VALUES ($1, $2, $3, $4, $4)`, documentID, document.CanonicalURL, document.Title, now); err != nil {
				return 0, fmt.Errorf("inserting knowledge document: %w", err)
			}
			previousVersionID = nil
		} else if err != nil {
			return 0, fmt.Errorf("locking knowledge document: %w", err)
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
			for _, chunk := range document.Chunks {
				if _, err := tx.Exec(queryCtx, `
INSERT INTO knowledge_chunks(id, version_id, ordinal, text, text_hash, created_at_ms)
VALUES ($1, $2, $3, $4, $5, $6)`,
					chunk.ID, versionID, chunk.Ordinal, chunk.Text, chunk.TextHash, now,
				); err != nil {
					return 0, fmt.Errorf("inserting knowledge chunk: %w", err)
				}
			}
		} else if err != nil {
			return 0, fmt.Errorf("loading knowledge document version: %w", err)
		}
		if previousVersionID != nil && *previousVersionID != versionID {
			if _, err := tx.Exec(queryCtx, "UPDATE knowledge_document_versions SET status = 'superseded' WHERE id = $1", *previousVersionID); err != nil {
				return 0, err
			}
			if _, err := tx.Exec(queryCtx, `
UPDATE knowledge_evidence
SET active = false, invalidated_at_ms = $2
WHERE version_id = $1 AND active`, *previousVersionID, now); err != nil {
				return 0, err
			}
		}
		if _, err := tx.Exec(queryCtx, "UPDATE knowledge_document_versions SET status = 'current' WHERE id = $1", versionID); err != nil {
			return 0, err
		}
		if _, err := tx.Exec(queryCtx, `
UPDATE knowledge_documents
SET title = $2, current_version_id = $3, current_content_hash = $4, updated_at_ms = $5
WHERE id = $1`, documentID, document.Title, versionID, document.ContentHash, now); err != nil {
			return 0, err
		}
		for _, chunk := range document.Chunks {
			chunkVersion[chunk.ID] = versionID
			chunkDocument[chunk.ID] = document
		}
	}

	for index, item := range prepared {
		if _, err := tx.Exec(queryCtx, "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))", item.slotKey); err != nil {
			return 0, err
		}
		var knowledgeID, existingValue string
		var found bool
		err := tx.QueryRow(queryCtx, `
SELECT id, COALESCE(value, '')
FROM knowledge_entries
WHERE fact_key = $1 AND status = 'verified'
FOR UPDATE`, item.slotKey).Scan(&knowledgeID, &existingValue)
		if errors.Is(err, pgx.ErrNoRows) {
			knowledgeID = newID()
		} else if err != nil {
			return 0, err
		} else {
			found = true
		}
		supersedesID := any(nil)
		if found && normalizeFactPart(existingValue) != normalizeFactPart(item.fact.Value) {
			supersedesID = knowledgeID
			if _, err := tx.Exec(queryCtx, `
UPDATE knowledge_entries
SET status = 'superseded', updated_at_ms = $2
WHERE id = $1 AND status = 'verified'`, knowledgeID, now); err != nil {
				return 0, err
			}
			knowledgeID = newID()
			found = false
		}
		if !found {
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
  $5, $6, $7, $8, $9, $10, $11,
  $12, $13, $14::public.vector, $15, $15
)`,
				knowledgeID, item.fact.Subject, item.fact.Statement, item.fact.ConfidenceBasisPoints,
				lockedBatch.ConversationID, lockedBatch.TurnID, supersedesID,
				item.fact.Subject, item.fact.Predicate, item.fact.Value, item.slotKey,
				modelID, contentHash, vector, now,
			); err != nil {
				return 0, fmt.Errorf("inserting structured knowledge fact[%d]: %w", index, err)
			}
		}
		for _, chunkID := range item.fact.EvidenceChunkIDs {
			versionID := chunkVersion[chunkID]
			if _, err := tx.Exec(queryCtx, `
INSERT INTO knowledge_evidence(knowledge_id, chunk_id, version_id, active, created_at_ms)
VALUES ($1, $2, $3, true, $4)
ON CONFLICT (knowledge_id, chunk_id) DO UPDATE
SET active = true, invalidated_at_ms = NULL`, knowledgeID, chunkID, versionID, now); err != nil {
				return 0, fmt.Errorf("merging knowledge evidence: %w", err)
			}
			document := chunkDocument[chunkID]
			source := sourceForDocument(lockedBatch, document.SourceID)
			if err := InsertCanonicalKnowledgeSource(queryCtx, tx, knowledgeID, newID(), AssistantSource{
				Title: document.Title, URL: document.CanonicalURL, Snippet: source.Snippet,
				Rank: source.Rank, FetchedAtUnixMS: document.FetchedAtUnixMS,
			}); err != nil {
				return 0, err
			}
		}
	}
	if _, err := tx.Exec(queryCtx, `
UPDATE knowledge_entries k
SET status = 'tombstone', updated_at_ms = $1
WHERE k.status = 'verified'
  AND k.fact_key IS NOT NULL
  AND NOT EXISTS (
    SELECT 1 FROM knowledge_evidence e
    WHERE e.knowledge_id = k.id AND e.active
  )`, now); err != nil {
		return 0, fmt.Errorf("tombstoning knowledge without evidence: %w", err)
	}
	if err := FinishKnowledgeIngestJob(queryCtx, tx, jobID, s.workerID, "succeeded", "", now); err != nil {
		return 0, err
	}
	if err := tx.Commit(queryCtx); err != nil {
		return 0, fmt.Errorf("committing knowledge document batch: %w", err)
	}
	_ = documentByURL
	return len(prepared), nil
}

func normalizeFactPart(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(value), " "))
}

func sourceForDocument(batch KnowledgeIngestBatch, sourceID string) KnowledgeIngestSource {
	for _, source := range batch.Sources {
		if source.ID == sourceID {
			return source
		}
	}
	return KnowledgeIngestSource{}
}
