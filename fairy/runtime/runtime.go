package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"fairy/character"
	"fairy/companion"
	"fairy/config"
	"fairy/identity"
	"fairy/logx"
	"fairy/memory"
	"fairy/model"
	"fairy/observability"
	pgstore "fairy/postgres"
	"fairy/profile"
	"fairy/search"
	"fairy/secret"
	"fairy/speech"
	"fairy/vectorindex"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Options configures a Session Core process.
type Options struct {
	ConfigRoot   string
	Logger       *zap.Logger
	LogStore     *observability.LogStore
	HTTPMetrics  *observability.HTTPMetrics
	Dependencies *Dependencies
	// LogEventsJSONL prints turn events to stdout (optional local debugging).
	LogEventsJSONL bool
}

type Dependencies struct {
	Database    *pgstore.Pool
	MemoryStore *memory.Store
	SecretStore *secret.Store
	VectorIndex *vectorindex.Client
}

// Runtime owns long-lived Core services for the HTTP/SSE Session Core.
type Runtime struct {
	ConfigRoot  string
	Logger      *zap.Logger
	Events      *EventHub
	Logs        *observability.LogStore
	HTTPMetrics *observability.HTTPMetrics
	StartedAt   time.Time
	Database    *pgstore.Pool
	VectorIndex *vectorindex.Client

	MemoryStore  *memory.Store
	Identity     *identity.Store
	Memory       *memory.MemoryService
	Secret       *secret.Store
	Model        *model.ModelService
	Companion    *companion.CompanionService
	Character    *character.CharacterService
	Config       *config.ConfigService
	ConfigReader *config.Reader
	Speech       *speech.SpeechService
	Profile      *profile.ProfileService
	WebSearch    *search.Service
	Bootstrap    *BootstrapService
	eventMu      sync.Mutex
	events       []companion.TurnEvent
	ownDatabase  bool
	ownVector    bool
	closeOnce    sync.Once
	closeErr     error
}

func Open(options Options) (*Runtime, error) {
	logStore := options.LogStore
	if logStore == nil {
		logStore = observability.NewLogStore(observability.DefaultLogCapacity)
	}
	logger := options.Logger
	if logger == nil {
		logger = logx.New(observability.NewLogCore(logStore, logx.LevelFromEnv()))
	} else {
		logger = logger.WithOptions(zap.WrapCore(func(core zapcore.Core) zapcore.Core {
			return zapcore.NewTee(core, observability.NewLogCore(logStore, logx.LevelFromEnv()))
		}))
	}
	httpMetrics := options.HTTPMetrics
	if httpMetrics == nil {
		httpMetrics = observability.NewHTTPMetrics()
	}
	configRoot := options.ConfigRoot
	if configRoot == "" {
		configRoot = os.Getenv("FAIRY_CONFIG_ROOT")
	}
	if configRoot == "" {
		configRoot = filepath.Join(os.Getenv("HOME"), "Library", "Application Support", "dev.rinai.fairy", "session-core", "v1")
	}

	database, memoryStore, secretStore, vectorClient, ownDatabase, ownVector, err := openDependencies(context.Background(), options.Dependencies)
	if err != nil {
		return nil, err
	}
	keepDependencies := false
	defer func() {
		if keepDependencies {
			return
		}
		if ownVector && vectorClient != nil {
			_ = vectorClient.Close()
		}
		if ownDatabase && database != nil {
			database.Close()
		}
	}()

	webSettings, err := config.ReadWebSearchSettings(configRoot)
	if err != nil {
		return nil, err
	}
	webSearch := search.NewServiceFromEnv(webSettings.BaseURL)
	modelService := model.NewModelService(configRoot, secretStore)
	companionService := companion.NewCompanionServiceWithRuntime(configRoot, memoryStore, modelService, webSearch)
	identityStore, err := identity.NewStore(database)
	if err != nil {
		return nil, err
	}
	companion.AttachOwnerIdentityStore(companionService, identityStore)
	characterService := character.NewCharacterService(configRoot)
	configService := config.NewConfigService(configRoot, secretStore)
	speechService := speech.NewSpeechService(configRoot, secretStore)
	profileService := profile.NewProfileService(configRoot)
	configReader := config.NewReader(configRoot)

	rt := &Runtime{
		ConfigRoot:   configRoot,
		Logger:       logger,
		Events:       NewEventHub(),
		Logs:         logStore,
		HTTPMetrics:  httpMetrics,
		StartedAt:    time.Now(),
		Database:     database,
		VectorIndex:  vectorClient,
		MemoryStore:  memoryStore,
		Identity:     identityStore,
		Memory:       memory.NewMemoryServiceWithStore(configRoot, memoryStore),
		Secret:       secretStore,
		Model:        modelService,
		Companion:    companionService,
		Character:    characterService,
		Config:       configService,
		ConfigReader: configReader,
		Speech:       speechService,
		Profile:      profileService,
		WebSearch:    webSearch,
		Bootstrap: NewBootstrapService(BootstrapOptions{
			AppName:                "FAIRY",
			MigrationStage:         "session-core",
			CoreVersion:            "0.1.0",
			RespondRuntimeMigrated: true,
		}),
		ownDatabase: ownDatabase,
		ownVector:   ownVector,
	}

	companion.AttachLogger(companionService, logger.Named("companion"))
	companion.AttachCharacterStore(companionService, characterService.CatalogStore())
	companion.AttachProfileStore(companionService, profileService.ProfileStore())
	companion.AttachConfigReader(companionService, configReader)
	companion.AttachSpeechSynthesizer(companionService, companionSpeechAdapter{service: speechService})
	attachSemanticEmbedder(companionService, modelService, configReader, logger.Named("semantic"))
	if vectorClient != nil {
		companion.AttachVectorIndex(companionService, vectorClient)
	}
	character.AttachLogger(characterService, logger.Named("character"))
	search.AttachLogger(webSearch, logger.Named("openserp"))

	companion.AttachEventEmitter(companionService, func(event companion.TurnEvent) {
		rt.eventMu.Lock()
		rt.events = append(rt.events, event)
		rt.eventMu.Unlock()
		rt.Events.Publish(event)
		if options.LogEventsJSONL {
			line, err := json.Marshal(event)
			if err != nil {
				logger.Warn("marshal turn event", zap.Error(err))
				return
			}
			fmt.Println(string(line))
		}
	})

	keepDependencies = true
	return rt, nil
}

