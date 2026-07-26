package runtime

import (
	"context"
	"encoding/json"
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
	vectorindex "fairy/vectorindex"
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
	// Profile selects full vs desktop-lite dependency rules. Empty defaults to full.
	Profile Profile
	// LogEventsJSONL prints turn events to stdout (optional local debugging).
	LogEventsJSONL bool
}

// Runtime owns long-lived Core services for the HTTP/SSE Session Core.
type Runtime struct {
	ConfigRoot    string
	Logger        *zap.Logger
	Events        *EventHub
	Participation *ParticipationHub
	Logs          *observability.LogStore
	HTTPMetrics   *observability.HTTPMetrics
	Messages      *observability.MessageMetrics
	StartedAt     time.Time
	Database      *pgstore.Pool
	VectorIndex   *vectorindex.Client

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

	runtimeProfile := options.Profile
	if runtimeProfile == "" {
		parsed, err := ProfileFromEnv(os.Getenv)
		if err != nil {
			return nil, err
		}
		runtimeProfile = parsed
	} else {
		parsed, err := ParseProfile(string(runtimeProfile))
		if err != nil {
			return nil, err
		}
		runtimeProfile = parsed
	}

	opened, err := openDependencies(context.Background(), options.Dependencies, runtimeProfile)
	if err != nil {
		return nil, err
	}
	keepDependencies := false
	defer func() {
		if keepDependencies {
			return
		}
		opened.closeOwned()
	}()

	services, err := wireCoreServices(configRoot, opened.Database, opened.MemoryStore, opened.SecretStore)
	if err != nil {
		return nil, err
	}
	messageMetrics := observability.NewMessageMetrics()

	rt := &Runtime{
		ConfigRoot:    configRoot,
		Logger:        logger,
		Events:        NewEventHub(),
		Participation: NewParticipationHub(),
		Logs:          logStore,
		HTTPMetrics:   httpMetrics,
		Messages:      messageMetrics,
		StartedAt:     time.Now(),
		Database:      opened.Database,
		VectorIndex:   opened.VectorIndex,
		MemoryStore:   opened.MemoryStore,
		Identity:      services.Identity,
		Memory:        services.Memory,
		Secret:        opened.SecretStore,
		Model:         services.Model,
		Companion:     services.Companion,
		Character:     services.Character,
		Config:        services.Config,
		ConfigReader:  services.ConfigReader,
		Speech:        services.Speech,
		Profile:       services.Profile,
		WebSearch:     services.WebSearch,
		Bootstrap: NewBootstrapService(BootstrapOptions{
			AppName:                "FAIRY",
			MigrationStage:         "session-core",
			CoreVersion:            "0.1.0",
			RespondRuntimeMigrated: true,
		}),
		ownDatabase: opened.OwnDatabase,
		ownVector:   opened.OwnVector,
	}

	companion.AttachLogger(services.Companion, logger.Named("companion"))
	companion.AttachMessageTelemetry(services.Companion, messageMetrics)
	companion.AttachCharacterCatalog(services.Companion, services.Character.CatalogStore())
	companion.AttachProfileSource(services.Companion, services.Profile.ProfileStore())
	companion.AttachConfigSource(services.Companion, services.ConfigReader)
	companion.AttachSpeechSynthesizer(services.Companion, companionSpeechAdapter{service: services.Speech})
	attachSemanticEmbedder(services.Companion, services.Model, services.ConfigReader, logger.Named("semantic"))
	if opened.VectorIndex != nil {
		companion.AttachVectorIndex(services.Companion, opened.VectorIndex)
	}
	character.AttachLogger(services.Character, logger.Named("character"))
	search.AttachLogger(services.WebSearch, logger.Named("openserp"))

	companion.AttachEventEmitter(services.Companion, func(event companion.TurnEvent) {
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
	companion.AttachParticipationEventEmitter(services.Companion, rt.Participation.Publish)

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
		rt.Participation.Close()
		rt.Messages.Close()
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
