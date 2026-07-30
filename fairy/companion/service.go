package companion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"

	"fairy/character"
	"fairy/config"
	"fairy/memory"
	"fairy/model"
	"fairy/reply"

	"go.uber.org/zap"

	"fairy/session"
)

type CompanionService struct {
	root                     string
	memory                   memoryPorts
	model                    ModelPort
	webSearch                WebSearchBackend
	knowledgeDocuments       knowledgeDocumentFetcher
	stickers                 StickerSearchPort
	speech                   SpeechRuntime
	characterLookup          CharacterLookup
	profiles                 ProfileSource
	cfg                      ConfigSource
	logger                   *zap.Logger
	backgroundErrorMu        sync.Mutex
	backgroundError          error
	loopMetrics              agentLoopMetrics
	interactionMu            sync.Mutex
	interactions             *interactionBindingCache
	capabilityMu             sync.RWMutex
	outputCapabilities       map[string]map[string]session.OutputCapabilities
	outputCapabilityLeases   int
	outputCapabilityCapacity int
	identities               OwnerIdentityPort
	emitMu                   sync.Mutex
	emit                     eventEmitter
	messageTelemetry         MessageTelemetry
	turnRegistry             *turnRegistry
	expressionDeliveries     *expressionDeliveryRegistry
	retention                *retentionEngine
	turns                    *TurnEngine
	desktopEvidence          DesktopEvidenceValidator
	desktopTool              DesktopToolCoordinator
	ambientReplies           AmbientReplyObserver
}

// SpeechRuntime is the speech capability Companion consumes. The reply package
// owns synthesis mechanics; Companion additionally needs one readiness decision
// before it creates any per-turn translation or synthesis work.
type SpeechRuntime interface {
	reply.SpeechSynthesizer
	SpeechReady() (bool, error)
}

type MessageTelemetry interface {
	Begin(source, conversationID string) string
	Participation(traceIDs []string, targetTraceID, action string)
	TurnStarted(traceID, conversationID, turnID string)
	TurnStage(conversationID, turnID, stage string)
	End(traceID, status string)
}

// WebSearchBackend is the optional OpenSERP sidecar search surface.
type WebSearchBackend interface {
	Search(ctx context.Context, query string, limit int) ([]WebSearchHit, error)
	Close() error
}

// AttachEventEmitter wires a Wails-free sink from main (package function, not a bound service method).
func AttachEventEmitter(s *CompanionService, emit eventEmitter) {
	if s == nil {
		return
	}
	s.emitMu.Lock()
	defer s.emitMu.Unlock()
	s.emit = emit
}

func AttachMessageTelemetry(s *CompanionService, telemetry MessageTelemetry) {
	if s == nil {
		return
	}
	s.emitMu.Lock()
	s.messageTelemetry = telemetry
	s.emitMu.Unlock()
}

func (s *CompanionService) emitEvent(event session.Event) {
	s.emitMu.Lock()
	emit := s.emit
	s.emitMu.Unlock()
	if emit != nil {
		emit(event)
	}
}

// publishLife allocates the next turn-event sequence and emits under one lock so
// concurrent utterance TTS cannot deliver duplicated or out-of-order sequences.
func (s *CompanionService) publishLife(life *turnLifecycle, produce func() (session.Event, error)) (session.Event, error) {
	if life == nil {
		return session.Event{}, errors.New("nil turn lifecycle")
	}
	return life.Publish(func() (session.Event, error) {
		event, err := produce()
		if err != nil {
			return session.Event{}, err
		}
		s.emitEvent(event)
		s.emitMu.Lock()
		telemetry := s.messageTelemetry
		s.emitMu.Unlock()
		if telemetry != nil {
			if stage := messageTelemetryStage(event); stage != "" {
				telemetry.TurnStage(event.ConversationID, event.TurnID, stage)
			}
		}
		return event, nil
	})
}

func messageTelemetryStage(event session.Event) string {
	var beat beatReadyPayload
	if json.Unmarshal(event.Payload, &beat) == nil && beat.Type == "beat.ready" && beat.Kind == reply.BeatKindFinal {
		return "first_beat"
	}
	switch event.State {
	case string(turnStateCompleted):
		return "completed"
	case string(turnStateFailed):
		return "failed"
	case string(turnStateInterrupted):
		return "interrupted"
	default:
		return ""
	}
}

