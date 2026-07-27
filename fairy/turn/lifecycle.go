package turn

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"fairy/model"
	"fairy/pkg/statemachine"
	"fairy/reply"
)

// Turn lifecycle and turn events mirror crates/fairy-domain/src/conversation.rs.

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

var turnStateTransitions = statemachine.MustTable(
	statemachine.Edge[TurnState]{From: TurnStateIdle, To: TurnStateInterpreting},
	statemachine.Edge[TurnState]{From: TurnStateInterpreting, To: TurnStateGathering},
	statemachine.Edge[TurnState]{From: TurnStateInterpreting, To: TurnStateInterrupted},
	statemachine.Edge[TurnState]{From: TurnStateInterpreting, To: TurnStateFailed},
	statemachine.Edge[TurnState]{From: TurnStateGathering, To: TurnStatePlanning},
	statemachine.Edge[TurnState]{From: TurnStateGathering, To: TurnStateInterrupted},
	statemachine.Edge[TurnState]{From: TurnStateGathering, To: TurnStateFailed},
	statemachine.Edge[TurnState]{From: TurnStatePlanning, To: TurnStateResponding},
	statemachine.Edge[TurnState]{From: TurnStatePlanning, To: TurnStateInterrupted},
	statemachine.Edge[TurnState]{From: TurnStatePlanning, To: TurnStateFailed},
	statemachine.Edge[TurnState]{From: TurnStateResponding, To: TurnStateCompleted},
	statemachine.Edge[TurnState]{From: TurnStateResponding, To: TurnStateInterrupted},
	statemachine.Edge[TurnState]{From: TurnStateResponding, To: TurnStateFailed},
)

func (s TurnState) canTransitionTo(next TurnState) bool { return turnStateTransitions.Allows(s, next) }

type WireError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

type StateChangedPayload struct {
	Type string `json:"type"`
}

