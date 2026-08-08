package core

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"fairy/api"
	"fairy/character"
	"fairy/companion"
	"fairy/config"
	"fairy/coredb"
	"fairy/desktopcapture"
	"fairy/initiative"
	"fairy/memory"
	"fairy/model"
	"fairy/observability"
	"fairy/session"
	"fairy/sticker"
)

// RuntimeOptions configures a Session Core process.
type RuntimeOptions struct {
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
	Captures      *desktopcapture.CaptureHub
	Logs          *observability.LogStore
	HTTPMetrics   *observability.HTTPMetrics
	Messages      *observability.MessageMetrics
	StartedAt     time.Time
	Database      *coredb.Pool

	MemoryStore  *memory.Store
	Identity     *memory.IdentityStore
	Memory       *memory.MemoryService
	Secret       *config.SecretStore
	Model        *model.ModelService
	Companion    *companion.CompanionService
	Initiative   *initiative.Service
	Character    *character.CharacterService
	Config       *config.ConfigService
	ConfigReader *config.Reader
	Profile      *config.ProfileService
	Stickers     *sticker.Store
	WebSearch    *companion.WebSearchService
	Bootstrap    *BootstrapService
	ownDatabase  bool
	closeOnce    sync.Once
	closeErr     error
}

func (rt *Runtime) APIDependencies() *api.Dependencies {
	if rt == nil {
		return nil
	}
	return &api.Dependencies{
		ConfigRoot: rt.ConfigRoot, Logger: rt.Logger, StartedAt: rt.StartedAt,
		Database: rt.Database, MemoryStore: rt.MemoryStore,
		Identity: rt.Identity, Memory: rt.Memory, Secret: rt.Secret,
		Companion: rt.Companion, Initiative: rt.Initiative, Character: rt.Character,
		Config: rt.Config, Profile: rt.Profile, Stickers: rt.Stickers, Captures: rt.Captures,
		Logs: rt.Logs, HTTPMetrics: rt.HTTPMetrics, Messages: rt.Messages,
		BootstrapStatus: func() (any, error) {
			return rt.Bootstrap.Status()
		},
		SubscribeTurnEvents: func(conversationID string) (api.EventSubscription, error) {
			subscription, err := rt.Events.Subscribe(conversationID)
			return api.EventSubscription{
				Events: subscription.Events, Failures: subscription.Failures, Cancel: subscription.Unsubscribe,
			}, err
		},
		SubscribeParticipation: func(conversationID string) (api.ParticipationSubscription, error) {
			subscription, err := rt.Participation.Subscribe(conversationID)
			return api.ParticipationSubscription{
				Events: subscription.Events, Failures: subscription.Failures, Cancel: subscription.Unsubscribe,
			}, err
		},
		TurnEventSubscriberCount: rt.Events.SubscriberCount,
	}
}

func Open(options RuntimeOptions) (*Runtime, error) {
	logStore := options.LogStore
	if logStore == nil {
		logStore = observability.NewLogStore(observability.DefaultLogCapacity)
	}
	logger := options.Logger
	if logger == nil {
		logger = observability.NewLogger(observability.NewLogCore(logStore, observability.LogLevelFromEnv()))
	} else {
		logger = logger.WithOptions(zap.WrapCore(func(core zapcore.Core) zapcore.Core {
			return zapcore.NewTee(core, observability.NewLogCore(logStore, observability.LogLevelFromEnv()))
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

	configReader := config.NewReader(configRoot)
	modelService := model.NewModelService(configRoot, opened.SecretStore)
	memoryStore := opened.MemoryStore
	if memoryStore == nil {
		embedder := semanticEmbedder(modelService, configReader, logger.Named("semantic"))
		memoryStore, err = memory.NewStoreFromPool(opened.Database)
		if err != nil {
			return nil, err
		}
		memoryStore.ReplaceSemanticEmbedder(embedder)
		opened.MemoryStore = memoryStore
	}
	services, err := wireCoreServices(configRoot, opened.Database, memoryStore, opened.SecretStore, modelService, configReader)
	if err != nil {
		return nil, err
	}
	services.Config.AttachSemanticEmbeddingRuntime(semanticEmbeddingRuntime{model: modelService, store: memoryStore})
	stickerStore, err := sticker.NewStore(opened.Database)
	if err != nil {
		return nil, err
	}
	companion.AttachStickerSearch(services.Companion, stickerStore)
	messageMetrics := observability.NewMessageMetrics()

	rt := &Runtime{
		ConfigRoot:    configRoot,
		Logger:        logger,
		Events:        NewEventHub(),
		Participation: NewParticipationHub(),
		Captures:      desktopcapture.NewCaptureHub(opened.MemoryStore),
		Logs:          logStore,
		HTTPMetrics:   httpMetrics,
		Messages:      messageMetrics,
		StartedAt:     time.Now(),
		Database:      opened.Database,
		MemoryStore:   opened.MemoryStore,
		Identity:      services.Identity,
		Memory:        services.Memory,
		Secret:        opened.SecretStore,
		Model:         services.Model,
		Companion:     services.Companion,
		Character:     services.Character,
		Config:        services.Config,
		ConfigReader:  services.ConfigReader,
		Profile:       services.Profile,
		Stickers:      stickerStore,
		WebSearch:     services.WebSearch,
		Bootstrap: NewBootstrapService(BootstrapOptions{
			AppName:     "FAIRY",
			CoreVersion: "0.1.0",
		}),
		ownDatabase: opened.OwnDatabase,
	}
	if err := rt.Captures.SettleRecovered(context.Background()); err != nil {
		return nil, fmt.Errorf("settling recovered desktop captures: %w", err)
	}

	companion.AttachLogger(services.Companion, logger.Named("companion"))
	companion.AttachMessageTelemetry(services.Companion, messageMetrics)
	companion.AttachCharacterLookup(services.Companion, services.Character.CatalogStore())
	companion.AttachProfileSource(services.Companion, services.Profile.ProfileStore())
	companion.AttachConfigSource(services.Companion, services.ConfigReader)
	companion.AttachDesktopToolCoordinator(services.Companion, rt.Captures)
	character.AttachLogger(services.Character, logger.Named("character"))
	companion.AttachWebSearchLogger(services.WebSearch, logger.Named("openserp"))

	initiativePorts := initiativeAdapter{
		companion: services.Companion, memory: opened.MemoryStore,
		characters: services.Character, config: services.ConfigReader, model: services.Model,
		messages: messageMetrics, events: rt.Participation, logger: logger.Named("initiative"),
	}
	rt.Initiative = initiative.NewService(context.Background(), initiative.ServiceOptions{
		Turns: initiativePorts, Interactions: initiativePorts, Decisions: initiativePorts,
		Learning: initiativePorts, Feedback: initiativePorts, Observer: initiativePorts,
	})
	companion.AttachDesktopEvidenceValidator(services.Companion, rt.Initiative.EvidenceValidator())
	companion.AttachAmbientReplyObserver(services.Companion, ambientReplyAdapter{service: rt.Initiative})

	companion.AttachEventEmitter(services.Companion, func(event session.Event) {
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
		if rt.Initiative != nil {
			rt.Initiative.Close()
		}
		rt.closeErr = rt.Companion.Close()
		rt.Events.Close()
		rt.Participation.Close()
		rt.Captures.Close()
		rt.Messages.Close()
		rt.Logs.Close()
		if rt.ownDatabase && rt.Database != nil {
			rt.Database.Close()
		}
	})
	return rt.closeErr
}
