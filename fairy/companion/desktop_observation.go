package companion

import (
	"context"
	"errors"
	"time"

	"fairy/pkg/nodegraph"
	"fairy/proactive"

	obs "fairy/contracts/observation"
	appobs "fairy/observation"
)

type DesktopObservation = obs.DesktopObservation
type DesktopGraphPlan = appobs.DesktopGraphPlan

func (s *CompanionService) ObserveDesktop(conversationID string, observation DesktopObservation) (DesktopGraphPlan, error) {
	if s == nil {
		return DesktopGraphPlan{}, errors.New("companion service is unavailable")
	}
	resolved, err := s.ResolveInteraction(conversationID)
	if err != nil {
		return DesktopGraphPlan{}, err
	}
	now := time.Now()
	envelope := appobs.TriggerEnvelope{
		Kind: appobs.TriggerDesktopObservation, ConversationID: conversationID, Resolved: resolved,
		Payload: appobs.DesktopObservationTriggerPayload{Observation: observation}, EvidenceIDs: []string{observation.ObservationID}, CreatedAt: now,
	}
	if err := envelope.Validate(now); err != nil {
		return DesktopGraphPlan{}, err
	}
	route, err := appobs.RouteCoreTrigger(envelope, observation.Privacy, true, now)
	if err != nil {
		return DesktopGraphPlan{}, err
	}
	if route.Pipeline == appobs.PipelineSilent {
		return DesktopGraphPlan{Action: appobs.DesktopActionSilent, OmitReasons: []string{"rule_tree:" + route.Branch}}, nil
	}
	if route.Pipeline != appobs.PipelineObservation {
		return DesktopGraphPlan{}, errors.New("desktop trigger selected an invalid entry graph")
	}
	if s.desktopEvidence == nil {
		s.desktopEvidence = proactive.NewEvidenceRegistry()
	}
	if err := s.desktopEvidence.Accept(observation, now); err != nil {
		return DesktopGraphPlan{}, err
	}
	rulebook := appobs.DesktopRulebook{
		Resolved: resolved, Trigger: observation.Trigger, Privacy: observation.Privacy,
		AllowsKnowledge: resolved.AllowsPersonalMemory(), AllowsPersonalMemory: resolved.AllowsPersonalMemory(),
		AllowsPlanner: resolved.AllowsPersonalMemory(), AllowsInitiation: resolved.AllowsPersonalMemory(),
		AttentionBudget: 1, MinSpacing: time.Minute, Now: now,
	}
	plan, err := appobs.CompileDesktopGraph(rulebook, observation)
	if err != nil {
		return DesktopGraphPlan{}, err
	}
	if s.desktopAttention == nil {
		s.desktopAttention = proactive.NewAttentionEvaluator()
	}
	action, err := s.desktopAttention.Evaluate(conversationID, plan, rulebook, rulebook.Now)
	if err != nil {
		return DesktopGraphPlan{}, err
	}
	graph, err := appobs.CompileDesktopTypedGraph(rulebook, observation)
	if err != nil {
		return DesktopGraphPlan{}, err
	}
	var initiate func(context.Context, obs.DesktopObservation) error
	if s.RespondRuntimeMigrated() {
		initiate = func(_ context.Context, accepted obs.DesktopObservation) error {
			s.backgroundJobs.Add(1)
			go func() {
				defer s.backgroundJobs.Add(-1)
				_, runErr := s.SubmitDesktopInitiation(DesktopInitiationRequest{
					ConversationID: conversationID, ObservationEvidenceIDs: []string{accepted.ObservationID},
				}, accepted)
				if runErr != nil {
					s.setBackgroundError(runErr)
				}
			}()
			return nil
		}
	}
	state, err := graph.InvokeObserved(
		context.Background(), "normalize", "final",
		appobs.DesktopGraphState{Observation: observation, AttentionDecision: action, Initiate: initiate},
		func(event nodegraph.Event) {
			plan.Diagnostics = append(plan.Diagnostics, appobs.DesktopGraphDiagnostic{
				Node: event.Node, Kind: event.Kind, Status: string(event.Status),
			})
		},
	)
	if err != nil {
		return DesktopGraphPlan{}, err
	}
	plan.Action = state.Action
	return plan, nil
}
