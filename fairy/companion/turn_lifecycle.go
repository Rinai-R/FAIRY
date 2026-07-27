package companion

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"fairy/model"
	"fairy/reply"
	"fairy/session"
)

// Turn lifecycle and turn events mirror crates/fairy-domain/src/conversation.rs.

type turnState string

const (
	turnStateIdle         turnState = "idle"
	turnStateInterpreting turnState = "interpreting"
	turnStateGathering    turnState = "gathering"
	turnStatePlanning     turnState = "planning"
	turnStateResponding   turnState = "responding"
	turnStateCompleted    turnState = "completed"
	turnStateInterrupted  turnState = "interrupted"
	turnStateFailed       turnState = "failed"
)

var turnStateTransitions = map[turnState]map[turnState]struct{}{
	turnStateIdle: {
		turnStateInterpreting: {},
	},
	turnStateInterpreting: {
		turnStateGathering: {}, turnStateInterrupted: {}, turnStateFailed: {},
	},
	turnStateGathering: {
		turnStatePlanning: {}, turnStateInterrupted: {}, turnStateFailed: {},
	},
	turnStatePlanning: {
		turnStateResponding: {}, turnStateInterrupted: {}, turnStateFailed: {},
	},
	turnStateResponding: {
		turnStateCompleted: {}, turnStateInterrupted: {}, turnStateFailed: {},
	},
}

func (s turnState) canTransitionTo(next turnState) bool {
	_, allowed := turnStateTransitions[s][next]
	return allowed
}

type wireError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
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
	Type   string             `json:"type"`
	Chains []reply.ReplyChain `json:"chains"`
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

