package memory

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"fairy/coredb"
	"fairy/session"
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

type Store struct {
	pool             *coredb.Pool
	semanticEmbedder SemanticEmbedder
	workerID         string
	jobLeaseDuration time.Duration
}

func NewStoreFromPool(pool *coredb.Pool) (*Store, error) {
	return newStoreFromPool(pool, nil, "memory-"+newID(), defaultJobLeaseDuration)
}

func NewStoreFromPoolWithEmbedder(pool *coredb.Pool, embedder SemanticEmbedder) (*Store, error) {
	if embedder == nil {
		return nil, errors.New("semantic embedder is required")
	}
	return newStoreFromPool(pool, embedder, "memory-"+newID(), defaultJobLeaseDuration)
}

func NewStoreFromPoolWithLease(pool *coredb.Pool, workerID string, leaseDuration time.Duration) (*Store, error) {
	return newStoreFromPool(pool, nil, workerID, leaseDuration)
}

func newStoreFromPool(pool *coredb.Pool, embedder SemanticEmbedder, workerID string, leaseDuration time.Duration) (*Store, error) {
	if pool == nil || pool.Raw() == nil {
		return nil, ErrDatabasePoolEmpty
	}
	if workerID == "" || workerID != strings.TrimSpace(workerID) || ContainsDisallowedControl(workerID) {
		return nil, ErrWorkerIDInvalid
	}
	if leaseDuration <= 0 {
		return nil, ErrJobLeaseInvalid
	}
	return &Store{pool: pool, semanticEmbedder: embedder, workerID: workerID, jobLeaseDuration: leaseDuration}, nil
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
	pendingExtractionTurns, err := countPostgresScalar(queryCtx, s.pool, "SELECT COUNT(*) FROM feedback_events WHERE type = 'personal_memory' AND status IN ('waiting_turn', 'pending')")
	if err != nil {
		return Summary{}, fmt.Errorf("counting pending extraction turns: %w", err)
	}
	runningBatches, err := countPostgresScalar(queryCtx, s.pool, "SELECT COUNT(DISTINCT claim_group_id) FROM feedback_events WHERE type = 'personal_memory' AND status = 'running'")
	if err != nil {
		return Summary{}, fmt.Errorf("counting running batches: %w", err)
	}
	failedBatches, err := countPostgresScalar(queryCtx, s.pool, "SELECT COUNT(*) FROM feedback_events WHERE type = 'personal_memory' AND status = 'failed'")
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

func countPostgresScalar(ctx context.Context, pool *coredb.Pool, query string) (int64, error) {
	var count int64
	if err := pool.Raw().QueryRow(ctx, query).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

func validateEndpointConversationKey(characterID string, binding session.Binding, digest string) error {
	if err := ValidateID("character_id", characterID); err != nil {
		return err
	}
	if err := binding.Validate(); err != nil {
		return err
	}
	if err := session.ValidateDigest(digest); err != nil {
		return errors.New("endpoint key digest is invalid")
	}
	return nil
}

func nowUnixMS() int64 {
	return time.Now().UnixMilli()
}

func newID() string {
	var data [16]byte
	if _, err := rand.Read(data[:]); err != nil {
		panic(err)
	}
	data[6] = (data[6] & 0x0f) | 0x40
	data[8] = (data[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(data[:])
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32]
}
