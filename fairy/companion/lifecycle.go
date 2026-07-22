package companion

import (
	"errors"
	"fmt"
	"strings"
	"sync"
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

type utterancePayload struct {
	Type        string `json:"type"`
	Seq         uint8  `json:"seq"`
	Text        string `json:"text"`
	VisualState string `json:"visualState"`
	Reason      string `json:"reason"`
}

type presencePayload struct {
	Type  string `json:"type"`
	Phase string `json:"phase"`
}

type replyPreviewPayload struct {
	Type   string       `json:"type"`
	Chains []ReplyChain `json:"chains"`
}

// beatReadyPayload is the paired text(+optional audio) delivery unit. Frontend
// reveals a beat only after this event (齐套才揭示).
type beatReadyPayload struct {
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

// BeatReadyCompletion is the lifecycle input for a paired beat delivery.
type BeatReadyCompletion struct {
	BeatID               string
	Kind                 string
	Index                uint8
	ChainIndex           int
	DisplayText          string
	SpeechText           string
	VisualState          string
	TargetIntervalMS     int64
	PaceWaitMS           int64
	PublishedPrefixCount int
	Reason               string
	Audio                *SpeechSynthesisResult
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

type speechRequestedPayload struct {
	Type                string  `json:"type"`
	Text                string  `json:"text"`
	CharacterRevision   uint64  `json:"characterRevision"`
	UserProfileRevision *uint64 `json:"userProfileRevision"`
}

type speechSynthesizedPayload struct {
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

type speechFailedPayload struct {
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
	Usage               []LaneModelUsage
	VisualState         string
	Chains              []ReplyChain
}

type SpeechSynthesisCompletion struct {
	// Index is the monotonic playback order across the turn.
	Index uint8
	// ChainIndex is the reply-chain index, or -1 for mid-ReAct utterance audio.
	ChainIndex int
	Text       string
	Result     SpeechSynthesisResult
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

func (l *TurnLifecycle) State() TurnState {
	return l.state
}

func (l *TurnLifecycle) Transition(next TurnState) (TurnEvent, error) {
	if !l.state.canTransitionTo(next) {
		return TurnEvent{}, fmt.Errorf("invalid turn state transition from %s to %s", l.state, next)
	}
	l.state = next
	return l.event(stateChangedPayload{Type: "state_changed"}), nil
}

func (l *TurnLifecycle) ReplyChain(index uint8, delta string, chain ReplyChain) (TurnEvent, error) {
	if l.state != TurnStateResponding {
		return TurnEvent{}, errors.New("只有 Responding 状态可以发送回复分段")
	}
	if delta == "" {
		return TurnEvent{}, errors.New("回复分段增量不能为空")
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
	return l.event(utterancePayload{
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
	return l.event(presencePayload{Type: "presence", Phase: phase}), nil
}

func (l *TurnLifecycle) ReplyPreview(chains []ReplyChain) (TurnEvent, error) {
	if l.state != TurnStatePlanning && l.state != TurnStateGathering {
		return TurnEvent{}, errors.New("只有 Gathering/Planning 状态可以发送 reply.preview")
	}
	if len(chains) == 0 {
		return TurnEvent{}, errors.New("reply.preview chains cannot be empty")
	}
	copyChains := append([]ReplyChain(nil), chains...)
	return l.event(replyPreviewPayload{Type: "reply.preview", Chains: copyChains}), nil
}

func (l *TurnLifecycle) Fail(code string, message string, retryable bool) (TurnEvent, error) {
	if !l.state.canTransitionTo(TurnStateFailed) {
		return TurnEvent{}, fmt.Errorf("invalid turn state transition from %s to failed", l.state)
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

func (l *TurnLifecycle) SpeechRequested(completion TurnCompletion) (TurnEvent, error) {
	if l.state != TurnStateCompleted && l.state != TurnStatePlanning && l.state != TurnStateResponding {
		return TurnEvent{}, errors.New("只有 Planning/Responding/Completed 状态可以请求语音")
	}
	return l.event(speechRequestedPayload{
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
	return l.event(speechSynthesizedPayload{
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
func (l *TurnLifecycle) BeatReady(completion BeatReadyCompletion) (TurnEvent, error) {
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
	payload := beatReadyPayload{
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
	return l.event(speechFailedPayload{Type: "speech.failed", Error: WireError{Code: code, Message: message, Retryable: retryable}}), nil
}

func (l *TurnLifecycle) Interrupt() (TurnEvent, error) {
	if !l.state.canTransitionTo(TurnStateInterrupted) {
		return TurnEvent{}, fmt.Errorf("invalid turn state transition from %s to interrupted", l.state)
	}
	l.state = TurnStateInterrupted
	return l.event(stateChangedPayload{Type: "state_changed"}), nil
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
