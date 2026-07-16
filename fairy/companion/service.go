package companion

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"

	"fairy/character"
	"fairy/config"
	"fairy/memory"
	"fairy/model"
	"fairy/profile"
)

type CompanionService struct {
	root               string
	memoryStore        *memory.Store
	modelService       *model.ModelService
	backgroundJobs     atomic.Int64
	extractionMu       sync.Mutex
	extractionIdle     map[string]context.CancelFunc
	backgroundErrorMu  sync.Mutex
	backgroundError    error
	continuationMu     sync.Mutex
	continuationByConv map[string]*model.ContinuationState
	gateMu             sync.Mutex
	gates              map[string]*conversationGate
	emitMu             sync.Mutex
	emit               EventEmitter
}

// AttachEventEmitter wires a Wails-free sink from main (package function, not a bound service method).
func AttachEventEmitter(s *CompanionService, emit EventEmitter) {
	if s == nil {
		return
	}
	s.emitMu.Lock()
	defer s.emitMu.Unlock()
	s.emit = emit
}

func (s *CompanionService) emitEvent(event HarnessEvent) {
	s.emitMu.Lock()
	emit := s.emit
	s.emitMu.Unlock()
	if emit != nil {
		emit(event)
	}
}

func NewCompanionService() *CompanionService {
	return &CompanionService{
		extractionIdle:     make(map[string]context.CancelFunc),
		continuationByConv: make(map[string]*model.ContinuationState),
		gates:              make(map[string]*conversationGate),
	}
}

func NewCompanionServiceWithRuntime(root string, memoryStore *memory.Store, modelService *model.ModelService) *CompanionService {
	return &CompanionService{
		root:               root,
		memoryStore:        memoryStore,
		modelService:       modelService,
		extractionIdle:     make(map[string]context.CancelFunc),
		continuationByConv: make(map[string]*model.ContinuationState),
		gates:              make(map[string]*conversationGate),
	}
}

func (s *CompanionService) SubmitTurn(request SubmitTurnRequest) (TurnOutcome, error) {
	if err := ValidateSubmitTurnRequest(request); err != nil {
		return TurnOutcome{}, err
	}
	if s == nil || !s.RespondRuntimeMigrated() {
		return TurnOutcome{}, ErrRespondRuntimeNotMigrated
	}
	states, err := s.availableVisualStatesForConversation(request.ConversationID)
	if err != nil {
		return TurnOutcome{}, err
	}
	return s.SubmitCompiledTurn(SubmitCompiledTurnRequest{
		ConversationID:        request.ConversationID,
		Input:                 request.Input,
		SpeechEnabled:         request.SpeechEnabled,
		MaxOutputTokens:       RespondMaxOutputTokens,
		AvailableVisualStates: states,
	})
}

func (s *CompanionService) RespondRuntimeMigrated() bool {
	return s != nil && s.memoryStore != nil && s.modelService != nil
}

