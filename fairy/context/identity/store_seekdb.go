package identity

import (
	"context"
	"database/sql"
	"encoding/hex"
	"fmt"

	"fairy/transport/session"
)

const principalDigestBytes = 32

// These package-local database/sql surfaces are deliberately operation-sized.
// Both *sql.DB and *sql.Tx implement them; domain consumers receive Store
// methods rather than any SQL dependency.
type identityExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

type identityQuerier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

type identityRowQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func bindOwnerSeekDB(ctx context.Context, database identityExecer, namespace string, digest []byte, createdAtUnixMS int64) error {
	_, err := database.ExecContext(ctx, `
INSERT INTO owner_identities(namespace, subject_digest, created_at_ms)
VALUES (?, ?, ?)
ON DUPLICATE KEY UPDATE subject_digest = VALUES(subject_digest)`, namespace, digest, createdAtUnixMS)
	return err
}

func listOwnersSeekDB(ctx context.Context, database identityQuerier) ([]OwnerIdentity, error) {
	rows, err := database.QueryContext(ctx, `
SELECT namespace, subject_digest, created_at_ms
FROM owner_identities
ORDER BY namespace ASC, subject_digest ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	owners := make([]OwnerIdentity, 0)
	for rows.Next() {
		var (
			owner  OwnerIdentity
			digest []byte
		)
		if err := rows.Scan(&owner.Namespace, &digest, &owner.CreatedAtUnixMS); err != nil {
			return nil, err
		}
		owner.PrincipalDigest, err = encodePrincipalDigest(digest)
		if err != nil {
			return nil, err
		}
		owners = append(owners, owner)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return owners, nil
}

func unbindOwnerSeekDB(ctx context.Context, database identityExecer, namespace string, digest []byte) (bool, error) {
	result, err := database.ExecContext(ctx,
		"DELETE FROM owner_identities WHERE namespace = ? AND subject_digest = ?",
		namespace, digest,
	)
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return rows == 1, nil
}

func isOwnerSeekDB(ctx context.Context, database identityRowQuerier, namespace string, digest []byte) (bool, error) {
	var exists bool
	err := database.QueryRowContext(ctx, `
SELECT EXISTS(
  SELECT 1 FROM owner_identities
  WHERE namespace = ? AND subject_digest = ?
)`, namespace, digest).Scan(&exists)
	return exists, err
}

func validateAndDecodeIdentity(namespace, principalDigest string) ([]byte, error) {
	if err := session.ValidateNamespace(namespace); err != nil {
		return nil, err
	}
	if err := session.ValidateDigest(principalDigest); err != nil {
		return nil, err
	}
	digest, err := hex.DecodeString(principalDigest)
	if err != nil || len(digest) != principalDigestBytes {
		return nil, fmt.Errorf("decoding principal digest: %w", ErrOwnerIdentityCorrupt)
	}
	return digest, nil
}

func encodePrincipalDigest(digest []byte) (string, error) {
	if len(digest) != principalDigestBytes {
		return "", ErrOwnerIdentityCorrupt
	}
	return hex.EncodeToString(digest), nil
}
