package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	appsession "fairy/app/session"
	"fairy/transport/desktopcapture"
	"fairy/transport/session"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/common/adaptor"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"go.uber.org/zap"
)

const (
	wsReadLimit      = session.DesktopCaptureMaxFrameBytes
	wsWriteWait      = 10 * time.Second
	wsPingInterval   = 30 * time.Second
	wsPongWait       = 60 * time.Second
	maxWSRequestJSON = 1 << 20

	sessionConnectionWatchCapacity       = appsession.ConnectionWatchCapacity
	sessionConnectionAssociationCapacity = appsession.ConnectionAssociationCapacity
	sessionConnectionTurnCapacity        = appsession.ConnectionTurnCapacity
	fairySessionProtocol                 = "fairy.session.v1"
	fairySessionTicketProtocolPrefix     = "fairy.session.ticket."
)

var (
	errSessionWatchCapacity       = appsession.ErrWatchCapacity
	errSessionAssociationCapacity = appsession.ErrAssociationCapacity
	errSessionTurnCapacity        = appsession.ErrTurnCapacity
	errSessionConnectionClosed    = appsession.ErrConnectionClosed
)

var sessionUpgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	Subprotocols:    []string{fairySessionProtocol},
	CheckOrigin:     allowedSessionOrigin,
}

// allowedSessionOrigin permits native clients (empty Origin) and local console hosts.
func allowedSessionOrigin(r *http.Request) bool {
	if r == nil {
		return false
	}
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return true
	}
	return isLocalConsoleOrigin(origin)
}

func isLocalConsoleOrigin(origin string) bool {
	origin = strings.ToLower(strings.TrimSpace(origin))
	switch {
	case origin == "http://127.0.0.1" || origin == "https://127.0.0.1":
		return true
	case origin == "http://localhost" || origin == "https://localhost":
		return true
	case strings.HasPrefix(origin, "http://127.0.0.1:") || strings.HasPrefix(origin, "https://127.0.0.1:"):
		return true
	case strings.HasPrefix(origin, "http://localhost:") || strings.HasPrefix(origin, "https://localhost:"):
		return true
	default:
		return false
	}
}

type wsClientFrame struct {
	Type               string                            `json:"type"`
	RequestID          string                            `json:"requestId,omitempty"`
	Endpoint           session.EndpointKind              `json:"endpoint,omitempty"`
	EndpointKey        string                            `json:"endpointKey,omitempty"`
	CharacterID        string                            `json:"characterId,omitempty"`
	Interaction        session.Context                   `json:"interaction,omitempty"`
	OutputCapabilities session.OutputCapabilities        `json:"outputCapabilities"`
	ConversationID     string                            `json:"conversationId,omitempty"`
	EvaluationReason   string                            `json:"evaluationReason,omitempty"`
	Messages           []session.AmbientObservation      `json:"messages,omitempty"`
	Message            *session.AmbientObservation       `json:"message,omitempty"`
	DesktopObservation *session.DesktopObservation       `json:"desktopObservation,omitempty"`
	Input              string                            `json:"input,omitempty"`
	MessageID          string                            `json:"messageId,omitempty"`
	TurnID             string                            `json:"turnId,omitempty"`
	CaptureResult      *session.DesktopCaptureResult     `json:"captureResult,omitempty"`
	DeliveryResult     *session.ExpressionDeliveryResult `json:"deliveryResult,omitempty"`
}

type wsServerFrame struct {
	Type           string                         `json:"type"`
	RequestID      string                         `json:"requestId,omitempty"`
	ConversationID string                         `json:"conversationId,omitempty"`
	CharacterID    string                         `json:"characterId,omitempty"`
	MessageCount   int                            `json:"messageCount,omitempty"`
	Endpoint       session.EndpointKind           `json:"endpoint,omitempty"`
	Error          string                         `json:"error,omitempty"`
	Payload        json.RawMessage                `json:"payload,omitempty"`
	Event          *session.Event                 `json:"event,omitempty"`
	Participation  *ParticipationEvent            `json:"participation,omitempty"`
	CaptureRequest *session.DesktopCaptureRequest `json:"captureRequest,omitempty"`
}

