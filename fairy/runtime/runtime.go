package runtime

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"fairy/character"
	"fairy/companion"
	"fairy/config"
	"fairy/desktop"
	"fairy/logx"
	"fairy/memory"
	"fairy/model"
	"fairy/profile"
	"fairy/search"
	"fairy/secret"
	"fairy/speech"
	"go.uber.org/zap"
)

// Options configures a Session Core process.
type Options struct {
	ConfigRoot string
	Logger     *zap.Logger
	// LogEventsJSONL prints harness events to stdout (CLI turn debugging).
	LogEventsJSONL bool
}

// Runtime owns long-lived Core services for CLI and HTTP surfaces.
type Runtime struct {
	ConfigRoot string
	Logger     *zap.Logger
	Events     *EventHub

	MemoryStore  *memory.Store
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
	Bootstrap    *desktop.BootstrapService

	eventMu sync.Mutex
	events  []companion.HarnessEvent
}

func Open(options Options) (*Runtime, error) {
	logger := options.Logger
	if logger == nil {
		logger = logx.New()
	}
	configRoot := options.ConfigRoot
	if configRoot == "" {
		configRoot = os.Getenv("FAIRY_CONFIG_ROOT")
	}
	if configRoot == "" {
		configRoot = filepath.Join(os.Getenv("HOME"), "Library", "Application Support", "dev.rinai.fairy", "harness", "v1")
	}

	memoryPath, err := memory.DatabasePath(configRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve memory path: %w", err)
	}
	memoryStore, err := memory.OpenOrCreate(memoryPath)
	if err != nil {
		return nil, fmt.Errorf("open memory store: %w", err)
	}
	secretPath, err := secret.DatabasePath(configRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve secret path: %w", err)
	}
	secretStore := secret.NewStore(secretPath)

	webSettings, err := config.ReadWebSearchSettings(configRoot)
	if err != nil {
		return nil, err
	}
	webSearch := search.NewServiceFromEnv(webSettings.BaseURL)
	modelService := model.NewModelService(configRoot, secretStore)
	companionService := companion.NewCompanionServiceWithRuntime(configRoot, memoryStore, modelService, webSearch)
	characterService := character.NewCharacterService(configRoot)
	configService := config.NewConfigService(configRoot, secretStore)
	speechService := speech.NewSpeechService(configRoot, secretStore)
	profileService := profile.NewProfileService(configRoot)
	configReader := config.NewReader(configRoot)

	rt := &Runtime{
		ConfigRoot:   configRoot,
		Logger:       logger,
		Events:       NewEventHub(),
		MemoryStore:  memoryStore,
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
		Bootstrap: desktop.NewBootstrapService(desktop.BootstrapOptions{
			AppName:                "FAIRY",
			MigrationStage:         "session-core",
			CoreVersion:            "0.1.0",
			RespondRuntimeMigrated: true,
		}),
	}

	companion.AttachLogger(companionService, logger.Named("companion"))
	companion.AttachCharacterStore(companionService, characterService.CatalogStore())
	companion.AttachProfileStore(companionService, profileService.ProfileStore())
	companion.AttachConfigReader(companionService, configReader)
	companion.AttachSpeechSynthesizer(companionService, companionSpeechAdapter{service: speechService})
	attachSemanticEmbedder(companionService, modelService, configReader, logger.Named("semantic"))
	character.AttachLogger(characterService, logger.Named("character"))
	search.AttachLogger(webSearch, logger.Named("openserp"))

	companion.AttachEventEmitter(companionService, func(event companion.HarnessEvent) {
		rt.eventMu.Lock()
		rt.events = append(rt.events, event)
		rt.eventMu.Unlock()
		rt.Events.Publish(event)
		if options.LogEventsJSONL {
			line, err := json.Marshal(event)
			if err != nil {
				logger.Warn("marshal harness event", zap.Error(err))
				return
			}
			fmt.Println(string(line))
		}
	})

	return rt, nil
}

func (rt *Runtime) Close() error {
	if rt == nil {
		return nil
	}
	return rt.Companion.Close()
}

func (rt *Runtime) DrainEvents() []companion.HarnessEvent {
	if rt == nil {
		return nil
	}
	rt.eventMu.Lock()
	defer rt.eventMu.Unlock()
	out := append([]companion.HarnessEvent(nil), rt.events...)
	rt.events = nil
	return out
}
