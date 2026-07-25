package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"os"

	platformsecrets "fairy/internal/platform/secrets"
	"fairy/memory"
	pgstore "fairy/postgres"
	vectorindex "fairy/internal/adapters/memory/qdrant"
)

// Dependencies allows tests and integration harnesses to inject infrastructure.
type Dependencies struct {
	Database    *pgstore.Pool
	MemoryStore *memory.Store
	SecretStore *platformsecrets.Store
	VectorIndex *vectorindex.Client
}

// OpenedDependencies records infrastructure opened during bootstrap.
type OpenedDependencies struct {
	Database    *pgstore.Pool
	MemoryStore *memory.Store
	SecretStore *platformsecrets.Store
	VectorIndex *vectorindex.Client
	OwnDatabase bool
	OwnVector   bool
}

func ValidateInjectedDependencies(deps *Dependencies) error {
	if deps == nil {
		return nil
	}
	if deps.MemoryStore == nil {
		return errors.New("injected memory store is required")
	}
	if deps.SecretStore == nil {
		return errors.New("injected secret store is required")
	}
	return nil
}

// OpenDependencies opens production infrastructure or returns injected dependencies.
func OpenDependencies(ctx context.Context, injected *Dependencies, runtimeProfile Profile) (*OpenedDependencies, error) {
	if err := ValidateInjectedDependencies(injected); err != nil {
		return nil, err
	}
	if injected != nil {
		return &OpenedDependencies{
			Database:    injected.Database,
			MemoryStore: injected.MemoryStore,
			SecretStore: injected.SecretStore,
			VectorIndex: injected.VectorIndex,
		}, nil
	}

	databaseConfig, err := pgstore.ConfigFromEnv(os.Getenv)
	if err != nil {
		return nil, fmt.Errorf("database configuration: %w", err)
	}
	database, err := pgstore.Open(ctx, databaseConfig)
	if err != nil {
		return nil, err
	}
	if _, err := pgstore.VerifySchema(ctx, database); err != nil {
		database.Close()
		return nil, fmt.Errorf("database schema: %w", err)
	}

	var vectorClient *vectorindex.Client
	ownVector := false
	vectorConfig, vectorErr := vectorindex.ConfigFromEnv(os.Getenv)
	switch {
	case vectorErr == nil:
		client, openErr := vectorindex.Open(ctx, vectorConfig)
		if openErr != nil {
			if runtimeProfile.RequiresVectorIndex() {
				database.Close()
				return nil, openErr
			}
			break
		}
		if _, verifyErr := client.VerifyCollection(ctx); verifyErr != nil {
			_ = client.Close()
			if runtimeProfile.RequiresVectorIndex() {
				database.Close()
				return nil, fmt.Errorf("qdrant collection: %w", verifyErr)
			}
			break
		}
		vectorClient = client
		ownVector = true
	case runtimeProfile.RequiresVectorIndex():
		database.Close()
		return nil, fmt.Errorf("qdrant configuration: %w", vectorErr)
	}

	secretCipher, err := platformsecrets.CipherFromEnv(os.Getenv)
	if err != nil {
		if ownVector && vectorClient != nil {
			_ = vectorClient.Close()
		}
		database.Close()
		return nil, fmt.Errorf("secret master key: %w", err)
	}
	secretStore, err := platformsecrets.OpenPostgresStore(database, secretCipher)
	if err != nil {
		if ownVector && vectorClient != nil {
			_ = vectorClient.Close()
		}
		database.Close()
		return nil, err
	}
	memoryStore, err := memory.NewStoreFromPool(database)
	if err != nil {
		if ownVector && vectorClient != nil {
			_ = vectorClient.Close()
		}
		database.Close()
		return nil, err
	}
	return &OpenedDependencies{
		Database:    database,
		MemoryStore: memoryStore,
		SecretStore: secretStore,
		VectorIndex: vectorClient,
		OwnDatabase: true,
		OwnVector:   ownVector,
	}, nil
}

func (opened *OpenedDependencies) CloseOwned() {
	if opened == nil {
		return
	}
	if opened.OwnVector && opened.VectorIndex != nil {
		_ = opened.VectorIndex.Close()
	}
	if opened.OwnDatabase && opened.Database != nil {
		opened.Database.Close()
	}
}
