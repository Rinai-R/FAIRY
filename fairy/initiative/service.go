package initiative

import (
	"context"
	"errors"
	"strings"
	"time"

	"fairy/session"
)

// TurnStarter is declared at Initiative's consumption boundary. Core adapts
// Companion to this contract, so neither orchestration package imports the
// other.
type TurnStarter interface {
	CancelTurnBeforeDelivery(conversationID string)
	SubmitTurn(TurnRequest) (TurnOutcome, error)
	ScheduleDesktopInitiation(conversationID string, evidenceIDs []string, observation session.DesktopObservation) error
}

type InteractionResolver interface {
	ResolveInteraction(conversationID string) (session.Resolved, error)
}

type Observer interface {
	BeginMessageTrace(source, conversationID, traceID string) string
	EndMessageTrace(traceID, status string)
	EmitParticipation(Event)
	RecordParticipation(traceIDs []string, targetTraceID, action string)
	WarnInitiative(message, conversationID string, generation uint64, err error)
}

type ServiceOptions struct {
	Turns        TurnStarter
	Interactions InteractionResolver
	Decisions    DecisionHost
	Learning     LearningHost
	Feedback     FeedbackHost
	Observer     Observer
}

// Service owns Initiative's ambient, desktop, learning, and feedback
// lifecycles. Core is responsible for injecting all cross-domain ports.
type Service struct {
	turns        TurnStarter
	interactions InteractionResolver
	observer     Observer
	inbox        *Inbox
	decisions    *Engine
	learning     *LearningEngine
	feedback     *FeedbackEngine
	attention    *AttentionEvaluator
	evidence     *EvidenceRegistry
}

func NewService(parent context.Context, options ServiceOptions) *Service {
	service := &Service{
		turns:        options.Turns,
		interactions: options.Interactions,
		observer:     options.Observer,
		decisions:    NewEngine(options.Decisions),
		learning:     NewLearningEngine(options.Learning, LearningQueueCapacity),
		feedback:     NewFeedbackEngine(options.Feedback, FeedbackQueueCapacity),
		attention:    NewAttentionEvaluator(),
		evidence:     NewEvidenceRegistry(),
	}
	service.inbox = NewInbox(parent, service)
	return service
}

func (s *Service) Close() {
	if s == nil {
		return
	}
	if s.inbox != nil {
		s.inbox.Close()
	}
	if s.learning != nil {
		s.learning.Close()
	}
	if s.feedback != nil {
		s.feedback.Close()
	}
}

func (s *Service) EvidenceValidator() *EvidenceRegistry {
	if s == nil {
		return nil
	}
	return s.evidence
}

func (s *Service) ObserveAmbient(conversationID string, observation AmbientObservation) error {
	if s == nil || s.inbox == nil {
		return errors.New("ambient inbox is not configured")
	}
	if s.interactions == nil {
		return errors.New("initiative interaction resolver is not configured")
	}
	resolved, err := s.interactions.ResolveInteraction(conversationID)
	if err != nil {
		return err
	}
	if !resolved.AllowsAmbientParticipation() {
		return errors.New("ambient observation requires initiation=ambient")
	}
	if resolved.Memory != session.MemoryPublic {
		return errors.New("ambient observation requires memory_policy=public")
	}
	return s.inbox.Observe(conversationID, observation)
}

func (s *Service) DecideParticipation(ctx context.Context, request ParticipationRequest) (ParticipationResult, error) {
	if s == nil || s.decisions == nil {
		return ParticipationResult{}, errors.New("participation runtime is not configured")
	}
	if err := ValidateParticipationRequest(request); err != nil {
		return ParticipationResult{}, err
	}
	if s.interactions == nil {
		return ParticipationResult{}, errors.New("initiative interaction resolver is not configured")
	}
	resolved, err := s.interactions.ResolveInteraction(request.ConversationID)
	if err != nil {
		return ParticipationResult{}, err
	}
	messageID := request.Messages[len(request.Messages)-1].MessageID
	now := time.Now()
	route, err := RouteTrigger(TriggerEnvelope{
		Kind: TriggerPublicAmbient, ConversationID: request.ConversationID, Resolved: resolved,
		Payload: PublicAmbientTrigger{MessageID: messageID}, EvidenceIDs: []string{messageID}, CreatedAt: now,
	}, DesktopPrivacyNormal, true, now)
	if err != nil {
		return ParticipationResult{}, err
	}
	if route.Pipeline != PipelineParticipation {
		return ParticipationResult{}, errors.New("public ambient trigger selected an invalid entry graph")
	}
	return s.decisions.DecideParticipation(ctx, request)
}

