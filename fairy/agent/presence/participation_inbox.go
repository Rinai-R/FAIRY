package presence

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"fairy/runtime/model"
)

type Host interface {
	BeginMessageTrace(source, conversationID, messageID, traceID string) string
	ObserveSocialFeedback(conversationID string, observation AmbientObservation)
	EnqueueSocialLearning(conversationID string, messages []AmbientObservation)
	DecideParticipation(context.Context, ParticipationRequest) (ParticipationResult, error)
	SubmitTurn(TurnRequest) (TurnOutcome, error)
	EndMessageTrace(traceID, status string)
	EmitParticipation(Event)
	RecordParticipation(traceIDs []string, targetTraceID, action string)
	WarnAmbient(message, conversationID string, generation uint64, err error)
}

type TurnRequest struct {
	ConversationID      string
	Input               string
	TraceID             string
	MessageSource       string
	ReplyIntent         *ReplyIntent
	RecentTargetReply   string
	PersonNoteSenderIDs []string
}

type TurnOutcome struct {
	ResponseText string
}

type Event struct {
	ConversationID   string                        `json:"conversationId"`
	Generation       uint64                        `json:"generation"`
	EvaluationReason ParticipationEvaluationReason `json:"evaluationReason"`
	Action           string                        `json:"action"`
	TargetMessageID  string                        `json:"targetMessageId,omitempty"`
	WaitSeconds      int                           `json:"waitSeconds,omitempty"`
	Usage            []model.LaneModelUsage        `json:"usage,omitempty"`
	ObservedAt       time.Time                     `json:"observedAt"`
}

// AmbientInbox owns per-conversation rolling observations, wait timers, and
// single-flight participation for public ambient groups.
type Inbox struct {
	host       Host
	ctx        context.Context
	cancel     context.CancelFunc
	after      func(time.Duration, func()) stoppableTimer
	decideHook func(context.Context, ambientBatch) (ParticipationResult, error)
	submitHook func(TurnRequest) (TurnOutcome, error)

	mu             sync.Mutex
	states         map[string]*ambientState
	stateCapacity  int
	accessSequence uint64
	closed         bool
	wg             sync.WaitGroup
}

type stoppableTimer interface {
	Stop() bool
}

type sequencedObservation struct {
	sequence    uint64
	observation AmbientObservation
}

type ambientState struct {
	messages              []sequencedObservation
	cacheMessages         []sequencedObservation
	recentMessageIDs      map[string]struct{}
	recentMessageIDOrder  []string
	generation            uint64
	acceptedGeneration    uint64
	running               bool
	timer                 stoppableTimer
	timerOwner            uint64
	decisionOwner         uint64
	decisionCancel        context.CancelFunc
	lastLearnedGeneration uint64
	lastObservedSequence  uint64
	recentRepliesBySender map[string]string
	recentReplyOrder      []string
	consecutiveSilent     int
	backoffUntil          time.Time
}

type ambientBatch struct {
	conversationID   string
	generation       uint64
	evaluationReason ParticipationEvaluationReason
	messages         []AmbientObservation
	cacheMessages    []AmbientObservation
}

const (
	socialLearningObservationThreshold = 20
	maxAmbientConversationStates       = 256
	maxAmbientRecentMessageIDs         = 128
	maxAmbientRecentReplies            = MaxAmbientObservations
	participationTraceSilentError      = "silent_error"
)

func NewInbox(parent context.Context, host Host) *Inbox {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	return &Inbox{
		host:          host,
		ctx:           ctx,
		cancel:        cancel,
		after:         func(delay time.Duration, callback func()) stoppableTimer { return time.AfterFunc(delay, callback) },
		states:        make(map[string]*ambientState),
		stateCapacity: maxAmbientConversationStates,
	}
}

