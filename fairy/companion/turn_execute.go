package companion

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"fairy/character"
	"fairy/config"
	"fairy/memory"
	"fairy/model"
	"fairy/persona"
	replyapp "fairy/reply"
	"fairy/session"

	"go.uber.org/zap"
)

func (e *TurnEngine) SubmitCompiledTurn(request SubmitCompiledTurnRequest) (outcome TurnOutcome, err error) {
	if err := ValidateSubmitCompiledTurnRequest(request); err != nil {
		return TurnOutcome{}, err
	}
	return e.submitCompiledTurn(request, nil, false)
}

func (e *TurnEngine) submitRuntimeTurn(request SubmitCompiledTurnRequest, resolved *session.Resolved) (TurnOutcome, error) {
	return e.submitCompiledTurn(request, resolved, true)
}

func (e *TurnEngine) submitCompiledTurn(
	request SubmitCompiledTurnRequest,
	preparedResolved *session.Resolved,
	deriveVisualStates bool,
) (outcome TurnOutcome, err error) {
	s := e.host
	var resolved session.Resolved
	if preparedResolved == nil {
		resolved, err = s.ResolveInteraction(request.ConversationID)
	} else {
		resolved = *preparedResolved
	}
	if err != nil {
		return TurnOutcome{}, err
	}
	if !s.TurnRuntimeReady() {
		return TurnOutcome{}, ErrTurnRuntimeUnavailable
	}
	request.TraceID = s.beginMessageTrace(request.MessageSource, request.ConversationID, request.TraceID)
	defer func() {
		if err != nil {
			s.endMessageTrace(request.TraceID, "failed")
		}
	}()
	preparation, err := s.prepareBeforeTurn(request, resolved, deriveVisualStates)
	if err != nil {
		return TurnOutcome{}, err
	}
	if preparation.compactionErr != nil {
		s.setBackgroundError(preparation.compactionErr)
	}
	if deriveVisualStates {
		request.AvailableVisualStates = preparation.visualStates
		if err := ValidateSubmitCompiledTurnRequest(request); err != nil {
			return TurnOutcome{}, err
		}
	}
	turnCtx, err := s.reserveTurn(request.ConversationID)
	if err != nil {
		s.endMessageTrace(request.TraceID, "failed")
		return TurnOutcome{}, err
	}
	var persisted memory.PersistedTurn
	if request.Initiation != nil {
		persisted, err = s.memory.turn.turns.BeginInitiationTurn(request.ConversationID, request.Initiation.ObservationEvidenceIDs)
	} else {
		persisted, err = s.memory.turn.turns.BeginTurn(request.ConversationID, request.Input)
	}
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
	life := newTurnLifecycle(request.ConversationID, persisted.ID)
	execution := &turnExecution{engine: e, service: s, request: request, persisted: persisted, life: life}
	fail := execution.fail
	transition := execution.transition

	contextSpan := s.startMessageSpan(request.TraceID, "准备上下文", "context", nil)
	gathered, prepareErr := e.prepareTurnContext(turnCtx, request, resolved, persisted.ID, transition, lg)
	if prepareErr != nil {
		code, cause := unwrapTurnPhaseError(prepareErr)
		s.finishMessageSpan(contextSpan, "failed", map[string]string{"errorCode": code})
		if errors.Is(cause, context.Canceled) {
			return fail("TURN_INTERRUPTED", ErrTurnInterrupted)
		}
		return fail(code, cause)
	}
	s.finishMessageSpan(contextSpan, "completed", map[string]string{
		"itemCount": fmt.Sprint(len(gathered.bootstrap.Messages)),
	})
	var (
		bootstrap           memory.ConversationPromptContext
		characterRecord     character.Record
		userProfile         *config.ProfileSnapshot
		socialContext       *SocialRespondContext
		retrieval           memory.RetrievalContext
		retrievalOmitReason string
	)
	agent := &agentLoopState{}
	bootstrap = gathered.bootstrap
	characterRecord = gathered.character
	userProfile = gathered.profile
	socialContext = gathered.socialContext
	retrieval = gathered.retrieval
	retrievalOmitReason = gathered.retrievalOmitReason
	if err := transition(turnStatePlanning); err != nil {
		return fail("INVALID_STATE_TRANSITION", err)
	}
	agent.connectionConfig, err = s.configSource().ModelConnection()
	if err != nil {
		return fail("MODEL_FAILED", err)
	}
	cacheKey := ""
	if agent.connectionConfig.Capabilities.PromptCacheKey {
		cacheKey = model.LaneCacheKey(request.ConversationID, model.PromptLaneRespond)
	}

	agent.webSearchEnabled = false
	if settings, err := s.configSource().WebSearchSettings(); err == nil {
		agent.webSearchEnabled = settings.Enabled
	}
	agent.stickerCandidates = make(stickerCandidateSet)
	agent.stickerToolEnabled, err = stickerToolAvailable(turnCtx, request.OutputCapabilities, s.stickers)
	if err != nil {
		lg.Warn("cognition loop", zap.String("phase", "sticker_capability_unavailable"), zap.Error(err))
		agent.stickerToolEnabled = false
	}
	agent.toolBudget = modelDrivenToolBudget(resolved)
	recordDesktopResult := func(callID, arguments string, evidence DesktopToolEvidence, toolErr error) error {
		var items []model.PromptItem
		if toolErr != nil {
			code := "capture_failed"
			var typed *DesktopToolError
			if errors.As(toolErr, &typed) && strings.TrimSpace(typed.Code) != "" {
				code = typed.Code
			}
			items = desktopToolFailurePromptItems(callID, arguments, code)
			s.appendRuntimeLedger(request.ConversationID, persisted.ID, runtimeLedgerEventTool, turnStatePlanning, code, map[string]any{
				"tool": toolDesktopObserve, "phase": "awaiting_tool", "status": "failed", "modelDrivenIndex": agent.modelDrivenTools + 1,
			})
		} else {
			items = desktopToolPromptItems(callID, arguments, evidence)
			s.appendRuntimeLedger(request.ConversationID, persisted.ID, runtimeLedgerEventTool, turnStatePlanning, "", map[string]any{
				"tool": toolDesktopObserve, "phase": "awaiting_tool", "status": "completed", "executionId": evidence.ExecutionID,
				"mediaType": evidence.MediaType, "width": evidence.Width, "height": evidence.Height, "modelDrivenIndex": agent.modelDrivenTools + 1,
			})
		}
		segments, err := toolContextSegments(items, time.Now())
		if err != nil {
			return err
		}
		agent.toolSegments = append(agent.toolSegments, segments...)
		return nil
	}
	appendRetrievalToolResult := func(call model.FunctionCall, status string, result memory.RetrievalContext) error {
		segments, err := toolContextSegments(retrievalToolPromptItems(call, status, result), time.Now())
		if err != nil {
			return err
		}
		agent.toolSegments = append(agent.toolSegments, segments...)
		return nil
	}
	appendRetrievalToolRuntime := func(call model.FunctionCall, query, status string, result, merged memory.RetrievalContext, metadata map[string]any) {
		if metadata == nil {
			metadata = map[string]any{}
		}
		metadata["tool"] = call.Name
		metadata["phase"] = "model_driven"
		metadata["status"] = status
		metadata["modelDrivenIndex"] = agent.modelDrivenTools + 1
		if strings.TrimSpace(query) != "" {
			metadata["queryHash"] = runtimeHash(query)
		}
		metadata["detail"] = runtimeRetrievalToolDetail(query, status, result, merged)
		s.appendRuntimeLedger(request.ConversationID, persisted.ID, runtimeLedgerEventTool, turnStatePlanning, "", metadata)
	}
	runAgent := func() (TurnOutcome, error) {
		for {
			allowTools := agent.modelDrivenTools < agent.toolBudget
			tools := []model.ToolSpec(nil)
			desktopEnabled := allowTools && !agent.desktopToolUsed && desktopToolAllowed(agent.connectionConfig.Capabilities.VisionInput, resolved, s.desktopTool, request.ConversationID)
			if allowTools {
				tools = respondToolSpecsForRuntime(agent.webSearchEnabled, resolved, desktopEnabled, agent.stickerToolEnabled)
			}
			instructions := respondInstructionsForInteraction(len(tools) > 0, resolved)
			instructions = stickerExpressionInstructions(instructions, agent.stickerToolEnabled)
			if desktopEnabled {
				instructions += " The desktop_observe tool provides one fresh main-display image when the current request genuinely requires visible screen context. Use it at most once, only when needed, and never claim to see anything not visible in its result."
			}
			if agent.stickerToolEnabled {
				instructions += " A sticker is a final expressive reply part, like an utterance, not an action announcement. Call sticker_search only when a sticker may fit; only IDs returned during this turn may appear in a final sticker chain. The candidate meaning comes exclusively from its human-authored description and tags."
			}
			if request.Initiation != nil {
				if request.Initiation.VisionRequested {
					instructions += " This is a Core-initiated private zero-message turn, not a user message. Decide whether a fresh desktop capture is necessary; use desktop_observe when visual evidence is required. Speak only if a brief natural check-in fits, never expose request evidence IDs, and never claim to see screen content unless the tool succeeds."
				} else {
					instructions += " This is a Core-initiated private companion turn, not a user message. Use the desktop_initiation context only as a coarse timing cue. Speak only if a brief natural check-in fits; never claim to see screen content, infer hidden activity, mention monitoring, or expose evidence IDs."
				}
			}
			instructions += agent.retryCorrection
			agent.modelCallAttempts++
			attempt := agent.modelCallAttempts
			assemblySpan := s.startMessageSpan(request.TraceID, "组装模型上下文", "context", map[string]string{
				"attempt": fmt.Sprint(attempt),
			})
			var slots []ContextSlot
			if socialContext == nil {
				slots, err = persona.BuildRespondContextSlots(characterRecord, userProfile, bootstrap.PromptWindow, bootstrap.Messages, request.AvailableVisualStates, retrieval, resolved)
			} else {
				slots, err = BuildRespondContextSlotsWithSocial(characterRecord, userProfile, bootstrap.PromptWindow, bootstrap.Messages, request.AvailableVisualStates, retrieval, resolved, *socialContext)
			}
			if err != nil {
				return fail("PROMPT_BUILD_FAILED", err)
			}
			if request.Initiation != nil {
				slots, err = AppendDesktopInitiationContext(slots, *request.Initiation)
				if err != nil {
					return fail("PROMPT_BUILD_FAILED", err)
				}
			}
			if retrievalOmitReason != "" && retrieval.Empty() {
				setContextSlotOmitReason(slots, "retrieved_context", retrievalOmitReason)
			}
			if agent.lastInputTokens > 0 && len(agent.toolSegments) > 0 {
				policy := compactionPolicyFromContextWindow(agent.connectionConfig.ContextWindowTokens)
				if policy.AutoInputTokenThreshold != nil &&
					agent.lastInputTokens >= *policy.AutoInputTokenThreshold {
					hardPressure := policy.hardPressure(agent.lastInputTokens)
					l1Plan := planL1ToolResultCompaction(l1PlanningInput{
						Segments: agent.toolSegments, NowUnixMS: time.Now().UnixMilli(),
						CurrentTokens: agent.lastInputTokens, TargetTokens: policy.TargetInputTokens,
						CacheObservation:    agent.lastCacheObservation,
						ExpectedFutureCalls: uint64(max(1, agent.toolBudget-agent.modelDrivenTools+1)),
						HardPressure:        hardPressure,
					})
					agent.toolSegments = applyL1CompactionPlan(agent.toolSegments, l1Plan)
					s.appendRuntimeLedger(
						request.ConversationID, persisted.ID,
						runtimeLedgerEventCompaction, turnStatePlanning, "",
						runtimeCompactionLedgerMetadata(
							"l1", "react_turn", watermarkName(hardPressure),
							l1Plan.CandidateCount, len(l1Plan.OmittedSegmentIDs),
							l1Plan.ReleasedTokens, l1Plan.InvalidatedCacheTokens,
							agent.lastCacheObservation, agent.lastCacheWriteObservation,
							agent.lastInputTokens, l1Plan.AfterTokens,
							bootstrap.PromptWindow.ProjectionRevision,
						),
					)
				}
			}
			input, err := (persona.ContextProjector{}).ProjectSlotsWithTail(slots, agent.toolSegments)
			if err != nil {
				return fail("PROMPT_BUILD_FAILED", err)
			}
			cacheInput := model.NewCacheKeyInput(model.PromptLaneRespond, agent.connectionConfig.Model, request.ConversationID, instructions)
			cacheInput.CharacterRevision = characterRecord.Revision
			cacheInput.ProfileRevision = profileRevisionValue(userProfile)
			cacheInput.PromptRevision = bootstrap.PromptWindow.Revision
			s.appendRuntimeLedger(request.ConversationID, persisted.ID, runtimeLedgerEventPrompt, turnStatePlanning, "", runtimePromptLedgerMetadata(input, slots, bootstrap.PromptWindow, bootstrap.Messages, request.AvailableVisualStates, retrieval, cacheInput, agent.connectionConfig.Capabilities.PromptCacheKey))
			agent.fullRequest = model.CompiledPromptRequest{
				Shape: model.ModelRequestShape{
					Lane:            model.PromptLaneRespond,
					Model:           agent.connectionConfig.Model,
					Instructions:    instructions,
					MaxOutputTokens: request.MaxOutputTokens,
					PromptCacheKey:  cacheKey,
				},
				Input:      input,
				Tools:      tools,
				CacheInput: &cacheInput,
			}
			var executeRequest model.CompiledPromptRequest
			var continuationMode string
			if agent.modelDrivenTools > 0 {
				// Retrieval changed mid-turn after tools; always full request.
				var matErr error
				executeRequest, matErr = model.MaterializeContinuationRequest(agent.fullRequest, model.ContinuationDecision{})
				if matErr != nil {
					return fail("MODEL_FAILED", matErr)
				}
				continuationMode = "full_post_tool"
				s.appendRuntimeLedger(request.ConversationID, persisted.ID, runtimeLedgerEventContinuation, turnStatePlanning, "", map[string]any{
					"incremental":         false,
					"fullReason":          "cognition_post_tool",
					"modelDrivenTools":    agent.modelDrivenTools,
					"previousStateSource": "none",
				})
			} else {
				decision, previous, contErr := s.decideContinuation(request.ConversationID, agent.connectionConfig.Capabilities.CacheRetention, bootstrap.PromptWindow.Revision, agent.fullRequest)
				if contErr != nil {
					return fail("MODEL_FAILED", contErr)
				}
				executeRequest, err = model.MaterializeContinuationRequest(agent.fullRequest, decision)
				if err != nil {
					return fail("MODEL_FAILED", err)
				}
				if decision.Incremental {
					continuationMode = "incremental"
				} else {
					continuationMode = "full:" + string(decision.FullReason)
				}
				s.appendRuntimeLedger(request.ConversationID, persisted.ID, runtimeLedgerEventContinuation, turnStatePlanning, "", runtimeContinuationLedgerMetadata(agent.connectionConfig.Capabilities.CacheRetention, previous, agent.fullRequest, executeRequest, decision))
			}
			s.finishMessageSpan(assemblySpan, "completed", map[string]string{
				"attempt":   fmt.Sprint(attempt),
				"itemCount": fmt.Sprint(len(input)),
				"cacheMode": continuationMode,
			})
			lg.Info("cognition loop",
				zap.String("phase", "model_call"),
				zap.Int("attempt", attempt),
				zap.Bool("allowTools", allowTools),
				zap.Bool("webSearch", agent.webSearchEnabled),
				zap.Int("toolCount", len(tools)),
				zap.String("continuation", continuationMode),
				zap.Int("inputItems", len(input)),
				zap.Int("personal", len(retrieval.PersonalMemories)),
				zap.Int("knowledge", len(retrieval.Knowledge)),
			)
			if err := turnCtx.Err(); err != nil {
				return fail("TURN_INTERRUPTED", ErrTurnInterrupted)
			}
			agent.events = make([]model.StreamEvent, 0)
			previewAccumulator := newStreamPreviewAccumulator(request.AvailableVisualStates)
			modelCallStarted := time.Now()
			firstByteAt := time.Time{}
			previewAt := time.Time{}
			streamCallback := func(event model.StreamEvent) {
				agent.events = append(agent.events, event)
				if firstByteAt.IsZero() {
					firstByteAt = time.Now()
					if _, presenceErr := s.publishLife(life, func() (session.Event, error) {
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
				if _, previewErr := s.publishLife(life, func() (session.Event, error) {
					return life.ReplyPreview(preview.Chains)
				}); previewErr != nil {
					lg.Warn("cognition loop", zap.String("phase", "preview_skipped"), zap.Error(previewErr))
				}
			}
			modelSpan := s.startMessageSpan(request.TraceID, "模型调用", "model", map[string]string{
				"attempt": fmt.Sprint(attempt), "model": agent.connectionConfig.Model, "cacheMode": continuationMode,
			})
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
			s.appendRuntimeLedger(request.ConversationID, persisted.ID, runtimeLedgerEventModel, turnStatePlanning, "", streamTiming)
			modelSpanStatus := "completed"
			modelSpanAttributes := map[string]string{"attempt": fmt.Sprint(attempt)}
			if err != nil {
				modelSpanStatus = "failed"
				modelSpanAttributes["errorCode"] = "MODEL_FAILED"
			}
			s.finishMessageSpan(modelSpan, modelSpanStatus, modelSpanAttributes)
			if !firstByteAt.IsZero() {
				s.loopMetrics.providerFirstByte(firstByteAt.Sub(modelCallStarted))
			}
			if !previewAt.IsZero() {
				s.loopMetrics.replyPreview(previewAt.Sub(modelCallStarted))
			}
			if err != nil {
				err = mapModelCancelError(turnCtx, err)
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
			usage := model.LaneUsageFromEvents(model.PromptLaneRespond, agent.events, bootstrap.PromptWindow.Revision)
			agent.finalUsage = usage
			if len(usage) > 0 {
				if usage[0].Usage.InputTokens != nil {
					agent.lastInputTokens = *usage[0].Usage.InputTokens
				}
				agent.lastCacheObservation = usage[0].Usage.CachedInputTokens
				agent.lastCacheWriteObservation = usage[0].Usage.CacheWriteTokens
			}
			s.appendRuntimeLedger(request.ConversationID, persisted.ID, runtimeLedgerEventModel, turnStatePlanning, "", runtimeModelLedgerMetadata(agent.events, usage))

			toolCalls := model.FunctionCallsFromEvents(agent.events)
			draft := collectText(agent.events)
			if strings.Contains(draft, `"gather"`) && len(toolCalls) == 0 {
				lg.Warn("cognition loop", zap.String("phase", "gather_json_rejected"), zap.Int("attempt", attempt))
				return fail("MODEL_RESPONSE_INVALID", errors.New("gather JSON is not supported; use function tools"))
			}
			if len(toolCalls) > 0 {
				if !allowTools {
					lg.Warn("cognition loop", zap.String("phase", "tool_rejected"), zap.String("reason", "budget_exhausted"), zap.Int("count", len(toolCalls)))
					return fail("MODEL_RESPONSE_INVALID", errors.New("tool budget exhausted"))
				}
				// Mid-ReAct in-character lines are transient display beats and do not
				// enter the persisted transcript.
				line := sanitizeUtteranceText(draft)
				if line != "" {
					if boundaryErr := validateTextForInteraction(line, resolved); boundaryErr != nil {
						lg.Warn("cognition loop", zap.String("phase", "utterance_omitted"), zap.String("reason", "public_peer_identity"))
						line = ""
					}
				}
				if line != "" {
					reason := toolUtteranceReason(toolCalls[0].Name)
					seq := agent.utteranceSeq
					agent.utteranceSeq++
					beatID := fmt.Sprintf("utt-%d", seq)
					lg.Info("cognition loop", zap.String("phase", "utterance_ready"), zap.Int("seq", seq), zap.String("reason", reason))
					if _, uttErr := s.publishLife(life, func() (session.Event, error) {
						return life.BeatReady(BeatReadyCompletion{
							BeatID:      beatID,
							Kind:        replyapp.BeatKindUtterance,
							Index:       uint8(execution.beatIndex),
							ChainIndex:  replyapp.ChainIndexUtterance,
							DisplayText: line,
							VisualState: "idle",
							Reason:      reason,
						})
					}); uttErr != nil {
						lg.Warn("cognition loop", zap.String("phase", "beat_skipped"), zap.String("beatId", beatID), zap.Error(uttErr))
					}
					execution.beatIndex++
				}
				for _, call := range toolCalls {
					if agent.modelDrivenTools >= agent.toolBudget {
						lg.Warn("cognition loop", zap.String("phase", "tool_rejected"), zap.String("reason", "budget_exhausted"), zap.String("tool", call.Name))
						return fail("MODEL_RESPONSE_INVALID", errors.New("tool budget exhausted"))
					}
					toolSpan := s.startMessageSpan(request.TraceID, "工具调用", "tool", map[string]string{
						"tool": call.Name, "callIndex": fmt.Sprint(agent.modelDrivenTools + 1),
					})
					query := ""
					var queryErr error
					if call.Name == toolDesktopObserve {
						queryErr = validateDesktopToolArguments(call.Arguments)
					} else {
						query, queryErr = parseToolQuery(call.Arguments)
					}
					if queryErr != nil {
						s.finishMessageSpan(toolSpan, "failed", map[string]string{
							"tool": call.Name, "status": "args_invalid", "errorCode": "MODEL_RESPONSE_INVALID",
						})
						lg.Warn("cognition loop", zap.String("phase", "tool_args_invalid"), zap.String("tool", call.Name), zap.Error(queryErr))
						toolResult := retrievalFromToolError(call.Name, queryErr)
						retrieval = mergeRetrievalContext(retrieval, toolResult)
						if err := appendRetrievalToolResult(call, "args_invalid", toolResult); err != nil {
							return fail("PROMPT_BUILD_FAILED", err)
						}
						retrievalOmitReason = ""
						appendRetrievalToolRuntime(call, "", "args_invalid", toolResult, retrieval, nil)
						agent.modelDrivenTools++
						continue
					}
					lg.Info("cognition loop",
						zap.String("phase", "tool_call"),
						zap.String("tool", call.Name),
						zap.String("callId", call.CallID),
						zap.Int("queryRunes", utf8.RuneCountInString(query)),
						zap.String("queryHash", runtimeHash(query)),
					)
					toolResultStatus := "ok"
					toolResult := memory.RetrievalContext{}
					appendToolResult := true
					switch call.Name {
					case toolDesktopObserve:
						appendToolResult = false
						if agent.desktopToolUsed || !desktopToolAllowed(agent.connectionConfig.Capabilities.VisionInput, resolved, s.desktopTool, request.ConversationID) {
							return fail("MODEL_RESPONSE_INVALID", errors.New("desktop_observe is unavailable for this interaction"))
						}
						agent.desktopToolUsed = true
						deadline := time.Now().Add(desktopToolTimeout)
						s.appendRuntimeLedger(request.ConversationID, persisted.ID, runtimeLedgerEventTool, turnStatePlanning, "", map[string]any{
							"tool": call.Name, "phase": "awaiting_tool", "status": "pending", "modelDrivenIndex": agent.modelDrivenTools + 1,
						})
						completed := make(chan struct{}, 1)
						executionHandle, toolErr := s.desktopTool.Begin(turnCtx, DesktopToolRequest{
							ConversationID: request.ConversationID, TurnID: persisted.ID, CallID: call.CallID, Deadline: deadline,
						}, func() {
							select {
							case completed <- struct{}{}:
							default:
							}
						})
						if toolErr != nil {
							toolResultStatus = "failed"
							if err := recordDesktopResult(call.CallID, call.Arguments, DesktopToolEvidence{}, toolErr); err != nil {
								return fail("PROMPT_BUILD_FAILED", err)
							}
							break
						}
						agent.pendingDesktop = &pendingDesktopTool{
							execution: executionHandle, callID: call.CallID, arguments: call.Arguments, completed: completed, spanID: toolSpan,
						}
						if toolErr = s.desktopTool.DispatchExecution(turnCtx, executionHandle); toolErr != nil {
							toolResultStatus = "failed"
							_, _ = s.desktopTool.Result(turnCtx, executionHandle.ID)
							agent.pendingDesktop = nil
							if err := recordDesktopResult(call.CallID, call.Arguments, DesktopToolEvidence{}, toolErr); err != nil {
								return fail("PROMPT_BUILD_FAILED", err)
							}
							break
						}
						return TurnOutcome{}, errAgentAwaitingDesktop
					case toolMemorySearch:
						if !resolved.AllowsPersonalMemory() {
							return fail("MODEL_RESPONSE_INVALID", errors.New("memory_search is unavailable for public interactions"))
						}
						extra, toolErr := s.retrieveMemoryForTool(bootstrap.Conversation.CharacterID, query)
						if toolErr != nil {
							toolResultStatus = "failed"
							toolResult = retrievalFromToolError(call.Name, toolErr)
							lg.Warn("cognition loop", zap.String("phase", "tool_failed"), zap.String("tool", call.Name), zap.Error(toolErr))
							retrieval = mergeRetrievalContext(retrieval, toolResult)
							appendRetrievalToolRuntime(call, query, "failed", toolResult, retrieval, nil)
						} else {
							toolResult = extra
							retrieval = mergeRetrievalContext(retrieval, extra)
							appendRetrievalToolRuntime(call, query, "ok", toolResult, retrieval, map[string]any{
								"personalCount":   len(extra.PersonalMemories),
								"knowledgeCount":  len(extra.Knowledge),
								"semanticStatus":  extra.SemanticStatus,
								"mergedPersonal":  len(retrieval.PersonalMemories),
								"mergedKnowledge": len(retrieval.Knowledge),
							})
							lg.Info("cognition loop",
								zap.String("phase", "tool_done"),
								zap.String("tool", call.Name),
								zap.Int("personalAdded", len(extra.PersonalMemories)),
								zap.Int("knowledgeAdded", len(extra.Knowledge)),
								zap.Int("mergedPersonal", len(retrieval.PersonalMemories)),
								zap.Int("mergedKnowledge", len(retrieval.Knowledge)),
								zap.Int("index", agent.modelDrivenTools+1),
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
							toolResultStatus = status
							toolResult = retrievalFromToolError(call.Name, toolErr)
							lg.Warn("cognition loop", zap.String("phase", "tool_failed"), zap.String("tool", call.Name), zap.Error(toolErr))
							retrieval = mergeRetrievalContext(retrieval, toolResult)
						} else {
							toolResult = extra
							retrieval = mergeRetrievalContext(retrieval, extra)
						}
						retrievalOmitReason = ""
						appendRetrievalToolRuntime(call, query, status, toolResult, retrieval, map[string]any{
							"personalCount":  len(extra.PersonalMemories),
							"knowledgeCount": len(extra.Knowledge),
						})
					case toolSocialContextSearch:
						if resolved.AllowsPersonalMemory() || !resolved.AllowsAmbientParticipation() {
							return fail("MODEL_RESPONSE_INVALID", errors.New("social_context_search is available only for public ambient interactions"))
						}
						extra, toolErr := s.selectSocialContextForTool(turnCtx, bootstrap.Conversation.CharacterID, request.ConversationID, query)
						status := "ok"
						if toolErr != nil {
							status = "failed"
							toolResultStatus = status
							toolResult = retrievalFromToolError(call.Name, toolErr)
							lg.Warn("cognition loop", zap.String("phase", "tool_failed"), zap.String("tool", call.Name), zap.Error(toolErr))
							retrieval = mergeRetrievalContext(retrieval, toolResult)
						} else {
							extra = boundSocialToolRetrieval(gathered.socialFeedbackContext, extra, memory.MaxSocialFeedbackIDs)
							toolResult = extra
							gathered.socialFeedbackContext = mergeSocialMemory(gathered.socialFeedbackContext, extra.SocialMemories)
							retrieval = mergeRetrievalContext(retrieval, extra)
						}
						retrievalOmitReason = ""
						appendRetrievalToolRuntime(call, query, status, toolResult, retrieval, map[string]any{
							"knowledgeCount": len(extra.Knowledge),
						})
					case toolSocialExpressionSelect:
						if resolved.AllowsPersonalMemory() || !resolved.AllowsAmbientParticipation() {
							return fail("MODEL_RESPONSE_INVALID", errors.New("social_expression_select is available only for public ambient interactions"))
						}
						extra, toolErr := s.selectSocialExpressionsForTool(turnCtx, bootstrap.Conversation.CharacterID, request.ConversationID, query)
						status := "ok"
						if toolErr != nil {
							status = "failed"
							toolResultStatus = status
							toolResult = retrievalFromToolError(call.Name, toolErr)
							lg.Warn("cognition loop", zap.String("phase", "tool_failed"), zap.String("tool", call.Name), zap.Error(toolErr))
							retrieval = mergeRetrievalContext(retrieval, toolResult)
						} else {
							extra = boundSocialToolRetrieval(gathered.socialFeedbackContext, extra, memory.MaxSocialFeedbackIDs)
							toolResult = extra
							gathered.socialFeedbackContext = mergeSocialMemory(gathered.socialFeedbackContext, extra.SocialMemories)
							retrieval = mergeRetrievalContext(retrieval, extra)
						}
						retrievalOmitReason = ""
						appendRetrievalToolRuntime(call, query, status, toolResult, retrieval, map[string]any{
							"knowledgeCount": len(extra.Knowledge),
						})
					case toolWebSearch:
						if !agent.webSearchEnabled {
							toolErr := errors.New("web search is disabled")
							toolResultStatus = "disabled"
							toolResult = retrievalFromToolError(call.Name, toolErr)
							lg.Warn("cognition loop", zap.String("phase", "tool_rejected"), zap.String("tool", call.Name), zap.String("reason", "disabled"))
							retrieval = mergeRetrievalContext(retrieval, toolResult)
							appendRetrievalToolRuntime(call, query, "disabled", toolResult, retrieval, nil)
						} else if s.webSearch == nil {
							toolResultStatus = "endpoint_missing"
							toolResult = retrievalFromToolError(call.Name, ErrWebSearchEndpointNotConfigured)
							lg.Warn("cognition loop", zap.String("phase", "tool_rejected"), zap.String("tool", call.Name), zap.String("reason", "endpoint_missing"))
							retrieval = mergeRetrievalContext(retrieval, toolResult)
							appendRetrievalToolRuntime(call, query, "endpoint_missing", toolResult, retrieval, nil)
						} else {
							hits, toolErr := s.webSearch.Search(turnCtx, query, 5)
							if toolErr != nil {
								toolResultStatus = "failed"
								toolResult = retrievalFromToolError(call.Name, toolErr)
								lg.Warn("cognition loop", zap.String("phase", "tool_failed"), zap.String("tool", call.Name), zap.Error(toolErr))
								retrieval = mergeRetrievalContext(retrieval, toolResult)
								appendRetrievalToolRuntime(call, query, "failed", toolResult, retrieval, nil)
							} else {
								batch, batchErr := newWebSearchBatch(
									request.ConversationID,
									persisted.ID,
									call.CallID,
									hits,
									time.Now().UnixMilli(),
								)
								if batchErr != nil {
									return fail("MODEL_RESPONSE_INVALID", batchErr)
								}
								gathered.knowledgeTasks = append(gathered.knowledgeTasks, memoryKnowledgeIngestTasks(batch)...)
								toolResult = retrievalFromWebSearchBatch(batch)
								retrieval = mergeRetrievalContext(retrieval, toolResult)
								appendRetrievalToolRuntime(call, query, "ok", toolResult, retrieval, map[string]any{
									"webHitCount":     len(hits),
									"webSourceCount":  len(batch.Sources),
									"mergedKnowledge": len(retrieval.Knowledge),
								})
								lg.Info("cognition loop",
									zap.String("phase", "tool_done"),
									zap.String("tool", call.Name),
									zap.Int("webHits", len(hits)),
									zap.Int("webSources", len(batch.Sources)),
									zap.Int("mergedKnowledge", len(retrieval.Knowledge)),
									zap.Int("index", agent.modelDrivenTools+1),
								)
							}
						}
					case toolStickerSearch:
						appendToolResult = false
						if !agent.stickerToolEnabled {
							return fail("MODEL_RESPONSE_INVALID", errors.New("sticker_search is unavailable for this session"))
						}
						candidates, toolErr := searchStickerCandidates(turnCtx, s.stickers, agent.stickerCandidates, query)
						segments, segmentErr := toolContextSegments(stickerToolPromptItems(call.CallID, call.Arguments, candidates, toolErr), time.Now())
						if segmentErr != nil {
							return fail("PROMPT_BUILD_FAILED", segmentErr)
						}
						agent.toolSegments = append(agent.toolSegments, segments...)
						status := "ok"
						if toolErr != nil {
							status = "failed"
							toolResultStatus = status
							lg.Warn("cognition loop", zap.String("phase", "tool_failed"), zap.String("tool", call.Name), zap.Error(toolErr))
						}
						s.appendRuntimeLedger(request.ConversationID, persisted.ID, runtimeLedgerEventTool, turnStatePlanning, "", map[string]any{
							"tool":             call.Name,
							"phase":            "model_driven",
							"status":           status,
							"queryHash":        runtimeHash(query),
							"candidateCount":   len(candidates),
							"candidateSetSize": len(agent.stickerCandidates),
							"modelDrivenIndex": agent.modelDrivenTools + 1,
						})
					default:
						toolErr := fmt.Errorf("tool %q is not whitelisted", call.Name)
						toolResultStatus = "not_whitelisted"
						toolResult = retrievalFromToolError(call.Name, toolErr)
						lg.Warn("cognition loop", zap.String("phase", "tool_rejected"), zap.String("tool", call.Name), zap.String("reason", "not_whitelisted"))
						retrieval = mergeRetrievalContext(retrieval, toolResult)
						appendRetrievalToolRuntime(call, "", "not_whitelisted", toolResult, retrieval, nil)
					}
					if appendToolResult {
						if err := appendRetrievalToolResult(call, toolResultStatus, toolResult); err != nil {
							return fail("PROMPT_BUILD_FAILED", err)
						}
					}
					toolSpanStatus := "completed"
					if toolResultStatus != "ok" {
						toolSpanStatus = "failed"
					}
					s.finishMessageSpan(toolSpan, toolSpanStatus, map[string]string{
						"tool": call.Name, "status": toolResultStatus, "callIndex": fmt.Sprint(agent.modelDrivenTools + 1),
					})
					retrievalOmitReason = ""
					agent.modelDrivenTools++
				}
				continue
			}
			compileSpan := s.startMessageSpan(request.TraceID, "编译回复", "compile", map[string]string{
				"attempt": fmt.Sprint(attempt),
			})
			agent.reply, err = compileReplyForInteractionWithExpressions(
				draft,
				request.AvailableVisualStates,
				resolved,
				request.ReplyIntent,
				agent.stickerCandidates.compileOptions(agent.stickerToolEnabled),
			)
			if err != nil {
				s.finishMessageSpan(compileSpan, "failed", map[string]string{"errorCode": "MODEL_RESPONSE_INVALID"})
				lg.Error("cognition loop",
					zap.String("phase", "compile_failed"),
					zap.Int("attempt", attempt),
					zap.Int("draftRunes", utf8.RuneCountInString(draft)),
					zap.Error(err),
				)
				if agent.replyCompileRetries < maxProtocolCompileRetries {
					agent.replyCompileRetries++
					if agent.firstCompileErr == nil {
						agent.firstCompileErr = err
					}
					agent.retryCorrection = replyCompileRetryCorrection(err)
					lg.Warn("cognition loop", zap.String("phase", "compile_retry"), zap.Int("attempt", attempt), zap.Int("retry", agent.replyCompileRetries))
					continue
				}
				return fail("MODEL_RESPONSE_INVALID", fmt.Errorf("model reply remained invalid after %d retries: first attempt: %v; final attempt: %w", maxProtocolCompileRetries, agent.firstCompileErr, err))
			}
			s.finishMessageSpan(compileSpan, "completed", map[string]string{
				"chainCount": fmt.Sprint(len(agent.reply.Chains)),
			})
			s.appendRuntimeLedger(request.ConversationID, persisted.ID, runtimeLedgerEventCompile, turnStatePlanning, "", map[string]any{
				"status":           "succeeded",
				"visualState":      agent.reply.VisualState,
				"chainCount":       len(agent.reply.Chains),
				"displayTextHash":  runtimeHash(agent.reply.DisplayText),
				"modelDrivenTools": agent.modelDrivenTools,
			})
			textLang := characterRecord.TextLanguage
			if textLang == "" {
				textLang = character.DefaultTextLanguage
			}
			lg.Info("cognition loop",
				zap.String("phase", "reply_ready"),
				zap.Int("chains", len(agent.reply.Chains)),
				zap.String("visual", agent.reply.VisualState),
				zap.Int("modelDrivenTools", agent.modelDrivenTools),
				zap.String("textLanguage", textLang),
				zap.Int("displayRunes", utf8.RuneCountInString(agent.reply.DisplayText)),
			)
			return TurnOutcome{}, nil
		}
	}
	for {
		agentOutcome, agentErr := runAgent()
		if !errors.Is(agentErr, errAgentAwaitingDesktop) {
			if agentErr != nil {
				return agentOutcome, agentErr
			}
			break
		}
		pending := agent.pendingDesktop
		if pending == nil {
			return fail("MODEL_FAILED", errors.New("desktop capture suspended without pending execution"))
		}
		select {
		case <-turnCtx.Done():
			_ = s.desktopTool.CancelTurn(context.Background(), request.ConversationID, persisted.ID)
			return fail("TURN_INTERRUPTED", ErrTurnInterrupted)
		case <-pending.completed:
		}
		if turnCtx.Err() != nil {
			_ = s.desktopTool.CancelTurn(context.Background(), request.ConversationID, persisted.ID)
			return fail("TURN_INTERRUPTED", ErrTurnInterrupted)
		}
		evidence, toolErr := s.desktopTool.Result(turnCtx, pending.execution.ID)
		if err := recordDesktopResult(pending.callID, pending.arguments, evidence, toolErr); err != nil {
			return fail("PROMPT_BUILD_FAILED", err)
		}
		desktopSpanStatus := "completed"
		desktopToolStatus := "ok"
		if toolErr != nil {
			desktopSpanStatus = "failed"
			desktopToolStatus = "failed"
		}
		s.finishMessageSpan(pending.spanID, desktopSpanStatus, map[string]string{
			"tool": toolDesktopObserve, "status": desktopToolStatus,
		})
		agent.pendingDesktop = nil
		agent.modelDrivenTools++
	}
	gathered.connectionConfig = agent.connectionConfig
	gathered.reply = agent.reply
	gathered.events = append([]model.StreamEvent(nil), agent.events...)
	gathered.fullRequest = agent.fullRequest
	gathered.finalUsage = append([]LaneModelUsage(nil), agent.finalUsage...)
	respondingOutcome, respondingErr := execution.deliverReply(turnCtx, gathered, turnStarted, lg)
	if respondingErr != nil {
		return respondingOutcome, respondingErr
	}
	return execution.persist(gathered, resolved, turnStarted)
}

func desktopInitiationRetrievalQuery(context DesktopInitiationContext) string {
	parts := []string{"桌面陪伴主动问候"}
	if value := strings.TrimSpace(context.Activity); value != "" {
		parts = append(parts, value)
	}
	if value := strings.TrimSpace(context.Lifecycle); value != "" {
		parts = append(parts, value)
	}
	return strings.Join(parts, " ")
}

func profileRevisionValue(snapshot *config.ProfileSnapshot) uint64 {
	if snapshot == nil {
		return 0
	}
	return snapshot.Revision
}
