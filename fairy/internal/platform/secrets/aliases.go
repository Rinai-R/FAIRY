package secrets

import (
	pgstore "fairy/postgres"
	fairysecret "fairy/secret"
)

type (
	Value  = fairysecret.Value
	Store  = fairysecret.Store
	Cipher = fairysecret.Cipher
)

const EnvMasterKey = fairysecret.EnvMasterKey

var (
	NewValue             = fairysecret.NewValue
	NewPostgresStore     = fairysecret.NewPostgresStore
	NewTestStore         = fairysecret.NewTestStore
	CipherFromEnv        = fairysecret.CipherFromEnv
	ErrCipherRequired    = fairysecret.ErrCipherRequired
	ErrMasterKeyRequired = fairysecret.ErrMasterKeyRequired
)

// OpenPostgresStore opens the production secret store on an existing pool.
func OpenPostgresStore(pool *pgstore.Pool, cipher *Cipher) (*Store, error) {
	return NewPostgresStore(pool, cipher)
}
