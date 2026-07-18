package companion

import (
	"context"
	"errors"
	"fmt"
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

	"go.uber.org/zap"
)

type CompanionService struct {
	root              string
	memoryStore       *memory.Store
	modelService      *model.ModelService
	webSearch         WebSearchBackend
	speech            SpeechSynthesizer
	characters        *character.Store
	profiles          *profile.Store
	cfg               *config.Reader
	logger            *zap.Logger
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
		logger:         zap.NewNop(),
		extractionIdle: make(map[string]context.CancelFunc),
		gates:          make(map[string]*conversationGate),
	}
}

// NewCompanionServiceWithRuntime wires the companion runtime. webSearch is owned
// by the composition root (main); pass nil when the sidecar is unavailable.
func NewCompanionServiceWithRuntime(root string, memoryStore *memory.Store, modelService *model.ModelService, webSearch WebSearchBackend) *CompanionService {
	return &CompanionService{
		root:           root,
		memoryStore:    memoryStore,
		modelService:   modelService,
		webSearch:      webSearch,
		logger:         zap.NewNop(),
		extractionIdle: make(map[string]context.CancelFunc),
		gates:          make(map[string]*conversationGate),
	}
}

// AttachLogger injects the process logger (dependency injection, no global).
func AttachLogger(s *CompanionService, logger *zap.Logger) {
	if s == nil || logger == nil {
		return
	}
	s.logger = logger
}

// AttachCharacterStore injects the character catalog store from main.
func AttachCharacterStore(s *CompanionService, store *character.Store) {
	if s == nil || store == nil {
		return
	}
	s.characters = store
}

// AttachProfileStore injects the user-profile store from main.
func AttachProfileStore(s *CompanionService, store *profile.Store) {
	if s == nil || store == nil {
		return
	}
	s.profiles = store
}

// AttachConfigReader injects the durable config reader from main.
func AttachConfigReader(s *CompanionService, reader *config.Reader) {
	if s == nil || reader == nil {
		return
	}
	s.cfg = reader
}

// AttachSpeechSynthesizer injects the optional speech backend from main.
func AttachSpeechSynthesizer(s *CompanionService, synthesizer SpeechSynthesizer) {
	if s == nil || synthesizer == nil {
		return
	}
	s.speech = synthesizer
}

func (s *CompanionService) characterStore() *character.Store {
	if s != nil && s.characters != nil {
		return s.characters
	}
	if s == nil {
		return character.NewStore("")
	}
	return character.NewStore(s.root)
}

func (s *CompanionService) profileStore() *profile.Store {
	if s != nil && s.profiles != nil {
		return s.profiles
	}
	if s == nil {
		return profile.NewStore("")
	}
	return profile.NewStore(s.root)
}

func (s *CompanionService) configReader() *config.Reader {
	if s != nil && s.cfg != nil {
		return s.cfg
	}
	if s == nil {
		return config.NewReader("")
	}
	return config.NewReader(s.root)
}

// Close performs graceful shutdown cleanup: it cancels in-flight turns and
// pending background extraction timers, then stops the openserp sidecar. It is
// safe to call multiple times and safe when no model runtime is attached.
func (s *CompanionService) Close() error {
	if s == nil {
		return nil
	}
	s.cancelActiveTurns()
	s.cancelExtractionTimers()
	if s.webSearch == nil {
		return nil
	}
	return s.webSearch.Close()
}

func (s *CompanionService) cancelActiveTurns() {
	s.gateMu.Lock()
	gates := make([]*conversationGate, 0, len(s.gates))
	for _, gate := range s.gates {
		gates = append(gates, gate)
	}
	s.gateMu.Unlock()
	for _, gate := range gates {
		gate.mu.Lock()
		if gate.activeTurn != nil {
			gate.activeTurn.cancel()
			gate.activeTurn = nil
		}
		gate.mu.Unlock()
	}
}

