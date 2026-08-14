package delivery

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	gomysql "github.com/go-sql-driver/mysql"

	"fairy/transport/session"
)

var (
	ErrSeekDBRequired    = errors.New("expression delivery SeekDB connection is required")
	ErrQueryLimitInvalid = errors.New("expression delivery query limit must be greater than zero")
	ErrNotFound          = errors.New("expression delivery was not found")
)

type Store struct {
	database   *sql.DB
	queryLimit time.Duration
	now        func() time.Time
}

func NewSeekDBStore(database *sql.DB, queryLimit time.Duration) (*Store, error) {
	if database == nil {
		return nil, ErrSeekDBRequired
	}
	if queryLimit <= 0 {
		return nil, ErrQueryLimitInvalid
	}
	return &Store{database: database, queryLimit: queryLimit, now: time.Now}, nil
}

func (s *Store) Record(ctx context.Context, result session.ExpressionDeliveryResult) error {
	if err := result.Validate(); err != nil {
		return err
	}
	queryCtx, cancel := s.queryContext(ctx)
	defer cancel()
	var external any
	if result.ExternalMessageID != "" {
		external = result.ExternalMessageID
	}
	var message any
	if result.ErrorMessage != "" {
		message = result.ErrorMessage
	}
	_, err := s.database.ExecContext(queryCtx, `
INSERT INTO expression_deliveries(
  conversation_id, turn_id, beat_id, status, external_message_id, error_message, created_at_ms
) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		result.ConversationID, result.TurnID, result.BeatID, result.Status, external, message, s.currentUnixMS(),
	)
	if isDuplicateSeekDBError(err) {
		return ErrDuplicate
	}
	if err != nil {
		return fmt.Errorf("record expression delivery: %w", err)
	}
	return nil
}

func (s *Store) Lookup(ctx context.Context, conversationID, turnID, beatID string) (session.ExpressionDeliveryResult, error) {
	queryCtx, cancel := s.queryContext(ctx)
	defer cancel()
	var (
		result   session.ExpressionDeliveryResult
		external sql.NullString
		message  sql.NullString
	)
	err := s.database.QueryRowContext(queryCtx, `
SELECT conversation_id, turn_id, beat_id, status, external_message_id, error_message
FROM expression_deliveries
WHERE conversation_id = ? AND turn_id = ? AND beat_id = ?`,
		conversationID, turnID, beatID,
	).Scan(&result.ConversationID, &result.TurnID, &result.BeatID, &result.Status, &external, &message)
	if errors.Is(err, sql.ErrNoRows) {
		return session.ExpressionDeliveryResult{}, ErrNotFound
	}
	if err != nil {
		return session.ExpressionDeliveryResult{}, fmt.Errorf("lookup expression delivery: %w", err)
	}
	if external.Valid {
		result.ExternalMessageID = external.String
	}
	if message.Valid {
		result.ErrorMessage = message.String
	}
	return result, nil
}

func (s *Store) queryContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(parent, s.queryLimit)
}

func (s *Store) currentUnixMS() int64 {
	now := time.Now
	if s != nil && s.now != nil {
		now = s.now
	}
	return max(now().UnixMilli(), int64(1))
}

func isDuplicateSeekDBError(err error) bool {
	var databaseError *gomysql.MySQLError
	return errors.As(err, &databaseError) && databaseError.Number == 1062
}
