package lifecycle

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"fairy/agent/reply"
	"fairy/runtime/model"
	"fairy/transport/session"
)

// Turn lifecycle and turn events mirror crates/fairy-domain/src/conversation.rs.

type State string

const (
	StateIdle         State = "idle"
	StateInterpreting State = "interpreting"
	StateGathering    State = "gathering"
	StatePlanning     State = "planning"
	StateResponding   State = "responding"
	StateCompleted    State = "completed"
	StateInterrupted  State = "interrupted"
	StateFailed       State = "failed"
)

var turnStateTransitions = map[State]map[State]struct{}{
	StateIdle: {
		StateInterpreting: {},
	},
	StateInterpreting: {
		StateGathering: {}, StateInterrupted: {}, StateFailed: {},
	},
	StateGathering: {
		StatePlanning: {}, StateInterrupted: {}, StateFailed: {},
	},
	StatePlanning: {
		StateResponding: {}, StateInterrupted: {}, StateFailed: {},
	},
	StateResponding: {
		StateCompleted: {}, StateInterrupted: {}, StateFailed: {},
	},
}

func (s State) canTransitionTo(next State) bool {
	_, allowed := turnStateTransitions[s][next]
	return allowed
}

type WireError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

type StateChangedPayload struct {
	Type string `json:"type"`
}

type ReplyChainPayload struct {
	Type        string                 `json:"type"`
	Index       uint8                  `json:"index"`
	Delta       string                 `json:"delta"`
	Text        string                 `json:"text"`
	VisualState string                 `json:"visualState"`
	Part        session.ExpressionPart `json:"part"`
}

type UtterancePayload struct {
	Type        string `json:"type"`
	Seq         uint8  `json:"seq"`
	Text        string `json:"text"`
	VisualState string `json:"visualState"`
	Reason      string `json:"reason"`
}

type PresencePayload struct {
	Type  string `json:"type"`
	Phase string `json:"phase"`
}

type ReplyPreviewPayload struct {
	Type   string             `json:"type"`
	Chains []reply.ReplyChain `json:"chains"`
}

// BeatReadyPayload is the ordered display delivery unit.
type BeatReadyPayload struct {
	Type                 string                  `json:"type"`
	BeatID               string                  `json:"beatId"`
	Kind                 string                  `json:"kind"` // utterance | final
	Index                uint8                   `json:"index"`
	ChainIndex           int                     `json:"chainIndex"`
	DisplayText          string                  `json:"displayText"`
	VisualState          string                  `json:"visualState"`
	TargetIntervalMS     int64                   `json:"targetIntervalMs"`
	PaceWaitMS           int64                   `json:"paceWaitMs"`
	PublishedPrefixCount int                     `json:"publishedPrefixCount"`
	Reason               string                  `json:"reason,omitempty"`
	ReplyTargetMessageID string                  `json:"replyTargetMessageId,omitempty"`
	Part                 *session.ExpressionPart `json:"part,omitempty"`
}

type CompletedPayload struct {
	Type                string                 `json:"type"`
	Text                string                 `json:"text"`
	Sources             []any                  `json:"sources"`
	CharacterRevision   uint64                 `json:"characterRevision"`
	UserProfileRevision *uint64                `json:"userProfileRevision"`
	Usage               []model.LaneModelUsage `json:"usage"`
	VisualState         string                 `json:"visualState"`
	Chains              []reply.ReplyChain     `json:"chains"`
}

type FailedPayload struct {
	Type  string    `json:"type"`
	Error WireError `json:"error"`
}

type Completion struct {
	Text                string
	Sources             []any
	CharacterRevision   uint64
	UserProfileRevision *uint64
	Usage               []model.LaneModelUsage
	VisualState         string
	Chains              []reply.ReplyChain
}

type EventEmitter func(session.Event)

type Lifecycle struct {
	mu             sync.Mutex
	conversationID string
	turnID         string
	state          State
	nextSequence   uint64
}

