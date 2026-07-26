package runtime

import (
	"fairy/character"
	"fairy/companion"
	"fairy/config"
	"fairy/identity"
	"fairy/memory"
	"fairy/model"
	pgstore "fairy/postgres"
	"fairy/profile"
	"fairy/search"
	"fairy/secret"
	"fairy/speech"
)

type coreServices struct {
	WebSearch    *search.Service
	Model        *model.ModelService
	Companion    *companion.CompanionService
	Identity     *identity.Store
	Character    *character.CharacterService
	Config       *config.ConfigService
	Speech       *speech.SpeechService
	Profile      *profile.ProfileService
	ConfigReader *config.Reader
	Memory       *memory.MemoryService
}

func wireCoreServices(configRoot string, database *pgstore.Pool, memoryStore *memory.Store, secretStore *secret.Store) (*coreServices, error) {
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

	return &coreServices{
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