func (s *Server) handleSessionWebSocket() app.HandlerFunc {
	return adaptor.HertzHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		authorized := header == "Bearer "+s.token
		if !authorized && !s.consumeBrowserSessionTicket(r) {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		if !allowedSessionOrigin(r) {
			http.Error(w, `{"error":"origin not allowed"}`, http.StatusForbidden)
			return
		}
		conn, err := sessionUpgrader.Upgrade(w, r, nil)
		if err != nil {
			s.logger.Warn("session websocket upgrade", zap.Error(err))
			return
		}
		session := newSessionConn(s, conn)
		session.run(r.Context())
	}))
}

func (s *Server) consumeBrowserSessionTicket(r *http.Request) bool {
	if s == nil || s.sessionTickets == nil || r == nil {
		return false
	}
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" || !isLocalConsoleOrigin(origin) {
		return false
	}
	protocols := websocket.Subprotocols(r)
	hasProtocol := false
	ticket := ""
	for _, protocol := range protocols {
		switch {
		case protocol == fairySessionProtocol:
			hasProtocol = true
		case strings.HasPrefix(protocol, fairySessionTicketProtocolPrefix):
			if ticket != "" {
				return false
			}
			ticket = strings.TrimPrefix(protocol, fairySessionTicketProtocolPrefix)
		}
	}
	return hasProtocol && ticket != "" && s.sessionTickets.consume(origin, ticket)
}

type sessionConn struct {
	server              *Server
	conn                *websocket.Conn
	ownerID             string
	writeMu             sync.Mutex
	watchMu             sync.Mutex
	watches             map[string]func()
	captureRoutes       map[string]captureRegistration
	capabilityBindings  map[string]struct{}
	watchCapacity       int
	associationCapacity int
	turnCapacity        int
	turnSlots           chan struct{}
	closed              bool
	closeOnce           sync.Once
}

type captureRegistration struct {
	id         string
	unregister func()
}

func newSessionConn(server *Server, conn *websocket.Conn) *sessionConn {
	return &sessionConn{
		server:              server,
		conn:                conn,
		ownerID:             uuid.NewString(),
		watches:             make(map[string]func()),
		captureRoutes:       make(map[string]captureRegistration),
		capabilityBindings:  make(map[string]struct{}),
		watchCapacity:       sessionConnectionWatchCapacity,
		associationCapacity: sessionConnectionAssociationCapacity,
		turnCapacity:        sessionConnectionTurnCapacity,
		turnSlots:           make(chan struct{}, sessionConnectionTurnCapacity),
	}
}

func (c *sessionConn) run(parent context.Context) {
	defer c.shutdown(nil)
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	c.conn.SetReadLimit(wsReadLimit)
	_ = c.conn.SetReadDeadline(time.Now().Add(wsPongWait))
	c.conn.SetPongHandler(func(string) error {
		return c.conn.SetReadDeadline(time.Now().Add(wsPongWait))
	})
	if err := c.write(wsServerFrame{Type: "ready"}); err != nil {
		return
	}
	go c.pingLoop(ctx)
	for {
		_, raw, err := c.conn.ReadMessage()
		if err != nil {
			return
		}
		var frame wsClientFrame
		if err := json.Unmarshal(raw, &frame); err != nil {
			_ = c.write(wsServerFrame{Type: "error", Error: "invalid JSON frame"})
			continue
		}
		if len(raw) > maxWSRequestJSON && frame.Type != "desktop.capture.result" {
			_ = c.write(wsServerFrame{Type: "error", RequestID: frame.RequestID, Error: "request body exceeds 1 MiB"})
			continue
		}
		c.dispatch(ctx, frame)
	}
}

func (c *sessionConn) pingLoop(ctx context.Context) {
	ticker := time.NewTicker(wsPingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.writeMu.Lock()
			_ = c.conn.SetWriteDeadline(time.Now().Add(wsWriteWait))
			err := c.conn.WriteMessage(websocket.PingMessage, nil)
			c.writeMu.Unlock()
			if err != nil {
				c.shutdown(nil)
				return
			}
		}
	}
}

func (c *sessionConn) dispatch(ctx context.Context, frame wsClientFrame) {
	switch frame.Type {
	case "session.open":
		c.handleOpen(ctx, frame)
	case "session.watch":
		c.handleWatch(frame)
	case "ambient.observe":
		c.handleObserve(frame)
	case "desktop.observe":
		c.handleDesktopObserve(frame)
	case "participation.decide":
		c.handleParticipate(ctx, frame)
	case "turn.submit":
		c.handleSubmitTurn(frame)
	case "turn.cancel":
		c.handleCancelTurn(frame)
	case "desktop.capture.result":
		c.handleCaptureResult(ctx, frame)
	case "expression.delivery":
		c.handleExpressionDelivery(frame)
	default:
		_ = c.write(wsServerFrame{Type: "error", RequestID: frame.RequestID, Error: "unknown frame type"})
	}
}