func (s *CompanionService) SubmitCompiledTurn(request SubmitCompiledTurnRequest) (TurnOutcome, error) {
	if err := ValidateSubmitCompiledTurnRequest(request); err != nil {
		return TurnOutcome{}, err
	}
	if !s.RespondRuntimeMigrated() {
		return TurnOutcome{}, ErrRespondRuntimeNotMigrated
	}
	turnCtx, err := s.reserveTurn(request.ConversationID)
	if err != nil {
		return TurnOutcome{}, err
	}
	persisted, err := s.memoryStore.BeginTurn(request.ConversationID, request.Input)
	if err != nil {
		s.endTurn(request.ConversationID, "")
		return TurnOutcome{}, err
	}
	s.bindTurn(request.ConversationID, persisted.ID)
	defer s.endTurn(request.ConversationID, persisted.ID)

	life := NewTurnLifecycle(request.ConversationID, persisted.ID)
	fail := func(code string, cause error) (TurnOutcome, error) {
		_ = s.memoryStore.FailTurn(request.ConversationID, persisted.ID, code, cause.Error(), false)
		if errors.Is(cause, ErrTurnInterrupted) {
			if event, err := life.Interrupt(); err == nil {
				s.emitEvent(event)
			}
		} else if event, err := life.Fail(code, cause.Error(), false); err == nil {
			s.emitEvent(event)
		}
		return TurnOutcome{}, cause
	}
	transition := func(state TurnState) error {
		event, err := life.Transition(state)
		if err != nil {
			return err
		}
		s.emitEvent(event)
		return nil
	}

	if err := transition(TurnStateInterpreting); err != nil {
		return fail("INVALID_STATE_TRANSITION", err)
	}
	bootstrap, err := s.memoryStore.LoadConversation(request.ConversationID)
	if err != nil {
		return fail("CONVERSATION_FAILED", err)
	}
	characterRecord, err := s.activeCharacter(bootstrap.Conversation.CharacterID)
	if err != nil {
		return fail("CHARACTER_NOT_AVAILABLE", err)
	}
	userProfile, err := profile.NewStore(s.root).Current()
	if err != nil {
		return fail("USER_PROFILE_UNAVAILABLE", err)
	}
	retrieval, err := s.memoryStore.Retrieve(bootstrap.Conversation.CharacterID, request.Input)
	if err != nil {
		return fail("RETRIEVAL_FAILED", err)
	}
	if err := transition(TurnStatePlanning); err != nil {
		return fail("INVALID_STATE_TRANSITION", err)
	}
	input, err := BuildRespondInput(characterRecord, userProfile, bootstrap.PromptWindow, bootstrap.Messages, request.AvailableVisualStates, retrieval)
	if err != nil {
		return fail("PROMPT_BUILD_FAILED", err)
	}
	connectionConfig, err := config.ReadModelConnection(s.root)
	if err != nil {
		return fail("MODEL_FAILED", err)
	}
	cacheKey := ""
	if connectionConfig.Capabilities.PromptCacheKey {
		cacheKey = model.LaneCacheKey(request.ConversationID, model.PromptLaneRespond)
	}
	fullRequest := model.CompiledPromptRequest{
		Shape: model.ModelRequestShape{
			Lane:            model.PromptLaneRespond,
			Model:           connectionConfig.Model,
			Instructions:    RespondInstructions,
			MaxOutputTokens: request.MaxOutputTokens,
			PromptCacheKey:  cacheKey,
		},
		Input: input,
	}
	previous := s.continuationState(request.ConversationID)
	decision := model.DecideContinuation(connectionConfig.Capabilities.CacheRetention, previous, fullRequest)
	executeRequest, err := model.MaterializeContinuationRequest(fullRequest, decision)
	if err != nil {
		return fail("MODEL_FAILED", err)
	}
	if err := transition(TurnStateResponding); err != nil {
		return fail("INVALID_STATE_TRANSITION", err)
	}
	if err := turnCtx.Err(); err != nil {
		return fail("TURN_INTERRUPTED", ErrTurnInterrupted)
	}
	events, err := s.modelService.ExecuteRequestContext(turnCtx, executeRequest)
	if err != nil {
		err = mapModelCancelError(err)
		code := "MODEL_FAILED"
		if errors.Is(err, ErrTurnInterrupted) {
			code = "TURN_INTERRUPTED"
		}
		return fail(code, err)
	}
	draft := collectText(events)
	reply, err := CompileReply(draft, request.AvailableVisualStates)
	if err != nil {
		return fail("MODEL_RESPONSE_INVALID", err)
	}
	for index, chain := range reply.Chains {
		delta := chain.Text
		if index > 0 {
			delta = "\n" + chain.Text
		}
		event, err := life.ReplyChain(uint8(index), delta, chain)
		if err != nil {
			return fail("MODEL_RESPONSE_INVALID", err)
		}
		s.emitEvent(event)
	}
	if _, err := s.memoryStore.CompleteTurn(request.ConversationID, persisted.ID, reply.DisplayText); err != nil {
		return TurnOutcome{}, err
	}
	var profileRevision *uint64
	if userProfile != nil {
		value := userProfile.Revision
		profileRevision = &value
	}
	usage := laneUsageFromEvents(events, bootstrap.PromptWindow.Revision)
	completed, err := life.Complete(TurnCompletion{
		Text:                reply.DisplayText,
		SpeechText:          reply.SpeechText,
		CharacterRevision:   characterRecord.Revision,
		UserProfileRevision: profileRevision,
		Usage:               usage,
		VisualState:         reply.VisualState,
		Chains:              reply.Chains,
	})
	if err != nil {
		return fail("INVALID_STATE_TRANSITION", err)
	}
	s.emitEvent(completed)
	s.updateContinuationState(request.ConversationID, connectionConfig.Capabilities.CacheRetention, fullRequest, reply.DisplayText, events)
	s.scheduleBackgroundExtraction(request.ConversationID)
	s.scheduleAutoCompaction(request.ConversationID, events)
	return TurnOutcome{
		ConversationID:  request.ConversationID,
		TurnID:          persisted.ID,
		ResponseText:    reply.DisplayText,
		SpeechText:      reply.SpeechText,
		SpeechRequested: request.SpeechEnabled,
		VisualState:     reply.VisualState,
		Chains:          reply.Chains,
		RespondMigrated: true,
	}, nil
}

