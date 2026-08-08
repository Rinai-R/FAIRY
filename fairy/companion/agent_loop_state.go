package companion

import (
	"errors"

	"fairy/config"
	"fairy/model"
)

var errAgentAwaitingDesktop = errors.New("agent awaiting desktop capture")

type pendingDesktopTool struct {
	execution DesktopToolExecution
	callID    string
	arguments string
	completed <-chan struct{}
	spanID    string
}

// agentLoopState contains only the concrete mutable state of Companion's
// bounded Respond/ReAct loop. It is intentionally private and domain-specific;
// it is not a generic workflow state or extensible blackboard.
type agentLoopState struct {
	connectionConfig          config.ModelConnection
	reply                     CompiledReply
	events                    []model.StreamEvent
	fullRequest               model.CompiledPromptRequest
	finalUsage                []LaneModelUsage
	modelDrivenTools          int
	modelCallAttempts         int
	replyCompileRetries       int
	firstCompileErr           error
	retryCorrection           string
	toolSegments              []model.ContextSegment
	lastInputTokens           uint64
	lastCacheObservation      model.CachedTokenObservation
	lastCacheWriteObservation model.CachedTokenObservation
	desktopToolUsed           bool
	utteranceSeq              int
	webSearchEnabled          bool
	stickerToolEnabled        bool
	stickerCandidates         stickerCandidateSet
	toolBudget                int
	pendingDesktop            *pendingDesktopTool
}
