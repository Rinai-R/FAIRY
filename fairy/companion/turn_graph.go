package companion

import (
	"context"
	"errors"
	"strings"

	"fairy/character"
	"fairy/config"
	"fairy/memory"
	"fairy/model"
	"fairy/pkg/nodegraph"
	"fairy/profile"

	"go.uber.org/zap"

	domain "fairy/interaction"
)

type turnGraphState struct {
	bootstrap           memory.ConversationPromptContext
	character           character.Record
	profile             *profile.Snapshot
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
	nodes          []nodegraph.Node[*turnGraphState]
	states         map[string]TurnState
	outcome        TurnOutcome
	runErr         error
}

func newTurnGraphProgram(host *CompanionService, conversationID, turnID string) *turnGraphProgram {
	return &turnGraphProgram{host: host, conversationID: conversationID, turnID: turnID, states: make(map[string]TurnState)}
}

func (p *turnGraphProgram) add(node nodegraph.Node[*turnGraphState], state TurnState) {
	p.nodes = append(p.nodes, node)
	p.states[node.Key] = state
}

func (p *turnGraphProgram) addOutcome(node, kind string, state TurnState, run func() (TurnOutcome, error)) {
	p.add(nodegraph.Step(node, kind, func(_ context.Context, graphState *turnGraphState) (*turnGraphState, error) {
		p.outcome, p.runErr = run()
		return graphState, p.runErr
	}), state)
}

func (p *turnGraphProgram) run(ctx context.Context, state *turnGraphState) (TurnOutcome, error) {
	if len(p.nodes) == 0 {
		return TurnOutcome{}, errors.New("turn graph has no nodes")
	}
	keys := make([]string, 0, len(p.nodes))
	builder := nodegraph.New[*turnGraphState](len(p.nodes)).Nodes(p.nodes...)
	for _, node := range p.nodes {
		keys = append(keys, node.Key)
	}
	if len(keys) > 1 {
		builder.Path(keys...)
	}
	graph, err := builder.Compile()
	if err != nil {
		return TurnOutcome{}, &turnStageError{code: "TURN_GRAPH_INVALID", cause: err}
	}
	_, invokeErr := graph.InvokeObserved(ctx, keys[0], keys[len(keys)-1], state, func(event nodegraph.Event) {
		p.host.appendRuntimeLedger(p.conversationID, p.turnID, runtimeLedgerEventNode, p.states[event.Node], "", map[string]any{
			"node": event.Node, "kind": event.Kind, "status": string(event.Status),
		})
	})
	if invokeErr != nil {
		if p.runErr != nil {
			return p.outcome, p.runErr
		}
		return p.outcome, invokeErr
	}
	return p.outcome, nil
}

type turnStageError struct {
	code  string
	cause error
}

func (e *turnStageError) Error() string { return e.cause.Error() }
func (e *turnStageError) Unwrap() error { return e.cause }

type turnTransition func(TurnState) error

func (e *TurnEngine) declareGatherNodes(
	request SubmitCompiledTurnRequest,
	resolved domain.Resolved,
	turnID string,
	transition turnTransition,
	logger *zap.Logger,
) *turnGraphState {
	s := e.host
	state := &turnGraphState{}
	state.program = newTurnGraphProgram(s, request.ConversationID, turnID)
	fail := func(code string, err error) error { return &turnStageError{code: code, cause: err} }

	state.program.add(
		nodegraph.Step("interpreting", "load_turn_context", func(_ context.Context, state *turnGraphState) (*turnGraphState, error) {
			if err := transition(TurnStateInterpreting); err != nil {
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
		}), TurnStateInterpreting)
	state.program.add(
		nodegraph.Step("gathering", "retrieve_context", func(ctx context.Context, state *turnGraphState) (*turnGraphState, error) {
			if err := transition(TurnStateGathering); err != nil {
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
			s.appendRuntimeLedger(request.ConversationID, turnID, runtimeLedgerEventGather, TurnStateGathering, "", map[string]any{
				"phase": state.gatherPhase, "personalCount": len(state.retrieval.PersonalMemories),
				"knowledgeCount": 0, "omitReason": state.retrievalOmitReason, "modelDrivenIndex": 0,
			})
			logger.Info("cognition loop", zap.String("phase", state.gatherPhase), zap.Int("personalCount", len(state.retrieval.PersonalMemories)))
			return state, nil
		}), TurnStateGathering)
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
	turnState TurnState,
	run func() (TurnOutcome, error),
) (TurnOutcome, error) {
	state.program.addOutcome(node, kind, turnState, run)
	return TurnOutcome{}, nil
}