func (s *CompanionService) beginMessageTrace(source, conversationID, traceID string) string {
	if traceID != "" {
		return traceID
	}
	s.emitMu.Lock()
	telemetry := s.messageTelemetry
	s.emitMu.Unlock()
	if telemetry == nil {
		return ""
	}
	if source == "" {
		source = "direct"
	}
	return telemetry.Begin(source, conversationID)
}

func (s *CompanionService) endMessageTrace(traceID, status string) {
	if traceID == "" {
		return
	}
	s.emitMu.Lock()
	telemetry := s.messageTelemetry
	s.emitMu.Unlock()
	if telemetry != nil {
		telemetry.End(traceID, status)
	}
}

func NewCompanionService() *CompanionService {
	service := &CompanionService{
		logger:                   zap.NewNop(),
		interactions:             newInteractionBindingCache(interactionBindingCacheCapacity),
		outputCapabilities:       make(map[string]map[string]session.OutputCapabilities),
		outputCapabilityCapacity: OutputCapabilityLeaseCapacity,
	}
	service.wireEngines()
	return service
}

// NewCompanionServiceWithRuntime wires the companion runtime. webSearch is owned
// by the composition root; pass nil when search is unavailable.
// When root is non-empty, character/profile/config ports are bound to that root;
// runtime Open may still replace them via Attach* with shared store handles.
func NewCompanionServiceWithRuntime(root string, store *memory.Store, model ModelPort, webSearch WebSearchBackend) *CompanionService {
	return newCompanionServiceWithPorts(root, memoryPortsFromStore(store), model, webSearch)
}

func newCompanionServiceWithPorts(root string, ports memoryPorts, model ModelPort, webSearch WebSearchBackend) *CompanionService {
	service := &CompanionService{
		root:                     root,
		memory:                   ports,
		model:                    model,
		webSearch:                webSearch,
		knowledgeDocuments:       newHTTPKnowledgeDocumentFetcher(),
		logger:                   zap.NewNop(),
		interactions:             newInteractionBindingCache(interactionBindingCacheCapacity),
		outputCapabilities:       make(map[string]map[string]session.OutputCapabilities),
		outputCapabilityCapacity: OutputCapabilityLeaseCapacity,
	}
	if strings.TrimSpace(root) != "" {
		service.characterLookup = character.NewStore(root)
		service.profiles = config.NewProfileStore(root)
		service.cfg = config.NewReader(root)
	}
	service.wireEngines()
	return service
}

// AttachLogger injects the process logger (dependency injection, no global).
func AttachLogger(s *CompanionService, logger *zap.Logger) {
	if s == nil || logger == nil {
		return
	}
	s.logger = logger
}

// AttachCharacterLookup injects ID-addressed character reads from the
// composition root.
func AttachCharacterLookup(s *CompanionService, lookup CharacterLookup) {
	if s == nil || lookup == nil {
		return
	}
	s.characterLookup = lookup
}

// AttachProfileSource injects the user-profile source from the composition root.
func AttachProfileSource(s *CompanionService, source ProfileSource) {
	if s == nil || source == nil {
		return
	}
	s.profiles = source
}

func AttachOwnerIdentityStore(s *CompanionService, store OwnerIdentityPort) {
	if s == nil || store == nil {
		return
	}
	s.identities = store
}

// AttachConfigSource injects durable config reads from the composition root.
func AttachConfigSource(s *CompanionService, source ConfigSource) {
	if s == nil || source == nil {
		return
	}
	s.cfg = source
}

// AttachSpeechRuntime injects the optional speech backend from the composition root.
func AttachSpeechRuntime(s *CompanionService, runtime SpeechRuntime) {
	if s == nil || runtime == nil {
		return
	}
	s.speech = runtime
}

func (s *CompanionService) profileSource() ProfileSource {
	if s != nil {
		return s.profiles
	}
	return nil
}

func (s *CompanionService) configSource() ConfigSource {
	if s != nil {
		return s.cfg
	}
	return nil
}

func (s *CompanionService) modelPort() ModelPort {
	if s != nil {
		return s.model
	}
	return nil
}

// Close performs graceful shutdown cleanup: it cancels in-flight turns and
// pending background extraction timers, then closes the web search client.
// It is safe to call multiple times and safe when no model runtime is attached.
func (s *CompanionService) Close() error {
	if s == nil {
		return nil
	}
	s.cancelActiveTurns()
	if s.expressionDeliveries != nil {
		s.expressionDeliveries.close()
	}
	if s.retention != nil {
		s.retention.close()
	}
	s.clearInteractionBindings()
	s.clearOutputCapabilities()
	if s.webSearch == nil {
		return nil
	}
	return s.webSearch.Close()
}

