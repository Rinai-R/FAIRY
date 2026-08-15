package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	turn "fairy/agent/conversation"
	initiative "fairy/agent/presence"
	"fairy/agent/sticker"
	"fairy/app/foundation"
	"fairy/context/character"
	historycompaction "fairy/context/history/compaction"
	historyruntime "fairy/context/history/runtime"
	history "fairy/context/history/transcript"
	"fairy/context/identity"
	"fairy/context/knowledge"
	memoryadmin "fairy/context/memory/admin"
	"fairy/context/memory/extraction"
	"fairy/context/memory/personal"
	"fairy/context/social"
	"fairy/runtime/config"
	"fairy/runtime/ledger"
	"fairy/runtime/model"
	"fairy/runtime/observability"
	observabilityhistory "fairy/runtime/observability/history"
	"fairy/transport/desktopcapture"
	"fairy/transport/session"
	api "fairy/transport/web"
)

var _ api.ObservabilityHistory = (*observabilityhistory.Store)(nil)

// RuntimeOptions configures a Session Core process.
type RuntimeOptions struct {
	ConfigRoot  string
	Logger      *zap.Logger
	LogStore    *observability.LogStore
	HTTPMetrics *observability.HTTPMetrics
	// Profile selects full vs desktop-lite labels. Empty defaults to full.
	// Both profiles use the local SeekDB foundation; neither probes PostgreSQL.
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
	History       *observabilityhistory.Store
	StartedAt     time.Time
	Foundation    *foundation.Foundation

	TranscriptStore    *history.Store
	CompactionStore    *historycompaction.Store
	RuntimeStore       *historyruntime.Store
	KnowledgeStore     *knowledge.Store
	MemoryStore        *personal.Store
	ExtractionStore    *extraction.Store
	SocialStore        *social.Store
	ObservabilityStore *ledger.Store
	Identity           *identity.Store
	Memory             *memoryadmin.Service
	Secret             *config.SecretStore
	Model              *model.ModelService
	Turn               *turn.Service
	Initiative         *initiative.Service
	Character          *character.CharacterService
	Config             *config.ConfigService
	ConfigReader       *config.Reader
	Profile            *config.ProfileService
	Stickers           *sticker.Store
	WebSearch          *knowledge.WebSearchService
	Bootstrap          *BootstrapService
	lifetimeCancel     context.CancelFunc
	closeOnce          sync.Once
	closeErr           error
}

