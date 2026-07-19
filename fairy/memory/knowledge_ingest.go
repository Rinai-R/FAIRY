package memory

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// KnowledgeIngestSnapshot is a retrieval hit queued for automatic knowledge write.
type KnowledgeIngestSnapshot struct {
	ConversationID string
	TurnID         string
	Query          string
	Title          string
	URL            string
	Snippet        string
	Rank           uint8
	FetchedAtUnixMS int64
}

// InsertVerifiedKnowledge writes searchable knowledge immediately (no human
// Confirm) and enqueues an embedding job.
func (s *Store) InsertVerifiedKnowledge(
	topic string,
	statement string,
	conversationID string,
	turnID string,
	confidenceBasisPoints uint16,
	sources []AssistantSource,
) (KnowledgeRecord, error) {
	topic = strings.TrimSpace(topic)
	statement = strings.TrimSpace(statement)
	if topic == "" || statement == "" {
		return KnowledgeRecord{}, errors.New("knowledge topic and statement are required")
	}
	if err := validateID("conversation_id", conversationID); err != nil {
		return KnowledgeRecord{}, err
	}
	if err := validateID("turn_id", turnID); err != nil {
		return KnowledgeRecord{}, err
	}
	if confidenceBasisPoints == 0 {
		confidenceBasisPoints = 7500
	}
	db, err := s.openWrite()
	if err != nil {
		return KnowledgeRecord{}, err
	}
	defer db.Close()
	now := nowUnixMS()
	id := newID()
	tx, err := db.Begin()
	if err != nil {
		return KnowledgeRecord{}, fmt.Errorf("beginning knowledge insert transaction: %w", err)
	}
	defer tx.Rollback()
	// Dedup: identical active statement is a no-op.
	var existingID string
	err = tx.QueryRow(
		"SELECT id FROM knowledge_entries WHERE status = 'verified' AND statement = ?1 LIMIT 1",
		statement,
	).Scan(&existingID)
	if err == nil && existingID != "" {
		if err := tx.Commit(); err != nil {
			return KnowledgeRecord{}, err
		}
		return knowledgeByID(db, existingID)
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return KnowledgeRecord{}, fmt.Errorf("checking duplicate knowledge: %w", err)
	}
	_, err = tx.Exec(
		`INSERT INTO knowledge_entries(
			id, topic, statement, status, verification_basis, confidence_basis_points,
			source_conversation_id, source_turn_id, created_at_ms, updated_at_ms
		) VALUES (?1, ?2, ?3, 'verified', 'retrieval_ingest', ?4, ?5, ?6, ?7, ?7)`,
		id, topic, statement, confidenceBasisPoints, conversationID, turnID, now,
	)
	if err != nil {
		return KnowledgeRecord{}, fmt.Errorf("inserting verified knowledge: %w", err)
	}
	for index, source := range sources {
		sourceID := newID()
		if _, err := tx.Exec(
			`INSERT INTO knowledge_sources(
				knowledge_id, source_id, title, url, snippet, rank, fetched_at_ms
			) VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7)`,
			id, sourceID, source.Title, source.URL, source.Snippet, source.Rank, source.FetchedAtUnixMS,
		); err != nil {
			return KnowledgeRecord{}, fmt.Errorf("inserting knowledge source[%d]: %w", index, err)
		}
	}
	if err := enqueueKnowledgeEmbeddingJob(tx, id, topic, statement, now); err != nil {
		return KnowledgeRecord{}, err
	}
	if err := tx.Commit(); err != nil {
		return KnowledgeRecord{}, fmt.Errorf("committing knowledge insert: %w", err)
	}
	return knowledgeByID(db, id)
}