func (s *CompanionService) cancelActiveTurns() {
	if s != nil && s.turnRegistry != nil {
		s.turnRegistry.CancelAll()
	}
}

func (s *CompanionService) wireEngines() {
	if s == nil {
		return
	}
	s.turnRegistry = newTurnRegistry(func(ctx context.Context, conversationID, turnID string) error {
		if s.desktopTool == nil {
			return nil
		}
		return s.desktopTool.CancelTurn(ctx, conversationID, turnID)
	})
	s.expressionDeliveries = newExpressionDeliveryRegistry(expressionDeliveryTimeout)
	s.retention = newRetentionEngine(s)
	if store := s.memory.retention.knowledge; store != nil {
		ready := true
		if readiness, ok := store.(interface{ KnowledgeIngestReady() bool }); ok {
			ready = readiness.KnowledgeIngestReady()
		}
		if ready {
			s.retention.start()
		}
	}
	s.turns = &TurnEngine{host: s}
}

func (s *CompanionService) SubmitTurn(request SubmitTurnRequest) (TurnOutcome, error) {
	if s == nil || s.turns == nil {
		return TurnOutcome{}, ErrTurnRuntimeUnavailable
	}
	return s.turns.SubmitTurn(request)
}

func (s *CompanionService) SubmitCompiledTurn(request SubmitCompiledTurnRequest) (TurnOutcome, error) {
	if s == nil || s.turns == nil {
		return TurnOutcome{}, ErrTurnRuntimeUnavailable
	}
	return s.turns.SubmitCompiledTurn(request)
}

func (s *CompanionService) SubmitDesktopInitiation(request DesktopInitiationRequest, observation session.DesktopObservation) (TurnOutcome, error) {
	if s == nil || s.turns == nil {
		return TurnOutcome{}, ErrTurnRuntimeUnavailable
	}
	return s.turns.SubmitDesktopInitiation(request, observation)
}

func (s *CompanionService) SubmitDesktopVisionInitiation(request DesktopVisionInitiationRequest) (TurnOutcome, error) {
	if s == nil || s.turns == nil {
		return TurnOutcome{}, ErrTurnRuntimeUnavailable
	}
	return s.turns.SubmitDesktopVisionInitiation(request)
}

func (s *CompanionService) CancelTurn(conversationID string, turnID string) error {
	if s == nil || s.turns == nil {
		return ErrTurnRuntimeUnavailable
	}
	return s.turns.CancelTurn(conversationID, turnID)
}

func (s *CompanionService) TurnRuntimeReady() bool {
	return s != nil &&
		s.memory.ready() &&
		s.model != nil &&
		s.characterLookup != nil &&
		s.profiles != nil &&
		s.cfg != nil
}

func (s *CompanionService) terminalPersistenceFailure(life *turnLifecycle, conversationID, turnID string, cause, persistenceErr error) (TurnOutcome, error) {
	err := fmt.Errorf("persisting turn terminal state: %w", persistenceErr)
	if cause != nil {
		err = errors.Join(cause, err)
	}
	const code = "TURN_TERMINAL_PERSISTENCE_FAILED"
	if _, lifeErr := s.publishLife(life, func() (session.Event, error) {
		return life.Fail(code, err.Error(), false)
	}); lifeErr != nil {
		err = errors.Join(err, lifeErr)
	}
	s.appendRuntimeLedger(conversationID, turnID, runtimeLedgerEventTerminal, turnStateFailed, code, runtimeFailureLedgerMetadata(code, err, false))
	return TurnOutcome{}, err
}