func (rt *Runtime) APIDependencies() *api.Dependencies {
	if rt == nil {
		return nil
	}
	return &api.Dependencies{
		ConfigRoot: rt.ConfigRoot, Logger: rt.Logger, StartedAt: rt.StartedAt,
		QueryStorageStatus: rt.storageStatus,
		TranscriptStore:    rt.TranscriptStore, RuntimeStore: rt.RuntimeStore, KnowledgeStore: rt.KnowledgeStore, MemoryStore: rt.MemoryStore, ObservabilityStore: rt.ObservabilityStore,
		Identity: rt.Identity, Memory: rt.Memory, Secret: rt.Secret,
		Turns: turnAPIAdapter{service: rt.Turn}, Initiative: initiativeAPIAdapter{service: rt.Initiative}, Character: rt.Character,
		Config: rt.Config, Profile: rt.Profile, Stickers: rt.Stickers, Captures: rt.Captures,
		Logs: rt.Logs, HTTPMetrics: rt.HTTPMetrics, Messages: rt.Messages,
		History: rt.History,
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

// StorageStatus is the credential-free readiness projection used by local
// management and the HTTP status handler. It never includes connection strings
// or master-key material.
func (rt *Runtime) StorageStatus(ctx context.Context) (api.StorageStatus, error) {
	return rt.storageStatus(ctx)
}

func (rt *Runtime) storageStatus(ctx context.Context) (api.StorageStatus, error) {
	if rt == nil || rt.Foundation == nil {
		return api.StorageStatus{Mode: "unavailable", Error: foundation.ErrFoundationClosed.Error()}, nil
	}
	status, err := rt.Foundation.Status(ctx)
	if err != nil {
		return api.StorageStatus{Mode: "production", Storage: "seekdb", Error: err.Error()}, nil
	}
	openConnections := 0
	if database, sqlErr := rt.Foundation.SQL(); sqlErr == nil && database != nil {
		openConnections = database.Stats().OpenConnections
	}
	return api.StorageStatus{
		Ready:           status.Schema.State == "current" && status.SecretsReady,
		Mode:            "production",
		Storage:         status.Storage,
		Descriptor:      status.SeekDB,
		Schema:          status.Schema,
		SecretsReady:    status.SecretsReady,
		OpenConnections: openConnections,
	}, nil
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

	configRoot, err := filepath.Abs(configRoot)
	if err != nil {
		return nil, fmt.Errorf("resolving config root: %w", err)
	}

	if options.Profile == "" {
		if _, err := ProfileFromEnv(os.Getenv); err != nil {
			return nil, err
		}
	} else if _, err := ParseProfile(string(options.Profile)); err != nil {
		return nil, err
	}

	lifetime, cancelLifetime := context.WithCancel(context.Background())
	opened, err := openFoundation(lifetime, configRoot)
	if err != nil {
		cancelLifetime()
		return nil, err
	}
	keepFoundation := false
	defer func() {
		if keepFoundation {
			return
		}
		closeCtx, cancel := context.WithTimeout(context.Background(), opened.ShutdownLimit())
		defer cancel()
		_ = opened.Close(closeCtx)
		cancelLifetime()
	}()

	database, err := opened.SQL()
	if err != nil {
		return nil, err
	}
	queryLimit := opened.QueryLimit()
	configReader := config.NewReader(configRoot)
	modelService := model.NewModelService(configRoot, opened.Secrets)
	embedder := semanticEmbedder(modelService, configReader, logger.Named("semantic"))
	memoryStore, err := personal.NewSeekDBStore(database, queryLimit, embedder)
	if err != nil {
		return nil, err
	}
	memoryStore.ReplaceSemanticEmbedder(embedder)
	extractionStore, err := extraction.NewSeekDBStoreWithPersonal(database, queryLimit, "core-extraction", 30*time.Second, memoryStore)
	if err != nil {
		return nil, err
	}
	extractionStore.ReplaceSemanticEmbedder(embedder)
	knowledgeStore, err := knowledge.NewSeekDBStore(database, queryLimit, embedder)
	if err != nil {
		return nil, err
	}
	socialStore, err := social.NewSeekDBStore(database, queryLimit)
	if err != nil {
		return nil, err
	}
	transcriptStore := opened.Conversations.Transcript
	compactionStore := opened.Conversations.Compaction
	runtimeStore := opened.Conversations.Runtime
	observabilityStore := opened.Observability.Ledger
	observabilityHistory := opened.Observability.History
	observabilityHistory.SetSinkDiagnostics(func(component string, recovered bool, err error) {
		if recovered {
			logger.Info("observability " + component + " recovered")
			return
		}
		logger.Warn("observability "+component+" failed", zap.Error(err))
	})
	restoredLogs, err := observabilityHistory.RecentLogs(context.Background(), observability.DefaultLogCapacity)
	if err != nil {
		return nil, fmt.Errorf("restoring observability logs: %w", err)
	}
	logStore.Restore(restoredLogs)
	logStore.SetHistorySink(observabilityHistory.EnqueueLog)
	services, err := wireCoreServices(configRoot, transcriptStore, compactionStore, runtimeStore, memoryStore, extractionStore, knowledgeStore, socialStore, opened.Identity, opened.Characters, opened.Profile, opened.Secrets, modelService, configReader)
	if err != nil {
		return nil, err
	}
	services.Config.AttachSemanticEmbeddingRuntime(semanticEmbeddingRuntime{model: modelService, store: semanticEmbedderPublishers{memoryStore, extractionStore, knowledgeStore}})
	stickerRoot := filepath.Join(configRoot, "sticker-content")
	if err := os.MkdirAll(stickerRoot, 0o700); err != nil {
		return nil, fmt.Errorf("creating sticker content root: %w", err)
	}
	stickerStore, err := sticker.NewSeekDBStore(database, stickerRoot, queryLimit)
	if err != nil {
		return nil, err
	}
	turn.AttachStickerSearch(services.Turn, stickerStore)
	messageMetrics := observability.NewMessageMetrics()
	messageMetrics.SetTerminalSink(observabilityHistory.EnqueueTrace)

	rt := &Runtime{
		ConfigRoot:         configRoot,
		Logger:             logger,
		Events:             NewEventHub(),
		Participation:      NewParticipationHub(),
		Captures:           desktopcapture.NewCaptureHub(observabilityStore),
		Logs:               logStore,
		HTTPMetrics:        httpMetrics,
		Messages:           messageMetrics,
		History:            observabilityHistory,
		StartedAt:          time.Now(),
		Foundation:         opened,
		TranscriptStore:    transcriptStore,
		CompactionStore:    compactionStore,
		RuntimeStore:       runtimeStore,
		KnowledgeStore:     knowledgeStore,
		MemoryStore:        memoryStore,
		ExtractionStore:    extractionStore,
		SocialStore:        socialStore,
		ObservabilityStore: observabilityStore,
		Identity:           services.Identity,
		Memory:             services.Memory,
		Secret:             opened.Secrets,
		Model:              services.Model,
		Turn:               services.Turn,
		Character:          services.Character,
		Config:             services.Config,
		ConfigReader:       services.ConfigReader,
		Profile:            services.Profile,
		Stickers:           stickerStore,
		WebSearch:          services.WebSearch,
		Bootstrap: NewBootstrapService(BootstrapOptions{
			AppName:     "FAIRY",
			CoreVersion: "0.1.0",
		}),
		lifetimeCancel: cancelLifetime,
	}
	if err := rt.Captures.SettleRecovered(context.Background()); err != nil {
		return nil, fmt.Errorf("settling recovered desktop captures: %w", err)
	}

	turn.AttachLogger(services.Turn, logger.Named("companion"))
	turn.AttachMessageTelemetry(services.Turn, messageMetrics)
	turn.AttachCharacterLookup(services.Turn, services.Character.CatalogStore())
	turn.AttachProfileSource(services.Turn, services.Profile.ProfileStore())
	turn.AttachConfigSource(services.Turn, services.ConfigReader)
	turn.AttachDesktopToolCoordinator(services.Turn, rt.Captures)
	character.AttachLogger(services.Character, logger.Named("character"))
	knowledge.AttachWebSearchLogger(services.WebSearch, logger.Named("openserp"))

	initiativePorts := initiativeAdapter{
		turns: services.Turn, history: transcriptStore, social: socialStore,
		characters: services.Character, config: services.ConfigReader, model: services.Model,
		messages: messageMetrics, events: rt.Participation, logger: logger.Named("initiative"),
	}
	rt.Initiative = initiative.NewService(context.Background(), initiative.ServiceOptions{
		Turns: initiativePorts, Interactions: initiativePorts, Decisions: initiativePorts,
		Learning: initiativePorts, Feedback: initiativePorts, Observer: initiativePorts,
	})
	turn.AttachDesktopEvidenceValidator(services.Turn, rt.Initiative.EvidenceValidator())
	turn.AttachAmbientReplyObserver(services.Turn, ambientReplyAdapter{service: rt.Initiative})

	turn.AttachEventEmitter(services.Turn, func(event session.Event) {
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
	keepFoundation = true
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
		if rt.Turn != nil {
			rt.closeErr = rt.Turn.Close()
		}
		if rt.Events != nil {
			rt.Events.Close()
		}
		if rt.Participation != nil {
			rt.Participation.Close()
		}
		if rt.Captures != nil {
			rt.Captures.Close()
		}
		if rt.Messages != nil {
			rt.Messages.Close()
		}
		if rt.Logs != nil {
			rt.Logs.Close()
		}
		if rt.Foundation != nil {
			closeCtx, cancel := context.WithTimeout(context.Background(), rt.Foundation.ShutdownLimit())
			rt.closeErr = errors.Join(rt.closeErr, rt.Foundation.Close(closeCtx))
			cancel()
		}
		if rt.lifetimeCancel != nil {
			rt.lifetimeCancel()
		}
	})
	return rt.closeErr
}
