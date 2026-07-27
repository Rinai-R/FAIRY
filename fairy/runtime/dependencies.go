package runtime

import (
	"context"
	"errors"
	"fmt"
	"os"

	"fairy/coredb"
	dbschema "fairy/coredb/schema"
	"fairy/memory"
	"fairy/secret"
	"fairy/vectorindex"
)

// Dependencies allows tests and integration harnesses to inject infrastructure.
type Dependencies struct {
	Database    *coredb.Pool
	MemoryStore *memory.Store
	SecretStore *secret.Store
	VectorIndex *vectorindex.Client
}

type openedDependencies struct {
	Database    *coredb.Pool
	MemoryStore *memory.Store
	SecretStore *secret.Store
	VectorIndex *vectorindex.Client
	OwnDatabase bool
	OwnVector   bool
}

func validateInjectedDependencies(deps *Dependencies) error {
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

func openDependencies(ctx context.Context, injected *Dependencies, runtimeProfile Profile) (*openedDependencies, error) {
	if err := validateInjectedDependencies(injected); err != nil {
		return nil, err
	}
	if injected != nil {
		return &openedDependencies{
			Database:    injected.Database,
			MemoryStore: injected.MemoryStore,
			SecretStore: injected.SecretStore,
			VectorIndex: injected.VectorIndex,
		}, nil
	}

	databaseConfig, err := coredb.ConfigFromEnv(os.Getenv)
	if err != nil {
		return nil, fmt.Errorf("database configuration: %w", err)
	}
	database, err := coredb.Open(ctx, databaseConfig)
	if err != nil {
		return nil, err
	}
	if _, err := dbschema.VerifySchema(ctx, database.Raw()); err != nil {
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

	secretCipher, err := secret.CipherFromEnv(os.Getenv)
	if err != nil {
		if ownVector && vectorClient != nil {
			_ = vectorClient.Close()
		}
		database.Close()
		return nil, fmt.Errorf("secret master key: %w", err)
	}
	secretStore, err := secret.NewPostgresStore(database, secretCipher)
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
	return &openedDependencies{
		Database:    database,
		MemoryStore: memoryStore,
		SecretStore: secretStore,
		VectorIndex: vectorClient,
		OwnDatabase: true,
		OwnVector:   ownVector,
	}, nil
}

func (opened *openedDependencies) closeOwned() {
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