func laneUsageFromEvents(events []model.StreamEvent, historyWindow uint64) []LaneModelUsage {
	promptTokens, completionTokens := 0, 0
	known := false
	for _, event := range events {
		if event.Type == "usage" && event.Usage != nil {
			promptTokens = event.Usage.PromptTokens
			completionTokens = event.Usage.CompletionTokens
			known = true
		}
	}
	if !known {
		return []LaneModelUsage{}
	}
	input := uint64(promptTokens)
	output := uint64(completionTokens)
	// Provider transport does not yet surface cached token fields; emit Missing
	// (same wire shape as Rust CachedTokenObservation::Missing).
	return []LaneModelUsage{{
		Lane:          string(model.PromptLaneRespond),
		HistoryWindow: historyWindow,
		Usage: LaneUsage{
			InputTokens:       &input,
			OutputTokens:      &output,
			CachedInputTokens: CacheMissing(),
			CacheWriteTokens:  CacheMissing(),
		},
	}}
}

func (s *CompanionService) CompactConversation(conversationID string) (memory.CompactionResult, error) {
	if !s.RespondRuntimeMigrated() {
		return memory.CompactionResult{}, ErrRespondRuntimeNotMigrated
	}
	if err := s.beginCompaction(conversationID); err != nil {
		return memory.CompactionResult{}, err
	}
	defer s.endCompaction(conversationID)

	bootstrap, err := s.memoryStore.LoadConversation(conversationID)
	if err != nil {
		return memory.CompactionResult{}, err
	}
	characterRecord, err := s.activeCharacter(bootstrap.Conversation.CharacterID)
	if err != nil {
		return memory.CompactionResult{}, err
	}
	userProfile, err := profile.NewStore(s.root).Current()
	if err != nil {
		return memory.CompactionResult{}, err
	}
	windowed := messagesAfterCutoff(bootstrap.Messages, bootstrap.PromptWindow.CutoffMessageSequence)
	if len(windowed) == 0 {
		return memory.CompactionResult{}, errors.New("compaction requires dialogue after the current prompt window cutoff")
	}
	input, err := BuildCompactInput(characterRecord, userProfile, bootstrap.PromptWindow, bootstrap.Messages)
	if err != nil {
		return memory.CompactionResult{}, err
	}
	events, err := s.modelService.ExecutePrompt(
		model.PromptLaneCompact,
		CompactInstructions,
		CompactMaxOutputTokens,
		input,
		model.LaneCacheKey(conversationID, model.PromptLaneCompact),
	)
	if err != nil {
		return memory.CompactionResult{}, err
	}
	summary, err := normalizeCompactionSummary(collectText(events))
	if err != nil {
		return memory.CompactionResult{}, err
	}
	result, err := s.memoryStore.CommitPromptWindow(conversationID, bootstrap.PromptWindow.Revision, summary)
	if err != nil {
		return memory.CompactionResult{}, err
	}
	result.RetainedDialogueItems = len(windowed)
	s.clearContinuationState(conversationID)
	return result, nil
}

