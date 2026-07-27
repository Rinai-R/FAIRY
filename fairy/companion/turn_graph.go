package companion

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"fairy/character"
	"fairy/config"
	"fairy/memory"
	"fairy/model"

	"go.uber.org/zap"

	"fairy/session"
)

type turnGraphState struct {
	bootstrap           memory.ConversationPromptContext
	character           character.Record
	profile             *config.ProfileSnapshot
	socialContext       *SocialRespondContext
	retrieval           memory.RetrievalContext
	retrievalOmitReason string
	gatherPhase         string
	connectionConfig    config.ModelConnection
	reply               CompiledReply
	events              []model.StreamEvent
	fullRequest         model.CompiledPromptRequest
	finalUsage          []LaneModelUsage
	ingestSnapshots     []memory.KnowledgeIngestSnapshot
	profileRevision     *uint64
	program             *turnGraphProgram
}

type turnGraphProgram struct {
	host           *CompanionService
	conversationID string
	turnID         string
	nodes          []turnGraphNode
	states         map[string]turnState
	outcome        TurnOutcome
	runErr         error
	validationErr  error
}

type turnGraphNode struct {
	key  string
	kind string
	run  func(context.Context, *turnGraphState) (*turnGraphState, error)
}

func turnStep(key, kind string, run func(context.Context, *turnGraphState) (*turnGraphState, error)) turnGraphNode {
	return turnGraphNode{key: key, kind: kind, run: run}
}

func newTurnGraphProgram(host *CompanionService, conversationID, turnID string) *turnGraphProgram {
	return &turnGraphProgram{host: host, conversationID: conversationID, turnID: turnID, states: make(map[string]turnState)}
}

func (p *turnGraphProgram) add(node turnGraphNode, state turnState) {
	if p.validationErr != nil {
		return
	}
	if strings.TrimSpace(node.key) == "" || strings.TrimSpace(node.kind) == "" || node.run == nil {
		p.validationErr = errors.New("turn graph node requires key, kind and runner")
		return
	}
	if _, exists := p.states[node.key]; exists {
		p.validationErr = errors.New("turn graph node key is duplicated")
		return
	}
	p.nodes = append(p.nodes, node)
	p.states[node.key] = state
}

func (p *turnGraphProgram) addOutcome(node, kind string, state turnState, run func() (TurnOutcome, error)) {
	p.add(turnStep(node, kind, func(_ context.Context, graphState *turnGraphState) (*turnGraphState, error) {
		p.outcome, p.runErr = run()
		return graphState, p.runErr
	}), state)
}

func (p *turnGraphProgram) run(ctx context.Context, state *turnGraphState) (TurnOutcome, error) {
	if p.validationErr != nil {
		return TurnOutcome{}, &turnStageError{code: "TURN_GRAPH_INVALID", cause: p.validationErr}
	}
	if len(p.nodes) == 0 {
		return TurnOutcome{}, errors.New("turn graph has no nodes")
	}
	for _, node := range p.nodes {
		if err := ctx.Err(); err != nil {
			return p.outcome, err
		}
		p.host.appendRuntimeLedger(p.conversationID, p.turnID, runtimeLedgerEventNode, p.states[node.key], "", map[string]any{
			"node": node.key, "kind": node.kind, "status": "started",
		})
		next, err := node.run(ctx, state)
		if err != nil {
			p.host.appendRuntimeLedger(p.conversationID, p.turnID, runtimeLedgerEventNode, p.states[node.key], "", map[string]any{
				"node": node.key, "kind": node.kind, "status": "failed",
			})
			if p.runErr != nil {
				return p.outcome, p.runErr
			}
			return p.outcome, fmt.Errorf("turn graph node %q failed: %w", node.key, err)
		}
		state = next
		p.host.appendRuntimeLedger(p.conversationID, p.turnID, runtimeLedgerEventNode, p.states[node.key], "", map[string]any{
			"node": node.key, "kind": node.kind, "status": "completed",
		})
	}
	return p.outcome, nil
}

type turnStageError struct {
	code  string
	cause error
}

func (e *turnStageError) Error() string { return e.cause.Error() }
func (e *turnStageError) Unwrap() error { return e.cause }

