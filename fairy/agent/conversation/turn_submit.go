package conversation

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"fairy/transport/session"
)

func (e *TurnEngine) SubmitDesktopVisionInitiation(request DesktopVisionInitiationRequest) (TurnOutcome, error) {
	s := e.host
	if strings.TrimSpace(request.ConversationID) == "" {
		return TurnOutcome{}, errors.New("conversation_id is required")
	}
	if s == nil || !s.TurnRuntimeReady() {
		return TurnOutcome{}, ErrTurnRuntimeUnavailable
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
		ConversationID:  request.ConversationID,
		MaxOutputTokens: RespondMaxOutputTokens, MessageSource: "desktop_vision_initiation",
		OutputCapabilities: s.OutputCapabilities(request.ConversationID),
		Initiation: &DesktopInitiationContext{
			ObservationEvidenceIDs: []string{requestID}, Trigger: "on_demand_vision", VisionRequested: true,
		},
	}, &resolved)
}

func (e *TurnEngine) SubmitDesktopInitiation(request DesktopInitiationRequest, observation session.DesktopObservation) (TurnOutcome, error) {
	s := e.host
	if err := ValidateDesktopInitiationRequest(request); err != nil {
		return TurnOutcome{}, err
	}
	if s == nil || !s.TurnRuntimeReady() {
		return TurnOutcome{}, ErrTurnRuntimeUnavailable
	}
	now := time.Now()
	if err := observation.Validate(now); err != nil {
		return TurnOutcome{}, err
	}
	if observation.Privacy != session.DesktopPrivacyNormal {
		return TurnOutcome{}, errors.New("desktop initiation requires normal privacy state")
	}
	resolved, err := s.ResolveInteraction(request.ConversationID)
	if err != nil {
		return TurnOutcome{}, err
	}
	if resolved.Endpoint != session.EndpointDesktop || !resolved.AllowsPersonalMemory() {
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
		ConversationID:  request.ConversationID,
		MaxOutputTokens: RespondMaxOutputTokens, MessageSource: "desktop_initiation",
		OutputCapabilities: s.OutputCapabilities(request.ConversationID),
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
	if s == nil || !s.TurnRuntimeReady() {
		return TurnOutcome{}, ErrTurnRuntimeUnavailable
	}
	var resolved *session.Resolved
	if request.MessageSource == "" || request.MessageSource == "direct" {
		current, err := s.ResolveInteraction(request.ConversationID)
		if err != nil {
			return TurnOutcome{}, err
		}
		resolved = &current
		if !allowsDirectTurn(current) {
			return TurnOutcome{}, errors.New("direct trigger requires a single direct interaction")
		}
	}
	request.TraceID = s.beginMessageTrace(request.MessageSource, request.ConversationID, request.MessageID, request.TraceID)
	if request.MessageSource == "" {
		request.MessageSource = "direct"
	}
	return e.submitRuntimeTurn(SubmitCompiledTurnRequest{
		ConversationID:       request.ConversationID,
		Input:                request.Input,
		MaxOutputTokens:      RespondMaxOutputTokens,
		TraceID:              request.TraceID,
		MessageID:            request.MessageID,
		MessageSource:        request.MessageSource,
		ReplyTargetMessageID: request.ReplyTargetMessageID,
		ReplyIntent:          request.ReplyIntent,
		RecentTargetReply:    request.RecentTargetReply,
		PersonNoteSenderIDs:  append([]string(nil), request.PersonNoteSenderIDs...),
		OutputCapabilities:   s.OutputCapabilities(request.ConversationID),
	}, resolved)
}

func allowsDirectTurn(resolved session.Resolved) bool {
	return resolved.Facts.Audience == session.AudienceSingle && resolved.Facts.Initiation == session.InitiationDirect
}
