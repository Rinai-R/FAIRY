package memory

import (
	"context"
	"net/url"
	"strings"
	"unicode/utf8"
)

// KnowledgeIngestSnapshot is a retrieval hit queued for automatic knowledge write.
type KnowledgeIngestSnapshot struct {
	ConversationID  string
	TurnID          string
	Query           string
	Title           string
	URL             string
	Snippet         string
	Rank            uint8
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
	return s.InsertVerifiedKnowledgeContext(context.Background(), topic, statement, conversationID, turnID, confidenceBasisPoints, sources)
}

func (s *Store) InsertVerifiedKnowledgeContext(
	ctx context.Context,
	topic string,
	statement string,
	conversationID string,
	turnID string,
	confidenceBasisPoints uint16,
	sources []AssistantSource,
) (KnowledgeRecord, error) {
	return s.insertVerifiedKnowledgePostgres(ctx, topic, statement, conversationID, turnID, confidenceBasisPoints, sources)
}

// Dedup: identical active statement is a no-op.

// EnqueueKnowledgeIngestSnapshots stores retrieval hits for a later auto-write pass.
func (s *Store) EnqueueKnowledgeIngestSnapshots(snapshots []KnowledgeIngestSnapshot) error {
	return s.EnqueueKnowledgeIngestSnapshotsContext(context.Background(), snapshots)
}

func (s *Store) EnqueueKnowledgeIngestSnapshotsContext(ctx context.Context, snapshots []KnowledgeIngestSnapshot) error {
	return s.enqueueKnowledgeIngestSnapshotsPostgres(ctx, snapshots)
}

// ProcessKnowledgeIngestJobs claims pending snapshots and writes verified knowledge
// without human confirmation. limit caps work per call.
func (s *Store) ProcessKnowledgeIngestJobs(limit int) (int, error) {
	return s.ProcessKnowledgeIngestJobsContext(context.Background(), limit)
}

func (s *Store) ProcessKnowledgeIngestJobsContext(ctx context.Context, limit int) (int, error) {
	return s.processKnowledgeIngestJobsPostgres(ctx, limit)
}

func acceptKnowledgeIngest(category, topic, statement, sourceURL string, rank uint8) bool {
	switch strings.TrimSpace(category) {
	case "anime", "game", "book":
	default:
		return false
	}
	topic = strings.TrimSpace(topic)
	statement = strings.TrimSpace(statement)
	if topic == "" || statement == "" || rank < 1 || rank > 5 {
		return false
	}
	if utf8.RuneCountInString(statement) < 8 {
		return false
	}
	parsed, err := url.Parse(strings.TrimSpace(sourceURL))
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Hostname() == "" || parsed.User != nil {
		return false
	}
	return true
}
