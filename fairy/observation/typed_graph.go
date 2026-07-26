package observation

import (
	"context"
	"errors"
	"time"

	"fairy/interaction"
	"fairy/pkg/nodegraph"
)

// DesktopGraphState is a closed request-scoped value. It is deliberately not
// an extensible blackboard: adding data requires changing this Core contract.
type DesktopGraphState struct {
	Observation         DesktopObservation
	Normalized          bool
	AttentionDecision   DesktopObservationAction
	Action              DesktopObservationAction
	KnowledgeSelected   bool
	MemorySelected      bool
	PlannerSelected     bool
	ResponseReady       bool
	Persisted           bool
	InitiationScheduled bool
	Initiate            func(context.Context, DesktopObservation) error
}

func CompileDesktopTypedGraph(rulebook DesktopRulebook, observation DesktopObservation) (*nodegraph.Graph[DesktopGraphState], error) {
	if rulebook.Resolved.Endpoint != interaction.EndpointDesktop || rulebook.Resolved.Facts.Audience != interaction.AudienceSingle || !rulebook.Resolved.AllowsPersonalMemory() {
		return nil, errors.New("desktop typed graph requires private desktop interaction")
	}
	now := rulebook.Now
	if now.IsZero() {
		now = time.Now()
	}
	if err := observation.Validate(now); err != nil {
		return nil, err
	}

	b := nodegraph.New[DesktopGraphState](MaxDesktopGraphNodes).
		Nodes(
			desktopStep("normalize", DesktopNodeNormalize, func(state DesktopGraphState) DesktopGraphState {
				state.Normalized = true
				return state
			}),
			desktopStep("attention", DesktopNodeEvaluate, func(state DesktopGraphState) DesktopGraphState {
				if state.AttentionDecision != "" {
					state.Action = state.AttentionDecision
					return state
				}
				state.Action = DesktopActionReact
				if rulebook.AttentionBudget <= 0 || observation.Privacy != DesktopPrivacyNormal {
					state.Action = DesktopActionSilent
				} else if rulebook.AllowsPlanner && rulebook.AllowsInitiation && observation.Lifecycle == DesktopLifecycleReturned {
					state.Action = DesktopActionInitiate
				}
				return state
			}),
			desktopStep("final", DesktopNodeFinal, func(state DesktopGraphState) DesktopGraphState { return state }),
		).
		Path("normalize", "attention")

	if rulebook.AttentionBudget <= 0 || observation.Privacy != DesktopPrivacyNormal {
		return b.Nodes(desktopStep("silent", DesktopNodeSilent, func(state DesktopGraphState) DesktopGraphState {
			state.Action = DesktopActionSilent
			return state
		})).Path("attention", "silent", "final").Compile()
	}

	b.Nodes(
		desktopStep("react", DesktopNodeReact, func(state DesktopGraphState) DesktopGraphState {
			state.Action = DesktopActionReact
			state.ResponseReady = true
			return state
		}),
		desktopStep("silent", DesktopNodeSilent, func(state DesktopGraphState) DesktopGraphState {
			state.Action = DesktopActionSilent
			return state
		}),
	).Path("react", "final").Path("silent", "final")
	endpoints := []string{"react", "silent"}

	if rulebook.AllowsPlanner && rulebook.AllowsInitiation && observation.Lifecycle == DesktopLifecycleReturned {
		step := nodegraph.Step("initiate", string(DesktopNodeInitiate), func(ctx context.Context, state DesktopGraphState) (DesktopGraphState, error) {
			state.Action = DesktopActionInitiate
			if state.Initiate != nil {
				if err := state.Initiate(ctx, state.Observation); err != nil {
					return state, err
				}
				state.InitiationScheduled = true
			}
			return state, nil
		})
		b.Nodes(step).Path("initiate", "final")
		endpoints = append(endpoints, "initiate")
	}

	return b.Branch("attention", func(_ context.Context, state DesktopGraphState) (string, error) {
		switch state.Action {
		case DesktopActionSilent:
			return "silent", nil
		case DesktopActionReact:
			return "react", nil
		case DesktopActionInitiate:
			return "initiate", nil
		default:
			return "", errors.New("attention selected an unsupported action")
		}
	}, endpoints...).Compile()
}

func desktopStep(key string, kind DesktopGraphNodeKind, apply func(DesktopGraphState) DesktopGraphState) nodegraph.Node[DesktopGraphState] {
	return nodegraph.Step(key, string(kind), func(_ context.Context, state DesktopGraphState) (DesktopGraphState, error) {
		return apply(state), nil
	})
}
