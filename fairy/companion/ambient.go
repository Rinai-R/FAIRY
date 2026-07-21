package companion

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// AmbientInbox owns per-conversation rolling observations, wait timers, and
// single-flight participation for public ambient groups.
type AmbientInbox struct {
	service    *CompanionService
	ctx        context.Context
	cancel     context.CancelFunc
	after      func(time.Duration, func()) stoppableTimer
	decideHook func(context.Context, ambientBatch) (GroupParticipationResult, error)

	mu     sync.Mutex
	states map[string]*ambientState
	closed bool
	wg     sync.WaitGroup
}

type stoppableTimer interface {
	Stop() bool
}

type sequencedObservation struct {
	sequence    uint64
	observation GroupObservation
}

type ambientState struct {
	messages           []sequencedObservation
	generation         uint64
	acceptedGeneration uint64
	running            bool
	timer              stoppableTimer
	timerOwner         uint64
}

type ambientBatch struct {
	conversationID   string
	generation       uint64
	evaluationReason GroupParticipationEvaluationReason
	messages         []GroupObservation
}

func newAmbientInbox(parent context.Context, service *CompanionService) *AmbientInbox {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	return &AmbientInbox{
		service: service,
		ctx:     ctx,
		cancel:  cancel,
		after:   func(delay time.Duration, callback func()) stoppableTimer { return time.AfterFunc(delay, callback) },
		states:  make(map[string]*ambientState),
	}
}

// ObserveGroupMessage enqueues one observation and schedules ambient participation.
func (s *CompanionService) ObserveGroupMessage(conversationID string, observation GroupObservation) error {
	if s == nil || s.ambient == nil {
		return errors.New("ambient inbox is not configured")
	}
	return s.ambient.Observe(conversationID, observation)
}

func (a *AmbientInbox) Observe(conversationID string, observation GroupObservation) error {
	if a == nil {
		return errors.New("ambient inbox is not configured")
	}
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return errors.New("conversation_id is required")
	}
	observation.IsNew = false
	if err := validateGroupObservation(observation); err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return context.Canceled
	}
	state := a.states[conversationID]
	if state == nil {
		state = &ambientState{}
		a.states[conversationID] = state
	}
	state.generation++
	state.messages = append(state.messages, sequencedObservation{sequence: state.generation, observation: observation})
	if len(state.messages) > maxGroupObservations {
		state.messages = state.messages[len(state.messages)-maxGroupObservations:]
	}
	a.cancelTimerLocked(state)
	if !state.running {
		a.startLocked(conversationID, state, GroupParticipationReasonMessage)
	}
	return nil
}

func (a *AmbientInbox) Close() {
	if a == nil {
		return
	}
	a.cancel()
	a.stop()
	a.wg.Wait()
}

func (a *AmbientInbox) stop() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return
	}
	a.closed = true
	for _, state := range a.states {
		a.cancelTimerLocked(state)
	}
}

func (a *AmbientInbox) startLocked(conversationID string, state *ambientState, reason GroupParticipationEvaluationReason) {
	if a.closed || state.running || len(state.messages) == 0 {
		return
	}
	state.running = true
	batch := snapshotAmbient(conversationID, state, reason)
	a.wg.Add(1)
	go a.run(batch)
}

func snapshotAmbient(conversationID string, state *ambientState, reason GroupParticipationEvaluationReason) ambientBatch {
	messages := make([]GroupObservation, 0, len(state.messages))
	for _, entry := range state.messages {
		observation := entry.observation
		observation.IsNew = reason == GroupParticipationReasonMessage && entry.sequence > state.acceptedGeneration
		messages = append(messages, observation)
	}
	return ambientBatch{
		conversationID: conversationID, generation: state.generation,
		evaluationReason: reason, messages: messages,
	}
}

func (a *AmbientInbox) run(batch ambientBatch) {
	defer a.wg.Done()
	for {
		decision, err := a.decide(batch)
		if err != nil {
			a.finishOrRefresh(batch, err)
			return
		}
		a.mu.Lock()
		state := a.states[batch.conversationID]
		if state == nil || a.closed {
			a.mu.Unlock()
			return
		}
		if state.generation != batch.generation {
			batch = snapshotAmbient(batch.conversationID, state, GroupParticipationReasonMessage)
			a.mu.Unlock()
			continue
		}
		state.acceptedGeneration = batch.generation
		switch decision.Action {
		case GroupParticipationSilent:
			state.running = false
			a.mu.Unlock()
			return
		case GroupParticipationWait:
			if decision.WaitSeconds == nil || *decision.WaitSeconds < 1 || *decision.WaitSeconds > 300 {
				state.running = false
				a.mu.Unlock()
				return
			}
			state.running = false
			a.scheduleWaitLocked(batch.conversationID, state, time.Duration(*decision.WaitSeconds)*time.Second)
			a.mu.Unlock()
			return
		case GroupParticipationReply:
			if decision.TargetMessageID == nil || !ambientBatchContains(batch, *decision.TargetMessageID) {
				state.running = false
				a.mu.Unlock()
				return
			}
			target := *decision.TargetMessageID
			messages := append([]GroupObservation(nil), batch.messages...)
			conversationID := batch.conversationID
			generation := batch.generation
			a.mu.Unlock()
			input, err := FormatGroupTurnInput(messages, target)
			if err == nil {
				_, err = a.service.SubmitTurn(SubmitTurnRequest{
					ConversationID: conversationID,
					Input:          input,
					Surface:        SurfaceIMGroup,
				})
			}
			a.mu.Lock()
			state = a.states[conversationID]
			if state != nil && !a.closed && state.generation != generation {
				batch = snapshotAmbient(conversationID, state, GroupParticipationReasonMessage)
				a.mu.Unlock()
				if err != nil && a.service != nil && a.service.logger != nil {
					a.service.logger.Warn("ambient reply failed before refresh")
				}
				continue
			}
			if state != nil {
				state.running = false
			}
			a.mu.Unlock()
			return
		default:
			state.running = false
			a.mu.Unlock()
			return
		}
	}
}

func (a *AmbientInbox) decide(batch ambientBatch) (GroupParticipationResult, error) {
	if a.decideHook != nil {
		return a.decideHook(a.ctx, batch)
	}
	return a.service.DecideGroupParticipation(a.ctx, GroupParticipationRequest{
		ConversationID:   batch.conversationID,
		EvaluationReason: batch.evaluationReason,
		Messages:         batch.messages,
	})
}

func (a *AmbientInbox) finishOrRefresh(batch ambientBatch, err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	state := a.states[batch.conversationID]
	if state == nil || a.closed {
		return
	}
	if state.generation != batch.generation {
		batch = snapshotAmbient(batch.conversationID, state, GroupParticipationReasonMessage)
		state.running = true
		a.wg.Add(1)
		go a.run(batch)
		return
	}
	state.running = false
	_ = err
}

func (a *AmbientInbox) scheduleWaitLocked(conversationID string, state *ambientState, delay time.Duration) {
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
		a.startLocked(conversationID, current, GroupParticipationReasonWaitElapsed)
	})
}

func (a *AmbientInbox) cancelTimerLocked(state *ambientState) {
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

// FormatGroupTurnInput marks the reply target and formats ambient observations for SubmitTurn.
func FormatGroupTurnInput(messages []GroupObservation, targetMessageID string) (string, error) {
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
