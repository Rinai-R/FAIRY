package bootstrap

import (
	"fairy/character"
	"fairy/companion"
	"fairy/identity"
	platformconfig "fairy/internal/platform/config"
	platformsecrets "fairy/internal/platform/secrets"
	"fairy/memory"
	"fairy/model"
	pgstore "fairy/postgres"
	"fairy/profile"
	"fairy/search"
	"fairy/speech"
)

// CoreServices holds domain services wired during process bootstrap.
type CoreServices struct {
	WebSearch    *search.Service
	Model        *model.ModelService
	Companion    *companion.CompanionService
	Identity     *identity.Store
	Character    *character.CharacterService
	Config       *platformconfig.ConfigService
	Speech       *speech.SpeechService
	Profile      *profile.ProfileService
	ConfigReader *platformconfig.Reader
	Memory       *memory.MemoryService
}

// WireCoreServices constructs cross-domain services from opened infrastructure.
func WireCoreServices(configRoot string, database *pgstore.Pool, memoryStore *memory.Store, secretStore *platformsecrets.Store) (*CoreServices, error) {
	webSettings, err := platformconfig.ReadWebSearchSettings(configRoot)
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
	configService := platformconfig.NewConfigService(configRoot, secretStore)
	speechService := speech.NewSpeechService(configRoot, secretStore)
	profileService := profile.NewProfileService(configRoot)
	configReader := platformconfig.NewReader(configRoot)

	return &CoreServices{
		WebSearch:    webSearch,
		Model:        modelService,
		Companion:    companionService,
		Identity:     identityStore,
		Character:    characterService,
		Config:       configService,
		Speech:       speechService,
		Profile:      profileService,
		ConfigReader: configReader,
		Memory:       memory.NewMemoryServiceWithStore(configRoot, memoryStore),
	}, nil
}