func (rt *Runtime) Close() error {
	if rt == nil {
		return nil
	}
	rt.closeOnce.Do(func() {
		rt.closeErr = rt.Companion.Close()
		rt.Events.Close()
		rt.Logs.Close()
		if rt.ownVector && rt.VectorIndex != nil {
			if closeErr := rt.VectorIndex.Close(); rt.closeErr == nil {
				rt.closeErr = closeErr
			}
		}
		if rt.ownDatabase && rt.Database != nil {
			rt.Database.Close()
		}
	})
	return rt.closeErr
}

func openDependencies(ctx context.Context, injected *Dependencies) (*pgstore.Pool, *memory.Store, *secret.Store, *vectorindex.Client, bool, bool, error) {
	if injected != nil {
		if injected.MemoryStore == nil {
			return nil, nil, nil, nil, false, false, errors.New("injected memory store is required")
		}
		if injected.SecretStore == nil {
			return nil, nil, nil, nil, false, false, errors.New("injected secret store is required")
		}
		return injected.Database, injected.MemoryStore, injected.SecretStore, injected.VectorIndex, false, false, nil
	}
	databaseConfig, err := pgstore.ConfigFromEnv(os.Getenv)
	if err != nil {
		return nil, nil, nil, nil, false, false, fmt.Errorf("database configuration: %w", err)
	}
	database, err := pgstore.Open(ctx, databaseConfig)
	if err != nil {
		return nil, nil, nil, nil, false, false, err
	}
	if _, err := pgstore.VerifySchema(ctx, database); err != nil {
		database.Close()
		return nil, nil, nil, nil, false, false, fmt.Errorf("database schema: %w", err)
	}
	vectorConfig, err := vectorindex.ConfigFromEnv(os.Getenv)
	if err != nil {
		database.Close()
		return nil, nil, nil, nil, false, false, fmt.Errorf("qdrant configuration: %w", err)
	}
	vectorClient, err := vectorindex.Open(ctx, vectorConfig)
	if err != nil {
		database.Close()
		return nil, nil, nil, nil, false, false, err
	}
	if _, err := vectorClient.VerifyCollection(ctx); err != nil {
		_ = vectorClient.Close()
		database.Close()
		return nil, nil, nil, nil, false, false, fmt.Errorf("qdrant collection: %w", err)
	}
	secretCipher, err := secret.CipherFromEnv(os.Getenv)
	if err != nil {
		_ = vectorClient.Close()
		database.Close()
		return nil, nil, nil, nil, false, false, fmt.Errorf("secret master key: %w", err)
	}
	secretStore, err := secret.NewPostgresStore(database, secretCipher)
	if err != nil {
		_ = vectorClient.Close()
		database.Close()
		return nil, nil, nil, nil, false, false, err
	}
	memoryStore, err := memory.NewStoreFromPool(database)
	if err != nil {
		_ = vectorClient.Close()
		database.Close()
		return nil, nil, nil, nil, false, false, err
	}
	return database, memoryStore, secretStore, vectorClient, true, true, nil
}

func (rt *Runtime) DrainEvents() []companion.TurnEvent {
	if rt == nil {
		return nil
	}
	rt.eventMu.Lock()
	defer rt.eventMu.Unlock()
	out := append([]companion.TurnEvent(nil), rt.events...)
	rt.events = nil
	return out
}