func (s *CompanionService) continuationState(conversationID string) *model.ContinuationState {
	s.continuationMu.Lock()
	defer s.continuationMu.Unlock()
	if s.continuationByConv == nil {
		return nil
	}
	return s.continuationByConv[conversationID]
}

func (s *CompanionService) clearContinuationState(conversationID string) {
	s.continuationMu.Lock()
	defer s.continuationMu.Unlock()
	delete(s.continuationByConv, conversationID)
}

func (s *CompanionService) updateContinuationState(
	conversationID string,
	cacheRetention bool,
	fullRequest model.CompiledPromptRequest,
	displayText string,
	events []model.StreamEvent,
) {
	s.continuationMu.Lock()
	defer s.continuationMu.Unlock()
	if s.continuationByConv == nil {
		s.continuationByConv = make(map[string]*model.ContinuationState)
	}
	if !cacheRetention {
		delete(s.continuationByConv, conversationID)
		return
	}
	responseID := model.ResponseIDFromEvents(events)
	if err := model.ValidatePreviousResponseID(responseID); err != nil {
		delete(s.continuationByConv, conversationID)
		return
	}
	s.continuationByConv[conversationID] = &model.ContinuationState{
		PreviousResponseID: responseID,
		PreviousRequest:    fullRequest,
		ResponseItems: []model.PromptItem{{
			Type:    model.PromptItemAssistantMessage,
			Content: displayText,
		}},
		ResponseComplete: true,
	}
}

func (s *CompanionService) activeCharacter(characterID string) (character.Record, error) {
	catalog, err := character.NewStore(s.root).List()
	if err != nil {
		return character.Record{}, err
	}
	for _, record := range catalog.Characters {
		if record.CharacterID == characterID {
			return record, nil
		}
	}
	return character.Record{}, errors.New("character is not available")
}

// availableVisualStatesForConversation mirrors Tauri IPC active_visual_states:
// load the conversation character appearance and expose prompt-safe state entries.
func (s *CompanionService) availableVisualStatesForConversation(conversationID string) ([]VisualState, error) {
	bootstrap, err := s.memoryStore.LoadConversation(conversationID)
	if err != nil {
		return nil, err
	}
	record, err := s.activeCharacter(bootstrap.Conversation.CharacterID)
	if err != nil {
		return nil, err
	}
	if record.Appearance.Status != "assigned" || record.Appearance.Visual == nil {
		return nil, errors.New("character appearance is unassigned")
	}
	states := make([]VisualState, 0, len(record.Appearance.Visual.States))
	for _, state := range record.Appearance.Visual.States {
		states = append(states, VisualState{ID: state.ID, Description: state.Description})
	}
	return states, nil
}

func collectText(events []model.StreamEvent) string {
	var builder strings.Builder
	for _, event := range events {
		if event.Type == "text_delta" {
			builder.WriteString(event.Data)
		}
	}
	return builder.String()
}

func promptItemsFromMessages(messages []memory.MessageRecord) []model.PromptItem {
	items := make([]model.PromptItem, 0, len(messages))
	for _, message := range messages {
		switch message.Role {
		case "user":
			items = append(items, model.PromptItem{Type: model.PromptItemUserMessage, Content: message.Content})
		case "assistant":
			items = append(items, model.PromptItem{Type: model.PromptItemAssistantMessage, Content: message.Content})
		}
	}
	return items
}