func (a *Inbox) Observe(conversationID string, observation AmbientObservation) error {
	if a == nil {
		return errors.New("ambient inbox is not configured")
	}
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return errors.New("conversation_id is required")
	}
	observation.IsNew = false
	if err := ValidateAmbientObservation(observation); err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return context.Canceled
	}
	if state := a.states[conversationID]; state != nil && state.hasRecentMessageID(observation.MessageID) {
		return nil
	}
	state, err := a.observeStateLocked(conversationID)
	if err != nil {
		return err
	}
	state.rememberMessageID(observation.MessageID)
	if a.host != nil {
		observation.TraceID = a.host.BeginMessageTrace("ambient", conversationID, observation.MessageID, observation.TraceID)
		a.host.ObserveSocialFeedback(conversationID, observation)
	}
	state.generation++
	entry := sequencedObservation{sequence: state.generation, observation: observation}
	state.messages = append(state.messages, entry)
	if len(state.messages) > MaxAmbientObservations {
		state.messages = state.messages[len(state.messages)-MaxAmbientObservations:]
	}
	state.cacheMessages = append(state.cacheMessages, entry)
	if len(state.cacheMessages) > MaxAmbientCacheObservations {
		state.cacheMessages = append([]sequencedObservation(nil), state.messages...)
	}
	if a.host != nil && state.generation-state.lastLearnedGeneration >= socialLearningObservationThreshold {
		a.host.EnqueueSocialLearning(conversationID, learningMessagesFromState(state))
		state.lastLearnedGeneration = state.generation
	}
	a.cancelTimerLocked(state)
	if state.decisionCancel != nil {
		state.decisionCancel()
	}
	state.consecutiveSilent = 0
	state.backoffUntil = time.Time{}
	if !state.running {
		a.startLocked(conversationID, state, ParticipationReasonMessage)
	}
	return nil
}

func (a *Inbox) observeStateLocked(conversationID string) (*ambientState, error) {
	if state := a.states[conversationID]; state != nil {
		a.touchStateLocked(state)
		return state, nil
	}
	capacity := a.stateCapacity
	if capacity < 1 {
		capacity = maxAmbientConversationStates
	}
	if len(a.states) >= capacity {
		victimID := ""
		var victimSequence uint64
		for candidateID, candidate := range a.states {
			if candidate.running || candidate.timer != nil || candidate.decisionCancel != nil {
				continue
			}
			if victimID == "" ||
				candidate.lastObservedSequence < victimSequence ||
				(candidate.lastObservedSequence == victimSequence && candidateID < victimID) {
				victimID = candidateID
				victimSequence = candidate.lastObservedSequence
			}
		}
		if victimID == "" {
			return nil, fmt.Errorf("ambient inbox conversation capacity %d exhausted", capacity)
		}
		delete(a.states, victimID)
	}
	state := &ambientState{}
	a.touchStateLocked(state)
	a.states[conversationID] = state
	return state, nil
}

func (a *Inbox) touchStateLocked(state *ambientState) {
	a.accessSequence++
	state.lastObservedSequence = a.accessSequence
}

func (state *ambientState) hasRecentMessageID(messageID string) bool {
	if state == nil || state.recentMessageIDs == nil {
		return false
	}
	_, found := state.recentMessageIDs[messageID]
	return found
}

func (state *ambientState) rememberMessageID(messageID string) {
	if state == nil || messageID == "" || state.hasRecentMessageID(messageID) {
		return
	}
	if state.recentMessageIDs == nil {
		state.recentMessageIDs = make(map[string]struct{}, maxAmbientRecentMessageIDs)
	}
	state.recentMessageIDs[messageID] = struct{}{}
	state.recentMessageIDOrder = append(state.recentMessageIDOrder, messageID)
	if len(state.recentMessageIDOrder) <= maxAmbientRecentMessageIDs {
		return
	}
	oldest := state.recentMessageIDOrder[0]
	delete(state.recentMessageIDs, oldest)
	state.recentMessageIDOrder = append([]string(nil), state.recentMessageIDOrder[1:]...)
}

func (a *Inbox) Close() {
	if a == nil {
		return
	}
	a.cancel()
	a.stop()
	a.wg.Wait()
}

func (a *Inbox) stop() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return
	}
	a.closed = true
	for _, state := range a.states {
		a.cancelTimerLocked(state)
		if state.decisionCancel != nil {
			state.decisionCancel()
			state.decisionCancel = nil
		}
	}
	clear(a.states)
}

