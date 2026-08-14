package appsession

import (
	"context"
	"errors"
	"strings"
	"sync"

	"fairy/transport/desktopcapture"
	"fairy/transport/session"

	"github.com/google/uuid"
)

type captureRegistration struct {
	id         string
	unregister func()
}

// Facade is the in-process Session connection. It shares Conversation, Turn,
// beat, and receipt contracts with the HTTP/WebSocket transport.
type Facade struct {
	service             *Service
	ownerID             string
	watchMu             sync.Mutex
	watches             map[string]func()
	turnEvents          map[string]<-chan session.Event
	participationEvents map[string]<-chan session.ParticipationEvent
	captureRoutes       map[string]captureRegistration
	capabilityBindings  map[string]struct{}
	watchCapacity       int
	associationCapacity int
	turnCapacity        int
	turnSlots           chan struct{}
	closed              bool
	closeOnce           sync.Once
	captureHandler      func(context.Context, session.DesktopCaptureRequest) session.DesktopCaptureResult
}

func NewFacade(service *Service) *Facade {
	return &Facade{
		service:             service,
		ownerID:             uuid.NewString(),
		watches:             make(map[string]func()),
		turnEvents:          make(map[string]<-chan session.Event),
		participationEvents: make(map[string]<-chan session.ParticipationEvent),
		captureRoutes:       make(map[string]captureRegistration),
		capabilityBindings:  make(map[string]struct{}),
		watchCapacity:       ConnectionWatchCapacity,
		associationCapacity: ConnectionAssociationCapacity,
		turnCapacity:        ConnectionTurnCapacity,
		turnSlots:           make(chan struct{}, ConnectionTurnCapacity),
	}
}

func (f *Facade) OpenSession(ctx context.Context, request session.OpenSessionRequest) (session.OpenSessionResponse, error) {
	if f == nil || f.service == nil {
		return session.OpenSessionResponse{}, ErrSessionUnavailable
	}
	result, err := f.service.Open(ctx, request)
	if err != nil {
		return session.OpenSessionResponse{}, err
	}
	if err := f.bindOutputCapabilities(result.ConversationID, request.OutputCapabilities); err != nil {
		return session.OpenSessionResponse{}, err
	}
	if request.Endpoint == session.EndpointDesktop && request.Interaction.Audience == session.AudienceSingle && request.Interaction.Presentation == session.PresentationEmbodied {
		if err := f.registerDesktopCapture(result.ConversationID, request.Endpoint, request.Interaction); err != nil {
			return session.OpenSessionResponse{}, err
		}
	}
	return result, nil
}

func (f *Facade) ListMessages(ctx context.Context, conversationID string, beforeSequence uint64, limit int) (session.MessagePage, error) {
	if f == nil || f.service == nil {
		return session.MessagePage{}, ErrSessionUnavailable
	}
	return f.service.ListMessages(ctx, conversationID, beforeSequence, limit)
}

func (f *Facade) SubmitTurn(ctx context.Context, conversationID string, request session.SubmitTurnRequest) (session.SubmitTurnResponse, error) {
	_ = ctx
	if f == nil || f.service == nil {
		return session.SubmitTurnResponse{}, ErrSessionUnavailable
	}
	release, err := f.acquireTurnSubmission()
	if err != nil {
		return session.SubmitTurnResponse{}, err
	}
	defer release()
	return f.service.SubmitTurn(TurnSubmission{
		ConversationID: conversationID,
		Input:          request.Input,
		MessageID:      request.MessageID,
	})
}

func (f *Facade) CancelTurn(_ context.Context, conversationID, turnID string) error {
	if f == nil || f.service == nil {
		return ErrSessionUnavailable
	}
	return f.service.CancelTurn(conversationID, turnID)
}

func (f *Facade) Watch(_ context.Context, conversationID string) (<-chan session.TurnEvent, error) {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return nil, errors.New("conversationId is required")
	}
	exists, err := f.reserveWatch(conversationID)
	if err != nil {
		return nil, err
	}
	if exists {
		f.watchMu.Lock()
		events := f.turnEvents[conversationID]
		f.watchMu.Unlock()
		return events, nil
	}
	watch, err := f.service.Watch(conversationID)
	if err != nil {
		f.rollbackWatchReservation(conversationID)
		return nil, err
	}
	unsubscribe := func() {
		watch.Turns.Unsubscribe()
		watch.Participation.Unsubscribe()
	}
	if err := f.commitWatch(conversationID, unsubscribe, watch); err != nil {
		unsubscribe()
		return nil, err
	}
	return watch.Turns.Events, nil
}

