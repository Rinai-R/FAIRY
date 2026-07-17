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
	"fairy/search"
)

type CompanionService struct {
	root              string
	memoryStore       *memory.Store
	modelService      *model.ModelService
	webSearch         WebSearchBackend
	backgroundJobs    atomic.Int64
	extractionMu      sync.Mutex
	extractionIdle    map[string]context.CancelFunc
	backgroundErrorMu sync.Mutex
	backgroundError   error
	gateMu            sync.Mutex
	gates             map[string]*conversationGate
	emitMu            sync.Mutex
	emit              EventEmitter
}

// WebSearchBackend is the optional OpenSERP sidecar search surface.
type WebSearchBackend interface {
	Search(ctx context.Context, query string, limit int) ([]search.Hit, error)
	Close() error
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
		extractionIdle: make(map[string]context.CancelFunc),
		gates:          make(map[string]*conversationGate),
	}
}

func NewCompanionServiceWithRuntime(root string, memoryStore *memory.Store, modelService *model.ModelService) *CompanionService {
	return &CompanionService{
		root:           root,
		memoryStore:    memoryStore,
		modelService:   modelService,
		webSearch:      search.NewService(root),
		extractionIdle: make(map[string]context.CancelFunc),
		gates:          make(map[string]*conversationGate),
	}
}

