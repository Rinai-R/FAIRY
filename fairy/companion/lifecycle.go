package companion

import (
	"errors"
	"fmt"
)

// Turn lifecycle and harness events mirror crates/fairy-domain/src/conversation.rs.

type TurnState string

const (
	TurnStateIdle         TurnState = "idle"
	TurnStateInterpreting TurnState = "interpreting"
	TurnStateGathering    TurnState = "gathering"
	TurnStatePlanning     TurnState = "planning"
	TurnStateResponding   TurnState = "responding"
	TurnStateCompleted    TurnState = "completed"
	TurnStateInterrupted  TurnState = "interrupted"
	TurnStateFailed       TurnState = "failed"
)

func (s TurnState) canTransitionTo(next TurnState) bool {
	switch s {
	case TurnStateIdle:
		return next == TurnStateInterpreting
	case TurnStateInterpreting:
		return next == TurnStateGathering || next == TurnStateInterrupted || next == TurnStateFailed
	case TurnStateGathering:
		return next == TurnStatePlanning || next == TurnStateInterrupted || next == TurnStateFailed
	case TurnStatePlanning:
		return next == TurnStateResponding || next == TurnStateInterrupted || next == TurnStateFailed
	case TurnStateResponding:
		return next == TurnStateCompleted || next == TurnStateInterrupted || next == TurnStateFailed
	default:
		return false
	}
}

type WireError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

// CachedTokenObservation mirrors fairy-domain CachedTokenObservation wire JSON:
// unsupported/missing → {"status":"..."} ; observed → {"status":"observed","tokens":N}.
type CachedTokenObservation struct {
	Status string  `json:"status"`
	Tokens *uint64 `json:"tokens,omitempty"`
}

func CacheUnsupported() CachedTokenObservation {
	return CachedTokenObservation{Status: "unsupported"}
}

func CacheMissing() CachedTokenObservation {
	return CachedTokenObservation{Status: "missing"}
}

func CacheObserved(tokens uint64) CachedTokenObservation {
	return CachedTokenObservation{Status: "observed", Tokens: &tokens}
}

type LaneUsage struct {
	InputTokens       *uint64                `json:"inputTokens"`
	OutputTokens      *uint64                `json:"outputTokens"`
	CachedInputTokens CachedTokenObservation `json:"cachedInputTokens"`
	CacheWriteTokens  CachedTokenObservation `json:"cacheWriteTokens"`
}

type LaneModelUsage struct {
	Lane          string    `json:"lane"`
	HistoryWindow uint64    `json:"historyWindow"`
	Usage         LaneUsage `json:"usage"`
}

type stateChangedPayload struct {
	Type string `json:"type"`
}

type replyChainPayload struct {
	Type        string `json:"type"`
	Index       uint8  `json:"index"`
	Delta       string `json:"delta"`
	Text        string `json:"text"`
	SpeechText  string `json:"speechText"`
	VisualState string `json:"visualState"`
}

type completedPayload struct {
	Type                string           `json:"type"`
	Text                string           `json:"text"`
	SpeechText          string           `json:"speechText"`
	Sources             []any            `json:"sources"`
	CharacterRevision   uint64           `json:"characterRevision"`
	UserProfileRevision *uint64          `json:"userProfileRevision"`
	Usage               []LaneModelUsage `json:"usage"`
	VisualState         string           `json:"visualState"`
	Chains              []ReplyChain     `json:"chains"`
}

type failedPayload struct {
	Type  string    `json:"type"`
	Error WireError `json:"error"`
}

type HarnessEvent struct {
	ConversationID string    `json:"conversationId"`
	TurnID         string    `json:"turnId"`
	Sequence       uint64    `json:"sequence"`
	State          TurnState `json:"state"`
	Payload        any       `json:"payload"`
}

type TurnCompletion struct {
	Text                string
	SpeechText          string
	Sources             []any
	CharacterRevision   uint64
	UserProfileRevision *uint64
	Usage               []LaneModelUsage
	VisualState         string
	Chains              []ReplyChain
}

type EventEmitter func(HarnessEvent)

type TurnLifecycle struct {
	conversationID string
	turnID         string
	state          TurnState
	nextSequence   uint64
}

func NewTurnLifecycle(conversationID string, turnID string) *TurnLifecycle {
	return &TurnLifecycle{
		conversationID: conversationID,
		turnID:         turnID,
		state:          TurnStateIdle,
		nextSequence:   1,
	}
}

func (l *TurnLifecycle) State() TurnState {
	return l.state
}

func (l *TurnLifecycle) Transition(next TurnState) (HarnessEvent, error) {
	if !l.state.canTransitionTo(next) {
		return HarnessEvent{}, fmt.Errorf("invalid turn state transition from %s to %s", l.state, next)
	}
	l.state = next
	return l.event(stateChangedPayload{Type: "state_changed"}), nil
}

func (l *TurnLifecycle) ReplyChain(index uint8, delta string, chain ReplyChain) (HarnessEvent, error) {
	if l.state != TurnStateResponding {
		return HarnessEvent{}, errors.New("只有 Responding 状态可以发送回复分段")
	}
	if delta == "" {
		return HarnessEvent{}, errors.New("回复分段增量不能为空")
	}
	return l.event(replyChainPayload{
		Type:        "reply_chain",
		Index:       index,
		Delta:       delta,
		Text:        chain.Text,
		SpeechText:  chain.SpeechText,
		VisualState: chain.VisualState,
	}), nil
}

func (l *TurnLifecycle) Fail(code string, message string, retryable bool) (HarnessEvent, error) {
	if !l.state.canTransitionTo(TurnStateFailed) {
		return HarnessEvent{}, fmt.Errorf("invalid turn state transition from %s to failed", l.state)
	}
	l.state = TurnStateFailed
	return l.event(failedPayload{
		Type: "failed",
		Error: WireError{
			Code:      code,
			Message:   message,
			Retryable: retryable,
		},
	}), nil
}

func (l *TurnLifecycle) Complete(completion TurnCompletion) (HarnessEvent, error) {
	if !l.state.canTransitionTo(TurnStateCompleted) {
		return HarnessEvent{}, fmt.Errorf("invalid turn state transition from %s to completed", l.state)
	}
	l.state = TurnStateCompleted
	sources := completion.Sources
	if sources == nil {
		sources = []any{}
	}
	usage := completion.Usage
	if usage == nil {
		usage = []LaneModelUsage{}
	}
	return l.event(completedPayload{
		Type:                "completed",
		Text:                completion.Text,
		SpeechText:          completion.SpeechText,
		Sources:             sources,
		CharacterRevision:   completion.CharacterRevision,
		UserProfileRevision: completion.UserProfileRevision,
		Usage:               usage,
		VisualState:         completion.VisualState,
		Chains:              completion.Chains,
	}), nil
}

func (l *TurnLifecycle) Interrupt() (HarnessEvent, error) {
	if !l.state.canTransitionTo(TurnStateInterrupted) {
		return HarnessEvent{}, fmt.Errorf("invalid turn state transition from %s to interrupted", l.state)
	}
	l.state = TurnStateInterrupted
	return l.event(stateChangedPayload{Type: "state_changed"}), nil
}

func (l *TurnLifecycle) event(payload any) HarnessEvent {
	event := HarnessEvent{
		ConversationID: l.conversationID,
		TurnID:         l.turnID,
		Sequence:       l.nextSequence,
		State:          l.state,
		Payload:        payload,
	}
	l.nextSequence++
	return event
}