func (f *Facade) ParticipationEvents(conversationID string) (<-chan session.ParticipationEvent, error) {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return nil, errors.New("conversation ID is required")
	}
	f.watchMu.Lock()
	defer f.watchMu.Unlock()
	if f.closed {
		return nil, ErrConnectionClosed
	}
	events := f.participationEvents[conversationID]
	if events == nil {
		return nil, errors.New("conversation is not watched")
	}
	return events, nil
}

func (f *Facade) ObserveAmbient(_ context.Context, conversationID string, message session.AmbientObservation) error {
	if f == nil || f.service == nil {
		return ErrSessionUnavailable
	}
	return f.service.ObserveAmbient(conversationID, message)
}

func (f *Facade) ObserveDesktop(_ context.Context, conversationID string, observation session.DesktopObservation) (session.DesktopObservationResponse, error) {
	if f == nil || f.service == nil {
		return session.DesktopObservationResponse{}, ErrSessionUnavailable
	}
	result, err := f.service.ObserveDesktop(conversationID, observation)
	if err != nil {
		return session.DesktopObservationResponse{}, err
	}
	nodes := make([]session.DesktopObservationStep, len(result.Nodes))
	for index, node := range result.Nodes {
		nodes[index] = session.DesktopObservationStep{
			ID: node.ID, Kind: node.Kind, Depends: append([]string(nil), node.Depends...), OmitCode: node.OmitCode,
		}
	}
	return session.DesktopObservationResponse{
		Action: result.Action, Nodes: nodes, OmitReasons: append([]string(nil), result.OmitReasons...),
	}, nil
}

func (f *Facade) DecideParticipation(ctx context.Context, conversationID string, request session.ParticipationRequest) (session.ParticipationResponse, error) {
	if f == nil || f.service == nil {
		return session.ParticipationResponse{}, ErrSessionUnavailable
	}
	return f.service.DecideParticipation(ctx, conversationID, request)
}

func (f *Facade) ReportExpressionDelivery(_ context.Context, result session.ExpressionDeliveryResult) error {
	if f == nil || f.service == nil {
		return ErrSessionUnavailable
	}
	if err := result.Validate(); err != nil {
		return err
	}
	f.watchMu.Lock()
	_, watched := f.watches[result.ConversationID]
	closed := f.closed
	f.watchMu.Unlock()
	if closed {
		return ErrConnectionClosed
	}
	if !watched {
		return ErrDeliveryUnavailable
	}
	return f.service.ReportExpressionDelivery(result)
}

func (f *Facade) SetDesktopCaptureHandler(handler func(context.Context, session.DesktopCaptureRequest) session.DesktopCaptureResult) error {
	if f == nil {
		return ErrConnectionClosed
	}
	f.watchMu.Lock()
	defer f.watchMu.Unlock()
	if f.closed {
		return ErrConnectionClosed
	}
	f.captureHandler = handler
	return nil
}

func (f *Facade) SubmitCaptureResult(ctx context.Context, result session.DesktopCaptureResult) error {
	if f == nil || f.service == nil {
		return ErrSessionUnavailable
	}
	if err := result.ValidateShape(); err != nil {
		return err
	}
	f.watchMu.Lock()
	registration, ok := f.captureRoutes[result.ConversationID]
	f.watchMu.Unlock()
	if !ok {
		return desktopcapture.ErrDesktopCaptureResultRejected
	}
	return f.service.AcceptDesktopCapture(ctx, registration.id, result)
}

func (f *Facade) Close() error {
	if f == nil {
		return nil
	}
	f.closeOnce.Do(func() {
		f.watchMu.Lock()
		f.closed = true
		unsubscribes := make([]func(), 0, len(f.watches))
		for id, unsubscribe := range f.watches {
			if unsubscribe != nil {
				unsubscribes = append(unsubscribes, unsubscribe)
			}
			delete(f.watches, id)
		}
		captureUnregisters := make([]func(), 0, len(f.captureRoutes))
		for id, registration := range f.captureRoutes {
			captureUnregisters = append(captureUnregisters, registration.unregister)
			delete(f.captureRoutes, id)
		}
		capabilityBindings := make([]string, 0, len(f.capabilityBindings))
		for conversationID := range f.capabilityBindings {
			capabilityBindings = append(capabilityBindings, conversationID)
			delete(f.capabilityBindings, conversationID)
		}
		f.turnEvents = make(map[string]<-chan session.Event)
		f.participationEvents = make(map[string]<-chan session.ParticipationEvent)
		f.watchMu.Unlock()
		for _, unsubscribe := range unsubscribes {
			unsubscribe()
		}
		for _, unregister := range captureUnregisters {
			unregister()
		}
		if f.service != nil {
			for _, conversationID := range capabilityBindings {
				f.service.UnbindOutputCapabilities(f.ownerID, conversationID)
			}
		}
	})
	return nil
}

