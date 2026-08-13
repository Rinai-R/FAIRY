package identity

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	coredb "fairy/runtime/database"
)

var (
	ErrIdentityDatabasePoolRequired = errors.New("identity database pool is required")
	ErrIdentitySeekDBRequired       = errors.New("identity SeekDB database is required")
	ErrIdentityQueryLimitInvalid    = errors.New("identity query limit must be greater than zero")
	ErrOwnerIdentityNotFound        = errors.New("owner identity does not exist")
	ErrOwnerIdentityCorrupt         = errors.New("owner identity record is corrupt")
)

type OwnerIdentity struct {
	Namespace       string `json:"namespace"`
	PrincipalDigest string `json:"principalDigest"`
	CreatedAtUnixMS int64  `json:"createdAtUnixMs"`
}

type Store struct {
	pool       *coredb.Pool
	seekDB     *sql.DB
	queryLimit time.Duration
	now        func() time.Time
}

// NewStore preserves the PostgreSQL-backed constructor while the legacy
// runtime is removed. New edge composition must use NewSeekDBStore.
func NewStore(pool *coredb.Pool) (*Store, error) {
	if pool == nil || pool.Raw() == nil {
		return nil, ErrIdentityDatabasePoolRequired
	}
	return &Store{pool: pool, now: time.Now}, nil
}

// NewSeekDBStore stores only fixed-width identity digests in the local SeekDB
// authority. It never falls back to the legacy PostgreSQL pool.
func NewSeekDBStore(database *sql.DB, queryLimit time.Duration) (*Store, error) {
	if database == nil {
		return nil, ErrIdentitySeekDBRequired
	}
	if queryLimit <= 0 {
		return nil, ErrIdentityQueryLimitInvalid
	}
	return &Store{seekDB: database, queryLimit: queryLimit, now: time.Now}, nil
}

func (s *Store) BindOwner(namespace, principalDigest string) error {
	return s.BindOwnerContext(context.Background(), namespace, principalDigest)
}

func (s *Store) BindOwnerContext(ctx context.Context, namespace, principalDigest string) error {
	digest, err := validateAndDecodeIdentity(namespace, principalDigest)
	if err != nil {
		return err
	}
	if s != nil && s.seekDB != nil {
		queryCtx, cancel := s.seekDBQueryContext(ctx)
		defer cancel()
		if err := bindOwnerSeekDB(queryCtx, s.seekDB, namespace, digest, s.currentUnixMillis()); err != nil {
			return fmt.Errorf("binding owner identity in SeekDB: %w", err)
		}
		return nil
	}
	if s == nil || s.pool == nil || s.pool.Raw() == nil {
		return ErrIdentityDatabasePoolRequired
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	_, err = s.pool.Raw().Exec(queryCtx, `
INSERT INTO owner_identities(namespace, subject_digest, created_at_ms)
VALUES ($1, $2, $3)
ON CONFLICT(namespace, subject_digest) DO NOTHING`, namespace, principalDigest, s.currentUnixMillis())
	if err != nil {
		return fmt.Errorf("binding owner identity: %w", err)
	}
	return nil
}

func (s *Store) ListOwners() ([]OwnerIdentity, error) {
	return s.ListOwnersContext(context.Background())
}

func (s *Store) ListOwnersContext(ctx context.Context) ([]OwnerIdentity, error) {
	if s != nil && s.seekDB != nil {
		queryCtx, cancel := s.seekDBQueryContext(ctx)
		defer cancel()
		owners, err := listOwnersSeekDB(queryCtx, s.seekDB)
		if err != nil {
			return nil, fmt.Errorf("listing owner identities from SeekDB: %w", err)
		}
		return owners, nil
	}
	if s == nil || s.pool == nil || s.pool.Raw() == nil {
		return nil, ErrIdentityDatabasePoolRequired
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	rows, err := s.pool.Raw().Query(queryCtx, `
SELECT namespace, subject_digest, created_at_ms
FROM owner_identities
ORDER BY namespace ASC, subject_digest ASC`)
	if err != nil {
		return nil, fmt.Errorf("listing owner identities: %w", err)
	}
	defer rows.Close()
	owners := make([]OwnerIdentity, 0)
	for rows.Next() {
		var owner OwnerIdentity
		if err := rows.Scan(&owner.Namespace, &owner.PrincipalDigest, &owner.CreatedAtUnixMS); err != nil {
			return nil, fmt.Errorf("scanning owner identity: %w", err)
		}
		owners = append(owners, owner)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading owner identities: %w", err)
	}
	return owners, nil
}

func (s *Store) UnbindOwner(namespace, principalDigest string) error {
	return s.UnbindOwnerContext(context.Background(), namespace, principalDigest)
}

func (s *Store) UnbindOwnerContext(ctx context.Context, namespace, principalDigest string) error {
	digest, err := validateAndDecodeIdentity(namespace, principalDigest)
	if err != nil {
		return err
	}
	if s != nil && s.seekDB != nil {
		queryCtx, cancel := s.seekDBQueryContext(ctx)
		defer cancel()
		removed, err := unbindOwnerSeekDB(queryCtx, s.seekDB, namespace, digest)
		if err != nil {
			return fmt.Errorf("unbinding owner identity from SeekDB: %w", err)
		}
		if !removed {
			return ErrOwnerIdentityNotFound
		}
		return nil
	}
	if s == nil || s.pool == nil || s.pool.Raw() == nil {
		return ErrIdentityDatabasePoolRequired
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	result, err := s.pool.Raw().Exec(queryCtx,
		"DELETE FROM owner_identities WHERE namespace = $1 AND subject_digest = $2",
		namespace, principalDigest,
	)
	if err != nil {
		return fmt.Errorf("unbinding owner identity: %w", err)
	}
	if result.RowsAffected() == 0 {
		return ErrOwnerIdentityNotFound
	}
	return nil
}

func (s *Store) IsOwner(namespace, principalDigest string) (bool, error) {
	return s.IsOwnerContext(context.Background(), namespace, principalDigest)
}

func (s *Store) IsOwnerContext(ctx context.Context, namespace, principalDigest string) (bool, error) {
	digest, err := validateAndDecodeIdentity(namespace, principalDigest)
	if err != nil {
		return false, err
	}
	if s != nil && s.seekDB != nil {
		queryCtx, cancel := s.seekDBQueryContext(ctx)
		defer cancel()
		exists, err := isOwnerSeekDB(queryCtx, s.seekDB, namespace, digest)
		if err != nil {
			return false, fmt.Errorf("checking owner identity in SeekDB: %w", err)
		}
		return exists, nil
	}
	if s == nil || s.pool == nil || s.pool.Raw() == nil {
		return false, ErrIdentityDatabasePoolRequired
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	var exists bool
	err = s.pool.Raw().QueryRow(queryCtx, `
SELECT EXISTS(
    SELECT 1 FROM owner_identities
    WHERE namespace = $1 AND subject_digest = $2
)`, namespace, principalDigest).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("checking owner identity: %w", err)
	}
	return exists, nil
}

func (s *Store) seekDBQueryContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(parent, s.queryLimit)
}

func (s *Store) currentUnixMillis() int64 {
	now := time.Now
	if s != nil && s.now != nil {
		now = s.now
	}
	return max(now().UnixMilli(), 1)
}
