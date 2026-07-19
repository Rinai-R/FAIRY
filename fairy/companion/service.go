package companion

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"fairy/character"
	"fairy/config"
	"fairy/memory"
	"fairy/memory/semantic"
	"fairy/model"
	"fairy/profile"
	"fairy/search"

	"go.uber.org/zap"
)

type CompanionService struct {
	root              string
	memoryStore       *memory.Store
	semanticEmbedder  semantic.Embedder
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

// publishLife allocates the next harness sequence and emits under one lock so
// concurrent utterance TTS cannot deliver duplicated or out-of-order sequences.
func (s *CompanionService) publishLife(life *TurnLifecycle, produce func() (HarnessEvent, error)) (HarnessEvent, error) {
	if life == nil {
		return HarnessEvent{}, errors.New("nil turn lifecycle")
	}
	life.mu.Lock()
	defer life.mu.Unlock()
	event, err := produce()
	if err != nil {
		return HarnessEvent{}, err
	}
	s.emitEvent(event)
	return event, nil
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

// AttachSemanticEmbedder injects the optional local semantic backend used by
// memory_search and bounded embedding job passes. A nil or unavailable embedder
// leaves the runtime on the existing FTS-only memory path.
func AttachSemanticEmbedder(s *CompanionService, embedder semantic.Embedder) {
	if s == nil || embedder == nil {
		return
	}
	s.semanticEmbedder = embedder
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
			if _, err := s.publishLife(life, life.Interrupt); err == nil {
				s.appendRuntimeLedger(request.ConversationID, persisted.ID, runtimeLedgerEventTerminal, TurnStateInterrupted, code, runtimeFailureLedgerMetadata(code, cause, false))
			} else {
				s.appendRuntimeLedger(request.ConversationID, persisted.ID, runtimeLedgerEventTerminal, TurnStateInterrupted, code, runtimeFailureLedgerMetadata(code, cause, false))
			}
		} else if _, err := s.publishLife(life, func() (HarnessEvent, error) {
			return life.Fail(code, cause.Error(), false)
		}); err == nil {
			s.appendRuntimeLedger(request.ConversationID, persisted.ID, runtimeLedgerEventTerminal, TurnStateFailed, code, runtimeFailureLedgerMetadata(code, cause, false))
		} else {
			s.appendRuntimeLedger(request.ConversationID, persisted.ID, runtimeLedgerEventTerminal, TurnStateFailed, code, runtimeFailureLedgerMetadata(code, cause, false))
		}
		return TurnOutcome{}, cause
	}
	transition := func(state TurnState) error {
		if _, err := s.publishLife(life, func() (HarnessEvent, error) {
			return life.Transition(state)
		}); err != nil {
			return err
		}
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
	// Single-consumer TTS pipeline for the whole turn: mid-ReAct utterance audio
	// and reply-chain audio are enqueued in order and synthesized one request per
	// semantic unit (stable timbre). Emission stays serial via publishLife.
	var (
		speechFlow      *speechPipeline
		speechPlayIndex int
		speechRequested bool
		utteranceSeq    int
		ingestSnapshots []memory.KnowledgeIngestSnapshot
	)
	if request.SpeechEnabled && s.speech != nil {
		capacity := maxReplyChains + maxModelDrivenToolCalls + 2
		speechFlow = newSpeechPipeline(turnCtx, s.speech, capacity, func(res speechPipelineResult) {
			s.handleSpeechResult(life, request.ConversationID, persisted.ID, res)
		})
		// Drain (and thus finish emitting speech.synthesized) before turn teardown.
		defer speechFlow.Close()
	}
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
			// Mid-ReAct in-character line: queue a paired beat (text+TTS). Do NOT
			// reveal the line until beat.ready — 齐套才揭示. Enqueue never blocks tools.
			if line := sanitizeUtteranceText(draft); line != "" {
				reason := toolUtteranceReason(toolCalls[0].Name)
				seq := utteranceSeq
				utteranceSeq++
				beatID := fmt.Sprintf("utt-%d", seq)
				lg.Info("cognition loop", zap.String("phase", "utterance_queued"), zap.Int("seq", seq), zap.String("reason", reason))
				if speechFlow == nil {
					if _, uttErr := s.publishLife(life, func() (HarnessEvent, error) {
						return life.BeatReady(BeatReadyCompletion{
							BeatID:      beatID,
							Kind:        beatKindUtterance,
							Index:       uint8(speechPlayIndex),
							ChainIndex:  chainIndexUtterance,
							DisplayText: line,
							SpeechText:  "",
							VisualState: "idle",
							Reason:      reason,
						})
					}); uttErr != nil {
						lg.Warn("cognition loop", zap.String("phase", "beat_skipped"), zap.String("beatId", beatID), zap.Error(uttErr))
					}
					speechPlayIndex++
				} else {
					play := speechPlayIndex
					speechPlayIndex++
					uttLine := line
					s.appendRuntimeLedger(request.ConversationID, persisted.ID, runtimeLedgerEventSpeech, TurnStatePlanning, "", map[string]any{
						"status":    "queued",
						"kind":      "utterance",
						"beatId":    beatID,
						"playIndex": play,
					})
					speechFlow.Enqueue(speechPipelineJob{
						BeatID:      beatID,
						Kind:        beatKindUtterance,
						PlayIndex:   play,
						ChainIndex:  chainIndexUtterance,
						DisplayText: uttLine,
						VisualState: "idle",
						Reason:      reason,
						Resolve: func() (string, error) {
							return s.resolveUtteranceSpeech(turnCtx, lg, characterRecord, uttLine, request.ConversationID, connectionConfig.Model)
						},
					})
				}
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
					extra, toolErr := s.retrieveMemoryForTool(bootstrap.Conversation.CharacterID, query)
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
							"semanticStatus":   extra.SemanticStatus,
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
							nowMS := time.Now().UnixMilli()
							for index, hit := range hits {
								ingestSnapshots = append(ingestSnapshots, memory.KnowledgeIngestSnapshot{
									ConversationID:  request.ConversationID,
									TurnID:          persisted.ID,
									Query:           query,
									Title:           hit.Title,
									URL:             hit.URL,
									Snippet:         hit.Snippet,
									Rank:            uint8(index + 1),
									FetchedAtUnixMS: nowMS,
								})
							}
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
		// chain = semantic unit = one TTS request: never split a chain further.
		s.appendRuntimeLedger(request.ConversationID, persisted.ID, runtimeLedgerEventCompile, TurnStatePlanning, "", map[string]any{
			"status":           "succeeded",
			"visualState":      reply.VisualState,
			"chainCount":       len(reply.Chains),
			"displayTextHash":  runtimeHash(reply.DisplayText),
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
	var profileRevision *uint64
	if userProfile != nil {
		value := userProfile.Revision
		profileRevision = &value
	}
	filledChains := make([]ReplyChain, 0, len(reply.Chains))
	for index, chain := range reply.Chains {
		partial := CompiledReply{
			DisplayText: chain.Text,
			VisualState: chain.VisualState,
			Chains:      []ReplyChain{chain},
		}
		filled, skipReason, fillErr := s.fillSpeechForTTS(turnCtx, lg, partial, characterRecord, request.SpeechEnabled, request.ConversationID, connectionConfig.Model)
		if fillErr != nil {
			lg.Warn("cognition loop", zap.String("phase", "speech_translate_skip"), zap.String("reason", skipReason), zap.Int("chain", index), zap.Error(fillErr))
			s.appendRuntimeLedger(request.ConversationID, persisted.ID, runtimeLedgerEventSpeech, TurnStateResponding, "TTS_SKIPPED", map[string]any{"status": "skipped", "reason": skipReason, "index": index})
			filledChains = append(filledChains, chain)
		} else if skipReason != "" {
			lg.Info("cognition loop", zap.String("phase", "speech_translate_skip"), zap.String("reason", skipReason), zap.Int("chain", index))
			filledChains = append(filledChains, chain)
		} else {
			chain = filled.Chains[0]
			filledChains = append(filledChains, chain)
		}
		delta := chain.Text
		if index > 0 {
			delta = "\n" + chain.Text
		}
		if _, err := s.publishLife(life, func() (HarnessEvent, error) {
			return life.ReplyChain(uint8(index), delta, chain)
		}); err != nil {
			return fail("MODEL_RESPONSE_INVALID", err)
		}
		if !request.SpeechEnabled {
			if _, beatErr := s.publishLife(life, func() (HarnessEvent, error) {
				return life.BeatReady(BeatReadyCompletion{
					BeatID:      fmt.Sprintf("final-%d", index),
					Kind:        beatKindFinal,
					Index:       uint8(speechPlayIndex),
					ChainIndex:  index,
					DisplayText: chain.Text,
					SpeechText:  "",
					VisualState: chain.VisualState,
				})
			}); beatErr != nil {
				lg.Warn("beat.ready skipped", zap.Int("chain", index), zap.Error(beatErr))
			}
			speechPlayIndex++
			continue
		}
		speechText := strings.TrimSpace(chain.SpeechText)
		if speechText == "" {
			if _, beatErr := s.publishLife(life, func() (HarnessEvent, error) {
				return life.BeatReady(BeatReadyCompletion{
					BeatID:      fmt.Sprintf("final-%d", index),
					Kind:        beatKindFinal,
					Index:       uint8(speechPlayIndex),
					ChainIndex:  index,
					DisplayText: chain.Text,
					SpeechText:  "",
					VisualState: chain.VisualState,
				})
			}); beatErr != nil {
				lg.Warn("beat.ready skipped", zap.Int("chain", index), zap.Error(beatErr))
			}
			speechPlayIndex++
			continue
		}
		if speechExceedsSoftLimit(speechText) {
			// Soft warning only: still synthesized as ONE request (stable timbre).
			lg.Warn("cognition loop",
				zap.String("phase", "speech_over_soft_limit"),
				zap.Int("chain", index),
				zap.Int("runes", utf8.RuneCountInString(speechText)),
				zap.Int("softLimit", maxSpeechChars),
			)
		}
		if speechFlow == nil {
			// No synthesizer: deliver text-only beat; never hang the bubble on audio.
			s.appendRuntimeLedger(request.ConversationID, persisted.ID, runtimeLedgerEventSpeech, TurnStateResponding, "TTS_SKIPPED", map[string]any{"status": "skipped", "reason": "speech_synthesizer_unavailable", "index": index})
			if _, beatErr := s.publishLife(life, func() (HarnessEvent, error) {
				return life.BeatReady(BeatReadyCompletion{
					BeatID:      fmt.Sprintf("final-%d", index),
					Kind:        beatKindFinal,
					Index:       uint8(speechPlayIndex),
					ChainIndex:  index,
					DisplayText: chain.Text,
					SpeechText:  speechText,
					VisualState: chain.VisualState,
				})
			}); beatErr != nil {
				lg.Warn("beat.ready skipped", zap.Int("chain", index), zap.Error(beatErr))
			}
			speechPlayIndex++
			continue
		}
		if !speechRequested {
			if _, err := s.publishLife(life, func() (HarnessEvent, error) {
				return life.SpeechRequested(TurnCompletion{
					Text:                chain.Text,
					SpeechText:          speechText,
					CharacterRevision:   characterRecord.Revision,
					UserProfileRevision: profileRevision,
				})
			}); err != nil {
				lg.Warn("tts request skipped", zap.String("turn", persisted.ID), zap.Error(err))
			} else {
				speechRequested = true
			}
		}
		play := speechPlayIndex
		speechPlayIndex++
		chainSpeech := speechText
		beatID := fmt.Sprintf("final-%d", index)
		s.appendRuntimeLedger(request.ConversationID, persisted.ID, runtimeLedgerEventSpeech, TurnStateResponding, "", map[string]any{
			"status":         "queued",
			"index":          index,
			"beatId":         beatID,
			"playIndex":      play,
			"speechTextHash": runtimeHash(chainSpeech),
		})
		chainDisplay := chain.Text
		chainVisual := chain.VisualState
		speechFlow.Enqueue(speechPipelineJob{
			BeatID:      beatID,
			Kind:        beatKindFinal,
			PlayIndex:   play,
			ChainIndex:  index,
			DisplayText: chainDisplay,
			VisualState: chainVisual,
			Resolve:     func() (string, error) { return chainSpeech, nil },
		})
	}
	rebuilt, rebuildErr := compiledReplyFromChains(filledChains)
	if rebuildErr != nil {
		return fail("MODEL_RESPONSE_INVALID", rebuildErr)
	}
	reply = rebuilt
	if _, err := s.memoryStore.CompleteTurn(request.ConversationID, persisted.ID, reply.DisplayText); err != nil {
		return TurnOutcome{}, err
	}
	// Drain all queued TTS before completing so every speech.synthesized is emitted
	// BEFORE completed. The frontend uses completed as "no more audio coming" to
	// decide when the bubble may fade; emitting it before audio arrives would
	// release the hold and flash the bubble away prematurely. Chain audio still
	// streams to the UI as each job finishes during this drain (idempotent Close;
	// the deferred Close remains a safety net for fail paths).
	if speechFlow != nil {
		speechFlow.Close()
	}
	if _, err := s.publishLife(life, func() (HarnessEvent, error) {
		return life.Complete(TurnCompletion{
			Text:                reply.DisplayText,
			SpeechText:          reply.SpeechText,
			CharacterRevision:   characterRecord.Revision,
			UserProfileRevision: profileRevision,
			Usage:               finalUsage,
			VisualState:         reply.VisualState,
			Chains:              reply.Chains,
		})
	}); err != nil {
		return fail("INVALID_STATE_TRANSITION", err)
	}
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
	s.scheduleKnowledgeIngest(ingestSnapshots)
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

func (s *CompanionService) emitSpeechFailure(life *TurnLifecycle, conversationID string, turnID string, code string, message string, retryable bool) {
	_, _ = s.publishLife(life, func() (HarnessEvent, error) {
		return life.SpeechFailed(code, message, retryable)
	})
}

// handleSpeechResult runs on the pipeline worker goroutine. publishLife holds the
// lifecycle lock, so emitting from here stays serialized with the main goroutine
// and preserves monotonic sequence numbers. Every finished job (including skipped
// / failed TTS) delivers beat.ready so the UI never waits forever for a paired beat.
func (s *CompanionService) handleSpeechResult(life *TurnLifecycle, conversationID string, turnID string, res speechPipelineResult) {
	display := strings.TrimSpace(res.DisplayText)
	if display == "" {
		display = strings.TrimSpace(res.Text)
	}
	if display == "" {
		return
	}
	kind := res.Kind
	if kind == "" {
		if res.ChainIndex == chainIndexUtterance {
			kind = beatKindUtterance
		} else {
			kind = beatKindFinal
		}
	}
	beatID := res.BeatID
	if beatID == "" {
		beatID = fmt.Sprintf("play-%d", res.PlayIndex)
	}
	completion := BeatReadyCompletion{
		BeatID:      beatID,
		Kind:        kind,
		Index:       uint8(res.PlayIndex),
		ChainIndex:  res.ChainIndex,
		DisplayText: display,
		SpeechText:  res.Text,
		VisualState: res.VisualState,
		Reason:      res.Reason,
	}
	if res.Err != nil {
		s.logger.Warn("tts failed; delivering text-only beat",
			zap.String("turn", turnID),
			zap.String("beatId", beatID),
			zap.Int("playIndex", res.PlayIndex),
			zap.Error(res.Err),
		)
		s.appendRuntimeLedger(conversationID, turnID, runtimeLedgerEventSpeech, life.State(), "TTS_FAILED", map[string]any{
			"status":    "failed",
			"beatId":    beatID,
			"playIndex": res.PlayIndex,
		})
	} else if !res.Skipped && res.Result.DataURL != "" {
		audio := res.Result
		completion.Audio = &audio
		s.logger.Info("tts synthesized",
			zap.String("turn", turnID),
			zap.String("beatId", beatID),
			zap.Int("playIndex", res.PlayIndex),
			zap.Int("chainIndex", res.ChainIndex),
			zap.String("mimeType", res.Result.MimeType),
		)
	}
	if _, err := s.publishLife(life, func() (HarnessEvent, error) {
		return life.BeatReady(completion)
	}); err != nil {
		s.logger.Warn("beat.ready skipped",
			zap.String("turn", turnID),
			zap.String("beatId", beatID),
			zap.Error(err),
		)
	}
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