type turnTransition func(turnState) error

func (e *TurnEngine) declareGatherNodes(
	request SubmitCompiledTurnRequest,
	resolved session.Resolved,
	turnID string,
	transition turnTransition,
	logger *zap.Logger,
) *turnGraphState {
	s := e.host
	state := &turnGraphState{}
	state.program = newTurnGraphProgram(s, request.ConversationID, turnID)
	fail := func(code string, err error) error { return &turnStageError{code: code, cause: err} }

	state.program.add(
		turnStep("interpreting", "load_turn_context", func(_ context.Context, state *turnGraphState) (*turnGraphState, error) {
			if err := transition(turnStateInterpreting); err != nil {
				return state, fail("INVALID_STATE_TRANSITION", err)
			}
			bootstrap, err := s.memory.turn.promptContext.LoadConversationPrompt(request.ConversationID)
			if err != nil {
				return state, fail("CONVERSATION_FAILED", err)
			}
			state.bootstrap = bootstrap
			record, err := s.activeCharacter(bootstrap.Conversation.CharacterID)
			if err != nil {
				return state, fail("CHARACTER_NOT_AVAILABLE", err)
			}
			state.character = record
			if resolved.AllowsPersonalMemory() {
				state.profile, err = s.profileSource().Current()
				if err != nil {
					return state, fail("USER_PROFILE_UNAVAILABLE", err)
				}
			}
			return state, nil
		}), turnStateInterpreting)
	state.program.add(
		turnStep("gathering", "retrieve_context", func(ctx context.Context, state *turnGraphState) (*turnGraphState, error) {
			if err := transition(turnStateGathering); err != nil {
				return state, fail("INVALID_STATE_TRANSITION", err)
			}
			social, err := s.retrieveSocialRespondContext(ctx, state.bootstrap.Conversation.CharacterID, request.ConversationID, resolved, request.ReplyIntent, request.PersonNoteSenderIDs)
			if err != nil {
				return state, fail("SOCIAL_MEMORY_FAILED", err)
			}
			state.socialContext = social
			if social != nil {
				social.RecentTargetReply = strings.TrimSpace(request.RecentTargetReply)
				logger.Info("cognition loop", zap.String("phase", "social_memory_retrieved"), zap.String("queryHash", runtimeHash(socialMemoryQuery(*request.ReplyIntent))), zap.Int("count", len(social.Memory.Entries)))
			}
			retrievalQuery := request.Input
			if request.Initiation != nil {
				retrievalQuery = desktopInitiationRetrievalQuery(*request.Initiation)
			}
			state.retrieval, err = s.retrieveCompanionPortrait(ctx, state.bootstrap.Conversation.CharacterID, retrievalQuery, resolved)
			if err != nil {
				return state, fail("MEMORY_RETRIEVAL_FAILED", err)
			}
			state.gatherPhase = "baseline_portrait"
			if state.retrieval.Empty() {
				state.retrievalOmitReason = "awaiting_tools"
				state.gatherPhase = "skip_auto_retrieve"
			}
			s.appendRuntimeLedger(request.ConversationID, turnID, runtimeLedgerEventGather, turnStateGathering, "", map[string]any{
				"phase": state.gatherPhase, "personalCount": len(state.retrieval.PersonalMemories),
				"knowledgeCount": 0, "omitReason": state.retrievalOmitReason, "modelDrivenIndex": 0,
			})
			logger.Info("cognition loop", zap.String("phase", state.gatherPhase), zap.Int("personalCount", len(state.retrieval.PersonalMemories)))
			return state, nil
		}), turnStateGathering)
	return state
}

func unwrapTurnStageError(err error) (string, error) {
	var stage *turnStageError
	if errors.As(err, &stage) {
		return stage.code, stage.cause
	}
	return "TURN_GRAPH_FAILED", err
}

func (e *TurnEngine) declareOutcomeNode(
	_ context.Context,
	state *turnGraphState,
	_ string,
	_ string,
	node string,
	kind string,
	turnState turnState,
	run func() (TurnOutcome, error),
) (TurnOutcome, error) {
	state.program.addOutcome(node, kind, turnState, run)
	return TurnOutcome{}, nil
}