func (c *sessionConn) handleExpressionDelivery(frame wsClientFrame) {
	if frame.DeliveryResult == nil {
		_ = c.write(wsServerFrame{Type: "error", RequestID: frame.RequestID, Error: "deliveryResult is required"})
		return
	}
	result := *frame.DeliveryResult
	if err := result.Validate(); err != nil {
		_ = c.write(wsServerFrame{Type: "error", RequestID: frame.RequestID, Error: err.Error()})
		return
	}
	c.watchMu.Lock()
	_, watched := c.watches[result.ConversationID]
	c.watchMu.Unlock()
	if !watched {
		_ = c.write(wsServerFrame{Type: "error", RequestID: frame.RequestID, Error: appsession.ErrDeliveryUnavailable.Error()})
		return
	}
	if err := c.server.sessionService().ReportExpressionDelivery(result); err != nil {
		_ = c.write(wsServerFrame{Type: "error", RequestID: frame.RequestID, Error: err.Error()})
		return
	}
	_ = c.write(wsServerFrame{Type: "ack", RequestID: frame.RequestID, ConversationID: result.ConversationID})
}

func (c *sessionConn) handleOpen(ctx context.Context, frame wsClientFrame) {
	result, err := c.server.sessionService().Open(ctx, session.OpenRequest{
		Endpoint: frame.Endpoint, EndpointKey: frame.EndpointKey, CharacterID: frame.CharacterID, Interaction: frame.Interaction,
	})
	if err != nil {
		_ = c.write(wsServerFrame{Type: "error", RequestID: frame.RequestID, Error: err.Error()})
		return
	}
	if err := c.bindOutputCapabilities(result.ConversationID, frame.OutputCapabilities); err != nil {
		_ = c.write(wsServerFrame{Type: "error", RequestID: frame.RequestID, Error: err.Error()})
		return
	}
	if frame.Endpoint == session.EndpointDesktop && frame.Interaction.Audience == session.AudienceSingle && frame.Interaction.Presentation == session.PresentationEmbodied {
		registrationID, unregister, registerErr := c.server.sessionService().RegisterDesktopCapture(result.ConversationID, frame.Endpoint, frame.Interaction, func(request session.DesktopCaptureRequest) error {
			requestCopy := request
			return c.write(wsServerFrame{Type: "desktop.capture.request", ConversationID: request.ConversationID, CaptureRequest: &requestCopy})
		})
		if registerErr != nil {
			_ = c.write(wsServerFrame{Type: "error", RequestID: frame.RequestID, Error: registerErr.Error()})
			return
		}
		c.watchMu.Lock()
		if c.closed {
			c.watchMu.Unlock()
			unregister()
			_ = c.write(wsServerFrame{Type: "error", RequestID: frame.RequestID, Error: errSessionConnectionClosed.Error()})
			return
		}
		if previous, ok := c.captureRoutes[result.ConversationID]; ok {
			previous.unregister()
		}
		c.captureRoutes[result.ConversationID] = captureRegistration{id: registrationID, unregister: unregister}
		c.watchMu.Unlock()
	}
	_ = c.write(wsServerFrame{
		Type: "session.opened", RequestID: frame.RequestID,
		ConversationID: result.ConversationID, CharacterID: result.CharacterID,
		MessageCount: result.MessageCount, Endpoint: result.Endpoint,
	})
}

func (c *sessionConn) bindOutputCapabilities(conversationID string, capabilities session.OutputCapabilities) error {
	if c == nil || c.server == nil || c.server.sessionService() == nil {
		return errors.New("companion service is unavailable")
	}
	added, err := c.reserveCapabilityBinding(conversationID)
	if err != nil {
		return err
	}
	if err := c.server.sessionService().BindOutputCapabilities(c.ownerID, conversationID, capabilities); err != nil {
		if added {
			c.rollbackCapabilityBinding(conversationID)
		}
		return err
	}
	c.watchMu.Lock()
	if c.closed {
		delete(c.capabilityBindings, conversationID)
		c.watchMu.Unlock()
		c.server.sessionService().UnbindOutputCapabilities(c.ownerID, conversationID)
		return errSessionConnectionClosed
	}
	c.watchMu.Unlock()
	return nil
}