func (s *CompanionService) cancelExtractionTimers() {
	s.extractionMu.Lock()
	timers := s.extractionIdle
	s.extractionIdle = make(map[string]context.CancelFunc)
	s.extractionMu.Unlock()
	for _, cancel := range timers {
		if cancel != nil {
			cancel()
		}
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

	lg := s.logger.With(zap.String("turn", persisted.ID))
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
	userProfile, err := s.profileStore().Current()
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
	lg.Info("cognition loop", zap.String("phase", "skip_auto_retrieve"))
	if err := transition(TurnStatePlanning); err != nil {
		return fail("INVALID_STATE_TRANSITION", err)
	}
	connectionConfig, err := s.configReader().ModelConnection()
	if err != nil {
		return fail("MODEL_FAILED", err)
	}
	cacheKey := ""
	if connectionConfig.Capabilities.PromptCacheKey {
		cacheKey = model.LaneCacheKey(request.ConversationID, model.PromptLaneRespond)
	}

	var (
		reply            CompiledReply
		events           []model.StreamEvent
		fullRequest      model.CompiledPromptRequest
		modelDrivenTools int
		finalUsage       []LaneModelUsage
	)
	webSearchEnabled := false
	if settings, err := s.configReader().WebSearchSettings(); err == nil {
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
				"incremental":         false,
				"fullReason":          "cognition_post_tool",
				"modelDrivenTools":    modelDrivenTools,
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
		lg.Info("cognition loop",
			zap.String("phase", "model_call"),
			zap.Int("attempt", attempt),
			zap.Bool("allowTools", allowTools),
			zap.Bool("webSearch", webSearchEnabled),
			zap.Int("toolCount", len(tools)),
			zap.String("continuation", continuationMode),
			zap.Int("inputItems", len(input)),
			zap.Int("personal", len(retrieval.PersonalMemories)),
			zap.Int("knowledge", len(retrieval.Knowledge)),
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
			lg.Error("cognition loop", zap.String("phase", "model_call_failed"), zap.Int("attempt", attempt), zap.String("code", code), zap.Error(err))
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
			lg.Warn("cognition loop", zap.String("phase", "gather_json_rejected"), zap.Int("attempt", attempt))
			return fail("MODEL_RESPONSE_INVALID", errors.New("gather JSON is not supported; use function tools"))
		}
		if len(toolCalls) > 0 {
			if !allowTools {
				lg.Warn("cognition loop", zap.String("phase", "tool_rejected"), zap.String("reason", "budget_exhausted"), zap.Int("count", len(toolCalls)))
				return fail("MODEL_RESPONSE_INVALID", errors.New("tool budget exhausted"))
			}
			for _, call := range toolCalls {
				if modelDrivenTools >= maxModelDrivenToolCalls {
					lg.Warn("cognition loop", zap.String("phase", "tool_rejected"), zap.String("reason", "budget_exhausted"), zap.String("tool", call.Name))
					return fail("MODEL_RESPONSE_INVALID", errors.New("tool budget exhausted"))
				}
				query, queryErr := parseToolQuery(call.Arguments)
				if queryErr != nil {
					lg.Warn("cognition loop", zap.String("phase", "tool_args_invalid"), zap.String("tool", call.Name), zap.Error(queryErr))
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
				lg.Info("cognition loop",
					zap.String("phase", "tool_call"),
					zap.String("tool", call.Name),
					zap.String("callId", call.CallID),
					zap.Int("queryRunes", utf8.RuneCountInString(query)),
					zap.String("queryHash", runtimeHash(query)),
				)
				switch call.Name {
				case toolMemorySearch:
					extra, toolErr := s.memoryStore.Retrieve(bootstrap.Conversation.CharacterID, query)
					if toolErr != nil {
						lg.Warn("cognition loop", zap.String("phase", "tool_failed"), zap.String("tool", call.Name), zap.Error(toolErr))
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
						lg.Info("cognition loop",
							zap.String("phase", "tool_done"),
							zap.String("tool", call.Name),
							zap.Int("personalAdded", len(extra.PersonalMemories)),
							zap.Int("knowledgeAdded", len(extra.Knowledge)),
							zap.Int("mergedPersonal", len(retrieval.PersonalMemories)),
							zap.Int("mergedKnowledge", len(retrieval.Knowledge)),
							zap.Int("index", modelDrivenTools+1),
						)
					}
				case toolWebSearch:
					if !webSearchEnabled {
						toolErr := errors.New("web search is disabled")
						lg.Warn("cognition loop", zap.String("phase", "tool_rejected"), zap.String("tool", call.Name), zap.String("reason", "disabled"))
						retrieval = mergeRetrievalContext(retrieval, retrievalFromToolError(call.Name, toolErr))
						s.appendRuntimeLedger(request.ConversationID, persisted.ID, runtimeLedgerEventTool, TurnStatePlanning, "", map[string]any{
							"tool":             call.Name,
							"phase":            "model_driven",
							"status":           "disabled",
							"queryHash":        runtimeHash(query),
							"modelDrivenIndex": modelDrivenTools + 1,
						})
					} else if s.webSearch == nil {
						lg.Warn("cognition loop", zap.String("phase", "tool_rejected"), zap.String("tool", call.Name), zap.String("reason", "binary_missing"))
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
							lg.Warn("cognition loop", zap.String("phase", "tool_failed"), zap.String("tool", call.Name), zap.Error(toolErr))
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
							lg.Info("cognition loop",
								zap.String("phase", "tool_done"),
								zap.String("tool", call.Name),
								zap.Int("webHits", len(hits)),
								zap.Int("mergedKnowledge", len(retrieval.Knowledge)),
								zap.Int("index", modelDrivenTools+1),
							)
						}
					}
				default:
					toolErr := fmt.Errorf("tool %q is not whitelisted", call.Name)
					lg.Warn("cognition loop", zap.String("phase", "tool_rejected"), zap.String("tool", call.Name), zap.String("reason", "not_whitelisted"))
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
			lg.Error("cognition loop",
				zap.String("phase", "compile_failed"),
				zap.Int("attempt", attempt),
				zap.Int("draftRunes", utf8.RuneCountInString(draft)),
				zap.Bool("draftHasSpeechTextKey", strings.Contains(draft, `"speechText"`)),
				zap.Error(err),
			)
			return fail("MODEL_RESPONSE_INVALID", err)
		}
		filled, skipReason, fillErr := s.fillSpeechForTTS(turnCtx, lg, reply, characterRecord, request.SpeechEnabled, request.ConversationID, connectionConfig.Model)
		if fillErr != nil {
			lg.Warn("cognition loop", zap.String("phase", "speech_translate_skip"), zap.String("reason", skipReason), zap.Error(fillErr))
			s.appendRuntimeLedger(request.ConversationID, persisted.ID, runtimeLedgerEventSpeech, TurnStatePlanning, "TTS_SKIPPED", map[string]any{"status": "skipped", "reason": skipReason})
		} else if skipReason != "" {
			lg.Info("cognition loop", zap.String("phase", "speech_translate_skip"), zap.String("reason", skipReason))
		} else {
			reply = filled
		}
		s.appendRuntimeLedger(request.ConversationID, persisted.ID, runtimeLedgerEventCompile, TurnStatePlanning, "", map[string]any{
			"status":           "succeeded",
			"visualState":      reply.VisualState,
			"chainCount":       len(reply.Chains),
			"displayTextHash":  runtimeHash(reply.DisplayText),
			"speechTextHash":   runtimeHash(reply.SpeechText),
			"modelDrivenTools": modelDrivenTools,
		})
		textLang := characterRecord.TextLanguage
		if textLang == "" {
			textLang = character.DefaultTextLanguage
		}
		speakLang := characterRecord.SpeakingLanguage
		if speakLang == "" {
			speakLang = character.DefaultSpeakingLanguage
		}
		lg.Info("cognition loop",
			zap.String("phase", "reply_ready"),
			zap.Int("chains", len(reply.Chains)),
			zap.String("visual", reply.VisualState),
			zap.Int("modelDrivenTools", modelDrivenTools),
			zap.String("textLanguage", textLang),
			zap.String("speakingLanguage", speakLang),
			zap.String("displayText", reply.DisplayText),
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
	s.handleCompletedSpeech(request, life, persisted.ID, characterRecord, userProfile, reply)
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

func (s *CompanionService) handleCompletedSpeech(request SubmitCompiledTurnRequest, life *TurnLifecycle, turnID string, characterRecord character.Record, userProfile *profile.Snapshot, reply CompiledReply) {
	if !request.SpeechEnabled {
		return
	}
	if strings.TrimSpace(reply.SpeechText) == "" {
		s.appendRuntimeLedger(request.ConversationID, turnID, runtimeLedgerEventSpeech, TurnStateCompleted, "TTS_SKIPPED", map[string]any{"status": "skipped", "reason": "speech_text_missing"})
		s.logger.Info("tts skipped", zap.String("reason", "speech_text_missing"), zap.String("turn", turnID))
		return
	}
	profileRevision := (*uint64)(nil)
	if userProfile != nil {
		value := userProfile.Revision
		profileRevision = &value
	}
	requested, err := life.SpeechRequested(TurnCompletion{
		Text:                reply.DisplayText,
		SpeechText:          reply.SpeechText,
		CharacterRevision:   characterRecord.Revision,
		UserProfileRevision: profileRevision,
	})
	if err == nil {
		s.emitEvent(requested)
	}
	if s.speech == nil {
		s.appendRuntimeLedger(request.ConversationID, turnID, runtimeLedgerEventSpeech, TurnStateCompleted, "TTS_SKIPPED", map[string]any{"status": "skipped", "reason": "speech_synthesizer_unavailable", "speechTextHash": runtimeHash(reply.SpeechText)})
		s.emitSpeechFailure(life, request.ConversationID, turnID, "TTS_SKIPPED", "语音服务未接入", false)
		s.logger.Info("tts skipped", zap.String("reason", "speech_synthesizer_unavailable"), zap.String("turn", turnID))
		return
	}
	result, err := s.speech.SynthesizeSpeech(SpeechSynthesisRequest{Text: reply.SpeechText})
	if err != nil {
		s.appendRuntimeLedger(request.ConversationID, turnID, runtimeLedgerEventSpeech, TurnStateCompleted, "TTS_FAILED", runtimeFailureLedgerMetadata("TTS_FAILED", err, false))
		s.emitSpeechFailure(life, request.ConversationID, turnID, "TTS_FAILED", err.Error(), false)
		s.logger.Warn("tts failed", zap.String("turn", turnID), zap.Error(err))
		return
	}
	s.appendRuntimeLedger(request.ConversationID, turnID, runtimeLedgerEventSpeech, TurnStateCompleted, "", map[string]any{"status": "synthesized", "speakerIDHash": runtimeHash(result.SpeakerID), "mimeType": result.MimeType, "format": result.Format, "speechTextHash": runtimeHash(reply.SpeechText)})
	if event, err := life.SpeechSynthesized(SpeechSynthesisCompletion{Text: reply.SpeechText, Result: result}); err == nil {
		s.emitEvent(event)
	}
	s.logger.Info("tts synthesized", zap.String("turn", turnID), zap.String("mimeType", result.MimeType), zap.String("format", result.Format))
}

func (s *CompanionService) emitSpeechFailure(life *TurnLifecycle, conversationID string, turnID string, code string, message string, retryable bool) {
	event, err := life.SpeechFailed(code, message, retryable)
	if err != nil {
		return
	}
	s.emitEvent(event)
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
	userProfile, err := s.profileStore().Current()
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
	connectionConfig, err := s.configReader().ModelConnection()
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
	catalog, err := s.characterStore().List()
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
