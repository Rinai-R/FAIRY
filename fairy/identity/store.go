package identity

import (
	"context"
	"errors"
	"fmt"
	"time"

	"fairy/interaction"
	pgstore "fairy/postgres"
)

var (
	ErrDatabasePoolRequired  = errors.New("identity database pool is required")
	ErrOwnerIdentityNotFound = errors.New("owner identity does not exist")
)

type OwnerIdentity struct {
	Namespace       string `json:"namespace"`
	PrincipalDigest string `json:"principalDigest"`
	CreatedAtUnixMS int64  `json:"createdAtUnixMs"`
}

type Store struct {
	pool *pgstore.Pool
	now  func() time.Time
}

func NewStore(pool *pgstore.Pool) (*Store, error) {
	if pool == nil || pool.Raw() == nil {
		return nil, ErrDatabasePoolRequired
	}
	return &Store{pool: pool, now: time.Now}, nil
}

func (s *Store) BindOwner(namespace, principalDigest string) error {
	return s.BindOwnerContext(context.Background(), namespace, principalDigest)
}

func (s *Store) BindOwnerContext(ctx context.Context, namespace, principalDigest string) error {
	if err := validateIdentity(namespace, principalDigest); err != nil {
		return err
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	_, err := s.pool.Raw().Exec(queryCtx, `
INSERT INTO owner_identities(namespace, subject_digest, created_at_ms)
VALUES ($1, $2, $3)
ON CONFLICT(namespace, subject_digest) DO NOTHING`, namespace, principalDigest, s.now().UnixMilli())
	if err != nil {
		return fmt.Errorf("binding owner identity: %w", err)
	}
	return nil
}

func (s *Store) ListOwners() ([]OwnerIdentity, error) {
	return s.ListOwnersContext(context.Background())
}

func (s *Store) ListOwnersContext(ctx context.Context) ([]OwnerIdentity, error) {
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
	if err := validateIdentity(namespace, principalDigest); err != nil {
		return err
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
	if err := validateIdentity(namespace, principalDigest); err != nil {
		return false, err
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	var exists bool
	err := s.pool.Raw().QueryRow(queryCtx, `
SELECT EXISTS(
    SELECT 1 FROM owner_identities
    WHERE namespace = $1 AND subject_digest = $2
)`, namespace, principalDigest).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("checking owner identity: %w", err)
	}
	return exists, nil
}

func validateIdentity(namespace, principalDigest string) error {
	if err := interaction.ValidateNamespace(namespace); err != nil {
		return err
	}
	if err := interaction.ValidateDigest(principalDigest); err != nil {
		return err
	}
	return nil
}