func (f *Facade) bindOutputCapabilities(conversationID string, capabilities session.OutputCapabilities) error {
	added, err := f.reserveCapabilityBinding(conversationID)
	if err != nil {
		return err
	}
	if err := f.service.BindOutputCapabilities(f.ownerID, conversationID, capabilities); err != nil {
		if added {
			f.rollbackCapabilityBinding(conversationID)
		}
		return err
	}
	f.watchMu.Lock()
	if f.closed {
		delete(f.capabilityBindings, conversationID)
		f.watchMu.Unlock()
		f.service.UnbindOutputCapabilities(f.ownerID, conversationID)
		return ErrConnectionClosed
	}
	f.watchMu.Unlock()
	return nil
}

func (f *Facade) registerDesktopCapture(conversationID string, endpoint session.EndpointKind, interaction session.Context) error {
	id, unregister, err := f.service.RegisterDesktopCapture(conversationID, endpoint, interaction, func(request session.DesktopCaptureRequest) error {
		f.watchMu.Lock()
		handler := f.captureHandler
		closed := f.closed
		registration, ok := f.captureRoutes[request.ConversationID]
		f.watchMu.Unlock()
		if closed || handler == nil || !ok {
			return desktopcapture.ErrDesktopCaptureSurfaceUnavailable
		}
		result := handler(context.Background(), request)
		return f.service.AcceptDesktopCapture(context.Background(), registration.id, result)
	})
	if err != nil {
		return err
	}
	f.watchMu.Lock()
	if f.closed {
		f.watchMu.Unlock()
		unregister()
		return ErrConnectionClosed
	}
	if previous, ok := f.captureRoutes[conversationID]; ok {
		previous.unregister()
	}
	f.captureRoutes[conversationID] = captureRegistration{id: id, unregister: unregister}
	f.watchMu.Unlock()
	return nil
}

func (f *Facade) reserveCapabilityBinding(conversationID string) (bool, error) {
	f.watchMu.Lock()
	defer f.watchMu.Unlock()
	if f.closed {
		return false, ErrConnectionClosed
	}
	if _, exists := f.capabilityBindings[conversationID]; exists {
		return false, nil
	}
	if len(f.capabilityBindings) >= f.associationCapacity {
		return false, ErrAssociationCapacity
	}
	f.capabilityBindings[conversationID] = struct{}{}
	return true, nil
}

func (f *Facade) rollbackCapabilityBinding(conversationID string) {
	f.watchMu.Lock()
	delete(f.capabilityBindings, conversationID)
	f.watchMu.Unlock()
}

func (f *Facade) reserveWatch(conversationID string) (bool, error) {
	f.watchMu.Lock()
	defer f.watchMu.Unlock()
	if f.closed {
		return false, ErrConnectionClosed
	}
	if _, exists := f.watches[conversationID]; exists {
		return true, nil
	}
	if len(f.watches) >= f.watchCapacity {
		return false, ErrWatchCapacity
	}
	f.watches[conversationID] = nil
	return false, nil
}

func (f *Facade) rollbackWatchReservation(conversationID string) {
	f.watchMu.Lock()
	if unsubscribe, reserved := f.watches[conversationID]; reserved && unsubscribe == nil {
		delete(f.watches, conversationID)
	}
	f.watchMu.Unlock()
}

func (f *Facade) commitWatch(conversationID string, unsubscribe func(), watch Watch) error {
	f.watchMu.Lock()
	defer f.watchMu.Unlock()
	if f.closed {
		delete(f.watches, conversationID)
		return ErrConnectionClosed
	}
	if _, reserved := f.watches[conversationID]; !reserved {
		return ErrConnectionClosed
	}
	f.watches[conversationID] = unsubscribe
	f.turnEvents[conversationID] = watch.Turns.Events
	f.participationEvents[conversationID] = watch.Participation.Events
	return nil
}

func (f *Facade) acquireTurnSubmission() (func(), error) {
	f.watchMu.Lock()
	if f.closed {
		f.watchMu.Unlock()
		return nil, ErrConnectionClosed
	}
	slots := f.turnSlots
	select {
	case slots <- struct{}{}:
		f.watchMu.Unlock()
	default:
		f.watchMu.Unlock()
		return nil, ErrTurnCapacity
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			<-slots
		})
	}, nil
}