func (s *Service) ObserveDesktop(conversationID string, observation session.DesktopObservation) (DesktopGraphPlan, error) {
	if s == nil {
		return DesktopGraphPlan{}, errors.New("initiative service is unavailable")
	}
	if s.interactions == nil {
		return DesktopGraphPlan{}, errors.New("initiative interaction resolver is not configured")
	}
	resolved, err := s.interactions.ResolveInteraction(conversationID)
	if err != nil {
		return DesktopGraphPlan{}, err
	}
	now := time.Now()
	envelope := TriggerEnvelope{
		Kind: TriggerDesktopObservation, ConversationID: conversationID, Resolved: resolved,
		Payload:     DesktopObservationTriggerPayload{Observation: observation},
		EvidenceIDs: []string{observation.ObservationID}, CreatedAt: now,
	}
	if err := envelope.Validate(now); err != nil {
		return DesktopGraphPlan{}, err
	}
	route, err := RouteTrigger(envelope, observation.Privacy, true, now)
	if err != nil {
		return DesktopGraphPlan{}, err
	}
	if route.Pipeline == PipelineSilent {
		return DesktopGraphPlan{Action: DesktopActionSilent, OmitReasons: []string{"rule_tree:" + route.Branch}}, nil
	}
	if route.Pipeline != PipelineObservation {
		return DesktopGraphPlan{}, errors.New("desktop trigger selected an invalid entry graph")
	}
	if s.evidence == nil {
		s.evidence = NewEvidenceRegistry()
	}
	if err := s.evidence.Accept(observation, now); err != nil {
		return DesktopGraphPlan{}, err
	}
	rulebook := DesktopRulebook{
		Resolved: resolved, Trigger: observation.Trigger, Privacy: observation.Privacy,
		AllowsKnowledge: resolved.AllowsPersonalMemory(), AllowsPersonalMemory: resolved.AllowsPersonalMemory(),
		AllowsPlanner: resolved.AllowsPersonalMemory(), AllowsInitiation: resolved.AllowsPersonalMemory(),
		AttentionBudget: 1, MinSpacing: time.Minute, Now: now,
	}
	plan, err := CompileDesktopGraph(rulebook, observation)
	if err != nil {
		return DesktopGraphPlan{}, err
	}
	if s.attention == nil {
		s.attention = NewAttentionEvaluator()
	}
	action, err := s.attention.Evaluate(conversationID, plan, rulebook, rulebook.Now)
	if err != nil {
		return DesktopGraphPlan{}, err
	}
	graph, err := CompileDesktopTypedGraph(rulebook, observation)
	if err != nil {
		return DesktopGraphPlan{}, err
	}
	var initiate func(context.Context, session.DesktopObservation) error
	if s.turns != nil {
		initiate = func(_ context.Context, accepted session.DesktopObservation) error {
			return s.turns.ScheduleDesktopInitiation(
				conversationID,
				[]string{accepted.ObservationID},
				accepted,
			)
		}
	}
	state, err := graph.InvokeObserved(
		context.Background(), "normalize", "final",
		DesktopGraphState{Observation: observation, AttentionDecision: action, Initiate: initiate},
		func(event DesktopGraphExecutionEvent) {
			plan.Diagnostics = append(plan.Diagnostics, DesktopGraphDiagnostic{
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

func (s *Service) ObserveAmbientReply(registration FeedbackRegistration) bool {
	if s == nil || s.feedback == nil {
		return false
	}
	return s.feedback.Register(registration)
}

func (s *Service) BeginMessageTrace(source, conversationID, traceID string) string {
	if s == nil || s.observer == nil {
		return traceID
	}
	return s.observer.BeginMessageTrace(source, conversationID, traceID)
}

func (s *Service) ObserveSocialFeedback(conversationID string, observation AmbientObservation) {
	if s != nil && s.feedback != nil {
		s.feedback.Observe(conversationID, observation)
	}
}

func (s *Service) EnqueueSocialLearning(conversationID string, messages []AmbientObservation) {
	if s != nil && s.learning != nil {
		s.learning.Enqueue(LearningSnapshot{ConversationID: conversationID, Messages: messages})
	}
}

func (s *Service) CancelTurnBeforeDelivery(conversationID string) {
	if s != nil && s.turns != nil {
		s.turns.CancelTurnBeforeDelivery(conversationID)
	}
}

func (s *Service) SubmitTurn(request TurnRequest) (TurnOutcome, error) {
	if s == nil || s.turns == nil {
		return TurnOutcome{}, errors.New("initiative turn starter is not configured")
	}
	return s.turns.SubmitTurn(request)
}

func (s *Service) EndMessageTrace(traceID, status string) {
	if s != nil && s.observer != nil {
		s.observer.EndMessageTrace(traceID, status)
	}
}

func (s *Service) EmitParticipation(event Event) {
	if s != nil && s.observer != nil {
		s.observer.EmitParticipation(event)
	}
}

func (s *Service) RecordParticipation(traceIDs []string, targetTraceID, action string) {
	if s != nil && s.observer != nil {
		s.observer.RecordParticipation(traceIDs, targetTraceID, action)
	}
}

func (s *Service) WarnAmbient(message, conversationID string, generation uint64, err error) {
	if s != nil && s.observer != nil {
		s.observer.WarnInitiative(strings.TrimSpace(message), conversationID, generation, err)
	}
}
