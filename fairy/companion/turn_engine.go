package companion

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"fairy/character"
	"fairy/memory"
	"fairy/model"
	"fairy/profile"
	"fairy/search"

	"go.uber.org/zap"
)

// TurnEngine owns direct/ambient turn submission and cancellation.
type TurnEngine struct {
	host *CompanionService
}

func (e *TurnEngine) SubmitTurn(request SubmitTurnRequest) (TurnOutcome, error) {
	s := e.host
	if err := ValidateSubmitTurnRequest(request); err != nil {
		return TurnOutcome{}, err
	}
	if s == nil || !s.RespondRuntimeMigrated() {
		return TurnOutcome{}, ErrRespondRuntimeNotMigrated
	}
	request.TraceID = s.beginMessageTrace(request.MessageSource, request.ConversationID, request.TraceID)
	if request.MessageSource == "" {
		request.MessageSource = "direct"
	}
	states, err := s.availableVisualStatesForConversation(request.ConversationID)
	if err != nil {
		s.endMessageTrace(request.TraceID, "failed")
		return TurnOutcome{}, err
	}
	return s.SubmitCompiledTurn(SubmitCompiledTurnRequest{
		ConversationID:        request.ConversationID,
		Input:                 request.Input,
		SpeechEnabled:         request.SpeechEnabled,
		MaxOutputTokens:       RespondMaxOutputTokens,
		AvailableVisualStates: states,
		TraceID:               request.TraceID,
		MessageSource:         request.MessageSource,
		ReplyIntent:           request.ReplyIntent,
		RecentTargetReply:     request.RecentTargetReply,
		PersonNoteSenderIDs:   append([]string(nil), request.PersonNoteSenderIDs...),
	})
}

