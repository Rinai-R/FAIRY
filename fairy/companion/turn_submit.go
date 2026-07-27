package companion

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	contracts "fairy/contracts/interaction"
	obs "fairy/contracts/observation"
	domain "fairy/interaction"
	appobs "fairy/observation"
)

func (e *TurnEngine) SubmitDesktopVisionInitiation(request DesktopVisionInitiationRequest) (TurnOutcome, error) {
	s := e.host
	if strings.TrimSpace(request.ConversationID) == "" {
		return TurnOutcome{}, errors.New("conversation_id is required")
	}
	if s == nil || !s.RespondRuntimeMigrated() {
		return TurnOutcome{}, ErrRespondRuntimeNotMigrated
	}
	resolved, err := s.ResolveInteraction(request.ConversationID)
	if err != nil {
		return TurnOutcome{}, err
	}
	connection, err := s.configSource().ModelConnection()
	if err != nil {
		return TurnOutcome{}, err
	}
	if !desktopToolAllowed(connection.Capabilities.VisionInput, resolved, s.desktopTool, request.ConversationID) {
		return TurnOutcome{}, errors.New("desktop vision initiation is unavailable")
	}
	requestID := uuid.NewString()
	return e.submitRuntimeTurn(SubmitCompiledTurnRequest{
		ConversationID: request.ConversationID, SpeechEnabled: request.SpeechEnabled,
		MaxOutputTokens: RespondMaxOutputTokens, MessageSource: "desktop_vision_initiation",
		Initiation: &DesktopInitiationContext{
			ObservationEvidenceIDs: []string{requestID}, Trigger: "on_demand_vision", VisionRequested: true,
		},
	}, &resolved)
}

func (e *TurnEngine) SubmitDesktopInitiation(request DesktopInitiationRequest, observation DesktopObservation) (TurnOutcome, error) {
	s := e.host
	if err := ValidateDesktopInitiationRequest(request); err != nil {
		return TurnOutcome{}, err
	}
	if s == nil || !s.RespondRuntimeMigrated() {
		return TurnOutcome{}, ErrRespondRuntimeNotMigrated
	}
	now := time.Now()
	if err := observation.Validate(now); err != nil {
		return TurnOutcome{}, err
	}
	if observation.Privacy != obs.DesktopPrivacyNormal {
		return TurnOutcome{}, errors.New("desktop initiation requires normal privacy state")
	}
	resolved, err := s.ResolveInteraction(request.ConversationID)
	if err != nil {
		return TurnOutcome{}, err
	}
	if resolved.Endpoint != contracts.EndpointDesktop || !resolved.AllowsPersonalMemory() {
		return TurnOutcome{}, errors.New("desktop initiation requires a private desktop interaction")
	}
	if s.desktopEvidence == nil {
		return TurnOutcome{}, errors.New("desktop evidence registry is unavailable")
	}
	for _, id := range request.ObservationEvidenceIDs {
		if !s.desktopEvidence.ContainsFresh(id, now) {
			return TurnOutcome{}, fmt.Errorf("desktop initiation evidence %q is unknown or expired", id)
		}
	}
	return e.submitRuntimeTurn(SubmitCompiledTurnRequest{
		ConversationID: request.ConversationID, SpeechEnabled: request.SpeechEnabled,
		MaxOutputTokens: RespondMaxOutputTokens, MessageSource: "desktop_initiation",
		Initiation: &DesktopInitiationContext{
			ObservationEvidenceIDs: append([]string(nil), request.ObservationEvidenceIDs...),
			Trigger:                string(observation.Trigger), Activity: string(observation.Activity), Lifecycle: string(observation.Lifecycle),
		},
	}, &resolved)
}

func (e *TurnEngine) SubmitTurn(request SubmitTurnRequest) (TurnOutcome, error) {
	s := e.host
	if err := ValidateSubmitTurnRequest(request); err != nil {
		return TurnOutcome{}, err
	}
	if s == nil || !s.RespondRuntimeMigrated() {
		return TurnOutcome{}, ErrRespondRuntimeNotMigrated
	}
	var resolved *domain.Resolved
	if request.MessageSource == "" || request.MessageSource == "direct" {
		current, err := s.ResolveInteraction(request.ConversationID)
		if err != nil {
			return TurnOutcome{}, err
		}
		resolved = &current
		now := time.Now()
		route, err := appobs.RouteCoreTrigger(appobs.TriggerEnvelope{
			Kind: appobs.TriggerDirect, ConversationID: request.ConversationID, Resolved: current,
			Payload: appobs.DirectTrigger{Input: request.Input}, CreatedAt: now, SpeechEnabled: request.SpeechEnabled,
		}, obs.DesktopPrivacyNormal, true, now)
		if err != nil {
			return TurnOutcome{}, err
		}
		if route.Pipeline != appobs.PipelineDirectTurn {
			return TurnOutcome{}, errors.New("direct trigger selected an invalid entry graph")
		}
	}
	request.TraceID = s.beginMessageTrace(request.MessageSource, request.ConversationID, request.TraceID)
	if request.MessageSource == "" {
		request.MessageSource = "direct"
	}
	return e.submitRuntimeTurn(SubmitCompiledTurnRequest{
		ConversationID:      request.ConversationID,
		Input:               request.Input,
		SpeechEnabled:       request.SpeechEnabled,
		MaxOutputTokens:     RespondMaxOutputTokens,
		TraceID:             request.TraceID,
		MessageSource:       request.MessageSource,
		ReplyIntent:         request.ReplyIntent,
		RecentTargetReply:   request.RecentTargetReply,
		PersonNoteSenderIDs: append([]string(nil), request.PersonNoteSenderIDs...),
	}, resolved)
}
