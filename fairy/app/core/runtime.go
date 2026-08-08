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

	turn "fairy/agent/conversation"
	initiative "fairy/agent/presence"
	"fairy/agent/sticker"
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
	coredb "fairy/runtime/database"
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
	History       *observabilityhistory.Store
	StartedAt     time.Time
	Database      *coredb.Pool

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
	ownDatabase        bool
	closeOnce          sync.Once
	closeErr           error
}

func (rt *Runtime) APIDependencies() *api.Dependencies {
	if rt == nil {
		return nil
	}
	return &api.Dependencies{
		ConfigRoot: rt.ConfigRoot, Logger: rt.Logger, StartedAt: rt.StartedAt,
		Database: rt.Database, TranscriptStore: rt.TranscriptStore, RuntimeStore: rt.RuntimeStore, KnowledgeStore: rt.KnowledgeStore, MemoryStore: rt.MemoryStore, ObservabilityStore: rt.ObservabilityStore,
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
	embedder := semanticEmbedder(modelService, configReader, logger.Named("semantic"))
	memoryStore := opened.MemoryStore
	if memoryStore == nil {
		memoryStore, err = personal.NewStoreFromPool(opened.Database, nil)
		if err != nil {
			return nil, err
		}
		opened.MemoryStore = memoryStore
	}
	memoryStore.ReplaceSemanticEmbedder(embedder)
	extractionStore, err := extraction.NewStoreFromPool(opened.Database, embedder)
	if err != nil {
		return nil, err
	}
	knowledgeStore, err := knowledge.NewStoreFromPool(opened.Database, embedder)
	if err != nil {
		return nil, err
	}
	socialStore, err := social.NewStoreFromPool(opened.Database)
	if err != nil {
		return nil, err
	}
	transcriptStore := opened.TranscriptStore
	if transcriptStore == nil {
		transcriptStore, err = history.NewStoreFromPool(opened.Database)
		if err != nil {
			return nil, err
		}
		opened.TranscriptStore = transcriptStore
	}
	compactionStore := opened.CompactionStore
	if compactionStore == nil {
		compactionStore, err = historycompaction.NewStoreFromPool(opened.Database)
		if err != nil {
			return nil, err
		}
		opened.CompactionStore = compactionStore
	}
	runtimeStore := opened.RuntimeStore
	if runtimeStore == nil {
		runtimeStore, err = historyruntime.NewStoreFromPool(opened.Database)
		if err != nil {
			return nil, err
		}
		opened.RuntimeStore = runtimeStore
	}
	observabilityStore, err := ledger.NewStoreFromPool(opened.Database)
	if err != nil {
		return nil, err
	}
	observabilityHistory, err := observabilityhistory.New(opened.Database)
	if err != nil {
		return nil, err
	}
	keepHistory := false
	defer func() {
		if !keepHistory {
			observabilityHistory.Close()
		}
	}()
	restoredLogs, err := observabilityHistory.RecentLogs(context.Background(), observability.DefaultLogCapacity)
	if err != nil {
		return nil, fmt.Errorf("restoring observability logs: %w", err)
	}
	logStore.Restore(restoredLogs)
	logStore.SetHistorySink(observabilityHistory.EnqueueLog)
	services, err := wireCoreServices(configRoot, opened.Database, transcriptStore, compactionStore, runtimeStore, memoryStore, extractionStore, knowledgeStore, socialStore, opened.SecretStore, modelService, configReader)
	if err != nil {
		return nil, err
	}
	services.Config.AttachSemanticEmbeddingRuntime(semanticEmbeddingRuntime{model: modelService, store: semanticEmbedderPublishers{memoryStore, extractionStore, knowledgeStore}})
	stickerStore, err := sticker.NewStore(opened.Database)
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
		Database:           opened.Database,
		TranscriptStore:    opened.TranscriptStore,
		CompactionStore:    opened.CompactionStore,
		RuntimeStore:       opened.RuntimeStore,
		KnowledgeStore:     knowledgeStore,
		MemoryStore:        opened.MemoryStore,
		ExtractionStore:    extractionStore,
		SocialStore:        socialStore,
		ObservabilityStore: observabilityStore,
		Identity:           services.Identity,
		Memory:             services.Memory,
		Secret:             opened.SecretStore,
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
		ownDatabase: opened.OwnDatabase,
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
		turns: services.Turn, history: opened.TranscriptStore, social: socialStore,
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
	keepDependencies = true
	keepHistory = true
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
		rt.closeErr = rt.Turn.Close()
		rt.Events.Close()
		rt.Participation.Close()
		rt.Captures.Close()
		rt.Messages.Close()
		rt.Logs.Close()
		rt.History.Close()
		if rt.ownDatabase && rt.Database != nil {
			rt.Database.Close()
		}
	})
	return rt.closeErr
}
