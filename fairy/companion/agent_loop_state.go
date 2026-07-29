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
}

// agentLoopState contains only the concrete mutable state of Companion's
// bounded Respond/ReAct loop. It is intentionally private and domain-specific;
// it is not a generic workflow state or extensible blackboard.
type agentLoopState struct {
	connectionConfig    config.ModelConnection
	reply               CompiledReply
	events              []model.StreamEvent
	fullRequest         model.CompiledPromptRequest
	finalUsage          []LaneModelUsage
	modelDrivenTools    int
	modelCallAttempts   int
	replyCompileRetries int
	firstCompileErr     error
	retryCorrection     string
	toolPromptItems     []model.PromptItem
	desktopToolUsed     bool
	utteranceSeq        int
	webSearchEnabled    bool
	stickerToolEnabled  bool
	stickerCandidates   stickerCandidateSet
	toolBudget          int
	pendingDesktop      *pendingDesktopTool
}
