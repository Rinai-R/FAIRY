package memory

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	pgstore "fairy/postgres"
)

const (
	SemanticEmbeddingModelID    = "bge-small-zh-v1.5"
	SemanticEmbeddingDimensions = 512
)

var (
	ErrDatabasePoolEmpty = errors.New("memory database pool is required")
	ErrWorkerIDInvalid   = errors.New("memory worker id is invalid")
	ErrJobLeaseInvalid   = errors.New("memory job lease duration is invalid")
)

const defaultJobLeaseDuration = 30 * time.Second

type Summary struct {
	Conversations           int64 `json:"conversations"`
	ActiveGlobalMemories    int64 `json:"activeGlobalMemories"`
	ActiveCharacterMemories int64 `json:"activeCharacterMemories"`
	NeedsReviewMemories     int64 `json:"needsReviewMemories"`
	PendingExtractionTurns  int64 `json:"pendingExtractionTurns"`
	RunningBatches          int64 `json:"runningBatches"`
	FailedBatches           int64 `json:"failedBatches"`
	CandidateKnowledge      int64 `json:"candidateKnowledge"`
	VerifiedKnowledge       int64 `json:"verifiedKnowledge"`
	ReadOnly                bool  `json:"readOnly"`
}

type Store struct {
	pool             *pgstore.Pool
	workerID         string
	jobLeaseDuration time.Duration
}

func NewStoreFromPool(pool *pgstore.Pool) (*Store, error) {
	return newStoreFromPoolWithLease(pool, "memory-"+newID(), defaultJobLeaseDuration)
}

func newStoreFromPoolWithLease(pool *pgstore.Pool, workerID string, leaseDuration time.Duration) (*Store, error) {
	if pool == nil || pool.Raw() == nil {
		return nil, ErrDatabasePoolEmpty
	}
	if workerID == "" || workerID != strings.TrimSpace(workerID) || containsDisallowedControl(workerID) {
		return nil, ErrWorkerIDInvalid
	}
	if leaseDuration <= 0 {
		return nil, ErrJobLeaseInvalid
	}
	return &Store{pool: pool, workerID: workerID, jobLeaseDuration: leaseDuration}, nil
}

func (s *Store) Summary() (Summary, error) {
	return s.SummaryContext(context.Background())
}

func (s *Store) SummaryContext(ctx context.Context) (Summary, error) {
	return s.postgresSummary(ctx)
}

func (s *Store) postgresSummary(ctx context.Context) (Summary, error) {
	if s == nil || s.pool == nil || s.pool.Raw() == nil {
		return Summary{}, ErrDatabasePoolEmpty
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	conversations, err := countPostgresScalar(queryCtx, s.pool, "SELECT COUNT(*) FROM conversations")
	if err != nil {
		return Summary{}, fmt.Errorf("counting conversations: %w", err)
	}
	activeGlobalMemories, err := countPostgresScalar(queryCtx, s.pool, "SELECT COUNT(*) FROM personal_memories WHERE scope_kind = 'global' AND review_status = 'ready' AND status = 'active'")
	if err != nil {
		return Summary{}, fmt.Errorf("counting global memories: %w", err)
	}
	activeCharacterMemories, err := countPostgresScalar(queryCtx, s.pool, "SELECT COUNT(*) FROM personal_memories WHERE scope_kind = 'character' AND review_status = 'ready' AND status = 'active'")
	if err != nil {
		return Summary{}, fmt.Errorf("counting character memories: %w", err)
	}
	needsReviewMemories, err := countPostgresScalar(queryCtx, s.pool, "SELECT COUNT(*) FROM personal_memories WHERE scope_kind = 'unassigned_legacy' AND review_status = 'needs_review' AND status = 'active'")
	if err != nil {
		return Summary{}, fmt.Errorf("counting needs-review memories: %w", err)
	}
	pendingExtractionTurns, err := countPostgresScalar(queryCtx, s.pool, "SELECT COUNT(*) FROM conversation_turns WHERE extraction_state = 'pending'")
	if err != nil {
		return Summary{}, fmt.Errorf("counting pending extraction turns: %w", err)
	}
	runningBatches, err := countPostgresScalar(queryCtx, s.pool, "SELECT COUNT(*) FROM extraction_batches WHERE status = 'running'")
	if err != nil {
		return Summary{}, fmt.Errorf("counting running batches: %w", err)
	}
	failedBatches, err := countPostgresScalar(queryCtx, s.pool, "SELECT COUNT(*) FROM extraction_batches WHERE status = 'failed'")
	if err != nil {
		return Summary{}, fmt.Errorf("counting failed batches: %w", err)
	}
	candidateKnowledge, err := countPostgresScalar(queryCtx, s.pool, "SELECT COUNT(*) FROM knowledge_entries WHERE status = 'candidate'")
	if err != nil {
		return Summary{}, fmt.Errorf("counting candidate knowledge: %w", err)
	}
	verifiedKnowledge, err := countPostgresScalar(queryCtx, s.pool, "SELECT COUNT(*) FROM knowledge_entries WHERE status = 'verified'")
	if err != nil {
		return Summary{}, fmt.Errorf("counting verified knowledge: %w", err)
	}
	return Summary{
		Conversations:           conversations,
		ActiveGlobalMemories:    activeGlobalMemories,
		ActiveCharacterMemories: activeCharacterMemories,
		NeedsReviewMemories:     needsReviewMemories,
		PendingExtractionTurns:  pendingExtractionTurns,
		RunningBatches:          runningBatches,
		FailedBatches:           failedBatches,
		CandidateKnowledge:      candidateKnowledge,
		VerifiedKnowledge:       verifiedKnowledge,
		ReadOnly:                true,
	}, nil
}

func countPostgresScalar(ctx context.Context, pool *pgstore.Pool, query string) (int64, error) {
	var count int64
	if err := pool.Raw().QueryRow(ctx, query).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}
