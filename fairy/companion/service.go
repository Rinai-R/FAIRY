package companion

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"fairy/character"
	"fairy/config"
	"fairy/interaction"
	"fairy/memory"
	"fairy/memory/semantic"
	"fairy/model"
	"fairy/profile"
	"fairy/search"

	"go.uber.org/zap"
)

type CompanionService struct {
	root              string
	memory            MemoryPort
	semanticEmbedder  semantic.Embedder
	vectorIndex       VectorIndex
	model             ModelPort
	webSearch         WebSearchBackend
	speech            SpeechSynthesizer
	characters        CharacterCatalog
	profiles          ProfileSource
	cfg               ConfigSource
	logger            *zap.Logger
	backgroundJobs    atomic.Int64
	extractionMu      sync.Mutex
	extractionIdle    map[string]context.CancelFunc
	backgroundErrorMu sync.Mutex
	backgroundError   error
	loopMetrics       agentLoopMetrics
	gateMu            sync.Mutex
	gates             map[string]*conversationGate
	interactionMu     sync.RWMutex
	interactions      map[string]interaction.Binding
	identities        OwnerIdentityPort
	emitMu            sync.Mutex
	emit              EventEmitter
	messageTelemetry  MessageTelemetry
	ambient           *AmbientInbox
	turns             *TurnEngine
	participation     *ParticipationEngine
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

func AttachMessageTelemetry(s *CompanionService, telemetry MessageTelemetry) {
	if s == nil {
		return
	}
	s.emitMu.Lock()
	s.messageTelemetry = telemetry
	s.emitMu.Unlock()
}

func (s *CompanionService) emitEvent(event TurnEvent) {
	s.emitMu.Lock()
	emit := s.emit
	s.emitMu.Unlock()
	if emit != nil {
		emit(event)
	}
}

// publishLife allocates the next turn-event sequence and emits under one lock so
// concurrent utterance TTS cannot deliver duplicated or out-of-order sequences.
func (s *CompanionService) publishLife(life *TurnLifecycle, produce func() (TurnEvent, error)) (TurnEvent, error) {
	if life == nil {
		return TurnEvent{}, errors.New("nil turn lifecycle")
	}
	life.mu.Lock()
	defer life.mu.Unlock()
	event, err := produce()
	if err != nil {
		return TurnEvent{}, err
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
}

func messageTelemetryStage(event TurnEvent) string {
	if beat, ok := event.Payload.(beatReadyPayload); ok && beat.Kind == beatKindFinal {
		return "first_beat"
	}
	switch event.State {
	case TurnStateCompleted:
		return "completed"
	case TurnStateFailed:
		return "failed"
	case TurnStateInterrupted:
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
		logger:         zap.NewNop(),
		extractionIdle: make(map[string]context.CancelFunc),
		gates:          make(map[string]*conversationGate),
		interactions:   make(map[string]interaction.Binding),
	}
	service.ambient = newAmbientInbox(context.Background(), service)
	service.wireEngines()
	return service
}

// NewCompanionServiceWithRuntime wires the companion runtime. webSearch is owned
// by the composition root; pass nil when search is unavailable.
// When root is non-empty, character/profile/config ports are bound to that root;
// runtime Open may still replace them via Attach* with shared store handles.
func NewCompanionServiceWithRuntime(root string, memory MemoryPort, model ModelPort, webSearch WebSearchBackend) *CompanionService {
	service := &CompanionService{
		root:           root,
		memory:         memory,
		model:          model,
		webSearch:      webSearch,
		logger:         zap.NewNop(),
		extractionIdle: make(map[string]context.CancelFunc),
		gates:          make(map[string]*conversationGate),
		interactions:   make(map[string]interaction.Binding),
	}
	if strings.TrimSpace(root) != "" {
		service.characters = character.NewStore(root)
		service.profiles = profile.NewStore(root)
		service.cfg = config.NewReader(root)
	}
	service.ambient = newAmbientInbox(context.Background(), service)
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

// AttachCharacterCatalog injects the character catalog from the composition root.
func AttachCharacterCatalog(s *CompanionService, catalog CharacterCatalog) {
	if s == nil || catalog == nil {
		return
	}
	s.characters = catalog
}

// AttachCharacterStore is retained for call-site compatibility; prefer AttachCharacterCatalog.
func AttachCharacterStore(s *CompanionService, store *character.Store) {
	AttachCharacterCatalog(s, store)
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

// AttachProfileStore is retained for call-site compatibility; prefer AttachProfileSource.
func AttachProfileStore(s *CompanionService, store *profile.Store) {
	AttachProfileSource(s, store)
}

// AttachConfigSource injects durable config reads from the composition root.
func AttachConfigSource(s *CompanionService, source ConfigSource) {
	if s == nil || source == nil {
		return
	}
	s.cfg = source
}

// AttachConfigReader is retained for call-site compatibility; prefer AttachConfigSource.
func AttachConfigReader(s *CompanionService, reader *config.Reader) {
	AttachConfigSource(s, reader)
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

type VectorIndex interface {
	memory.VectorIndex
	memory.SemanticVectorIndex
}

func AttachVectorIndex(s *CompanionService, index VectorIndex) {
	if s == nil || index == nil {
		return
	}
	s.vectorIndex = index
}

func (s *CompanionService) characterCatalog() CharacterCatalog {
	if s != nil {
		return s.characters
	}
	return nil
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

func (s *CompanionService) memoryPort() MemoryPort {
	if s != nil {
		return s.memory
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
	if s.ambient != nil {
		s.ambient.Close()
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

func (s *CompanionService) wireEngines() {
	if s == nil {
		return
	}
	s.turns = &TurnEngine{host: s}
	s.participation = &ParticipationEngine{host: s}
}

func (s *CompanionService) SubmitTurn(request SubmitTurnRequest) (TurnOutcome, error) {
	if s == nil || s.turns == nil {
		return TurnOutcome{}, ErrRespondRuntimeNotMigrated
	}
	return s.turns.SubmitTurn(request)
}

func (s *CompanionService) SubmitCompiledTurn(request SubmitCompiledTurnRequest) (TurnOutcome, error) {
	if s == nil || s.turns == nil {
		return TurnOutcome{}, ErrRespondRuntimeNotMigrated
	}
	return s.turns.SubmitCompiledTurn(request)
}

func (s *CompanionService) CancelTurn(conversationID string, turnID string) error {
	if s == nil || s.turns == nil {
		return ErrRespondRuntimeNotMigrated
	}
	return s.turns.CancelTurn(conversationID, turnID)
}

func (s *CompanionService) DecideParticipation(ctx context.Context, request ParticipationRequest) (ParticipationResult, error) {
	if s == nil || s.participation == nil {
		return ParticipationResult{}, ErrRespondRuntimeNotMigrated
	}
	return s.participation.DecideParticipation(ctx, request)
}

func (s *CompanionService) RespondRuntimeMigrated() bool {
	return s != nil &&
		s.memory != nil &&
		s.model != nil &&
		s.characters != nil &&
		s.profiles != nil &&
		s.cfg != nil
}

func (s *CompanionService) emitSpeechFailure(life *TurnLifecycle, conversationID string, turnID string, code string, message string, retryable bool) {
	_, _ = s.publishLife(life, func() (TurnEvent, error) {
		return life.SpeechFailed(code, message, retryable)
	})
}

func (s *CompanionService) terminalPersistenceFailure(life *TurnLifecycle, conversationID, turnID string, cause, persistenceErr error) (TurnOutcome, error) {
	err := fmt.Errorf("persisting turn terminal state: %w", persistenceErr)
	if cause != nil {
		err = errors.Join(cause, err)
	}
	const code = "TURN_TERMINAL_PERSISTENCE_FAILED"
	if _, lifeErr := s.publishLife(life, func() (TurnEvent, error) {
		return life.Fail(code, err.Error(), false)
	}); lifeErr != nil {
		err = errors.Join(err, lifeErr)
	}
	s.appendRuntimeLedger(conversationID, turnID, runtimeLedgerEventTerminal, TurnStateFailed, code, runtimeFailureLedgerMetadata(code, err, false))
	return TurnOutcome{}, err
}

// handleSpeechResult runs on the pipeline worker goroutine. publishLife holds the
// lifecycle lock, so emitting from here stays serialized with the main goroutine
// and preserves monotonic sequence numbers. Skipped and ordinary failed TTS jobs
// deliver text-only beats; cancelled jobs are discarded by the turn delivery owner.
func (s *CompanionService) handleSpeechResult(life *TurnLifecycle, conversationID string, turnID string, delivery *replyDelivery, res speechPipelineResult) {
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
	if _, err := s.publishLife(life, func() (TurnEvent, error) {
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

	bootstrap, err := s.memory.LoadConversation(conversationID)
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
	var userProfile *profile.Snapshot
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
	states, err := s.availableVisualStatesForConversation(conversationID)
	if err != nil {
		return memory.CompactionResult{}, err
	}
	input, err := BuildCompactInput(characterRecord, userProfile, bootstrap.PromptWindow, bootstrap.Messages, states, resolved)
	if err != nil {
		return memory.CompactionResult{}, err
	}
	cacheKey := ""
	connectionConfig, err := s.configSource().ModelConnection()
	if err != nil {
		return memory.CompactionResult{}, err
	}
	if connectionConfig.Capabilities.PromptCacheKey {
		cacheKey = model.LaneCacheKey(conversationID, model.PromptLaneRespond)
	}
	events, err := s.model.ExecutePrompt(
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
	result, err := s.memory.CommitPromptWindow(conversationID, bootstrap.PromptWindow.Revision, summary)
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
	catalogPort := s.characterCatalog()
	if catalogPort == nil {
		return character.Record{}, errors.New("character catalog is not configured")
	}
	catalog, err := catalogPort.List()
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
	bootstrap, err := s.memory.LoadConversation(conversationID)
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
