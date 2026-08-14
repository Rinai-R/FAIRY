package appsession

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	history "fairy/context/history/transcript"
	"fairy/transport/session"
)

func (s *Service) Open(ctx context.Context, request session.OpenRequest) (session.OpenResponse, error) {
	if s == nil || s.secret == nil || s.characters == nil || s.transcript == nil || s.turns == nil {
		return session.OpenResponse{}, ErrSessionUnavailable
	}
	if err := request.Interaction.Validate(request.Endpoint); err != nil {
		return session.OpenResponse{}, err
	}
	if strings.TrimSpace(request.EndpointKey) == "" {
		return session.OpenResponse{}, errors.New("endpointKey is required")
	}
	endpointKeyDigest, err := s.secret.DigestEndpointKey(request.Endpoint, request.EndpointKey)
	if err != nil {
		return session.OpenResponse{}, err
	}
	principalDigest := ""
	if request.Interaction.Principal != nil {
		principalDigest, err = s.secret.DigestPrincipal(*request.Interaction.Principal)
		if err != nil {
			return session.OpenResponse{}, err
		}
	}
	binding, err := session.NewBinding(request.Endpoint, request.Interaction, principalDigest)
	if err != nil {
		return session.OpenResponse{}, err
	}
	catalog, err := s.characters.ListCharacters()
	if err != nil {
		return session.OpenResponse{}, err
	}
	selected := catalog.Active
	characterID := strings.TrimSpace(request.CharacterID)
	if characterID != "" {
		selected = nil
		for index := range catalog.Characters {
			if catalog.Characters[index].CharacterID == characterID {
				selected = &catalog.Characters[index]
				break
			}
		}
		if selected == nil {
			return session.OpenResponse{}, errors.New("character not found")
		}
	}
	if selected == nil {
		return session.OpenResponse{}, errors.New("no active character")
	}
	bootstrap, err := s.transcript.OpenOrCreateEndpointConversationContext(ctx, selected.CharacterID, binding, endpointKeyDigest)
	if err != nil {
		return session.OpenResponse{}, err
	}
	if err := s.turns.BindInteraction(bootstrap.Conversation.ID, binding); err != nil {
		return session.OpenResponse{}, err
	}
	return session.OpenResponse{
		ConversationID: bootstrap.Conversation.ID,
		CharacterID:    bootstrap.Conversation.CharacterID,
		MessageCount:   len(bootstrap.Messages),
		Endpoint:       request.Endpoint,
	}, nil
}

func (s *Service) ListMessages(ctx context.Context, conversationID string, beforeSequence uint64, limit int) (session.MessagePage, error) {
	if s == nil || s.transcript == nil {
		return session.MessagePage{}, ErrSessionUnavailable
	}
	if limit == 0 {
		limit = history.DefaultMessagePageLimit
	}
	page, err := s.transcript.ListConversationMessagesBeforeContext(ctx, conversationID, beforeSequence, limit)
	if err != nil {
		return session.MessagePage{}, err
	}
	return projectMessagePage(page), nil
}

func (s *Service) SubmitTurn(request TurnSubmission) (session.SubmitTurnResponse, error) {
	if s == nil || s.turns == nil {
		return session.SubmitTurnResponse{}, ErrSessionUnavailable
	}
	if strings.TrimSpace(request.Input) == "" {
		return session.SubmitTurnResponse{}, errors.New("input is required")
	}
	outcome, err := s.turns.SubmitTurn(request)
	if err != nil {
		return session.SubmitTurnResponse{}, err
	}
	return projectSubmitTurnResponse(outcome)
}

func (s *Service) CancelTurn(conversationID, turnID string) error {
	if s == nil || s.turns == nil {
		return ErrSessionUnavailable
	}
	return s.turns.CancelTurn(conversationID, turnID)
}

func (s *Service) ObserveAmbient(conversationID string, observation session.AmbientObservation) error {
	if s == nil || s.initiative == nil {
		return errors.New("initiative service is unavailable")
	}
	return s.initiative.ObserveAmbient(conversationID, observation)
}

func (s *Service) ObserveDesktop(conversationID string, observation session.DesktopObservation) (DesktopObservationResult, error) {
	if s == nil || s.initiative == nil {
		return DesktopObservationResult{}, errors.New("initiative service is unavailable")
	}
	return s.initiative.ObserveDesktop(conversationID, observation)
}

func (s *Service) DecideParticipation(ctx context.Context, conversationID string, request session.ParticipationRequest) (session.ParticipationResponse, error) {
	if s == nil || s.initiative == nil {
		return session.ParticipationResponse{}, errors.New("initiative service is unavailable")
	}
	return s.initiative.DecideParticipation(ctx, conversationID, request)
}