func (s *CompanionService) Close() error {
	if s == nil || s.webSearch == nil {
		return nil
	}
	return s.webSearch.Close()
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
	if err := s.maybeCompactBeforeTurn(request); err != nil {
		s.setBackgroundError(err)
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
			s.appendRuntimeLedger(request.ConversationID, persisted.ID, runtimeLedgerEventTerminal, TurnStateInterrupted, code, runtimeFailureLedgerMetadata(code, cause, false))
		} else if event, err := life.Fail(code, cause.Error(), false); err == nil {
			s.emitEvent(event)
			s.appendRuntimeLedger(request.ConversationID, persisted.ID, runtimeLedgerEventTerminal, TurnStateFailed, code, runtimeFailureLedgerMetadata(code, cause, false))
		} else {
			s.appendRuntimeLedger(request.ConversationID, persisted.ID, runtimeLedgerEventTerminal, TurnStateFailed, code, runtimeFailureLedgerMetadata(code, cause, false))
		}
		return TurnOutcome{}, cause
	}
	transition := func(state TurnState) error {
		event, err := life.Transition(state)
		if err != nil {
			return err
		}
		s.emitEvent(event)
		s.appendRuntimeLedger(request.ConversationID, persisted.ID, runtimeLedgerEventTransition, state, "", map[string]any{
			"source": "turn_lifecycle",
		})
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
	if err := transition(TurnStateGathering); err != nil {
		return fail("INVALID_STATE_TRANSITION", err)
	}
	retrieval, retrievalErr := s.memoryStore.Retrieve(bootstrap.Conversation.CharacterID, request.Input)
	retrievalOmitReason := ""
	if retrievalErr != nil {
		retrieval = memory.RetrievalContext{}
		retrievalOmitReason = "retrieval_failed"
	}
	s.appendRuntimeLedger(request.ConversationID, persisted.ID, runtimeLedgerEventGather, TurnStateGathering, "", map[string]any{
		"tool":             gatherToolMemorySearch,
		"phase":            "initial",
		"queryHash":        runtimeHash(request.Input),
		"personalCount":    len(retrieval.PersonalMemories),
		"knowledgeCount":   len(retrieval.Knowledge),
		"omitReason":       retrievalOmitReason,
		"modelDrivenIndex": 0,
	})
	if err := transition(TurnStatePlanning); err != nil {
		return fail("INVALID_STATE_TRANSITION", err)
	}
	connectionConfig, err := config.ReadModelConnection(s.root)
	if err != nil {
		return fail("MODEL_FAILED", err)
	}
	cacheKey := ""
	if connectionConfig.Capabilities.PromptCacheKey {
		cacheKey = model.LaneCacheKey(request.ConversationID, model.PromptLaneRespond)
	}

	var (
		reply              CompiledReply
		events             []model.StreamEvent
		fullRequest        model.CompiledPromptRequest
		modelDrivenGathers int
		finalUsage         []LaneModelUsage
	)
	webSearchEnabled := false
	if settings, err := config.ReadWebSearchSettings(s.root); err == nil {
		webSearchEnabled = settings.Enabled
	}
	for {
		allowGather := modelDrivenGathers < maxModelDrivenMemoryGathers
		instructions := RespondInstructions
		if allowGather {
			instructions = RespondGatherInstructions(webSearchEnabled)
		}
		slots, err := BuildRespondContextSlots(characterRecord, userProfile, bootstrap.PromptWindow, bootstrap.Messages, request.AvailableVisualStates, retrieval)
		if err != nil {
			return fail("PROMPT_BUILD_FAILED", err)
		}
		if retrievalOmitReason != "" {
			setContextSlotOmitReason(slots, "retrieved_context", retrievalOmitReason)
		}
		input := PromptItemsFromContextSlots(slots)
		s.appendRuntimeLedger(request.ConversationID, persisted.ID, runtimeLedgerEventPrompt, TurnStatePlanning, "", runtimePromptLedgerMetadata(input, slots, bootstrap.PromptWindow, bootstrap.Messages, request.AvailableVisualStates, retrieval))
		fullRequest = model.CompiledPromptRequest{
			Shape: model.ModelRequestShape{
				Lane:            model.PromptLaneRespond,
				Model:           connectionConfig.Model,
				Instructions:    instructions,
				MaxOutputTokens: request.MaxOutputTokens,
				PromptCacheKey:  cacheKey,
			},
			Input: input,
		}
		var executeRequest model.CompiledPromptRequest
		if modelDrivenGathers > 0 {
			// Retrieval changed mid-turn; never continue from a gather JSON response.
			var matErr error
			executeRequest, matErr = model.MaterializeContinuationRequest(fullRequest, model.ContinuationDecision{})
			if matErr != nil {
				return fail("MODEL_FAILED", matErr)
			}
			s.appendRuntimeLedger(request.ConversationID, persisted.ID, runtimeLedgerEventContinuation, TurnStatePlanning, "", map[string]any{
				"incremental":         false,
				"fullReason":          "cognition_post_gather",
				"modelDrivenGathers":  modelDrivenGathers,
				"previousStateSource": "none",
			})
		} else {
			decision, previous, contErr := s.decideContinuation(request.ConversationID, connectionConfig.Capabilities.CacheRetention, bootstrap.PromptWindow.Revision, fullRequest)
			if contErr != nil {
				return fail("MODEL_FAILED", contErr)
			}
			executeRequest, err = model.MaterializeContinuationRequest(fullRequest, decision)
			if err != nil {
				return fail("MODEL_FAILED", err)
			}
			s.appendRuntimeLedger(request.ConversationID, persisted.ID, runtimeLedgerEventContinuation, TurnStatePlanning, "", runtimeContinuationLedgerMetadata(connectionConfig.Capabilities.CacheRetention, previous, fullRequest, executeRequest, decision))
		}
		if err := turnCtx.Err(); err != nil {
			return fail("TURN_INTERRUPTED", ErrTurnInterrupted)
		}
		events, err = s.modelService.ExecuteRequestContext(turnCtx, executeRequest)
		if err != nil {
			err = mapModelCancelError(err)
			code := "MODEL_FAILED"
			if errors.Is(err, ErrTurnInterrupted) {
				code = "TURN_INTERRUPTED"
			}
			return fail(code, err)
		}
		if err := turnCtx.Err(); err != nil {
			return fail("TURN_INTERRUPTED", ErrTurnInterrupted)
		}
		usage := laneUsageFromEvents(events, bootstrap.PromptWindow.Revision)
		finalUsage = usage
		s.appendRuntimeLedger(request.ConversationID, persisted.ID, runtimeLedgerEventModel, TurnStatePlanning, "", runtimeModelLedgerMetadata(events, usage))
		draft := collectText(events)
		kind, gatherReq, parseErr := ParseCognitionOutput(draft)
		if parseErr != nil {
			return fail("MODEL_RESPONSE_INVALID", parseErr)
		}
		if kind == CognitionGather {
			if !allowGather {
				return fail("MODEL_RESPONSE_INVALID", errors.New("gather budget exhausted"))
			}
			switch gatherReq.Tool {
			case gatherToolMemorySearch:
				extra, gatherErr := s.memoryStore.Retrieve(bootstrap.Conversation.CharacterID, gatherReq.Query)
				if gatherErr != nil {
					return fail("MODEL_RESPONSE_INVALID", gatherErr)
				}
				retrieval = mergeRetrievalContext(retrieval, extra)
				retrievalOmitReason = ""
				modelDrivenGathers++
				s.appendRuntimeLedger(request.ConversationID, persisted.ID, runtimeLedgerEventGather, TurnStatePlanning, "", map[string]any{
					"tool":             gatherReq.Tool,
					"phase":            "model_driven",
					"queryHash":        runtimeHash(gatherReq.Query),
					"personalCount":    len(extra.PersonalMemories),
					"knowledgeCount":   len(extra.Knowledge),
					"mergedPersonal":   len(retrieval.PersonalMemories),
					"mergedKnowledge":  len(retrieval.Knowledge),
					"modelDrivenIndex": modelDrivenGathers,
				})
			case gatherToolWebSearch:
				if !webSearchEnabled {
					return fail("MODEL_RESPONSE_INVALID", errors.New("web search is disabled"))
				}
				if s.webSearch == nil {
					return fail("MODEL_RESPONSE_INVALID", search.ErrBinaryNotFound)
				}
				hits, gatherErr := s.webSearch.Search(turnCtx, gatherReq.Query, 5)
				if gatherErr != nil {
					return fail("MODEL_RESPONSE_INVALID", gatherErr)
				}
				extra := retrievalFromWebHits(hits)
				retrieval = mergeRetrievalContext(retrieval, extra)
				retrievalOmitReason = ""
				modelDrivenGathers++
				s.appendRuntimeLedger(request.ConversationID, persisted.ID, runtimeLedgerEventGather, TurnStatePlanning, "", map[string]any{
					"tool":             gatherReq.Tool,
					"phase":            "model_driven",
					"queryHash":        runtimeHash(gatherReq.Query),
					"webHitCount":      len(hits),
					"mergedKnowledge":  len(retrieval.Knowledge),
					"modelDrivenIndex": modelDrivenGathers,
				})
			default:
				return fail("MODEL_RESPONSE_INVALID", errors.New("gather tool is not whitelisted"))
			}
			continue
		}
		reply, err = CompileReply(draft, request.AvailableVisualStates)
		if err != nil {
			return fail("MODEL_RESPONSE_INVALID", err)
		}
		s.appendRuntimeLedger(request.ConversationID, persisted.ID, runtimeLedgerEventCompile, TurnStatePlanning, "", map[string]any{
			"status":               "succeeded",
			"visualState":          reply.VisualState,
			"chainCount":           len(reply.Chains),
			"displayTextHash":      runtimeHash(reply.DisplayText),
			"speechTextHash":       runtimeHash(reply.SpeechText),
			"modelDrivenGathers":   modelDrivenGathers,
		})
		break
	}
	if err := transition(TurnStateResponding); err != nil {
		return fail("INVALID_STATE_TRANSITION", err)
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
	completed, err := life.Complete(TurnCompletion{
		Text:                reply.DisplayText,
		SpeechText:          reply.SpeechText,
		CharacterRevision:   characterRecord.Revision,
		UserProfileRevision: profileRevision,
		Usage:               finalUsage,
		VisualState:         reply.VisualState,
		Chains:              reply.Chains,
	})
	if err != nil {
		return fail("INVALID_STATE_TRANSITION", err)
	}
	s.emitEvent(completed)
	s.appendRuntimeLedger(request.ConversationID, persisted.ID, runtimeLedgerEventTerminal, TurnStateCompleted, "", runtimeTerminalLedgerMetadata("completed", reply, finalUsage))
	contextWindow, err := s.recordObservedContextWindow(request.ConversationID, bootstrap.PromptWindow.Revision, finalUsage)
	if err != nil {
		s.appendRuntimeLedger(request.ConversationID, persisted.ID, runtimeLedgerEventContextWindow, TurnStateCompleted, "CONTEXT_WINDOW_STATE_FAILED", runtimeFailureLedgerMetadata("CONTEXT_WINDOW_STATE_FAILED", err, false))
	} else {
		s.appendRuntimeLedger(request.ConversationID, persisted.ID, runtimeLedgerEventContextWindow, TurnStateCompleted, "", runtimeContextWindowLedgerMetadata(contextWindow))
	}
	// Persist the successful reply request shape (AllowGather or reply-only after gather).
	if err := s.updateContinuationState(request.ConversationID, connectionConfig.Capabilities.CacheRetention, bootstrap.PromptWindow.Revision, fullRequest, reply.DisplayText, events); err != nil {
		s.appendRuntimeLedger(request.ConversationID, persisted.ID, runtimeLedgerEventContinuation, TurnStateCompleted, "CONTINUATION_STATE_FAILED", runtimeFailureLedgerMetadata("CONTINUATION_STATE_FAILED", err, false))
	}
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
	var cachedInputTokens *uint64
	var cacheWriteTokens *uint64
	known := false
	for _, event := range events {
		if event.Type == "usage" && event.Usage != nil {
			promptTokens = event.Usage.PromptTokens
			completionTokens = event.Usage.CompletionTokens
			cachedInputTokens = event.Usage.CachedInputTokens
			cacheWriteTokens = event.Usage.CacheWriteTokens
			known = true
		}
	}
	if !known {
		return []LaneModelUsage{}
	}
	input := uint64(promptTokens)
	output := uint64(completionTokens)
	return []LaneModelUsage{{
		Lane:          string(model.PromptLaneRespond),
		HistoryWindow: historyWindow,
		Usage: LaneUsage{
			InputTokens:       &input,
			OutputTokens:      &output,
			CachedInputTokens: cacheObservationFromProvider(cachedInputTokens),
			CacheWriteTokens:  cacheObservationFromProvider(cacheWriteTokens),
		},
	}}
}

func cacheObservationFromProvider(tokens *uint64) CachedTokenObservation {
	if tokens == nil {
		return CacheMissing()
	}
	return CacheObserved(*tokens)
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
	states, err := s.availableVisualStatesForConversation(conversationID)
	if err != nil {
		return memory.CompactionResult{}, err
	}
	input, err := BuildCompactInput(characterRecord, userProfile, bootstrap.PromptWindow, bootstrap.Messages, states)
	if err != nil {
		return memory.CompactionResult{}, err
	}
	cacheKey := ""
	connectionConfig, err := config.ReadModelConnection(s.root)
	if err != nil {
		return memory.CompactionResult{}, err
	}
	if connectionConfig.Capabilities.PromptCacheKey {
		cacheKey = model.LaneCacheKey(conversationID, model.PromptLaneRespond)
	}
	events, err := s.modelService.ExecutePrompt(
		model.PromptLaneCompact,
		CompactInstructions,
		CompactMaxOutputTokens,
		input,
		cacheKey,
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
	if _, err := s.advanceContextWindowAfterCompaction(conversationID, result.WindowRevision); err != nil {
		return memory.CompactionResult{}, err
	}
	if err := s.clearContinuationState(conversationID); err != nil {
		return memory.CompactionResult{}, err
	}
	return result, nil
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