type ReplyChainPayload struct {
	Type        string `json:"type"`
	Index       uint8  `json:"index"`
	Delta       string `json:"delta"`
	Text        string `json:"text"`
	SpeechText  string `json:"speechText"`
	VisualState string `json:"visualState"`
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

// BeatReadyPayload is the paired text(+optional audio) delivery unit. Frontend
// reveals a beat only after this event (齐套才揭示).
type BeatReadyPayload struct {
	Type                 string `json:"type"`
	BeatID               string `json:"beatId"`
	Kind                 string `json:"kind"` // utterance | final
	Index                uint8  `json:"index"`
	ChainIndex           int    `json:"chainIndex"`
	DisplayText          string `json:"displayText"`
	SpeechText           string `json:"speechText"`
	VisualState          string `json:"visualState"`
	TargetIntervalMS     int64  `json:"targetIntervalMs"`
	PaceWaitMS           int64  `json:"paceWaitMs"`
	PublishedPrefixCount int    `json:"publishedPrefixCount"`
	Reason               string `json:"reason,omitempty"`
	SpeakerID            string `json:"speakerId,omitempty"`
	MimeType             string `json:"mimeType,omitempty"`
	Format               string `json:"format,omitempty"`
	DataURL              string `json:"dataUrl,omitempty"`
}

type CompletedPayload struct {
	Type                string                 `json:"type"`
	Text                string                 `json:"text"`
	SpeechText          string                 `json:"speechText"`
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

type SpeechRequestedPayload struct {
	Type                string  `json:"type"`
	Text                string  `json:"text"`
	CharacterRevision   uint64  `json:"characterRevision"`
	UserProfileRevision *uint64 `json:"userProfileRevision"`
}

type SpeechSynthesizedPayload struct {
	Type string `json:"type"`
	// Index is the monotonic playback order across the whole turn (utterance audio
	// first, then reply chains), used by the frontend to order playback.
	Index uint8 `json:"index"`
	// ChainIndex is the reply-chain index this audio belongs to, or -1 for
	// mid-ReAct utterance audio (which must not drive reply-chain bubble reveal).
	ChainIndex int    `json:"chainIndex"`
	Text       string `json:"text"`
	SpeakerID  string `json:"speakerId"`
	MimeType   string `json:"mimeType"`
	Format     string `json:"format"`
	DataURL    string `json:"dataUrl"`
}

type SpeechFailedPayload struct {
	Type  string    `json:"type"`
	Error WireError `json:"error"`
}

type TurnEvent struct {
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
	Usage               []model.LaneModelUsage
	VisualState         string
	Chains              []reply.ReplyChain
}

type SpeechSynthesisCompletion struct {
	// Index is the monotonic playback order across the turn.
	Index uint8
	// ChainIndex is the reply-chain index, or -1 for mid-ReAct utterance audio.
	ChainIndex int
	Text       string
	Result     reply.SpeechSynthesisResult
}

type EventEmitter func(TurnEvent)

type TurnLifecycle struct {
	mu             sync.Mutex
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

// Publish serializes one lifecycle mutation and its event construction. The
// caller must perform the mutation inside produce; lifecycle methods remain
// intentionally small and deterministic.
func (l *TurnLifecycle) Publish(produce func() (TurnEvent, error)) (TurnEvent, error) {
	if l == nil {
		return TurnEvent{}, errors.New("nil turn lifecycle")
	}
	if produce == nil {
		return TurnEvent{}, errors.New("nil turn lifecycle mutation")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return produce()
}

func (l *TurnLifecycle) State() TurnState {
	if l == nil {
		return TurnStateIdle
	}
	return l.state
}

func (l *TurnLifecycle) Transition(next TurnState) (TurnEvent, error) {
	if !l.state.canTransitionTo(next) {
		return TurnEvent{}, fmt.Errorf("invalid turn state transition from %s to %s", l.state, next)
	}
	l.state = next
	return l.event(StateChangedPayload{Type: "state_changed"}), nil
}

func (l *TurnLifecycle) ReplyChain(index uint8, delta string, chain reply.ReplyChain) (TurnEvent, error) {
	if l.state != TurnStateResponding {
		return TurnEvent{}, errors.New("只有 Responding 状态可以发送回复分段")
	}
	if delta == "" {
		return TurnEvent{}, errors.New("回复分段增量不能为空")
	}
	return l.event(ReplyChainPayload{
		Type:        "reply_chain",
		Index:       index,
		Delta:       delta,
		Text:        chain.Text,
		SpeechText:  chain.SpeechText,
		VisualState: chain.VisualState,
	}), nil
}

// Utterance emits a progressive in-character line during gathering/planning.
// It does not enter transcript; final reply_chain remains the persisted answer.
func (l *TurnLifecycle) Utterance(seq uint8, text string, visualState string, reason string) (TurnEvent, error) {
	if l.state != TurnStatePlanning && l.state != TurnStateGathering {
		return TurnEvent{}, errors.New("只有 Gathering/Planning 状态可以发送 progressive utterance")
	}
	if strings.TrimSpace(text) == "" {
		return TurnEvent{}, errors.New("utterance text cannot be empty")
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
	}), nil
}

// Presence and Preview are temporary events. They are never transcript
// messages; beat.ready and completed remain the final display contract.
func (l *TurnLifecycle) Presence(phase string) (TurnEvent, error) {
	if l.state != TurnStatePlanning && l.state != TurnStateGathering {
		return TurnEvent{}, errors.New("只有 Gathering/Planning 状态可以发送 presence")
	}
	if strings.TrimSpace(phase) == "" {
		phase = "model_stream"
	}
	return l.event(PresencePayload{Type: "presence", Phase: phase}), nil
}

func (l *TurnLifecycle) ReplyPreview(chains []reply.ReplyChain) (TurnEvent, error) {
	if l.state != TurnStatePlanning && l.state != TurnStateGathering {
		return TurnEvent{}, errors.New("只有 Gathering/Planning 状态可以发送 reply.preview")
	}
	if len(chains) == 0 {
		return TurnEvent{}, errors.New("reply.preview chains cannot be empty")
	}
	copyChains := append([]reply.ReplyChain(nil), chains...)
	return l.event(ReplyPreviewPayload{Type: "reply.preview", Chains: copyChains}), nil
}

func (l *TurnLifecycle) Fail(code string, message string, retryable bool) (TurnEvent, error) {
	if !l.state.canTransitionTo(TurnStateFailed) {
		return TurnEvent{}, fmt.Errorf("invalid turn state transition from %s to failed", l.state)
	}
	l.state = TurnStateFailed
	return l.event(FailedPayload{
		Type: "failed",
		Error: WireError{
			Code:      code,
			Message:   message,
			Retryable: retryable,
		},
	}), nil
}

func (l *TurnLifecycle) Complete(completion TurnCompletion) (TurnEvent, error) {
	if !l.state.canTransitionTo(TurnStateCompleted) {
		return TurnEvent{}, fmt.Errorf("invalid turn state transition from %s to completed", l.state)
	}
	l.state = TurnStateCompleted
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
		SpeechText:          completion.SpeechText,
		Sources:             sources,
		CharacterRevision:   completion.CharacterRevision,
		UserProfileRevision: completion.UserProfileRevision,
		Usage:               usage,
		VisualState:         completion.VisualState,
		Chains:              completion.Chains,
	}), nil
}

func (l *TurnLifecycle) SpeechRequested(completion TurnCompletion) (TurnEvent, error) {
	if l.state != TurnStateCompleted && l.state != TurnStatePlanning && l.state != TurnStateResponding {
		return TurnEvent{}, errors.New("只有 Planning/Responding/Completed 状态可以请求语音")
	}
	return l.event(SpeechRequestedPayload{
		Type:                "speech.requested",
		Text:                completion.SpeechText,
		CharacterRevision:   completion.CharacterRevision,
		UserProfileRevision: completion.UserProfileRevision,
	}), nil
}

func (l *TurnLifecycle) SpeechSynthesized(completion SpeechSynthesisCompletion) (TurnEvent, error) {
	if l.state != TurnStateCompleted && l.state != TurnStatePlanning && l.state != TurnStateResponding {
		return TurnEvent{}, errors.New("只有 Planning/Responding/Completed 状态可以完成语音合成")
	}
	return l.event(SpeechSynthesizedPayload{
		Type:       "speech.synthesized",
		Index:      completion.Index,
		ChainIndex: completion.ChainIndex,
		Text:       completion.Text,
		SpeakerID:  completion.Result.SpeakerID,
		MimeType:   completion.Result.MimeType,
		Format:     completion.Result.Format,
		DataURL:    completion.Result.DataURL,
	}), nil
}

// BeatReady emits a paired display(+optional audio) beat. Allowed in planning
// (utterance beats) and responding (final beats).
func (l *TurnLifecycle) BeatReady(completion reply.BeatReadyCompletion) (TurnEvent, error) {
	if l.state != TurnStatePlanning && l.state != TurnStateGathering && l.state != TurnStateResponding {
		return TurnEvent{}, errors.New("只有 Gathering/Planning/Responding 状态可以发送 beat.ready")
	}
	if strings.TrimSpace(completion.DisplayText) == "" {
		return TurnEvent{}, errors.New("beat.ready displayText cannot be empty")
	}
	if completion.BeatID == "" {
		return TurnEvent{}, errors.New("beat.ready beatId cannot be empty")
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
		SpeechText:           completion.SpeechText,
		VisualState:          visual,
		TargetIntervalMS:     completion.TargetIntervalMS,
		PaceWaitMS:           completion.PaceWaitMS,
		PublishedPrefixCount: completion.PublishedPrefixCount,
		Reason:               completion.Reason,
	}
	if completion.Audio != nil {
		payload.SpeakerID = completion.Audio.SpeakerID
		payload.MimeType = completion.Audio.MimeType
		payload.Format = completion.Audio.Format
		payload.DataURL = completion.Audio.DataURL
	}
	return l.event(payload), nil
}

func (l *TurnLifecycle) SpeechFailed(code string, message string, retryable bool) (TurnEvent, error) {
	if l.state != TurnStateCompleted && l.state != TurnStatePlanning && l.state != TurnStateResponding {
		return TurnEvent{}, errors.New("只有 Planning/Responding/Completed 状态可以发送语音失败事件")
	}
	return l.event(SpeechFailedPayload{Type: "speech.failed", Error: WireError{Code: code, Message: message, Retryable: retryable}}), nil
}

func (l *TurnLifecycle) Interrupt() (TurnEvent, error) {
	if !l.state.canTransitionTo(TurnStateInterrupted) {
		return TurnEvent{}, fmt.Errorf("invalid turn state transition from %s to interrupted", l.state)
	}
	l.state = TurnStateInterrupted
	return l.event(StateChangedPayload{Type: "state_changed"}), nil
}

func (l *TurnLifecycle) event(payload any) TurnEvent {
	event := TurnEvent{
		ConversationID: l.conversationID,
		TurnID:         l.turnID,
		Sequence:       l.nextSequence,
		State:          l.state,
		Payload:        payload,
	}
	l.nextSequence++
	return event
}
