package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"fairy/companion"
	"fairy/desktopcapture"
	"fairy/initiative"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/common/adaptor"
	"github.com/gorilla/websocket"
	"go.uber.org/zap"

	"fairy/session"
)

const (
	wsReadLimit      = session.DesktopCaptureMaxFrameBytes
	wsWriteWait      = 10 * time.Second
	wsPingInterval   = 30 * time.Second
	wsPongWait       = 60 * time.Second
	maxWSRequestJSON = 1 << 20
)

var sessionUpgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
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
	Type               string                                   `json:"type"`
	RequestID          string                                   `json:"requestId,omitempty"`
	Endpoint           session.EndpointKind                     `json:"endpoint,omitempty"`
	EndpointKey        string                                   `json:"endpointKey,omitempty"`
	Interaction        session.Context                          `json:"interaction,omitempty"`
	OutputCapabilities session.OutputCapabilities               `json:"outputCapabilities"`
	ConversationID     string                                   `json:"conversationId,omitempty"`
	EvaluationReason   initiative.ParticipationEvaluationReason `json:"evaluationReason,omitempty"`
	Messages           []initiative.AmbientObservation          `json:"messages,omitempty"`
	Message            *initiative.AmbientObservation           `json:"message,omitempty"`
	DesktopObservation *session.DesktopObservation              `json:"desktopObservation,omitempty"`
	Input              string                                   `json:"input,omitempty"`
	SpeechEnabled      bool                                     `json:"speechEnabled,omitempty"`
	TurnID             string                                   `json:"turnId,omitempty"`
	CaptureResult      *session.DesktopCaptureResult            `json:"captureResult,omitempty"`
	DeliveryResult     *session.ExpressionDeliveryResult        `json:"deliveryResult,omitempty"`
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
	Participation  *initiative.Event              `json:"participation,omitempty"`
	CaptureRequest *session.DesktopCaptureRequest `json:"captureRequest,omitempty"`
}

func (s *Server) handleSessionWebSocket() app.HandlerFunc {
	return adaptor.HertzHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		if header != "Bearer "+s.token {
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
		session := &sessionConn{server: s, conn: conn, watches: make(map[string]func()), captureRoutes: make(map[string]captureRegistration)}
		session.run(r.Context())
	}))
}

type sessionConn struct {
	server        *Server
	conn          *websocket.Conn
	writeMu       sync.Mutex
	watchMu       sync.Mutex
	watches       map[string]func()
	captureRoutes map[string]captureRegistration
	closeOnce     sync.Once
}

type captureRegistration struct {
	id         string
	unregister func()
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
	if !watched || !c.server.rt.Companion.OutputCapabilities(result.ConversationID).Sticker {
		_ = c.write(wsServerFrame{Type: "error", RequestID: frame.RequestID, Error: "sticker delivery report is unavailable for this session"})
		return
	}
	if err := c.server.rt.Companion.ReportExpressionDelivery(result); err != nil {
		_ = c.write(wsServerFrame{Type: "error", RequestID: frame.RequestID, Error: err.Error()})
		return
	}
	_ = c.write(wsServerFrame{Type: "ack", RequestID: frame.RequestID, ConversationID: result.ConversationID})
}

