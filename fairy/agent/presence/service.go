package presence

import (
	"context"
	"errors"
	"strings"
	"time"

	"fairy/runtime/model"
	"fairy/transport/session"
)

// TurnStarter is declared at Presence's consumption boundary. Core adapts
// Conversation to this contract, so neither execution domain imports the other.
type TurnStarter interface {
	CancelTurnBeforeDelivery(conversationID string)
	SubmitTurn(TurnRequest) (TurnOutcome, error)
	ScheduleDesktopInitiation(conversationID string, evidenceIDs []string, observation session.DesktopObservation) error
}

type InteractionResolver interface {
	ResolveInteraction(conversationID string) (session.Resolved, error)
}

type Observer interface {
	BeginMessageTrace(source, conversationID, messageID, traceID string) string
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

// Service owns Initiative's ambient, desktop, and experience
// lifecycles. Core is responsible for injecting all cross-domain ports.
type Service struct {
	turns        TurnStarter
	interactions InteractionResolver
	observer     Observer
	inbox        *Inbox
	decisions    *Engine
	experience   *ExperienceLoop
	attention    *AttentionEvaluator
	evidence     *EvidenceRegistry
}

func NewService(parent context.Context, options ServiceOptions) *Service {
	service := &Service{
		turns:        options.Turns,
		interactions: options.Interactions,
		observer:     options.Observer,
		decisions:    NewEngine(options.Decisions),
		experience:   NewExperienceLoop(options.Learning, options.Feedback),
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
	if s.experience != nil {
		s.experience.Close()
	}
	if s.attention != nil {
		s.attention.Clear()
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
		return ParticipationResult{}, errors.New("public ambient trigger selected an invalid initiative entry")
	}
	return s.decisions.DecideParticipation(ctx, request)
}

func (s *Service) ObserveDesktop(conversationID string, observation session.DesktopObservation) (DesktopObservationResult, error) {
	if s == nil {
		return DesktopObservationResult{}, errors.New("initiative service is unavailable")
	}
	if s.interactions == nil {
		return DesktopObservationResult{}, errors.New("initiative interaction resolver is not configured")
	}
	resolved, err := s.interactions.ResolveInteraction(conversationID)
	if err != nil {
		return DesktopObservationResult{}, err
	}
	now := time.Now()
	envelope := TriggerEnvelope{
		Kind: TriggerDesktopObservation, ConversationID: conversationID, Resolved: resolved,
		Payload:     DesktopObservationTriggerPayload{Observation: observation},
		EvidenceIDs: []string{observation.ObservationID}, CreatedAt: now,
	}
	if err := envelope.Validate(now); err != nil {
		return DesktopObservationResult{}, err
	}
	route, err := RouteTrigger(envelope, observation.Privacy, true, now)
	if err != nil {
		return DesktopObservationResult{}, err
	}
	if route.Pipeline == PipelineSilent {
		// Keep the historical omit code stable for existing Session clients.
		return DesktopObservationResult{Nodes: []DesktopObservationStep{}, Action: DesktopActionSilent, OmitReasons: []string{"rule_tree:" + route.Branch}}, nil
	}
	if route.Pipeline != PipelineObservation {
		return DesktopObservationResult{}, errors.New("desktop trigger selected an invalid initiative entry")
	}
	if s.evidence == nil {
		s.evidence = NewEvidenceRegistry()
	}
	if err := s.evidence.Accept(observation, now); err != nil {
		return DesktopObservationResult{}, err
	}
	rulebook := DesktopRulebook{
		Resolved: resolved, Trigger: observation.Trigger, Privacy: observation.Privacy,
		AllowsKnowledge: resolved.AllowsPersonalMemory(), AllowsPersonalMemory: resolved.AllowsPersonalMemory(),
		AllowsPlanner: resolved.AllowsPersonalMemory(), AllowsInitiation: resolved.AllowsPersonalMemory(),
		AttentionBudget: 1, MinSpacing: time.Minute, Now: now,
	}
	result, err := DecideDesktopObservation(rulebook, observation)
	if err != nil {
		return DesktopObservationResult{}, err
	}
	if s.attention == nil {
		s.attention = NewAttentionEvaluator()
	}
	action, err := s.attention.Evaluate(conversationID, result.Action, rulebook, rulebook.Now)
	if err != nil {
		return DesktopObservationResult{}, err
	}
	if action == DesktopActionInitiate && s.turns != nil {
		if err := s.turns.ScheduleDesktopInitiation(
			conversationID,
			[]string{observation.ObservationID},
			observation,
		); err != nil {
			return DesktopObservationResult{}, err
		}
	}
	result.Action = action
	result.Diagnostics = desktopObservationDiagnostics(action)
	return result, nil
}

func (s *Service) ObserveAmbientReply(registration FeedbackRegistration) bool {
	if s == nil || s.experience == nil {
		return false
	}
	return s.experience.CompleteReply(registration)
}

func (s *Service) BeginMessageTrace(source, conversationID, messageID, traceID string) string {
	if s == nil || s.observer == nil {
		return traceID
	}
	return s.observer.BeginMessageTrace(source, conversationID, messageID, traceID)
}

func (s *Service) ObserveSocialFeedback(conversationID string, observation AmbientObservation) {
	if s != nil && s.experience != nil {
		s.experience.Observe(conversationID, observation)
	}
}

func (s *Service) EnqueueSocialLearning(conversationID string, messages []AmbientObservation) {
	if s != nil && s.experience != nil {
		s.experience.EnqueueEpisode(conversationID, messages)
	}
}

func (s *Service) ExperienceStats() ExperienceStats {
	if s == nil || s.experience == nil {
		return ExperienceStats{CacheIdentityVersion: model.PromptCacheKeyVersion}
	}
	return s.experience.Stats()
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
