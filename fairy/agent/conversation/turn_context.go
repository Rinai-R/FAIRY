package conversation

import (
	"context"
	"errors"
	"fairy/agent/conversation/lifecycle"
	"fairy/agent/reply"
	"fairy/agent/tool"
	"fairy/context/character"
	history "fairy/context/history/transcript"
	"fairy/context/knowledge"
	"fairy/context/recall"
	"fairy/context/social"
	"fairy/runtime/config"
	"fairy/runtime/model"
	"fairy/transport/session"
	"strings"

	"go.uber.org/zap"
)

// turnContext is the concrete working data for one Companion turn. It is not a
// workflow blackboard: every field belongs to the fixed turn lifecycle and is
// consumed by a named Companion phase.
type turnContext struct {
	bootstrap             history.ConversationPromptContext
	character             character.Record
	profile               *config.ProfileSnapshot
	socialContext         *SocialRespondContext
	socialFeedbackContext social.SocialMemoryContext
	retrieval             recall.Context
	retrievalOmitReason   string
	gatherPhase           string
	connectionConfig      config.ModelConnection
	reply                 reply.CompiledReply
	events                []model.StreamEvent
	fullRequest           model.CompiledPromptRequest
	finalUsage            []LaneModelUsage
	profileRevision       *uint64
	knowledgeTasks        []knowledge.IngestTask
}

type turnPhaseError struct {
	code  string
	cause error
}

func (e *turnPhaseError) Error() string { return e.cause.Error() }
func (e *turnPhaseError) Unwrap() error { return e.cause }

type turnTransition func(lifecycle.State) error

func (e *TurnEngine) prepareTurnContext(
	ctx context.Context,
	request SubmitCompiledTurnRequest,
	resolved session.Resolved,
	turnID string,
	transition turnTransition,
	logger *zap.Logger,
) (*turnContext, error) {
	s := e.host
	state := &turnContext{}
	fail := func(code string, err error) error { return &turnPhaseError{code: code, cause: err} }

	if err := transition(lifecycle.StateInterpreting); err != nil {
		return nil, fail("INVALID_STATE_TRANSITION", err)
	}
	bootstrap, err := s.memory.turn.promptContext.LoadConversationPrompt(request.ConversationID)
	if err != nil {
		return nil, fail("CONVERSATION_FAILED", err)
	}
	state.bootstrap = bootstrap
	record, err := s.activeCharacter(bootstrap.Conversation.CharacterID)
	if err != nil {
		return nil, fail("CHARACTER_NOT_AVAILABLE", err)
	}
	state.character = record
	if resolved.AllowsPersonalMemory() {
		state.profile, err = s.profileSource().Current()
		if err != nil {
			return nil, fail("USER_PROFILE_UNAVAILABLE", err)
		}
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := transition(lifecycle.StateGathering); err != nil {
		return nil, fail("INVALID_STATE_TRANSITION", err)
	}
	social, err := s.retrieveSocialRespondContext(
		ctx,
		state.bootstrap.Conversation.CharacterID,
		request.ConversationID,
		resolved,
		request.ReplyIntent,
		request.PersonNoteSenderIDs,
	)
	if err != nil {
		return nil, fail("SOCIAL_MEMORY_FAILED", err)
	}
	state.socialContext = social
	if social != nil {
		state.socialFeedbackContext = tool.MergeSocialMemory(state.socialFeedbackContext, social.Memory)
		social.RecentTargetReply = strings.TrimSpace(request.RecentTargetReply)
		logger.Info(
			"cognition loop",
			zap.String("phase", "social_memory_retrieved"),
			zap.String("queryHash", runtimeHash(socialMemoryQuery(*request.ReplyIntent))),
			zap.Int("count", len(social.Memory.Entries)),
		)
	}
	retrievalQuery := request.Input
	if request.Initiation != nil {
		retrievalQuery = desktopInitiationRetrievalQuery(*request.Initiation)
	}
	state.retrieval, err = s.retrieveCompanionPortrait(
		ctx,
		state.bootstrap.Conversation.CharacterID,
		retrievalQuery,
		resolved,
	)
	if err != nil {
		return nil, fail("MEMORY_RETRIEVAL_FAILED", err)
	}
	state.gatherPhase = "baseline_portrait"
	if state.retrieval.Empty() {
		state.retrievalOmitReason = "awaiting_tools"
		state.gatherPhase = "skip_auto_retrieve"
	}
	s.appendRuntimeLedger(request.ConversationID, turnID, runtimeLedgerEventGather, lifecycle.StateGathering, "", map[string]any{
		"phase": state.gatherPhase, "personalCount": len(state.retrieval.PersonalMemories),
		"knowledgeCount": 0, "omitReason": state.retrievalOmitReason, "modelDrivenIndex": 0,
	})
	logger.Info(
		"cognition loop",
		zap.String("phase", state.gatherPhase),
		zap.Int("personalCount", len(state.retrieval.PersonalMemories)),
	)
	return state, nil
}

func unwrapTurnPhaseError(err error) (string, error) {
	var phase *turnPhaseError
	if errors.As(err, &phase) {
		return phase.code, phase.cause
	}
	return "TURN_PHASE_FAILED", err
}