func (a *Inbox) startLocked(conversationID string, state *ambientState, reason ParticipationEvaluationReason) {
	if a.closed || state.running || len(state.messages) == 0 {
		return
	}
	if reason != ParticipationReasonMessage && !state.backoffUntil.IsZero() {
		now := time.Now()
		if now.Before(state.backoffUntil) {
			a.scheduleWaitLocked(conversationID, state, state.backoffUntil.Sub(now))
			return
		}
	}
	state.running = true
	batch := snapshotAmbient(conversationID, state, reason)
	decisionCtx, decisionOwner := a.beginDecisionLocked(state)
	a.wg.Add(1)
	go a.run(batch, decisionCtx, decisionOwner)
}

func (a *Inbox) beginDecisionLocked(state *ambientState) (context.Context, uint64) {
	state.decisionOwner++
	ctx, cancel := context.WithCancel(a.ctx)
	state.decisionCancel = cancel
	return ctx, state.decisionOwner
}

func snapshotAmbient(conversationID string, state *ambientState, reason ParticipationEvaluationReason) ambientBatch {
	messages := make([]AmbientObservation, 0, len(state.messages))
	for _, entry := range state.messages {
		observation := entry.observation
		observation.IsNew = reason == ParticipationReasonMessage && entry.sequence > state.acceptedGeneration
		messages = append(messages, observation)
	}
	cacheMessages := make([]AmbientObservation, 0, len(state.cacheMessages))
	for _, entry := range state.cacheMessages {
		observation := entry.observation
		observation.IsNew = false
		cacheMessages = append(cacheMessages, observation)
	}
	return ambientBatch{
		conversationID: conversationID, generation: state.generation,
		evaluationReason: reason, messages: messages, cacheMessages: cacheMessages,
	}
}

func (a *Inbox) run(batch ambientBatch, decisionCtx context.Context, decisionOwner uint64) {
	defer a.wg.Done()
	for {
		decision, err := a.decide(decisionCtx, batch)
		a.mu.Lock()
		state := a.states[batch.conversationID]
		if state == nil || a.closed {
			a.mu.Unlock()
			return
		}
		if state.decisionOwner == decisionOwner {
			if state.decisionCancel != nil {
				state.decisionCancel()
			}
			state.decisionCancel = nil
		}
		if state.generation != batch.generation {
			batch = snapshotAmbient(batch.conversationID, state, ParticipationReasonMessage)
			decisionCtx, decisionOwner = a.beginDecisionLocked(state)
			a.mu.Unlock()
			continue
		}
		if err != nil {
			a.warn("ambient participation failed", batch, err)
			a.recordParticipation(batch, "", participationTraceSilentError)
			a.publishParticipation(batch, "failed", "", 0, nil)
			state.running = false
			a.mu.Unlock()
			return
		}
		state.acceptedGeneration = batch.generation
		switch decision.Action {
		case ParticipationSilent:
			a.recordParticipation(batch, "", "silent")
			a.publishParticipation(batch, "silent", "", 0, decision.Usage)
			state.consecutiveSilent++
			if delay := idleBackoffDelay(state.consecutiveSilent); delay > 0 {
				state.backoffUntil = time.Now().Add(delay)
			}
			state.running = false
			a.mu.Unlock()
			return
		case ParticipationWait:
			state.consecutiveSilent = 0
			state.backoffUntil = time.Time{}
			if decision.WaitSeconds == nil || *decision.WaitSeconds < 1 || *decision.WaitSeconds > 300 {
				a.warn("ambient wait decision invalid", batch, nil)
				a.recordParticipation(batch, "", participationTraceSilentError)
				a.publishParticipation(batch, "failed", "", 0, nil)
				state.running = false
				a.mu.Unlock()
				return
			}
			a.recordParticipation(batch, "", "wait")
			a.publishParticipation(batch, "wait", "", *decision.WaitSeconds, decision.Usage)
			state.running = false
			a.scheduleWaitLocked(batch.conversationID, state, time.Duration(*decision.WaitSeconds)*time.Second)
			a.mu.Unlock()
			return
		case ParticipationReply:
			state.consecutiveSilent = 0
			state.backoffUntil = time.Time{}
			if decision.TargetMessageID == nil || !ambientBatchContains(batch, *decision.TargetMessageID) {
				a.warn("ambient reply target invalid", batch, nil)
				a.recordParticipation(batch, "", participationTraceSilentError)
				a.publishParticipation(batch, "failed", "", 0, nil)
				state.running = false
				a.mu.Unlock()
				return
			}
			target := *decision.TargetMessageID
			targetSenderID := ambientSenderID(batch, target)
			recentTargetReply := ""
			if state.recentRepliesBySender != nil {
				recentTargetReply = state.recentRepliesBySender[targetSenderID]
			}
			targetTraceID := ambientTraceID(batch, target)
			a.recordParticipation(batch, targetTraceID, "reply")
			a.publishParticipation(batch, "reply", target, 0, decision.Usage)
			messages := append([]AmbientObservation(nil), batch.messages...)
			conversationID := batch.conversationID
			generation := batch.generation
			a.mu.Unlock()
			input, err := FormatAmbientTurnInput(messages, target)
			if err == nil {
				var outcome TurnOutcome
				outcome, err = a.submit(TurnRequest{
					ConversationID:      conversationID,
					Input:               input,
					TraceID:             targetTraceID,
					MessageSource:       "ambient",
					ReplyIntent:         decision.Intent,
					RecentTargetReply:   recentTargetReply,
					PersonNoteSenderIDs: SenderIDs(messages),
				})
				if err == nil && targetSenderID != "" && strings.TrimSpace(outcome.ResponseText) != "" {
					a.mu.Lock()
					current := a.states[conversationID]
					if current != nil {
						current.rememberRecentReply(targetSenderID, outcome.ResponseText)
					}
					a.mu.Unlock()
				}
			} else if a.host != nil {
				a.host.EndMessageTrace(targetTraceID, "failed")
			}
			a.mu.Lock()
			state = a.states[conversationID]
			if state != nil && !a.closed && state.generation != generation {
				batch = snapshotAmbient(conversationID, state, ParticipationReasonMessage)
				decisionCtx, decisionOwner = a.beginDecisionLocked(state)
				a.mu.Unlock()
				if err != nil {
					a.warn("ambient reply failed before refresh", batch, err)
				}
				continue
			}
			if state != nil {
				state.running = false
			}
			a.mu.Unlock()
			return
		default:
			a.warn("ambient participation action invalid", batch, nil)
			a.recordParticipation(batch, "", participationTraceSilentError)
			a.publishParticipation(batch, "failed", "", 0, nil)
			state.running = false
			a.mu.Unlock()
			return
		}
	}
}

