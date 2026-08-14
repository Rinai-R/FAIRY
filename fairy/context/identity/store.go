package identity

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

var (
	ErrIdentitySeekDBRequired    = errors.New("identity SeekDB database is required")
	ErrIdentityQueryLimitInvalid = errors.New("identity query limit must be greater than zero")
	ErrOwnerIdentityNotFound     = errors.New("owner identity does not exist")
	ErrOwnerIdentityCorrupt      = errors.New("owner identity record is corrupt")
)

type OwnerIdentity struct {
	Namespace       string `json:"namespace"`
	PrincipalDigest string `json:"principalDigest"`
	CreatedAtUnixMS int64  `json:"createdAtUnixMs"`
}

type Store struct {
	seekDB     *sql.DB
	queryLimit time.Duration
	now        func() time.Time
}

// NewSeekDBStore stores only fixed-width identity digests in the local SeekDB
// authority.
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
	if s == nil || s.seekDB == nil {
		return ErrIdentitySeekDBRequired
	}
	queryCtx, cancel := s.seekDBQueryContext(ctx)
	defer cancel()
	if err := bindOwnerSeekDB(queryCtx, s.seekDB, namespace, digest, s.currentUnixMillis()); err != nil {
		return fmt.Errorf("binding owner identity in SeekDB: %w", err)
	}
	return nil
}

func (s *Store) ListOwners() ([]OwnerIdentity, error) {
	return s.ListOwnersContext(context.Background())
}

func (s *Store) ListOwnersContext(ctx context.Context) ([]OwnerIdentity, error) {
	if s == nil || s.seekDB == nil {
		return nil, ErrIdentitySeekDBRequired
	}
	queryCtx, cancel := s.seekDBQueryContext(ctx)
	defer cancel()
	owners, err := listOwnersSeekDB(queryCtx, s.seekDB)
	if err != nil {
		return nil, fmt.Errorf("listing owner identities from SeekDB: %w", err)
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
	if s == nil || s.seekDB == nil {
		return ErrIdentitySeekDBRequired
	}
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

func (s *Store) IsOwner(namespace, principalDigest string) (bool, error) {
	return s.IsOwnerContext(context.Background(), namespace, principalDigest)
}

func (s *Store) IsOwnerContext(ctx context.Context, namespace, principalDigest string) (bool, error) {
	digest, err := validateAndDecodeIdentity(namespace, principalDigest)
	if err != nil {
		return false, err
	}
	if s == nil || s.seekDB == nil {
		return false, ErrIdentitySeekDBRequired
	}
	queryCtx, cancel := s.seekDBQueryContext(ctx)
	defer cancel()
	exists, err := isOwnerSeekDB(queryCtx, s.seekDB, namespace, digest)
	if err != nil {
		return false, fmt.Errorf("checking owner identity in SeekDB: %w", err)
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