func (s *Service) ReportExpressionDelivery(result session.ExpressionDeliveryResult) error {
	if s == nil || s.turns == nil {
		return ErrSessionUnavailable
	}
	if err := result.Validate(); err != nil {
		return err
	}
	return s.turns.ReportExpressionDelivery(result)
}

func (s *Service) BindOutputCapabilities(ownerID, conversationID string, capabilities session.OutputCapabilities) error {
	if s == nil || s.turns == nil {
		return ErrSessionUnavailable
	}
	return s.turns.BindOutputCapabilities(ownerID, conversationID, capabilities)
}

func (s *Service) UnbindOutputCapabilities(ownerID, conversationID string) {
	if s == nil || s.turns == nil {
		return
	}
	s.turns.UnbindOutputCapabilities(ownerID, conversationID)
}

func (s *Service) Watch(conversationID string) (Watch, error) {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return Watch{}, errors.New("conversationId is required")
	}
	if s == nil || s.subscribeTurns == nil || s.subscribeParticipation == nil {
		return Watch{}, errors.New("event subscriptions are unavailable")
	}
	turns, err := s.subscribeTurns(conversationID)
	if err != nil {
		return Watch{}, err
	}
	participation, err := s.subscribeParticipation(conversationID)
	if err != nil {
		turns.Unsubscribe()
		return Watch{}, err
	}
	return Watch{Turns: turns, Participation: participation}, nil
}

func (s *Service) RegisterDesktopCapture(conversationID string, endpoint session.EndpointKind, interaction session.Context, send func(session.DesktopCaptureRequest) error) (string, func(), error) {
	if s == nil || s.captures == nil {
		return "", nil, errors.New("capture hub is not configured")
	}
	return s.captures.Register(conversationID, endpoint, interaction, send)
}

func (s *Service) AcceptDesktopCapture(ctx context.Context, registrationID string, result session.DesktopCaptureResult) error {
	if s == nil || s.captures == nil {
		return errors.New("capture hub is not configured")
	}
	return s.captures.AcceptResult(ctx, registrationID, result)
}

func projectMessagePage(page history.MessagePage) session.MessagePage {
	messages := make([]session.MessageRecord, 0, len(page.Messages))
	for _, record := range page.Messages {
		messages = append(messages, projectMessage(record))
	}
	return session.MessagePage{Messages: messages, NextBeforeSequence: page.NextBeforeSequence}
}

func projectMessage(record history.MessageRecord) session.MessageRecord {
	parts := make([]session.ExpressionPart, len(record.Parts))
	for index, part := range record.Parts {
		parts[index] = session.ExpressionPart{
			Kind:        session.ExpressionKind(part.Kind),
			Text:        part.Text,
			VisualState: part.VisualState,
		}
		if part.Sticker != nil {
			parts[index].Sticker = &session.StickerReference{
				ID:          part.Sticker.ID,
				Description: part.Sticker.Description,
				MIMEType:    part.Sticker.MIMEType,
			}
		}
	}
	return session.MessageRecord{
		ID:              record.ID,
		ConversationID:  record.ConversationID,
		TurnID:          record.TurnID,
		Sequence:        record.Sequence,
		Role:            record.Role,
		Content:         record.Content,
		Parts:           parts,
		CreatedAtUnixMS: record.CreatedAtUnixMS,
	}
}

func projectSubmitTurnResponse(outcome any) (session.SubmitTurnResponse, error) {
	switch value := outcome.(type) {
	case session.SubmitTurnResponse:
		return value, nil
	case session.Outcome:
		return session.SubmitTurnResponse{Outcome: value}, nil
	case *session.SubmitTurnResponse:
		if value == nil {
			return session.SubmitTurnResponse{}, errors.New("submit turn response is missing required fields")
		}
		return *value, nil
	default:
		raw, err := json.Marshal(outcome)
		if err != nil {
			return session.SubmitTurnResponse{}, err
		}
		var result session.SubmitTurnResponse
		if err := json.Unmarshal(raw, &result); err != nil {
			return session.SubmitTurnResponse{}, err
		}
		if result.Outcome.ConversationID == "" && result.Outcome.TurnID == "" {
			if err := json.Unmarshal(raw, &result.Outcome); err != nil {
				return session.SubmitTurnResponse{}, err
			}
		}
		if result.Outcome.ConversationID == "" || result.Outcome.TurnID == "" {
			return session.SubmitTurnResponse{}, errors.New("submit turn response is missing required fields")
		}
		return result, nil
	}
}