func (c *sessionConn) reserveCapabilityBinding(conversationID string) (bool, error) {
	c.watchMu.Lock()
	defer c.watchMu.Unlock()
	if c.closed {
		return false, errSessionConnectionClosed
	}
	if c.capabilityBindings == nil {
		c.capabilityBindings = make(map[string]struct{})
	}
	if _, exists := c.capabilityBindings[conversationID]; exists {
		return false, nil
	}
	capacity := c.associationCapacity
	if capacity <= 0 {
		capacity = sessionConnectionAssociationCapacity
	}
	if len(c.capabilityBindings) >= capacity {
		return false, errSessionAssociationCapacity
	}
	c.capabilityBindings[conversationID] = struct{}{}
	return true, nil
}

func (c *sessionConn) rollbackCapabilityBinding(conversationID string) {
	c.watchMu.Lock()
	delete(c.capabilityBindings, conversationID)
	c.watchMu.Unlock()
}

func (c *sessionConn) handleCaptureResult(ctx context.Context, frame wsClientFrame) {
	if frame.CaptureResult == nil {
		_ = c.write(wsServerFrame{Type: "error", RequestID: frame.RequestID, Error: "captureResult is required"})
		return
	}
	result := *frame.CaptureResult
	c.watchMu.Lock()
	registration, ok := c.captureRoutes[result.ConversationID]
	c.watchMu.Unlock()
	if !ok {
		_ = c.write(wsServerFrame{Type: "error", RequestID: frame.RequestID, Error: desktopcapture.ErrDesktopCaptureResultRejected.Error()})
		return
	}
	if err := c.server.sessionService().AcceptDesktopCapture(ctx, registration.id, result); err != nil {
		_ = c.write(wsServerFrame{Type: "error", RequestID: frame.RequestID, Error: err.Error()})
		return
	}
	_ = c.write(wsServerFrame{Type: "ack", RequestID: frame.RequestID, ConversationID: result.ConversationID})
}

func (c *sessionConn) handleWatch(frame wsClientFrame) {
	conversationID := strings.TrimSpace(frame.ConversationID)
	if conversationID == "" {
		_ = c.write(wsServerFrame{Type: "error", RequestID: frame.RequestID, Error: "conversationId is required"})
		return
	}
	if c.server.sessionService() == nil {
		_ = c.write(wsServerFrame{Type: "error", RequestID: frame.RequestID, Error: "event subscriptions are unavailable"})
		return
	}
	exists, err := c.reserveWatch(conversationID)
	if err != nil {
		_ = c.write(wsServerFrame{Type: "error", RequestID: frame.RequestID, Error: err.Error()})
		return
	}
	if exists {
		_ = c.write(wsServerFrame{Type: "ack", RequestID: frame.RequestID, ConversationID: conversationID})
		return
	}
	watch, err := c.server.sessionService().Watch(conversationID)
	if err != nil {
		c.rollbackWatchReservation(conversationID)
		_ = c.write(wsServerFrame{Type: "error", RequestID: frame.RequestID, Error: err.Error()})
		return
	}
	unsubscribe := func() {
		watch.Turns.Unsubscribe()
		watch.Participation.Unsubscribe()
	}
	if err := c.commitWatch(conversationID, unsubscribe); err != nil {
		unsubscribe()
		_ = c.write(wsServerFrame{Type: "error", RequestID: frame.RequestID, Error: err.Error()})
		return
	}
	go c.forwardTurnEvents(conversationID, watch.Turns)
	go c.forwardParticipationEvents(conversationID, watch.Participation)
	_ = c.write(wsServerFrame{Type: "ack", RequestID: frame.RequestID, ConversationID: conversationID})
}

func (c *sessionConn) rollbackWatchReservation(conversationID string) {
	c.watchMu.Lock()
	if unsubscribe, reserved := c.watches[conversationID]; reserved && unsubscribe == nil {
		delete(c.watches, conversationID)
	}
	c.watchMu.Unlock()
}

func (c *sessionConn) reserveWatch(conversationID string) (bool, error) {
	c.watchMu.Lock()
	defer c.watchMu.Unlock()
	if c.closed {
		return false, errSessionConnectionClosed
	}
	if c.watches == nil {
		c.watches = make(map[string]func())
	}
	if _, exists := c.watches[conversationID]; exists {
		return true, nil
	}
	capacity := c.watchCapacity
	if capacity <= 0 {
		capacity = sessionConnectionWatchCapacity
	}
	if len(c.watches) >= capacity {
		return false, errSessionWatchCapacity
	}
	c.watches[conversationID] = nil
	return false, nil
}

