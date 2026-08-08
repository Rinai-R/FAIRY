package core

import (
	"fairy/character"
	"fairy/companion"
	"fairy/config"
	"fairy/coredb"
	"fairy/memory"
	"fairy/model"
)

type coreServices struct {
	WebSearch    *companion.WebSearchService
	Model        *model.ModelService
	Companion    *companion.CompanionService
	Identity     *memory.IdentityStore
	Character    *character.CharacterService
	Config       *config.ConfigService
	Profile      *config.ProfileService
	ConfigReader *config.Reader
	Memory       *memory.MemoryService
}

func wireCoreServices(configRoot string, database *coredb.Pool, memoryStore *memory.Store, secretStore *config.SecretStore, modelService *model.ModelService, configReader *config.Reader) (*coreServices, error) {
	webSettings, err := config.ReadWebSearchSettings(configRoot)
	if err != nil {
		return nil, err
	}
	webSearch := companion.NewWebSearchService(config.ResolveWebSearchBaseURL(webSettings.BaseURL))
	if modelService == nil {
		modelService = model.NewModelService(configRoot, secretStore)
	}
	companionService := companion.NewCompanionServiceWithRuntime(configRoot, memoryStore, modelService, webSearch)
	identityStore, err := memory.NewIdentityStore(database)
	if err != nil {
		return nil, err
	}
	companion.AttachOwnerIdentityStore(companionService, identityStore)
	characterService := character.NewCharacterService(configRoot)
	configService := config.NewConfigService(configRoot, secretStore)
	profileService := config.NewProfileService(configRoot)
	if configReader == nil {
		configReader = config.NewReader(configRoot)
	}

	return &coreServices{
		WebSearch:    webSearch,
		Model:        modelService,
		Companion:    companionService,
		Identity:     identityStore,
		Character:    characterService,
		Config:       configService,
		Profile:      profileService,
		ConfigReader: configReader,
		Memory:       memory.NewMemoryServiceWithStore(configRoot, memoryStore),
	}, nil
}
