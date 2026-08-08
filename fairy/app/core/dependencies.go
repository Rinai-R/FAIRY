package core

import (
	"context"
	"errors"
	"fmt"
	"os"

	historycompaction "fairy/context/history/compaction"
	historyruntime "fairy/context/history/runtime"
	history "fairy/context/history/transcript"
	"fairy/context/memory/personal"
	"fairy/runtime/config"
	coredb "fairy/runtime/database"
)

// Dependencies allows tests and integration harnesses to inject infrastructure.
type Dependencies struct {
	Database        *coredb.Pool
	TranscriptStore *history.Store
	CompactionStore *historycompaction.Store
	RuntimeStore    *historyruntime.Store
	MemoryStore     *personal.Store
	SecretStore     *config.SecretStore
}

type openedDependencies struct {
	Database        *coredb.Pool
	TranscriptStore *history.Store
	CompactionStore *historycompaction.Store
	RuntimeStore    *historyruntime.Store
	MemoryStore     *personal.Store
	SecretStore     *config.SecretStore
	OwnDatabase     bool
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
			Database:        injected.Database,
			TranscriptStore: injected.TranscriptStore,
			CompactionStore: injected.CompactionStore,
			RuntimeStore:    injected.RuntimeStore,
			MemoryStore:     injected.MemoryStore,
			SecretStore:     injected.SecretStore,
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
	if _, err := coredb.VerifySchema(ctx, database.Raw()); err != nil {
		database.Close()
		return nil, fmt.Errorf("database schema: %w", err)
	}

	secretCipher, err := config.SecretCipherFromEnv(os.Getenv)
	if err != nil {
		database.Close()
		return nil, fmt.Errorf("secret master key: %w", err)
	}
	secretStore, err := config.NewPostgresSecretStore(database, secretCipher)
	if err != nil {
		database.Close()
		return nil, err
	}
	return &openedDependencies{
		Database:    database,
		SecretStore: secretStore,
		OwnDatabase: true,
	}, nil
}

func (opened *openedDependencies) closeOwned() {
	if opened == nil {
		return
	}
	if opened.OwnDatabase && opened.Database != nil {
		opened.Database.Close()
	}
}