// EnqueueKnowledgeIngestSnapshots stores retrieval hits for a later auto-write pass.
func (s *Store) EnqueueKnowledgeIngestSnapshots(snapshots []KnowledgeIngestSnapshot) error {
	if len(snapshots) == 0 {
		return nil
	}
	db, err := s.openWrite()
	if err != nil {
		return err
	}
	defer db.Close()
	now := nowUnixMS()
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, snap := range snapshots {
		if strings.TrimSpace(snap.Title) == "" && strings.TrimSpace(snap.Snippet) == "" {
			continue
		}
		if err := validateID("conversation_id", snap.ConversationID); err != nil {
			return err
		}
		if err := validateID("turn_id", snap.TurnID); err != nil {
			return err
		}
		_, err := tx.Exec(
			`INSERT INTO knowledge_ingest_jobs(
				id, conversation_id, turn_id, query, title, url, snippet, rank, fetched_at_ms, status, created_at_ms, updated_at_ms
			) VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9, 'pending', ?10, ?10)`,
			newID(), snap.ConversationID, snap.TurnID, snap.Query, snap.Title, snap.URL, snap.Snippet, snap.Rank, snap.FetchedAtUnixMS, now,
		)
		if err != nil {
			return fmt.Errorf("queueing knowledge ingest job: %w", err)
		}
	}
	return tx.Commit()
}

// ProcessKnowledgeIngestJobs claims pending snapshots and writes verified knowledge
// without human confirmation. limit caps work per call.
func (s *Store) ProcessKnowledgeIngestJobs(limit int) (int, error) {
	if limit < 1 {
		limit = 1
	}
	db, err := s.openWrite()
	if err != nil {
		return 0, err
	}
	defer db.Close()
	rows, err := db.Query(
		`SELECT id, conversation_id, turn_id, query, title, url, snippet, rank, fetched_at_ms
		 FROM knowledge_ingest_jobs WHERE status = 'pending'
		 ORDER BY created_at_ms ASC, id ASC LIMIT ?1`,
		limit,
	)
	if err != nil {
		return 0, fmt.Errorf("listing knowledge ingest jobs: %w", err)
	}
	defer rows.Close()
	type job struct {
		id, conversationID, turnID, query, title, url, snippet string
		rank                                                   uint8
		fetchedAt                                              int64
	}
	jobs := make([]job, 0, limit)
	for rows.Next() {
		var item job
		if err := rows.Scan(&item.id, &item.conversationID, &item.turnID, &item.query, &item.title, &item.url, &item.snippet, &item.rank, &item.fetchedAt); err != nil {
			return 0, err
		}
		jobs = append(jobs, item)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	written := 0
	for _, item := range jobs {
		topic := strings.TrimSpace(item.title)
		if topic == "" {
			topic = strings.TrimSpace(item.query)
		}
		statement := strings.TrimSpace(item.snippet)
		if statement == "" {
			statement = topic
		}
		if !acceptKnowledgeIngest(topic, statement) {
			_, _ = db.Exec("UPDATE knowledge_ingest_jobs SET status = 'dropped', updated_at_ms = ?2 WHERE id = ?1", item.id, nowUnixMS())
			continue
		}
		_, err := s.InsertVerifiedKnowledge(topic, statement, item.conversationID, item.turnID, 7000, []AssistantSource{{
			Title:           item.title,
			URL:             item.url,
			Snippet:         item.snippet,
			Rank:            item.rank,
			FetchedAtUnixMS: item.fetchedAt,
		}})
		if err != nil {
			_, _ = db.Exec(
				"UPDATE knowledge_ingest_jobs SET status = 'failed', error_message = ?2, updated_at_ms = ?3 WHERE id = ?1",
				item.id, err.Error(), nowUnixMS(),
			)
			continue
		}
		_, _ = db.Exec("UPDATE knowledge_ingest_jobs SET status = 'succeeded', updated_at_ms = ?2 WHERE id = ?1", item.id, nowUnixMS())
		written++
	}
	return written, nil
}

// acceptKnowledgeIngest applies structural reject rules only (no domain/topic
// examples) so cold ingest does not overfit to particular subjects.
func acceptKnowledgeIngest(topic, statement string) bool {
	topic = strings.TrimSpace(topic)
	statement = strings.TrimSpace(statement)
	if topic == "" || statement == "" {
		return false
	}
	if utf8.RuneCountInString(statement) < 8 {
		return false
	}
	lower := strings.ToLower(statement)
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
		if !strings.ContainsAny(statement, " \t\n") {
			return false
		}
	}
	return true
}
