package companion

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"unicode/utf8"

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
	// Compatibility pulse only: no automatic memory retrieve. Evidence comes from tool calls.
	retrieval := memory.RetrievalContext{}
	retrievalOmitReason := "awaiting_tools"
	s.appendRuntimeLedger(request.ConversationID, persisted.ID, runtimeLedgerEventGather, TurnStateGathering, "", map[string]any{
		"phase":            "skip_auto_retrieve",
		"personalCount":    0,
		"knowledgeCount":   0,
		"omitReason":       retrievalOmitReason,
		"modelDrivenIndex": 0,
	})
	log.Printf("cognition loop turn=%s phase=skip_auto_retrieve", persisted.ID)
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
		reply             CompiledReply
		events            []model.StreamEvent
		fullRequest       model.CompiledPromptRequest
		modelDrivenTools  int
		finalUsage        []LaneModelUsage
	)
	webSearchEnabled := false
	if settings, err := config.ReadWebSearchSettings(s.root); err == nil {
		webSearchEnabled = settings.Enabled
	}
	for {
		allowTools := modelDrivenTools < maxModelDrivenToolCalls
		tools := []model.ToolSpec(nil)
		if allowTools {
			tools = RespondToolSpecs(webSearchEnabled)
		}
		instructions := RespondInstructionsForTools(allowTools)
		attempt := modelDrivenTools + 1
		slots, err := BuildRespondContextSlots(characterRecord, userProfile, bootstrap.PromptWindow, bootstrap.Messages, request.AvailableVisualStates, retrieval)
		if err != nil {
			return fail("PROMPT_BUILD_FAILED", err)
		}
		if retrievalOmitReason != "" && retrieval.Empty() {
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
			Tools: tools,
		}
		var executeRequest model.CompiledPromptRequest
		continuationMode := "decide"
		if modelDrivenTools > 0 {
			// Retrieval changed mid-turn after tools; always full request.
			var matErr error
			executeRequest, matErr = model.MaterializeContinuationRequest(fullRequest, model.ContinuationDecision{})
			if matErr != nil {
				return fail("MODEL_FAILED", matErr)
			}
			continuationMode = "full_post_tool"
			s.appendRuntimeLedger(request.ConversationID, persisted.ID, runtimeLedgerEventContinuation, TurnStatePlanning, "", map[string]any{
				"incremental":        false,
				"fullReason":         "cognition_post_tool",
				"modelDrivenTools":   modelDrivenTools,
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
			if decision.Incremental {
				continuationMode = "incremental"
			} else {
				continuationMode = "full:" + string(decision.FullReason)
			}
			s.appendRuntimeLedger(request.ConversationID, persisted.ID, runtimeLedgerEventContinuation, TurnStatePlanning, "", runtimeContinuationLedgerMetadata(connectionConfig.Capabilities.CacheRetention, previous, fullRequest, executeRequest, decision))
		}
		log.Printf(
			"cognition loop turn=%s phase=model_call attempt=%d allowTools=%v webSearch=%v toolCount=%d continuation=%s inputItems=%d personal=%d knowledge=%d",
			persisted.ID,
			attempt,
			allowTools,
			webSearchEnabled,
			len(tools),
			continuationMode,
			len(input),
			len(retrieval.PersonalMemories),
			len(retrieval.Knowledge),
		)
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
			log.Printf("cognition loop turn=%s phase=model_call_failed attempt=%d code=%s err=%v", persisted.ID, attempt, code, err)
			return fail(code, err)
		}
		if err := turnCtx.Err(); err != nil {
			return fail("TURN_INTERRUPTED", ErrTurnInterrupted)
		}
		usage := laneUsageFromEvents(events, bootstrap.PromptWindow.Revision)
		finalUsage = usage
		s.appendRuntimeLedger(request.ConversationID, persisted.ID, runtimeLedgerEventModel, TurnStatePlanning, "", runtimeModelLedgerMetadata(events, usage))

		toolCalls := model.FunctionCallsFromEvents(events)
		draft := collectText(events)
		if strings.Contains(draft, `"gather"`) && len(toolCalls) == 0 {
			log.Printf("cognition loop turn=%s phase=gather_json_rejected attempt=%d", persisted.ID, attempt)
			return fail("MODEL_RESPONSE_INVALID", errors.New("gather JSON is not supported; use function tools"))
		}
		if len(toolCalls) > 0 {
			if !allowTools {
				log.Printf("cognition loop turn=%s phase=tool_rejected reason=budget_exhausted count=%d", persisted.ID, len(toolCalls))
				return fail("MODEL_RESPONSE_INVALID", errors.New("tool budget exhausted"))
			}
			for _, call := range toolCalls {
				if modelDrivenTools >= maxModelDrivenToolCalls {
					log.Printf("cognition loop turn=%s phase=tool_rejected reason=budget_exhausted tool=%s", persisted.ID, call.Name)
					return fail("MODEL_RESPONSE_INVALID", errors.New("tool budget exhausted"))
				}
				query, queryErr := parseToolQuery(call.Arguments)
				if queryErr != nil {
					log.Printf("cognition loop turn=%s phase=tool_args_invalid tool=%s err=%v", persisted.ID, call.Name, queryErr)
					retrieval = mergeRetrievalContext(retrieval, retrievalFromToolError(call.Name, queryErr))
					retrievalOmitReason = ""
					modelDrivenTools++
					s.appendRuntimeLedger(request.ConversationID, persisted.ID, runtimeLedgerEventTool, TurnStatePlanning, "", map[string]any{
						"tool":             call.Name,
						"phase":            "model_driven",
						"status":           "args_invalid",
						"modelDrivenIndex": modelDrivenTools,
					})
					continue
				}
				log.Printf(
					"cognition loop turn=%s phase=tool_call tool=%s callId=%s queryRunes=%d queryHash=%s",
					persisted.ID,
					call.Name,
					call.CallID,
					utf8.RuneCountInString(query),
					runtimeHash(query),
				)
				switch call.Name {
				case toolMemorySearch:
					extra, toolErr := s.memoryStore.Retrieve(bootstrap.Conversation.CharacterID, query)
					if toolErr != nil {
						log.Printf("cognition loop turn=%s phase=tool_failed tool=%s err=%v", persisted.ID, call.Name, toolErr)
						retrieval = mergeRetrievalContext(retrieval, retrievalFromToolError(call.Name, toolErr))
						s.appendRuntimeLedger(request.ConversationID, persisted.ID, runtimeLedgerEventTool, TurnStatePlanning, "", map[string]any{
							"tool":             call.Name,
							"phase":            "model_driven",
							"status":           "failed",
							"queryHash":        runtimeHash(query),
							"modelDrivenIndex": modelDrivenTools + 1,
						})
					} else {
						retrieval = mergeRetrievalContext(retrieval, extra)
						s.appendRuntimeLedger(request.ConversationID, persisted.ID, runtimeLedgerEventTool, TurnStatePlanning, "", map[string]any{
							"tool":             call.Name,
							"phase":            "model_driven",
							"status":           "ok",
							"queryHash":        runtimeHash(query),
							"personalCount":    len(extra.PersonalMemories),
							"knowledgeCount":   len(extra.Knowledge),
							"mergedPersonal":   len(retrieval.PersonalMemories),
							"mergedKnowledge":  len(retrieval.Knowledge),
							"modelDrivenIndex": modelDrivenTools + 1,
						})
						log.Printf(
							"cognition loop turn=%s phase=tool_done tool=%s personal+=%d knowledge+=%d mergedPersonal=%d mergedKnowledge=%d index=%d",
							persisted.ID,
							call.Name,
							len(extra.PersonalMemories),
							len(extra.Knowledge),
							len(retrieval.PersonalMemories),
							len(retrieval.Knowledge),
							modelDrivenTools+1,
						)
					}
				case toolWebSearch:
					if !webSearchEnabled {
						toolErr := errors.New("web search is disabled")
						log.Printf("cognition loop turn=%s phase=tool_rejected tool=%s reason=disabled", persisted.ID, call.Name)
						retrieval = mergeRetrievalContext(retrieval, retrievalFromToolError(call.Name, toolErr))
						s.appendRuntimeLedger(request.ConversationID, persisted.ID, runtimeLedgerEventTool, TurnStatePlanning, "", map[string]any{
							"tool":             call.Name,
							"phase":            "model_driven",
							"status":           "disabled",
							"queryHash":        runtimeHash(query),
							"modelDrivenIndex": modelDrivenTools + 1,
						})
					} else if s.webSearch == nil {
						log.Printf("cognition loop turn=%s phase=tool_rejected tool=%s reason=binary_missing", persisted.ID, call.Name)
						retrieval = mergeRetrievalContext(retrieval, retrievalFromToolError(call.Name, search.ErrBinaryNotFound))
						s.appendRuntimeLedger(request.ConversationID, persisted.ID, runtimeLedgerEventTool, TurnStatePlanning, "", map[string]any{
							"tool":             call.Name,
							"phase":            "model_driven",
							"status":           "binary_missing",
							"queryHash":        runtimeHash(query),
							"modelDrivenIndex": modelDrivenTools + 1,
						})
					} else {
						hits, toolErr := s.webSearch.Search(turnCtx, query, 5)
						if toolErr != nil {
							log.Printf("cognition loop turn=%s phase=tool_failed tool=%s err=%v", persisted.ID, call.Name, toolErr)
							retrieval = mergeRetrievalContext(retrieval, retrievalFromToolError(call.Name, toolErr))
							s.appendRuntimeLedger(request.ConversationID, persisted.ID, runtimeLedgerEventTool, TurnStatePlanning, "", map[string]any{
								"tool":             call.Name,
								"phase":            "model_driven",
								"status":           "failed",
								"queryHash":        runtimeHash(query),
								"modelDrivenIndex": modelDrivenTools + 1,
							})
						} else {
							extra := retrievalFromWebHits(hits)
							retrieval = mergeRetrievalContext(retrieval, extra)
							s.appendRuntimeLedger(request.ConversationID, persisted.ID, runtimeLedgerEventTool, TurnStatePlanning, "", map[string]any{
								"tool":             call.Name,
								"phase":            "model_driven",
								"status":           "ok",
								"queryHash":        runtimeHash(query),
								"webHitCount":      len(hits),
								"mergedKnowledge":  len(retrieval.Knowledge),
								"modelDrivenIndex": modelDrivenTools + 1,
							})
							log.Printf(
								"cognition loop turn=%s phase=tool_done tool=%s webHits=%d mergedKnowledge=%d index=%d",
								persisted.ID,
								call.Name,
								len(hits),
								len(retrieval.Knowledge),
								modelDrivenTools+1,
							)
						}
					}
				default:
					toolErr := fmt.Errorf("tool %q is not whitelisted", call.Name)
					log.Printf("cognition loop turn=%s phase=tool_rejected tool=%s reason=not_whitelisted", persisted.ID, call.Name)
					retrieval = mergeRetrievalContext(retrieval, retrievalFromToolError(call.Name, toolErr))
					s.appendRuntimeLedger(request.ConversationID, persisted.ID, runtimeLedgerEventTool, TurnStatePlanning, "", map[string]any{
						"tool":             call.Name,
						"phase":            "model_driven",
						"status":           "not_whitelisted",
						"modelDrivenIndex": modelDrivenTools + 1,
					})
				}
				retrievalOmitReason = ""
				modelDrivenTools++
			}
			continue
		}
		reply, err = CompileReply(draft, request.AvailableVisualStates)
		if err != nil {
			log.Printf("cognition loop turn=%s phase=compile_failed attempt=%d err=%v", persisted.ID, attempt, err)
			return fail("MODEL_RESPONSE_INVALID", err)
		}
		s.appendRuntimeLedger(request.ConversationID, persisted.ID, runtimeLedgerEventCompile, TurnStatePlanning, "", map[string]any{
			"status":             "succeeded",
			"visualState":        reply.VisualState,
			"chainCount":         len(reply.Chains),
			"displayTextHash":    runtimeHash(reply.DisplayText),
			"speechTextHash":     runtimeHash(reply.SpeechText),
			"modelDrivenTools":   modelDrivenTools,
		})
		log.Printf(
			"cognition loop turn=%s phase=reply_ready chains=%d visual=%q modelDrivenTools=%d",
			persisted.ID,
			len(reply.Chains),
			reply.VisualState,
			modelDrivenTools,
		)
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