type completedPayload struct {
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

type failedPayload struct {
	Type  string    `json:"type"`
	Error wireError `json:"error"`
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
	Error wireError `json:"error"`
}

type turnCompletion struct {
	Text                string
	SpeechText          string
	Sources             []any
	CharacterRevision   uint64
	UserProfileRevision *uint64
	Usage               []model.LaneModelUsage
	VisualState         string
	Chains              []reply.ReplyChain
}

type speechSynthesisCompletion struct {
	// Index is the monotonic playback order across the turn.
	Index uint8
	// ChainIndex is the reply-chain index, or -1 for mid-ReAct utterance audio.
	ChainIndex int
	Text       string
	Result     reply.SpeechSynthesisResult
}

type eventEmitter func(session.Event)

type turnLifecycle struct {
	mu             sync.Mutex
	conversationID string
	turnID         string
	state          turnState
	nextSequence   uint64
}

func newTurnLifecycle(conversationID string, turnID string) *turnLifecycle {
	return &turnLifecycle{
		conversationID: conversationID,
		turnID:         turnID,
		state:          turnStateIdle,
		nextSequence:   1,
	}
}

// Publish serializes one lifecycle mutation and its event construction. The
// caller must perform the mutation inside produce; lifecycle methods remain
// intentionally small and deterministic.
func (l *turnLifecycle) Publish(produce func() (session.Event, error)) (session.Event, error) {
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

func (l *turnLifecycle) State() turnState {
	if l == nil {
		return turnStateIdle
	}
	return l.state
}

func (l *turnLifecycle) Transition(next turnState) (session.Event, error) {
	if !l.state.canTransitionTo(next) {
		return session.Event{}, fmt.Errorf("invalid turn state transition from %s to %s", l.state, next)
	}
	l.state = next
	return l.event(stateChangedPayload{Type: "state_changed"})
}

func (l *turnLifecycle) ReplyChain(index uint8, delta string, chain reply.ReplyChain) (session.Event, error) {
	if l.state != turnStateResponding {
		return session.Event{}, errors.New("只有 Responding 状态可以发送回复分段")
	}
	if delta == "" {
		return session.Event{}, errors.New("回复分段增量不能为空")
	}
	return l.event(replyChainPayload{
		Type:        "reply_chain",
		Index:       index,
		Delta:       delta,
		Text:        chain.Text,
		SpeechText:  chain.SpeechText,
		VisualState: chain.VisualState,
	})
}

// Utterance emits a progressive in-character line during gathering/planning.
// It does not enter transcript; final reply_chain remains the persisted answer.
func (l *turnLifecycle) Utterance(seq uint8, text string, visualState string, reason string) (session.Event, error) {
	if l.state != turnStatePlanning && l.state != turnStateGathering {
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
	return l.event(utterancePayload{
		Type:        "utterance",
		Seq:         seq,
		Text:        text,
		VisualState: visualState,
		Reason:      reason,
	})
}

// Presence and Preview are temporary events. They are never transcript
// messages; beat.ready and completed remain the final display contract.
func (l *turnLifecycle) Presence(phase string) (session.Event, error) {
	if l.state != turnStatePlanning && l.state != turnStateGathering {
		return session.Event{}, errors.New("只有 Gathering/Planning 状态可以发送 presence")
	}
	if strings.TrimSpace(phase) == "" {
		phase = "model_stream"
	}
	return l.event(presencePayload{Type: "presence", Phase: phase})
}

func (l *turnLifecycle) ReplyPreview(chains []reply.ReplyChain) (session.Event, error) {
	if l.state != turnStatePlanning && l.state != turnStateGathering {
		return session.Event{}, errors.New("只有 Gathering/Planning 状态可以发送 reply.preview")
	}
	if len(chains) == 0 {
		return session.Event{}, errors.New("reply.preview chains cannot be empty")
	}
	copyChains := append([]reply.ReplyChain(nil), chains...)
	return l.event(replyPreviewPayload{Type: "reply.preview", Chains: copyChains})
}

func (l *turnLifecycle) Fail(code string, message string, retryable bool) (session.Event, error) {
	if !l.state.canTransitionTo(turnStateFailed) {
		return session.Event{}, fmt.Errorf("invalid turn state transition from %s to failed", l.state)
	}
	l.state = turnStateFailed
	return l.event(failedPayload{
		Type: "failed",
		Error: wireError{
			Code:      code,
			Message:   message,
			Retryable: retryable,
		},
	})
}

func (l *turnLifecycle) Complete(completion turnCompletion) (session.Event, error) {
	if !l.state.canTransitionTo(turnStateCompleted) {
		return session.Event{}, fmt.Errorf("invalid turn state transition from %s to completed", l.state)
	}
	l.state = turnStateCompleted
	sources := completion.Sources
	if sources == nil {
		sources = []any{}
	}
	usage := completion.Usage
	if usage == nil {
		usage = []model.LaneModelUsage{}
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
	})
}

func (l *turnLifecycle) SpeechRequested(completion turnCompletion) (session.Event, error) {
	if l.state != turnStateCompleted && l.state != turnStatePlanning && l.state != turnStateResponding {
		return session.Event{}, errors.New("只有 Planning/Responding/Completed 状态可以请求语音")
	}
	return l.event(speechRequestedPayload{
		Type:                "speech.requested",
		Text:                completion.SpeechText,
		CharacterRevision:   completion.CharacterRevision,
		UserProfileRevision: completion.UserProfileRevision,
	})
}

func (l *turnLifecycle) SpeechSynthesized(completion speechSynthesisCompletion) (session.Event, error) {
	if l.state != turnStateCompleted && l.state != turnStatePlanning && l.state != turnStateResponding {
		return session.Event{}, errors.New("只有 Planning/Responding/Completed 状态可以完成语音合成")
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
	})
}

// BeatReady emits a paired display(+optional audio) beat. Allowed in planning
// (utterance beats) and responding (final beats).
func (l *turnLifecycle) BeatReady(completion reply.BeatReadyCompletion) (session.Event, error) {
	if l.state != turnStatePlanning && l.state != turnStateGathering && l.state != turnStateResponding {
		return session.Event{}, errors.New("只有 Gathering/Planning/Responding 状态可以发送 beat.ready")
	}
	if strings.TrimSpace(completion.DisplayText) == "" {
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
	return l.event(payload)
}

func (l *turnLifecycle) SpeechFailed(code string, message string, retryable bool) (session.Event, error) {
	if l.state != turnStateCompleted && l.state != turnStatePlanning && l.state != turnStateResponding {
		return session.Event{}, errors.New("只有 Planning/Responding/Completed 状态可以发送语音失败事件")
	}
	return l.event(speechFailedPayload{Type: "speech.failed", Error: wireError{Code: code, Message: message, Retryable: retryable}})
}

func (l *turnLifecycle) Interrupt() (session.Event, error) {
	if !l.state.canTransitionTo(turnStateInterrupted) {
		return session.Event{}, fmt.Errorf("invalid turn state transition from %s to interrupted", l.state)
	}
	l.state = turnStateInterrupted
	return l.event(stateChangedPayload{Type: "state_changed"})
}

func (l *turnLifecycle) event(payload any) (session.Event, error) {
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