func (e *TurnEngine) SubmitCompiledTurn(request SubmitCompiledTurnRequest) (outcome TurnOutcome, err error) {
	s := e.host
	if err := ValidateSubmitCompiledTurnRequest(request); err != nil {
		return TurnOutcome{}, err
	}
	resolved, err := s.ResolveInteraction(request.ConversationID)
	if err != nil {
		return TurnOutcome{}, err
	}
	if !s.RespondRuntimeMigrated() {
		return TurnOutcome{}, ErrRespondRuntimeNotMigrated
	}
	request.TraceID = s.beginMessageTrace(request.MessageSource, request.ConversationID, request.TraceID)
	defer func() {
		if err != nil {
			s.endMessageTrace(request.TraceID, "failed")
		}
	}()
	if err := s.maybeCompactBeforeTurn(request); err != nil {
		s.setBackgroundError(err)
	}
	turnCtx, err := s.reserveTurn(request.ConversationID)
	if err != nil {
		s.endMessageTrace(request.TraceID, "failed")
		return TurnOutcome{}, err
	}
	persisted, err := s.memory.BeginTurn(request.ConversationID, request.Input)
	if err != nil {
		s.endTurn(request.ConversationID, "")
		s.endMessageTrace(request.TraceID, "failed")
		return TurnOutcome{}, err
	}
	s.bindTurn(request.ConversationID, persisted.ID)
	s.emitMu.Lock()
	telemetry := s.messageTelemetry
	s.emitMu.Unlock()
	if telemetry != nil && request.TraceID != "" {
		telemetry.TurnStarted(request.TraceID, request.ConversationID, persisted.ID)
	}
	defer s.endTurn(request.ConversationID, persisted.ID)
	turnStarted := time.Now()

	lg := s.logger.With(zap.String("turn", persisted.ID))
	life := NewTurnLifecycle(request.ConversationID, persisted.ID)
	var speechFlow *speechPipeline
	var finalDelivery *replyDelivery
	fail := func(code string, cause error) (TurnOutcome, error) {
		if speechFlow != nil {
			speechFlow.Close()
		}
		if errors.Is(cause, ErrTurnInterrupted) {
			var published []ReplyChain
			planned := 0
			if finalDelivery != nil {
				published = finalDelivery.Snapshot()
				planned = finalDelivery.PlannedCount()
			}
			prefix := ""
			if len(published) > 0 {
				reply, err := compiledReplyFromChains(published)
				if err != nil {
					return s.terminalPersistenceFailure(life, request.ConversationID, persisted.ID, cause, err)
				}
				prefix = reply.DisplayText
			}
			if _, err := s.memory.InterruptTurn(request.ConversationID, persisted.ID, prefix); err != nil {
				return s.terminalPersistenceFailure(life, request.ConversationID, persisted.ID, cause, err)
			}
			if _, err := s.publishLife(life, life.Interrupt); err != nil {
				return TurnOutcome{}, errors.Join(cause, err)
			}
			s.appendRuntimeLedger(request.ConversationID, persisted.ID, runtimeLedgerEventTerminal, TurnStateInterrupted, code, runtimeInterruptedTerminalLedgerMetadata(planned, published))
			return TurnOutcome{}, cause
		}
		if err := s.memory.FailTurn(request.ConversationID, persisted.ID, code, cause.Error(), false); err != nil {
			return s.terminalPersistenceFailure(life, request.ConversationID, persisted.ID, cause, err)
		}
		if _, err := s.publishLife(life, func() (TurnEvent, error) {
			return life.Fail(code, cause.Error(), false)
		}); err != nil {
			return TurnOutcome{}, errors.Join(cause, err)
		}
		s.appendRuntimeLedger(request.ConversationID, persisted.ID, runtimeLedgerEventTerminal, TurnStateFailed, code, runtimeFailureLedgerMetadata(code, cause, false))
		return TurnOutcome{}, cause
	}
	transition := func(state TurnState) error {
		if _, err := s.publishLife(life, func() (TurnEvent, error) {
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
	bootstrap, err := s.memory.LoadConversation(request.ConversationID)
	if err != nil {
		return fail("CONVERSATION_FAILED", err)
	}
	characterRecord, err := s.activeCharacter(bootstrap.Conversation.CharacterID)
	if err != nil {
		return fail("CHARACTER_NOT_AVAILABLE", err)
	}
	var userProfile *profile.Snapshot
	if resolved.AllowsPersonalMemory() {
		userProfile, err = s.profileSource().Current()
		if err != nil {
			return fail("USER_PROFILE_UNAVAILABLE", err)
		}
	}
	if err := transition(TurnStateGathering); err != nil {
		return fail("INVALID_STATE_TRANSITION", err)
	}
	socialContext, err := s.retrieveSocialRespondContext(turnCtx, bootstrap.Conversation.CharacterID, request.ConversationID, resolved, request.ReplyIntent, request.PersonNoteSenderIDs)
	if err != nil {
		return fail("SOCIAL_MEMORY_FAILED", err)
	}
	if socialContext != nil {
		socialContext.RecentTargetReply = strings.TrimSpace(request.RecentTargetReply)
		lg.Info("cognition loop", zap.String("phase", "social_memory_retrieved"), zap.String("queryHash", runtimeHash(socialMemoryQuery(*request.ReplyIntent))), zap.Int("count", len(socialContext.Memory.Entries)))
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
	connectionConfig, err := s.configSource().ModelConnection()
	if err != nil {
		return fail("MODEL_FAILED", err)
	}
	cacheKey := ""
	if connectionConfig.Capabilities.PromptCacheKey {
		cacheKey = model.LaneCacheKey(request.ConversationID, model.PromptLaneRespond)
	}

	var (
		reply               CompiledReply
		events              []model.StreamEvent
		fullRequest         model.CompiledPromptRequest
		modelDrivenTools    int
		modelCallAttempts   int
		replyCompileRetries int
		firstCompileErr     error
		retryCorrection     string
		finalUsage          []LaneModelUsage
	)
	// Single-consumer TTS pipeline for the whole turn: mid-ReAct utterance audio
	// and reply-chain audio are enqueued in order and synthesized one request per
	// semantic unit (stable timbre). Emission stays serial via publishLife.
	var (
		speechPlayIndex int
		speechRequested bool
		utteranceSeq    int
		ingestSnapshots []memory.KnowledgeIngestSnapshot
	)
	if request.SpeechEnabled && s.speech != nil {
		capacity := maxReplyChains + modelDrivenToolBudget(resolved) + 2
		speechFlow = newSpeechPipeline(turnCtx, s.speech, capacity, func(res speechPipelineResult) {
			s.handleSpeechResult(life, request.ConversationID, persisted.ID, finalDelivery, res)
		})
		// Drain (and thus finish emitting speech.synthesized) before turn teardown.
		defer speechFlow.Close()
	}
	webSearchEnabled := false
	if settings, err := s.configSource().WebSearchSettings(); err == nil {
		webSearchEnabled = settings.Enabled
	}
	toolBudget := modelDrivenToolBudget(resolved)
	for {
		allowTools := modelDrivenTools < toolBudget
		tools := []model.ToolSpec(nil)
		if allowTools {
			tools = RespondToolSpecsForInteraction(webSearchEnabled, resolved)
		}
		instructions := RespondInstructionsForInteraction(len(tools) > 0, resolved)
		instructions += retryCorrection
		modelCallAttempts++
		attempt := modelCallAttempts
		var slots []ContextSlot
		if socialContext == nil {
			slots, err = BuildRespondContextSlots(characterRecord, userProfile, bootstrap.PromptWindow, bootstrap.Messages, request.AvailableVisualStates, retrieval, resolved)
		} else {
			slots, err = BuildRespondContextSlotsWithSocial(characterRecord, userProfile, bootstrap.PromptWindow, bootstrap.Messages, request.AvailableVisualStates, retrieval, resolved, *socialContext)
		}
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
		events = make([]model.StreamEvent, 0)
		previewAccumulator := newStreamPreviewAccumulator(request.AvailableVisualStates)
		modelCallStarted := time.Now()
		firstByteAt := time.Time{}
		previewAt := time.Time{}
		streamCallback := func(event model.StreamEvent) {
			events = append(events, event)
			if firstByteAt.IsZero() {
				firstByteAt = time.Now()
				if _, presenceErr := s.publishLife(life, func() (TurnEvent, error) {
					return life.Presence("model_stream")
				}); presenceErr != nil {
					lg.Warn("cognition loop", zap.String("phase", "presence_skipped"), zap.Error(presenceErr))
				}
			}
			preview, ready := previewAccumulator.Observe(event)
			if !ready || !allowReplyPreviewForInteraction(resolved) {
				return
			}
			previewAt = time.Now()
			if _, previewErr := s.publishLife(life, func() (TurnEvent, error) {
				return life.ReplyPreview(preview.Chains)
			}); previewErr != nil {
				lg.Warn("cognition loop", zap.String("phase", "preview_skipped"), zap.Error(previewErr))
			}
		}
		if streaming, ok := s.model.(StreamingModelPort); ok {
			err = streaming.ExecuteRequestContextStream(turnCtx, executeRequest, streamCallback)
		} else {
			var collected []model.StreamEvent
			collected, err = s.model.ExecuteRequestContext(turnCtx, executeRequest)
			for _, event := range collected {
				streamCallback(event)
			}
		}
		streamTiming := map[string]any{"phase": "model_stream_timing"}
		if !firstByteAt.IsZero() {
			streamTiming["firstByteMs"] = firstByteAt.Sub(modelCallStarted).Milliseconds()
		}
		if !previewAt.IsZero() {
			streamTiming["previewMs"] = previewAt.Sub(modelCallStarted).Milliseconds()
		}
		streamTiming["completedMs"] = time.Since(modelCallStarted).Milliseconds()
		s.appendRuntimeLedger(request.ConversationID, persisted.ID, runtimeLedgerEventModel, TurnStatePlanning, "", streamTiming)
		if !firstByteAt.IsZero() {
			s.loopMetrics.providerFirstByte(firstByteAt.Sub(modelCallStarted))
		}
		if !previewAt.IsZero() {
			s.loopMetrics.replyPreview(previewAt.Sub(modelCallStarted))
		}
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
			line := sanitizeUtteranceText(draft)
			if line != "" {
				if boundaryErr := validateTextForInteraction(line, resolved); boundaryErr != nil {
					lg.Warn("cognition loop", zap.String("phase", "utterance_omitted"), zap.String("reason", "public_peer_identity"))
					line = ""
				}
			}
			if line != "" {
				reason := toolUtteranceReason(toolCalls[0].Name)
				seq := utteranceSeq
				utteranceSeq++
				beatID := fmt.Sprintf("utt-%d", seq)
				lg.Info("cognition loop", zap.String("phase", "utterance_queued"), zap.Int("seq", seq), zap.String("reason", reason))
				if speechFlow == nil {
					if _, uttErr := s.publishLife(life, func() (TurnEvent, error) {
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
				if modelDrivenTools >= toolBudget {
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
					if !resolved.AllowsPersonalMemory() {
						return fail("MODEL_RESPONSE_INVALID", errors.New("memory_search is unavailable for public interactions"))
					}
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
				case toolPublicMemorySearch:
					if resolved.AllowsPersonalMemory() {
						return fail("MODEL_RESPONSE_INVALID", errors.New("public_memory_search is available only for public interactions"))
					}
					extra, toolErr := s.retrievePublicKnowledgeForTool(turnCtx, query)
					status := "ok"
					if toolErr != nil {
						status = "failed"
						lg.Warn("cognition loop", zap.String("phase", "tool_failed"), zap.String("tool", call.Name), zap.Error(toolErr))
						retrieval = mergeRetrievalContext(retrieval, retrievalFromToolError(call.Name, toolErr))
					} else {
						retrieval = mergeRetrievalContext(retrieval, extra)
					}
					retrievalOmitReason = ""
					modelDrivenTools++
					s.appendRuntimeLedger(request.ConversationID, persisted.ID, runtimeLedgerEventTool, TurnStatePlanning, "", map[string]any{
						"tool":             call.Name,
						"phase":            "model_driven",
						"status":           status,
						"queryHash":        runtimeHash(query),
						"personalCount":    len(extra.PersonalMemories),
						"knowledgeCount":   len(extra.Knowledge),
						"modelDrivenIndex": modelDrivenTools,
					})
				case toolSocialContextSearch:
					if resolved.AllowsPersonalMemory() || !resolved.AllowsAmbientParticipation() {
						return fail("MODEL_RESPONSE_INVALID", errors.New("social_context_search is available only for public ambient interactions"))
					}
					extra, toolErr := s.selectSocialContextForTool(turnCtx, bootstrap.Conversation.CharacterID, request.ConversationID, query)
					status := "ok"
					if toolErr != nil {
						status = "failed"
						lg.Warn("cognition loop", zap.String("phase", "tool_failed"), zap.String("tool", call.Name), zap.Error(toolErr))
						retrieval = mergeRetrievalContext(retrieval, retrievalFromToolError(call.Name, toolErr))
					} else {
						retrieval = mergeRetrievalContext(retrieval, extra)
					}
					retrievalOmitReason = ""
					modelDrivenTools++
					s.appendRuntimeLedger(request.ConversationID, persisted.ID, runtimeLedgerEventTool, TurnStatePlanning, "", map[string]any{
						"tool":             call.Name,
						"phase":            "model_driven",
						"status":           status,
						"queryHash":        runtimeHash(query),
						"knowledgeCount":   len(extra.Knowledge),
						"modelDrivenIndex": modelDrivenTools,
					})
				case toolSocialExpressionSelect:
					if resolved.AllowsPersonalMemory() || !resolved.AllowsAmbientParticipation() {
						return fail("MODEL_RESPONSE_INVALID", errors.New("social_expression_select is available only for public ambient interactions"))
					}
					extra, toolErr := s.selectSocialExpressionsForTool(turnCtx, bootstrap.Conversation.CharacterID, request.ConversationID, query)
					status := "ok"
					if toolErr != nil {
						status = "failed"
						lg.Warn("cognition loop", zap.String("phase", "tool_failed"), zap.String("tool", call.Name), zap.Error(toolErr))
						retrieval = mergeRetrievalContext(retrieval, retrievalFromToolError(call.Name, toolErr))
					} else {
						retrieval = mergeRetrievalContext(retrieval, extra)
					}
					retrievalOmitReason = ""
					modelDrivenTools++
					s.appendRuntimeLedger(request.ConversationID, persisted.ID, runtimeLedgerEventTool, TurnStatePlanning, "", map[string]any{
						"tool":             call.Name,
						"phase":            "model_driven",
						"status":           status,
						"queryHash":        runtimeHash(query),
						"knowledgeCount":   len(extra.Knowledge),
						"modelDrivenIndex": modelDrivenTools,
					})
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
						lg.Warn("cognition loop", zap.String("phase", "tool_rejected"), zap.String("tool", call.Name), zap.String("reason", "endpoint_missing"))
						retrieval = mergeRetrievalContext(retrieval, retrievalFromToolError(call.Name, search.ErrEndpointNotConfigured))
						s.appendRuntimeLedger(request.ConversationID, persisted.ID, runtimeLedgerEventTool, TurnStatePlanning, "", map[string]any{
							"tool":             call.Name,
							"phase":            "model_driven",
							"status":           "endpoint_missing",
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
							ingestSnapshots = append(ingestSnapshots, knowledgeIngestSnapshots(request.ConversationID, persisted.ID, query, hits, time.Now().UnixMilli())...)
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
		reply, err = compileReplyForInteraction(draft, request.AvailableVisualStates, resolved, request.ReplyIntent)
		if err != nil {
			lg.Error("cognition loop",
				zap.String("phase", "compile_failed"),
				zap.Int("attempt", attempt),
				zap.Int("draftRunes", utf8.RuneCountInString(draft)),
				zap.Bool("draftHasSpeechTextKey", strings.Contains(draft, `"speechText"`)),
				zap.Error(err),
			)
			if replyCompileRetries < maxProtocolCompileRetries {
				replyCompileRetries++
				if firstCompileErr == nil {
					firstCompileErr = err
				}
				retryCorrection = replyCompileRetryCorrection(err)
				lg.Warn("cognition loop", zap.String("phase", "compile_retry"), zap.Int("attempt", attempt), zap.Int("retry", replyCompileRetries))
				continue
			}
			return fail("MODEL_RESPONSE_INVALID", fmt.Errorf("model reply remained invalid after %d retries: first attempt: %v; final attempt: %w", maxProtocolCompileRetries, firstCompileErr, err))
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
			zap.Int("displayRunes", utf8.RuneCountInString(reply.DisplayText)),
		)
		break
	}
	if err := transition(TurnStateResponding); err != nil {
		return fail("INVALID_STATE_TRANSITION", err)
	}
	s.markTurnDelivering(request.ConversationID, persisted.ID)
	var profileRevision *uint64
	if userProfile != nil {
		value := userProfile.Revision
		profileRevision = &value
	}
	var firstBeatOnce sync.Once
	finalDelivery = newReplyDelivery(
		turnCtx,
		len(reply.Chains),
		func(completion BeatReadyCompletion) error {
			_, err := s.publishLife(life, func() (TurnEvent, error) {
				return life.BeatReady(completion)
			})
			if err == nil && completion.Kind == beatKindFinal {
				firstBeatOnce.Do(func() { s.loopMetrics.firstBeat(time.Since(turnStarted)) })
			}
			return err
		},
		func(record replyDeliveryRecord) {
			s.appendRuntimeLedger(
				request.ConversationID,
				persisted.ID,
				runtimeLedgerEventBeatDelivery,
				TurnStateResponding,
				"",
				runtimeBeatDeliveryLedgerMetadata(
					record.Status,
					record.Kind,
					record.ChainIndex,
					record.PlayIndex,
					record.TargetInterval.Milliseconds(),
					record.PaceWait.Milliseconds(),
					record.PublishedPrefixCount,
				),
			)
		},
	)
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
		if _, err := s.publishLife(life, func() (TurnEvent, error) {
			return life.ReplyChain(uint8(index), delta, chain)
		}); err != nil {
			return fail("MODEL_RESPONSE_INVALID", err)
		}
		speechText := strings.TrimSpace(chain.SpeechText)
		if speechText != "" && speechExceedsSoftLimit(speechText) {
			// Soft warning only: still synthesized as ONE request (stable timbre).
			lg.Warn("cognition loop",
				zap.String("phase", "speech_over_soft_limit"),
				zap.Int("chain", index),
				zap.Int("runes", utf8.RuneCountInString(speechText)),
				zap.Int("softLimit", maxSpeechChars),
			)
		}
		if speechFlow == nil {
			if request.SpeechEnabled {
				// No synthesizer: deliver text-only beat; never hang the bubble on audio.
				s.appendRuntimeLedger(request.ConversationID, persisted.ID, runtimeLedgerEventSpeech, TurnStateResponding, "TTS_SKIPPED", map[string]any{"status": "skipped", "reason": "speech_synthesizer_unavailable", "index": index})
			}
			play := speechPlayIndex
			speechPlayIndex++
			if err := finalDelivery.Deliver(chain, BeatReadyCompletion{
				BeatID:      fmt.Sprintf("final-%d", index),
				Kind:        beatKindFinal,
				Index:       uint8(play),
				ChainIndex:  index,
				DisplayText: chain.Text,
				SpeechText:  speechText,
				VisualState: chain.VisualState,
			}); err != nil {
				code := "MODEL_RESPONSE_INVALID"
				if errors.Is(err, ErrTurnInterrupted) {
					code = "TURN_INTERRUPTED"
				}
				return fail(code, err)
			}
			continue
		}
		if speechText != "" && !speechRequested {
			if _, err := s.publishLife(life, func() (TurnEvent, error) {
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
	if speechFlow != nil {
		speechFlow.Close()
	}
	if err := finalDelivery.Err(); err != nil {
		code := "MODEL_RESPONSE_INVALID"
		if errors.Is(err, ErrTurnInterrupted) {
			code = "TURN_INTERRUPTED"
		}
		return fail(code, err)
	}
	if !finalDelivery.Complete() {
		return fail("MODEL_RESPONSE_INVALID", errors.New("final reply delivery did not publish every planned chain"))
	}
	if _, err := s.memory.CompleteTurn(request.ConversationID, persisted.ID, reply.DisplayText); err != nil {
		return s.terminalPersistenceFailure(life, request.ConversationID, persisted.ID, nil, err)
	}
	// Drain all queued TTS before completing so every speech.synthesized is emitted
	// BEFORE completed. The frontend uses completed as "no more audio coming" to
	// decide when the bubble may fade; emitting it before audio arrives would
	// release the hold and flash the bubble away prematurely. Chain audio still
	// streams to the UI as each job finishes during this drain (idempotent Close;
	// the deferred Close remains a safety net for fail paths).
	if _, err := s.publishLife(life, func() (TurnEvent, error) {
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
	s.loopMetrics.completed(time.Since(turnStarted))
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
	if resolved.AllowsPersonalMemory() {
		s.scheduleBackgroundExtraction(request.ConversationID)
	}
	if resolved.AllowsAmbientParticipation() && !resolved.AllowsPersonalMemory() && s.socialFeedback != nil && strings.TrimSpace(reply.DisplayText) != "" {
		entryIDs := []string(nil)
		if socialContext != nil {
			entryIDs = socialMemoryEntryIDs(socialContext.Memory)
		}
		s.socialFeedback.Register(socialFeedbackRegistration{
			CharacterID: bootstrap.Conversation.CharacterID, ConversationID: request.ConversationID,
			TurnID: persisted.ID, EntryIDs: entryIDs, ReplyText: reply.DisplayText,
		})
	}
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
	return laneUsageFromEventsForLane(model.PromptLaneRespond, events, historyWindow)
}

func laneUsageFromEventsForLane(lane model.PromptLane, events []model.StreamEvent, historyWindow uint64) []LaneModelUsage {
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
		Lane:          string(lane),
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

func (e *TurnEngine) CancelTurn(conversationID string, turnID string) error {
	s := e.host
	if s == nil || !s.RespondRuntimeMigrated() {
		return ErrRespondRuntimeNotMigrated
	}
	if conversationID == "" || turnID == "" {
		return errors.New("conversation_id and turn_id are required")
	}
	s.gateMu.Lock()
	gate := s.gates[conversationID]
	s.gateMu.Unlock()
	if gate == nil {
		return ErrTurnNotActive
	}
	gate.mu.Lock()
	defer gate.mu.Unlock()
	if gate.activeTurn == nil || gate.activeTurn.turnID != turnID {
		return ErrTurnNotActive
	}
	gate.activeTurn.cancel()
	return nil
}

func mapModelCancelError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return ErrTurnInterrupted
	}
	return err
}