func (c *sessionConn) handleOpen(ctx context.Context, frame wsClientFrame) {
	result, err := c.server.openSession(ctx, frame.Endpoint, frame.EndpointKey, frame.Interaction, frame.OutputCapabilities)
	if err != nil {
		_ = c.write(wsServerFrame{Type: "error", RequestID: frame.RequestID, Error: err.Error()})
		return
	}
	if frame.Endpoint == session.EndpointDesktop && frame.Interaction.Audience == session.AudienceSingle && frame.Interaction.Presentation == session.PresentationEmbodied {
		registrationID, unregister, registerErr := c.server.rt.Captures.Register(result.ConversationID, frame.Endpoint, frame.Interaction, func(request session.DesktopCaptureRequest) error {
			requestCopy := request
			return c.write(wsServerFrame{Type: "desktop.capture.request", ConversationID: request.ConversationID, CaptureRequest: &requestCopy})
		})
		if registerErr != nil {
			_ = c.write(wsServerFrame{Type: "error", RequestID: frame.RequestID, Error: registerErr.Error()})
			return
		}
		c.watchMu.Lock()
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
	if err := c.server.rt.Captures.AcceptResult(ctx, registration.id, result); err != nil {
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
	c.watchMu.Lock()
	if _, exists := c.watches[conversationID]; exists {
		c.watchMu.Unlock()
		_ = c.write(wsServerFrame{Type: "ack", RequestID: frame.RequestID, ConversationID: conversationID})
		return
	}
	if c.server.rt.SubscribeTurnEvents == nil || c.server.rt.SubscribeParticipation == nil {
		c.watchMu.Unlock()
		_ = c.write(wsServerFrame{Type: "error", RequestID: frame.RequestID, Error: "event subscriptions are unavailable"})
		return
	}
	subscription := c.server.rt.SubscribeTurnEvents(conversationID)
	participation := c.server.rt.SubscribeParticipation(conversationID)
	c.watches[conversationID] = func() {
		subscription.Unsubscribe()
		participation.Unsubscribe()
	}
	c.watchMu.Unlock()
	go c.forwardTurnEvents(conversationID, subscription)
	go c.forwardParticipationEvents(conversationID, participation)
	_ = c.write(wsServerFrame{Type: "ack", RequestID: frame.RequestID, ConversationID: conversationID})
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
	if c.server.rt.Initiative == nil {
		_ = c.write(wsServerFrame{Type: "error", RequestID: frame.RequestID, Error: "initiative service is unavailable"})
		return
	}
	if err := c.server.rt.Initiative.ObserveAmbient(frame.ConversationID, *frame.Message); err != nil {
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
	if c.server.rt.Initiative == nil {
		_ = c.write(wsServerFrame{Type: "error", RequestID: frame.RequestID, Error: "initiative service is unavailable"})
		return
	}
	plan, err := c.server.rt.Initiative.ObserveDesktop(frame.ConversationID, *frame.DesktopObservation)
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
	if c.server.rt.Initiative == nil {
		_ = c.write(wsServerFrame{Type: "error", RequestID: frame.RequestID, Error: "initiative service is unavailable"})
		return
	}
	result, err := c.server.rt.Initiative.DecideParticipation(ctx, initiative.ParticipationRequest{
		ConversationID:   frame.ConversationID,
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
	go func() {
		outcome, err := c.server.rt.Companion.SubmitTurn(companion.SubmitTurnRequest{
			ConversationID: frame.ConversationID,
			Input:          frame.Input,
			SpeechEnabled:  frame.SpeechEnabled,
		})
		if err != nil {
			_ = c.write(wsServerFrame{Type: "error", RequestID: frame.RequestID, Error: err.Error()})
			return
		}
		payload, err := json.Marshal(map[string]any{"outcome": outcome})
		if err != nil {
			_ = c.write(wsServerFrame{Type: "error", RequestID: frame.RequestID, Error: err.Error()})
			return
		}
		_ = c.write(wsServerFrame{Type: "result", RequestID: frame.RequestID, Payload: payload})
	}()
}

func (c *sessionConn) handleCancelTurn(frame wsClientFrame) {
	if err := c.server.rt.Companion.CancelTurn(frame.ConversationID, frame.TurnID); err != nil {
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
		unsubscribes := make([]func(), 0, len(c.watches))
		for id, unsubscribe := range c.watches {
			unsubscribes = append(unsubscribes, unsubscribe)
			delete(c.watches, id)
		}
		captureUnregisters := make([]func(), 0, len(c.captureRoutes))
		for id, registration := range c.captureRoutes {
			captureUnregisters = append(captureUnregisters, registration.unregister)
			delete(c.captureRoutes, id)
		}
		c.watchMu.Unlock()
		for _, unsubscribe := range unsubscribes {
			unsubscribe()
		}
		for _, unregister := range captureUnregisters {
			unregister()
		}
		_ = c.conn.Close()
	})
}

type openSessionResult struct {
	ConversationID string
	CharacterID    string
	MessageCount   int
	Endpoint       session.EndpointKind
}

func (s *Server) openSession(ctx context.Context, endpoint session.EndpointKind, endpointKey string, interactionContext session.Context, outputCapabilities session.OutputCapabilities) (openSessionResult, error) {
	if err := interactionContext.Validate(endpoint); err != nil {
		return openSessionResult{}, err
	}
	if strings.TrimSpace(endpointKey) == "" {
		return openSessionResult{}, errors.New("endpointKey is required")
	}
	endpointKeyDigest, err := s.rt.Secret.DigestEndpointKey(endpoint, endpointKey)
	if err != nil {
		return openSessionResult{}, err
	}
	principalDigest := ""
	if interactionContext.Principal != nil {
		principalDigest, err = s.rt.Secret.DigestPrincipal(*interactionContext.Principal)
		if err != nil {
			return openSessionResult{}, err
		}
	}
	binding, err := session.NewBinding(endpoint, interactionContext, principalDigest)
	if err != nil {
		return openSessionResult{}, err
	}
	catalog, err := s.rt.Character.ListCharacters()
	if err != nil {
		return openSessionResult{}, err
	}
	if catalog.Active == nil {
		return openSessionResult{}, errors.New("no active character")
	}
	bootstrap, err := s.rt.MemoryStore.OpenOrCreateEndpointConversationContext(ctx, catalog.Active.CharacterID, binding, endpointKeyDigest)
	if err != nil {
		return openSessionResult{}, err
	}
	if err := s.rt.Companion.BindInteraction(bootstrap.Conversation.ID, binding); err != nil {
		return openSessionResult{}, err
	}
	if err := s.rt.Companion.BindOutputCapabilities(bootstrap.Conversation.ID, outputCapabilities); err != nil {
		return openSessionResult{}, err
	}
	return openSessionResult{
		ConversationID: bootstrap.Conversation.ID,
		CharacterID:    bootstrap.Conversation.CharacterID,
		MessageCount:   len(bootstrap.Messages),
		Endpoint:       endpoint,
	}, nil
}
