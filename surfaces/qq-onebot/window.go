package main

import (
	"context"
	"errors"
	"sync"
	"time"

	"fairy/coreclient"
)

const maxGroupWindowMessages = 20

type groupWindowBatch struct {
	groupID          int64
	generation       uint64
	evaluationReason string
	messages         []coreclient.GroupObservation
	send             func(string) error
}

type groupWindowDecision struct {
	coreclient.GroupParticipationResponse
	conversationID string
}

type sequencedGroupObservation struct {
	sequence    uint64
	observation coreclient.GroupObservation
}

type stoppableTimer interface {
	Stop() bool
}

type groupWindowState struct {
	messages           []sequencedGroupObservation
	send               func(string) error
	generation         uint64
	acceptedGeneration uint64
	running            bool
	timer              stoppableTimer
	timerOwner         uint64
}

type groupWindow struct {
	ctx     context.Context
	cancel  context.CancelFunc
	decide  func(context.Context, groupWindowBatch) (groupWindowDecision, error)
	reply   func(context.Context, groupWindowBatch, groupWindowDecision) error
	onError func(int64, error)
	after   func(time.Duration, func()) stoppableTimer

	mu     sync.Mutex
	groups map[int64]*groupWindowState
	closed bool
	wg     sync.WaitGroup
}

func newGroupWindow(
	ctx context.Context,
	decide func(context.Context, groupWindowBatch) (groupWindowDecision, error),
	reply func(context.Context, groupWindowBatch, groupWindowDecision) error,
	onError func(int64, error),
) (*groupWindow, error) {
	if ctx == nil || decide == nil || reply == nil {
		return nil, errors.New("group window context, decision, and reply callbacks are required")
	}
	windowCtx, cancel := context.WithCancel(ctx)
	w := &groupWindow{
		ctx: windowCtx, cancel: cancel, decide: decide, reply: reply, onError: onError,
		after:  func(delay time.Duration, callback func()) stoppableTimer { return time.AfterFunc(delay, callback) },
		groups: make(map[int64]*groupWindowState),
	}
	go func() {
		<-windowCtx.Done()
		w.stop()
	}()
	return w, nil
}

func (w *groupWindow) Add(groupID int64, observation coreclient.GroupObservation, send func(string) error) error {
	if w == nil || groupID <= 0 || send == nil {
		return errors.New("group window is not configured")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return context.Canceled
	}
	state := w.groups[groupID]
	if state == nil {
		state = &groupWindowState{}
		w.groups[groupID] = state
	}
	state.generation++
	observation.IsNew = false
	state.messages = append(state.messages, sequencedGroupObservation{sequence: state.generation, observation: observation})
	if len(state.messages) > maxGroupWindowMessages {
		state.messages = state.messages[len(state.messages)-maxGroupWindowMessages:]
	}
	state.send = send
	w.cancelTimerLocked(state)
	if !state.running {
		w.startLocked(groupID, state, "message")
	}
	return nil
}

func (w *groupWindow) startLocked(groupID int64, state *groupWindowState, reason string) {
	if w.closed || state.running || len(state.messages) == 0 {
		return
	}
	state.running = true
	batch := snapshotGroupWindow(groupID, state, reason)
	w.wg.Add(1)
	go w.run(groupID, batch)
}

func snapshotGroupWindow(groupID int64, state *groupWindowState, reason string) groupWindowBatch {
	messages := make([]coreclient.GroupObservation, 0, len(state.messages))
	for _, entry := range state.messages {
		observation := entry.observation
		observation.IsNew = reason == "message" && entry.sequence > state.acceptedGeneration
		messages = append(messages, observation)
	}
	return groupWindowBatch{
		groupID: groupID, generation: state.generation, evaluationReason: reason,
		messages: messages, send: state.send,
	}
}

func (w *groupWindow) run(groupID int64, batch groupWindowBatch) {
	defer w.wg.Done()
	for {
		decision, err := w.decide(w.ctx, batch)
		if err != nil {
			w.report(groupID, err)
			w.mu.Lock()
			state := w.groups[groupID]
			if state != nil && !w.closed && state.generation != batch.generation {
				batch = snapshotGroupWindow(groupID, state, "message")
				w.mu.Unlock()
				continue
			}
			if state != nil {
				state.running = false
			}
			w.mu.Unlock()
			return
		}

		w.mu.Lock()
		state := w.groups[groupID]
		if state == nil || w.closed {
			w.mu.Unlock()
			return
		}
		if state.generation != batch.generation {
			batch = snapshotGroupWindow(groupID, state, "message")
			w.mu.Unlock()
			continue
		}
		state.acceptedGeneration = batch.generation
		switch decision.Action {
		case "silent":
			state.running = false
			w.mu.Unlock()
			return
		case "wait":
			if decision.WaitSeconds == nil || *decision.WaitSeconds < 1 || *decision.WaitSeconds > 300 {
				state.running = false
				w.mu.Unlock()
				w.report(groupID, errors.New("invalid wait participation action"))
				return
			}
			state.running = false
			w.scheduleWaitLocked(groupID, state, time.Duration(*decision.WaitSeconds)*time.Second)
			w.mu.Unlock()
			return
		case "reply":
			if decision.TargetMessageID == nil || !batchContainsMessageID(batch, *decision.TargetMessageID) {
				state.running = false
				w.mu.Unlock()
				w.report(groupID, errors.New("invalid reply participation target"))
				return
			}
			w.mu.Unlock()
			err = w.reply(w.ctx, batch, decision)
			if err != nil {
				w.report(groupID, err)
			}
			w.mu.Lock()
			state = w.groups[groupID]
			if state != nil && !w.closed && state.generation != batch.generation {
				batch = snapshotGroupWindow(groupID, state, "message")
				w.mu.Unlock()
				continue
			}
			if state != nil {
				state.running = false
			}
			w.mu.Unlock()
			return
		default:
			state.running = false
			w.mu.Unlock()
			w.report(groupID, errors.New("invalid group participation action"))
			return
		}
	}
}

func batchContainsMessageID(batch groupWindowBatch, target string) bool {
	for _, observation := range batch.messages {
		if observation.MessageID == target {
			return true
		}
	}
	return false
}

func (w *groupWindow) scheduleWaitLocked(groupID int64, state *groupWindowState, delay time.Duration) {
	state.timerOwner++
	owner := state.timerOwner
	state.timer = w.after(delay, func() {
		w.mu.Lock()
		defer w.mu.Unlock()
		if w.closed {
			return
		}
		current := w.groups[groupID]
		if current == nil || current.timerOwner != owner || current.running || current.generation != current.acceptedGeneration {
			return
		}
		current.timer = nil
		w.startLocked(groupID, current, "wait_elapsed")
	})
}

func (w *groupWindow) cancelTimerLocked(state *groupWindowState) {
	state.timerOwner++
	if state.timer != nil {
		state.timer.Stop()
		state.timer = nil
	}
}

func (w *groupWindow) report(groupID int64, err error) {
	if err != nil && w.onError != nil && !errors.Is(err, context.Canceled) {
		w.onError(groupID, err)
	}
}

func (w *groupWindow) Close() {
	if w == nil {
		return
	}
	w.cancel()
	w.stop()
	w.wg.Wait()
}

func (w *groupWindow) stop() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return
	}
	w.closed = true
	for _, state := range w.groups {
		w.cancelTimerLocked(state)
	}
}