func (state *ambientState) rememberRecentReply(senderID, replyText string) {
	if state == nil {
		return
	}
	senderID = strings.TrimSpace(senderID)
	replyText = strings.TrimSpace(replyText)
	if senderID == "" || replyText == "" {
		return
	}
	if state.recentRepliesBySender == nil {
		state.recentRepliesBySender = make(map[string]string, maxAmbientRecentReplies)
	}
	for index, existing := range state.recentReplyOrder {
		if existing != senderID {
			continue
		}
		copy(state.recentReplyOrder[index:], state.recentReplyOrder[index+1:])
		state.recentReplyOrder[len(state.recentReplyOrder)-1] = ""
		state.recentReplyOrder = state.recentReplyOrder[:len(state.recentReplyOrder)-1]
		break
	}
	if _, found := state.recentRepliesBySender[senderID]; !found && len(state.recentReplyOrder) >= maxAmbientRecentReplies {
		oldest := state.recentReplyOrder[0]
		delete(state.recentRepliesBySender, oldest)
		copy(state.recentReplyOrder, state.recentReplyOrder[1:])
		state.recentReplyOrder[len(state.recentReplyOrder)-1] = ""
		state.recentReplyOrder = state.recentReplyOrder[:len(state.recentReplyOrder)-1]
	}
	state.recentRepliesBySender[senderID] = replyText
	state.recentReplyOrder = append(state.recentReplyOrder, senderID)
}

func (a *Inbox) publishParticipation(batch ambientBatch, action, targetMessageID string, waitSeconds int, usage []model.LaneModelUsage) {
	if a == nil || a.host == nil {
		return
	}
	a.host.EmitParticipation(Event{
		ConversationID: batch.conversationID, Generation: batch.generation,
		EvaluationReason: batch.evaluationReason, Action: action,
		TargetMessageID: targetMessageID, WaitSeconds: waitSeconds, Usage: usage, ObservedAt: time.Now().UTC(),
	})
}