func (c *sessionConn) commitWatch(conversationID string, unsubscribe func()) error {
	c.watchMu.Lock()
	defer c.watchMu.Unlock()
	if c.closed {
		delete(c.watches, conversationID)
		return errSessionConnectionClosed
	}
	if _, reserved := c.watches[conversationID]; !reserved {
		return errSessionConnectionClosed
	}
	c.watches[conversationID] = unsubscribe
	return nil
}

func (c *sessionConn) forwardParticipationEvents(conversationID string, subscription ParticipationSubscription) {
	defer subscription.Unsubscribe()
	for {
		select {
		case err, ok := <-subscription.Failures:
			if ok && err != nil {
				c.shutdown(err)
			}
			return
		case event, ok := <-subscription.Events:
			if !ok {
				return
			}
			ev := event
			if err := c.write(wsServerFrame{Type: "participation.event", ConversationID: conversationID, Participation: &ev}); err != nil {
				c.shutdown(nil)
				return
			}
		}
	}
}

func (c *sessionConn) forwardTurnEvents(conversationID string, subscription EventSubscription) {
	defer subscription.Unsubscribe()
	for {
		select {
		case err, ok := <-subscription.Failures:
			if ok && err != nil {
				c.shutdown(err)
			}
			return
		default:
		}
		select {
		case err, ok := <-subscription.Failures:
			if ok && err != nil {
				c.shutdown(err)
			}
			return
		case event, ok := <-subscription.Events:
			if !ok {
				select {
				case err, failureOpen := <-subscription.Failures:
					if failureOpen && err != nil {
						c.shutdown(err)
					}
				default:
				}
				return
			}
			ev := event
			if err := c.write(wsServerFrame{Type: "turn.event", ConversationID: conversationID, Event: &ev}); err != nil {
				c.shutdown(nil)
				return
			}
		}
	}
}

func (c *sessionConn) handleObserve(frame wsClientFrame) {
	if frame.Message == nil {
		_ = c.write(wsServerFrame{Type: "error", RequestID: frame.RequestID, Error: "message is required"})
		return
	}
	if err := c.server.sessionService().ObserveAmbient(frame.ConversationID, *frame.Message); err != nil {
		_ = c.write(wsServerFrame{Type: "error", RequestID: frame.RequestID, Error: err.Error()})
		return
	}
	_ = c.write(wsServerFrame{Type: "ack", RequestID: frame.RequestID, ConversationID: strings.TrimSpace(frame.ConversationID)})
}

func (c *sessionConn) handleDesktopObserve(frame wsClientFrame) {
	if frame.DesktopObservation == nil {
		_ = c.write(wsServerFrame{Type: "error", RequestID: frame.RequestID, Error: "desktopObservation is required"})
		return
	}
	plan, err := c.server.sessionService().ObserveDesktop(frame.ConversationID, *frame.DesktopObservation)
	if err != nil {
		_ = c.write(wsServerFrame{Type: "error", RequestID: frame.RequestID, Error: err.Error()})
		return
	}
	payload, err := json.Marshal(plan)
	if err != nil {
		_ = c.write(wsServerFrame{Type: "error", RequestID: frame.RequestID, Error: "encoding desktop observation result"})
		return
	}
	_ = c.write(wsServerFrame{Type: "result", RequestID: frame.RequestID, ConversationID: strings.TrimSpace(frame.ConversationID), Payload: payload})
}

func (c *sessionConn) handleParticipate(ctx context.Context, frame wsClientFrame) {
	result, err := c.server.sessionService().DecideParticipation(ctx, frame.ConversationID, session.ParticipationRequest{
		EvaluationReason: frame.EvaluationReason,
		Messages:         frame.Messages,
	})
	if err != nil {
		_ = c.write(wsServerFrame{Type: "error", RequestID: frame.RequestID, Error: err.Error()})
		return
	}
	payload, err := json.Marshal(result)
	if err != nil {
		_ = c.write(wsServerFrame{Type: "error", RequestID: frame.RequestID, Error: err.Error()})
		return
	}
	_ = c.write(wsServerFrame{Type: "result", RequestID: frame.RequestID, Payload: payload})
}