// handleSpeechResult runs on the pipeline worker goroutine. publishLife holds the
// lifecycle lock, so emitting from here stays serialized with the main goroutine
// and preserves monotonic sequence numbers. Skipped and ordinary failed TTS jobs
// deliver text-only beats; cancelled jobs are discarded by the turn delivery owner.
func (s *CompanionService) handleSpeechResult(life *turnLifecycle, conversationID string, turnID string, delivery *reply.Delivery, res reply.SpeechResult) {
	display := strings.TrimSpace(res.DisplayText)
	if display == "" {
		display = strings.TrimSpace(res.Text)
	}
	if display == "" {
		return
	}
	if errors.Is(res.Err, context.Canceled) || errors.Is(res.Err, ErrTurnInterrupted) {
		s.appendRuntimeLedger(conversationID, turnID, runtimeLedgerEventSpeech, life.State(), "TTS_CANCELLED", map[string]any{
			"status":     "cancelled",
			"beatId":     res.BeatID,
			"playIndex":  res.PlayIndex,
			"chainIndex": res.ChainIndex,
		})
		if res.ChainIndex >= 0 && delivery != nil {
			delivery.Cancel(res.ChainIndex, res.PlayIndex, display)
		}
		return
	}
	kind := res.Kind
	if kind == "" {
		if res.ChainIndex == reply.ChainIndexUtterance {
			kind = reply.BeatKindUtterance
		} else {
			kind = reply.BeatKindFinal
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
	if res.ChainIndex >= 0 && delivery != nil {
		chain := ReplyChain{Text: display, SpeechText: res.Text, VisualState: res.VisualState}
		if err := delivery.Deliver(chain, completion); err != nil && !errors.Is(err, ErrTurnInterrupted) {
			s.logger.Warn("final beat delivery failed",
				zap.String("turn", turnID),
				zap.String("beatId", beatID),
				zap.Error(err),
			)
		}
		return
	}
	if _, err := s.publishLife(life, func() (session.Event, error) {
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
	return s.compactConversation(conversationID, "manual")
}

func (s *CompanionService) compactConversation(conversationID string, trigger string) (memory.CompactionResult, error) {
	if !s.TurnRuntimeReady() {
		return memory.CompactionResult{}, ErrTurnRuntimeUnavailable
	}
	if err := s.beginCompaction(conversationID); err != nil {
		return memory.CompactionResult{}, err
	}
	defer s.endCompaction(conversationID)

	bootstrap, err := s.memory.turn.promptContext.LoadConversationPrompt(conversationID)
	if err != nil {
		return memory.CompactionResult{}, err
	}
	characterRecord, err := s.activeCharacter(bootstrap.Conversation.CharacterID)
	if err != nil {
		return memory.CompactionResult{}, err
	}
	resolved, err := s.ResolveInteraction(conversationID)
	if err != nil {
		return memory.CompactionResult{}, err
	}
	var userProfile *config.ProfileSnapshot
	if resolved.AllowsPersonalMemory() {
		userProfile, err = s.profileSource().Current()
		if err != nil {
			return memory.CompactionResult{}, err
		}
	}
	windowed := messagesAfterCutoff(bootstrap.Messages, bootstrap.PromptWindow.CutoffMessageSequence)
	if len(windowed) == 0 {
		return memory.CompactionResult{}, errors.New("compaction requires dialogue after the current prompt window cutoff")
	}
	states, err := visualStatesFromCharacter(characterRecord)
	if err != nil {
		return memory.CompactionResult{}, err
	}
	input, err := buildCompactInput(characterRecord, userProfile, bootstrap.PromptWindow, bootstrap.Messages, states, resolved)
	if err != nil {
		return memory.CompactionResult{}, err
	}
	cacheKey := ""
	connectionConfig, err := s.configSource().ModelConnection()
	if err != nil {
		return memory.CompactionResult{}, err
	}
	if connectionConfig.Capabilities.PromptCacheKey {
		cacheKey = model.LaneCacheKey(conversationID, model.PromptLaneCompact)
	}
	cacheInput := model.NewCacheKeyInput(model.PromptLaneCompact, connectionConfig.Model, conversationID, CompactInstructions)
	cacheInput.CharacterRevision = characterRecord.Revision
	cacheInput.ProfileRevision = profileRevisionValue(userProfile)
	cacheInput.PromptRevision = bootstrap.PromptWindow.Revision
	events, err := s.model.ExecuteRequestContext(context.Background(), model.CompiledPromptRequest{
		Shape: model.ModelRequestShape{
			Lane: model.PromptLaneCompact, Model: connectionConfig.Model, Instructions: CompactInstructions,
			MaxOutputTokens: CompactMaxOutputTokens, PromptCacheKey: cacheKey,
		},
		Input: input, CacheInput: &cacheInput,
	})
	if err != nil {
		return memory.CompactionResult{}, err
	}
	summary, err := normalizeCompactionSummary(collectText(events))
	if err != nil {
		return memory.CompactionResult{}, err
	}
	tail := selectRecentCompleteTurnTail(windowed, 2_048)
	cutoff := bootstrap.PromptWindow.CutoffMessageSequence
	if len(tail) > 0 && tail[0].Sequence > 0 {
		cutoff = tail[0].Sequence - 1
	}
	projection := memory.EmptyPromptProjection()
	projection.RecentTailStartSequence = cutoff + 1
	if cutoff > 0 {
		projection.Omissions = append(projection.Omissions, memory.PromptProjectionOmission{
			StartMessageSequence: 1,
			EndMessageSequence:   cutoff,
			Reason:               "full_compact",
			CompactRevision:      bootstrap.PromptWindow.Revision + 1,
		})
	}
	existingWindow, foundWindow, err := s.memory.turn.runtimeState.LoadContextWindow(conversationID, string(model.PromptLaneRespond))
	if err != nil {
		return memory.CompactionResult{}, err
	}
	contextWindow := nextCompactionCommittedContextWindowRecord(
		conversationID,
		bootstrap.PromptWindow.Revision+1,
		existingWindow,
		foundWindow,
	)
	result, err := s.memory.turn.contextRetention.CommitTieredCompaction(
		conversationID,
		bootstrap.PromptWindow.Revision,
		bootstrap.PromptWindow.ProjectionRevision,
		summary,
		cutoff,
		projection,
		contextWindow,
		string(model.PromptLaneRespond),
	)
	if err != nil {
		return memory.CompactionResult{}, err
	}
	result.RetainedDialogueItems = len(tail)
	s.appendRuntimeLedger(
		conversationID, windowed[len(windowed)-1].TurnID,
		runtimeLedgerEventCompaction, turnStateCompleted, "",
		runtimeCompactionLedgerMetadata(
			"l3", trigger, "hard", len(windowed), len(windowed)-len(tail),
			estimateMessagesTokens(windowed)-estimateMessagesTokens(tail), 0,
			model.CacheMissing(), model.CacheMissing(),
			estimateMessagesTokens(windowed), estimateMessagesTokens(tail),
			bootstrap.PromptWindow.ProjectionRevision+1,
		),
	)
	return result, nil
}

func estimateMessagesTokens(messages []memory.MessageRecord) uint64 {
	var tokens uint64
	for _, message := range messages {
		tokens += estimatePromptTokens(uint64(utf8.RuneCountInString(memory.PromptMessageText(message))))
	}
	return tokens
}

func selectRecentCompleteTurnTail(messages []memory.MessageRecord, tokenBudget uint64) []memory.MessageRecord {
	if len(messages) == 0 || tokenBudget == 0 {
		return nil
	}
	start := len(messages)
	var used uint64
	for end := len(messages); end > 0; {
		turnID := messages[end-1].TurnID
		begin := end - 1
		var turnTokens uint64
		hasUser, hasAssistant := false, false
		for begin >= 0 && messages[begin].TurnID == turnID {
			message := messages[begin]
			turnTokens += estimatePromptTokens(uint64(utf8.RuneCountInString(memory.PromptMessageText(message))))
			hasUser = hasUser || message.Role == "user"
			hasAssistant = hasAssistant || message.Role == "assistant"
			begin--
		}
		begin++
		if !hasUser || !hasAssistant || used+turnTokens > tokenBudget {
			break
		}
		start = begin
		used += turnTokens
		end = begin
	}
	if start == len(messages) {
		return nil
	}
	return append([]memory.MessageRecord(nil), messages[start:]...)
}

func (s *CompanionService) activeCharacter(characterID string) (character.Record, error) {
	if s == nil || s.characterLookup == nil {
		return character.Record{}, errors.New("character lookup is not configured")
	}
	record, found, err := s.characterLookup.Lookup(characterID)
	if err != nil {
		return character.Record{}, err
	}
	if !found {
		return character.Record{}, errors.New("character is not available")
	}
	return record, nil
}

func visualStatesFromCharacter(record character.Record) ([]VisualState, error) {
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
		content := memory.PromptMessageText(message)
		switch message.Role {
		case "user":
			items = append(items, model.PromptItem{Type: model.PromptItemUserMessage, Content: content})
		case "assistant":
			items = append(items, model.PromptItem{Type: model.PromptItemAssistantMessage, Content: content})
		}
	}
	return items
}