func (a *Inbox) recordParticipation(batch ambientBatch, targetTraceID, action string) {
	if a == nil || a.host == nil {
		return
	}
	traceIDs := make([]string, 0, len(batch.messages))
	for _, observation := range batch.messages {
		if observation.TraceID != "" {
			traceIDs = append(traceIDs, observation.TraceID)
		}
	}
	a.host.RecordParticipation(traceIDs, targetTraceID, action)
}

func ambientTraceID(batch ambientBatch, messageID string) string {
	for _, observation := range batch.messages {
		if observation.MessageID == messageID {
			return observation.TraceID
		}
	}
	return ""
}

func ambientSenderID(batch ambientBatch, messageID string) string {
	for _, observation := range batch.messages {
		if observation.MessageID == messageID {
			return observation.SenderID
		}
	}
	return ""
}

func SenderIDs(messages []AmbientObservation) []string {
	ids := make([]string, 0, len(messages))
	seen := make(map[string]struct{}, len(messages))
	for _, observation := range messages {
		id := strings.TrimSpace(observation.SenderID)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids
}

func learningMessagesFromState(state *ambientState) []AmbientObservation {
	if state == nil {
		return nil
	}
	start := len(state.cacheMessages) - socialLearningObservationThreshold
	if start < 0 {
		start = 0
	}
	messages := make([]AmbientObservation, 0, len(state.cacheMessages)-start)
	for _, item := range state.cacheMessages[start:] {
		observation := item.observation
		observation.IsNew = false
		observation.TraceID = ""
		messages = append(messages, observation)
	}
	return messages
}

func (a *Inbox) warn(message string, batch ambientBatch, err error) {
	if a != nil && a.host != nil {
		a.host.WarnAmbient(message, batch.conversationID, batch.generation, err)
	}
}

func (a *Inbox) decide(ctx context.Context, batch ambientBatch) (ParticipationResult, error) {
	if a.decideHook != nil {
		return a.decideHook(ctx, batch)
	}
	if a.host == nil {
		return ParticipationResult{}, errors.New("ambient inbox host is not configured")
	}
	return a.host.DecideParticipation(ctx, ParticipationRequest{
		ConversationID:   batch.conversationID,
		EvaluationReason: batch.evaluationReason,
		Messages:         batch.messages,
		CacheMessages:    batch.cacheMessages,
	})
}

func (a *Inbox) submit(request TurnRequest) (TurnOutcome, error) {
	if a.submitHook != nil {
		return a.submitHook(request)
	}
	if a.host == nil {
		return TurnOutcome{}, errors.New("ambient inbox host is not configured")
	}
	return a.host.SubmitTurn(request)
}

func (a *Inbox) scheduleWaitLocked(conversationID string, state *ambientState, delay time.Duration) {
	state.timerOwner++
	owner := state.timerOwner
	state.timer = a.after(delay, func() {
		a.mu.Lock()
		defer a.mu.Unlock()
		if a.closed {
			return
		}
		current := a.states[conversationID]
		if current == nil || current.timerOwner != owner || current.running || current.generation != current.acceptedGeneration {
			return
		}
		current.timer = nil
		a.startLocked(conversationID, current, ParticipationReasonWaitElapsed)
	})
}

func (a *Inbox) cancelTimerLocked(state *ambientState) {
	state.timerOwner++
	if state.timer != nil {
		state.timer.Stop()
		state.timer = nil
	}
}

func ambientBatchContains(batch ambientBatch, target string) bool {
	for _, observation := range batch.messages {
		if observation.MessageID == target {
			return true
		}
	}
	return false
}

// FormatAmbientTurnInput marks the reply target and formats ambient observations for SubmitTurn.
func FormatAmbientTurnInput(messages []AmbientObservation, targetMessageID string) (string, error) {
	var builder strings.Builder
	targets := 0
	for index, observation := range messages {
		if index > 0 {
			builder.WriteByte('\n')
		}
		if observation.MessageID == targetMessageID {
			builder.WriteString("[reply-target]")
			targets++
		}
		fmt.Fprintf(&builder, "[%s/%s] %s", observation.SenderName, observation.SenderID, observation.Text)
	}
	if targets != 1 {
		return "", errors.New("group reply target must match exactly one observation")
	}
	return builder.String(), nil
}