func (c *sessionConn) handleSubmitTurn(frame wsClientFrame) {
	if strings.TrimSpace(frame.Input) == "" {
		_ = c.write(wsServerFrame{Type: "error", RequestID: frame.RequestID, Error: "input is required"})
		return
	}
	release, err := c.acquireTurnSubmission()
	if err != nil {
		_ = c.write(wsServerFrame{Type: "error", RequestID: frame.RequestID, Error: err.Error()})
		return
	}
	go func() {
		defer release()
		response, err := c.server.sessionService().SubmitTurn(appsession.TurnSubmission{
			ConversationID: frame.ConversationID,
			Input:          frame.Input,
			MessageID:      frame.MessageID,
		})
		if err != nil {
			_ = c.write(wsServerFrame{Type: "error", RequestID: frame.RequestID, Error: err.Error()})
			return
		}
		payload, err := json.Marshal(response)
		if err != nil {
			_ = c.write(wsServerFrame{Type: "error", RequestID: frame.RequestID, Error: err.Error()})
			return
		}
		_ = c.write(wsServerFrame{Type: "result", RequestID: frame.RequestID, Payload: payload})
	}()
}

func (c *sessionConn) acquireTurnSubmission() (func(), error) {
	if c == nil {
		return nil, errSessionConnectionClosed
	}
	c.watchMu.Lock()
	if c.closed {
		c.watchMu.Unlock()
		return nil, errSessionConnectionClosed
	}
	if c.turnSlots == nil {
		capacity := c.turnCapacity
		if capacity <= 0 {
			capacity = sessionConnectionTurnCapacity
		}
		c.turnSlots = make(chan struct{}, capacity)
	}
	slots := c.turnSlots
	select {
	case slots <- struct{}{}:
		c.watchMu.Unlock()
	default:
		c.watchMu.Unlock()
		return nil, errSessionTurnCapacity
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			<-slots
		})
	}, nil
}

func (c *sessionConn) handleCancelTurn(frame wsClientFrame) {
	if err := c.server.sessionService().CancelTurn(frame.ConversationID, frame.TurnID); err != nil {
		_ = c.write(wsServerFrame{Type: "error", RequestID: frame.RequestID, Error: err.Error()})
		return
	}
	payload, _ := json.Marshal(map[string]any{"ok": true})
	_ = c.write(wsServerFrame{Type: "result", RequestID: frame.RequestID, Payload: payload})
}

func (c *sessionConn) write(frame wsServerFrame) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_ = c.conn.SetWriteDeadline(time.Now().Add(wsWriteWait))
	return c.conn.WriteJSON(frame)
}

func (c *sessionConn) shutdown(reason error) {
	c.closeOnce.Do(func() {
		if reason != nil {
			code := websocket.CloseInternalServerErr
			if errors.Is(reason, ErrEventSubscriberOverflow) || errors.Is(reason, ErrParticipationSubscriberOverflow) {
				code = websocket.CloseTryAgainLater
			}
			c.writeMu.Lock()
			_ = c.conn.WriteControl(
				websocket.CloseMessage,
				websocket.FormatCloseMessage(code, reason.Error()),
				time.Now().Add(wsWriteWait),
			)
			c.writeMu.Unlock()
		}
		c.watchMu.Lock()
		c.closed = true
		unsubscribes := make([]func(), 0, len(c.watches))
		for id, unsubscribe := range c.watches {
			if unsubscribe != nil {
				unsubscribes = append(unsubscribes, unsubscribe)
			}
			delete(c.watches, id)
		}
		captureUnregisters := make([]func(), 0, len(c.captureRoutes))
		for id, registration := range c.captureRoutes {
			captureUnregisters = append(captureUnregisters, registration.unregister)
			delete(c.captureRoutes, id)
		}
		capabilityBindings := make([]string, 0, len(c.capabilityBindings))
		for conversationID := range c.capabilityBindings {
			capabilityBindings = append(capabilityBindings, conversationID)
			delete(c.capabilityBindings, conversationID)
		}
		c.watchMu.Unlock()
		for _, unsubscribe := range unsubscribes {
			unsubscribe()
		}
		for _, unregister := range captureUnregisters {
			unregister()
		}
		if c.server != nil && c.server.sessionService() != nil {
			for _, conversationID := range capabilityBindings {
				c.server.sessionService().UnbindOutputCapabilities(c.ownerID, conversationID)
			}
		}
		_ = c.conn.Close()
	})
}
