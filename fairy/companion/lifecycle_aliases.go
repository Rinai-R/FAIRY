package companion

import turnruntime "fairy/turn"

// Turn lifecycle implementation lives in fairy/turn. These aliases preserve
// the existing Companion-facing names while keeping ownership in the domain
// package.
type TurnState = turnruntime.TurnState

const (
	TurnStateIdle         = turnruntime.TurnStateIdle
	TurnStateInterpreting = turnruntime.TurnStateInterpreting
	TurnStateGathering    = turnruntime.TurnStateGathering
	TurnStatePlanning     = turnruntime.TurnStatePlanning
	TurnStateResponding   = turnruntime.TurnStateResponding
	TurnStateCompleted    = turnruntime.TurnStateCompleted
	TurnStateInterrupted  = turnruntime.TurnStateInterrupted
	TurnStateFailed       = turnruntime.TurnStateFailed
)

type WireError = turnruntime.WireError

type stateChangedPayload = turnruntime.StateChangedPayload
type replyChainPayload = turnruntime.ReplyChainPayload
type utterancePayload = turnruntime.UtterancePayload
type presencePayload = turnruntime.PresencePayload
type replyPreviewPayload = turnruntime.ReplyPreviewPayload
type beatReadyPayload = turnruntime.BeatReadyPayload
type completedPayload = turnruntime.CompletedPayload
type failedPayload = turnruntime.FailedPayload
type speechRequestedPayload = turnruntime.SpeechRequestedPayload
type speechSynthesizedPayload = turnruntime.SpeechSynthesizedPayload
type speechFailedPayload = turnruntime.SpeechFailedPayload

type TurnEvent = turnruntime.TurnEvent
type TurnCompletion = turnruntime.TurnCompletion
type SpeechSynthesisCompletion = turnruntime.SpeechSynthesisCompletion
type EventEmitter = turnruntime.EventEmitter
type TurnLifecycle = turnruntime.TurnLifecycle

func NewTurnLifecycle(conversationID, turnID string) *TurnLifecycle {
	return turnruntime.NewTurnLifecycle(conversationID, turnID)
}
