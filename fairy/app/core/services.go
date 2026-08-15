package core

import (
	"fmt"

	turn "fairy/agent/conversation"
	retention "fairy/agent/learning"
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
	"fairy/runtime/model"
)

type coreServices struct {
	WebSearch    turn.WebSearchBackend
	Model        *model.ModelService
	Turn         *turn.Service
	Retention    *retention.Service
	Identity     *identity.Store
	Character    *character.CharacterService
	Config       *config.ConfigService
	Profile      *config.ProfileService
	ConfigReader *config.Reader
	Memory       *memoryadmin.Service
}

func wireCoreServices(
	configRoot string,
	transcriptStore *history.Store,
	compactionStore *historycompaction.Store,
	runtimeStore *historyruntime.Store,
	memoryStore *personal.Store,
	extractionStore *extraction.Store,
	knowledgeStore *knowledge.Store,
	socialStore *social.Store,
	identityStore *identity.Store,
	characterStore *character.Store,
	profileStore *config.ProfileStore,
	secretStore *config.SecretStore,
	modelService *model.ModelService,
	configReader *config.Reader,
) (*coreServices, error) {
	webSearch := turn.WebSearchBackend(nil)
	if modelService == nil {
		modelService = model.NewModelService(configRoot, secretStore)
	}
	turnService := turn.NewServiceWithRuntime(configRoot, transcriptStore, compactionStore, runtimeStore, memoryStore, extractionStore, knowledgeStore, socialStore, modelService, webSearch)
	if identityStore == nil {
		return nil, fmt.Errorf("identity store is required")
	}
	turn.AttachOwnerIdentityStore(turnService, identityStore)
	characterService, err := character.NewCharacterServiceWithStore(characterStore)
	if err != nil {
		return nil, err
	}
	configService := config.NewConfigService(configRoot, secretStore)
	profileService, err := config.NewProfileServiceWithStore(profileStore)
	if err != nil {
		return nil, err
	}
	if configReader == nil {
		configReader = config.NewReader(configRoot)
	}
	retentionService := retention.New(retention.Options{
		Extraction: extractionStore,
		Knowledge:  knowledgeStore,
		Documents:  knowledge.UnavailableDocumentFetcher{},
		Model:      modelService,
		Config:     configReader,
		Character: func(characterID string) (character.Record, error) {
			record, found, err := characterService.CatalogStore().Lookup(characterID)
			if err != nil {
				return character.Record{}, err
			}
			if !found {
				return character.Record{}, fmt.Errorf("character %q not found", characterID)
			}
			return record, nil
		},
		ObserveError: func(err error) { turn.ObserveBackgroundError(turnService, err) },
		ClearError:   func() { turn.ClearBackgroundError(turnService) },
		RecordKnowledgeRun: func(task knowledge.IngestTask, events []model.StreamEvent, usage []model.LaneModelUsage) {
			turn.RecordKnowledgeRun(turnService, task, events, usage)
		},
	})
	turn.AttachRetention(turnService, retentionAdapter{service: retentionService})
	turn.AttachDeferredTurnScheduler(turnService, deferredTurnAdapter{service: retentionService})
	if knowledgeStore != nil && knowledgeStore.KnowledgeIngestReady() {
		retentionService.Start()
	}

	return &coreServices{
		WebSearch:    webSearch,
		Model:        modelService,
		Turn:         turnService,
		Retention:    retentionService,
		Identity:     identityStore,
		Character:    characterService,
		Config:       configService,
		Profile:      profileService,
		ConfigReader: configReader,
		Memory:       memoryadmin.NewServiceWithStore(configRoot, memoryStore, extractionStore),
	}, nil
}