func New(conversationID string, turnID string) *Lifecycle {
	return &Lifecycle{
		conversationID: conversationID,
		turnID:         turnID,
		state:          StateIdle,
		nextSequence:   1,
	}
}

// Publish serializes one lifecycle mutation and its event construction. The
// caller must perform the mutation inside produce; lifecycle methods remain
// intentionally small and deterministic.
func (l *Lifecycle) Publish(produce func() (session.Event, error)) (session.Event, error) {
	if l == nil {
		return session.Event{}, errors.New("nil turn lifecycle")
	}
	if produce == nil {
		return session.Event{}, errors.New("nil turn lifecycle mutation")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return produce()
}

func (l *Lifecycle) State() State {
	if l == nil {
		return StateIdle
	}
	return l.state
}

func (l *Lifecycle) Transition(next State) (session.Event, error) {
	if !l.state.canTransitionTo(next) {
		return session.Event{}, fmt.Errorf("invalid turn state transition from %s to %s", l.state, next)
	}
	l.state = next
	return l.event(StateChangedPayload{Type: "state_changed"})
}

func (l *Lifecycle) ReplyChain(index uint8, delta string, chain reply.ReplyChain) (session.Event, error) {
	if l.state != StateResponding {
		return session.Event{}, errors.New("只有 Responding 状态可以发送回复分段")
	}
	if delta == "" && chain.Kind != reply.ChainSticker {
		return session.Event{}, errors.New("回复分段增量不能为空")
	}
	return l.event(ReplyChainPayload{
		Type:        "reply_chain",
		Index:       index,
		Delta:       delta,
		Text:        chain.Text,
		VisualState: chain.VisualState,
		Part:        sessionExpressionPart(chain),
	})
}

// Utterance emits a progressive in-character line during gathering/planning.
// It does not enter transcript; final reply_chain remains the persisted answer.
func (l *Lifecycle) Utterance(seq uint8, text string, visualState string, reason string) (session.Event, error) {
	if l.state != StatePlanning && l.state != StateGathering {
		return session.Event{}, errors.New("只有 Gathering/Planning 状态可以发送 progressive utterance")
	}
	if strings.TrimSpace(text) == "" {
		return session.Event{}, errors.New("utterance text cannot be empty")
	}
	if reason == "" {
		reason = "thinking"
	}
	if visualState == "" {
		visualState = "idle"
	}
	return l.event(UtterancePayload{
		Type:        "utterance",
		Seq:         seq,
		Text:        text,
		VisualState: visualState,
		Reason:      reason,
	})
}

// Presence and Preview are temporary events. They are never transcript
// messages; beat.ready and completed remain the final display contract.
func (l *Lifecycle) Presence(phase string) (session.Event, error) {
	if l.state != StatePlanning && l.state != StateGathering {
		return session.Event{}, errors.New("只有 Gathering/Planning 状态可以发送 presence")
	}
	if strings.TrimSpace(phase) == "" {
		phase = "model_stream"
	}
	return l.event(PresencePayload{Type: "presence", Phase: phase})
}

func (l *Lifecycle) ReplyPreview(chains []reply.ReplyChain) (session.Event, error) {
	if l.state != StatePlanning && l.state != StateGathering {
		return session.Event{}, errors.New("只有 Gathering/Planning 状态可以发送 reply.preview")
	}
	if len(chains) == 0 {
		return session.Event{}, errors.New("reply.preview chains cannot be empty")
	}
	copyChains := append([]reply.ReplyChain(nil), chains...)
	return l.event(ReplyPreviewPayload{Type: "reply.preview", Chains: copyChains})
}

func (l *Lifecycle) Fail(code string, message string, retryable bool) (session.Event, error) {
	if !l.state.canTransitionTo(StateFailed) {
		return session.Event{}, fmt.Errorf("invalid turn state transition from %s to failed", l.state)
	}
	l.state = StateFailed
	return l.event(FailedPayload{
		Type: "failed",
		Error: WireError{
			Code:      code,
			Message:   message,
			Retryable: retryable,
		},
	})
}

func (l *Lifecycle) Complete(completion Completion) (session.Event, error) {
	if !l.state.canTransitionTo(StateCompleted) {
		return session.Event{}, fmt.Errorf("invalid turn state transition from %s to completed", l.state)
	}
	l.state = StateCompleted
	sources := completion.Sources
	if sources == nil {
		sources = []any{}
	}
	usage := completion.Usage
	if usage == nil {
		usage = []model.LaneModelUsage{}
	}
	return l.event(CompletedPayload{
		Type:                "completed",
		Text:                completion.Text,
		Sources:             sources,
		CharacterRevision:   completion.CharacterRevision,
		UserProfileRevision: completion.UserProfileRevision,
		Usage:               usage,
		VisualState:         completion.VisualState,
		Chains:              completion.Chains,
	})
}

// BeatReady emits an ordered display beat. It is allowed in gathering/planning
// for progressive utterances and in responding for final chains.
func (l *Lifecycle) BeatReady(completion reply.BeatReadyCompletion) (session.Event, error) {
	if l.state != StatePlanning && l.state != StateGathering && l.state != StateResponding {
		return session.Event{}, errors.New("只有 Gathering/Planning/Responding 状态可以发送 beat.ready")
	}
	stickerBeat := completion.Chain != nil && completion.Chain.Kind == reply.ChainSticker
	if strings.TrimSpace(completion.DisplayText) == "" && !stickerBeat {
		return session.Event{}, errors.New("beat.ready displayText cannot be empty")
	}
	if completion.BeatID == "" {
		return session.Event{}, errors.New("beat.ready beatId cannot be empty")
	}
	kind := completion.Kind
	if kind == "" {
		kind = "utterance"
	}
	visual := completion.VisualState
	if visual == "" {
		visual = "idle"
	}
	payload := BeatReadyPayload{
		Type:                 "beat.ready",
		BeatID:               completion.BeatID,
		Kind:                 kind,
		Index:                completion.Index,
		ChainIndex:           completion.ChainIndex,
		DisplayText:          completion.DisplayText,
		VisualState:          visual,
		TargetIntervalMS:     completion.TargetIntervalMS,
		PaceWaitMS:           completion.PaceWaitMS,
		PublishedPrefixCount: completion.PublishedPrefixCount,
		Reason:               completion.Reason,
		ReplyTargetMessageID: completion.ReplyTargetMessageID,
	}
	if completion.Chain != nil {
		part := sessionExpressionPart(*completion.Chain)
		payload.Part = &part
	}
	return l.event(payload)
}

func (l *Lifecycle) Interrupt() (session.Event, error) {
	if !l.state.canTransitionTo(StateInterrupted) {
		return session.Event{}, fmt.Errorf("invalid turn state transition from %s to interrupted", l.state)
	}
	l.state = StateInterrupted
	return l.event(StateChangedPayload{Type: "state_changed"})
}

func (l *Lifecycle) event(payload any) (session.Event, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return session.Event{}, fmt.Errorf("encode turn event payload: %w", err)
	}
	event := session.Event{
		ConversationID: l.conversationID,
		TurnID:         l.turnID,
		Sequence:       l.nextSequence,
		State:          string(l.state),
		Payload:        raw,
	}
	l.nextSequence++
	return event, nil
}

func sessionExpressionPart(chain reply.ReplyChain) session.ExpressionPart {
	if chain.Kind == reply.ChainSticker && chain.Sticker != nil {
		return session.ExpressionPart{
			Kind:        session.ExpressionSticker,
			VisualState: chain.VisualState,
			Sticker: &session.StickerReference{
				ID: chain.Sticker.ID, Description: chain.Sticker.Description, MIMEType: chain.Sticker.MIMEType,
			},
		}
	}
	return session.ExpressionPart{
		Kind: session.ExpressionUtterance, Text: chain.Text, VisualState: chain.VisualState,
	}
}
