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
	"fairy/interaction"
	fairyruntime "fairy/runtime"
	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/common/adaptor"
	"github.com/gorilla/websocket"
	"go.uber.org/zap"
)

const (
	wsReadLimit      = 1 << 20
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
	Type             string                                  `json:"type"`
	RequestID        string                                  `json:"requestId,omitempty"`
	Endpoint         interaction.EndpointKind                `json:"endpoint,omitempty"`
	EndpointKey      string                                  `json:"endpointKey,omitempty"`
	Interaction      interaction.Context                     `json:"interaction,omitempty"`
	ConversationID   string                                  `json:"conversationId,omitempty"`
	EvaluationReason companion.ParticipationEvaluationReason `json:"evaluationReason,omitempty"`
	Messages         []companion.AmbientObservation          `json:"messages,omitempty"`
	Message          *companion.AmbientObservation           `json:"message,omitempty"`
	Input            string                                  `json:"input,omitempty"`
	SpeechEnabled    bool                                    `json:"speechEnabled,omitempty"`
	TurnID           string                                  `json:"turnId,omitempty"`
}

type wsServerFrame struct {
	Type           string                   `json:"type"`
	RequestID      string                   `json:"requestId,omitempty"`
	ConversationID string                   `json:"conversationId,omitempty"`
	CharacterID    string                   `json:"characterId,omitempty"`
	MessageCount   int                      `json:"messageCount,omitempty"`
	Endpoint       interaction.EndpointKind `json:"endpoint,omitempty"`
	Error          string                   `json:"error,omitempty"`
	Payload        json.RawMessage          `json:"payload,omitempty"`
	Event          *companion.TurnEvent     `json:"event,omitempty"`
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
		session := &sessionConn{server: s, conn: conn, watches: make(map[string]func())}
		session.run(r.Context())
	}))
}

type sessionConn struct {
	server    *Server
	conn      *websocket.Conn
	writeMu   sync.Mutex
	watchMu   sync.Mutex
	watches   map[string]func()
	closeOnce sync.Once
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
		if len(raw) > maxWSRequestJSON {
			_ = c.write(wsServerFrame{Type: "error", Error: "request body exceeds 1 MiB"})
			continue
		}
		var frame wsClientFrame
		if err := json.Unmarshal(raw, &frame); err != nil {
			_ = c.write(wsServerFrame{Type: "error", Error: "invalid JSON frame"})
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
	case "participation.decide":
		c.handleParticipate(ctx, frame)
	case "turn.submit":
		c.handleSubmitTurn(frame)
	case "turn.cancel":
		c.handleCancelTurn(frame)
	default:
		_ = c.write(wsServerFrame{Type: "error", RequestID: frame.RequestID, Error: "unknown frame type"})
	}
}

func (c *sessionConn) handleOpen(ctx context.Context, frame wsClientFrame) {
	result, err := c.server.openSession(ctx, frame.Endpoint, frame.EndpointKey, frame.Interaction)
	if err != nil {
		_ = c.write(wsServerFrame{Type: "error", RequestID: frame.RequestID, Error: err.Error()})
		return
	}
	_ = c.write(wsServerFrame{
		Type: "session.opened", RequestID: frame.RequestID,
		ConversationID: result.ConversationID, CharacterID: result.CharacterID,
		MessageCount: result.MessageCount, Endpoint: result.Endpoint,
	})
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
	subscription := c.server.rt.Events.Subscribe(conversationID)
	c.watches[conversationID] = subscription.Unsubscribe
	c.watchMu.Unlock()
	go c.forwardTurnEvents(conversationID, subscription)
	_ = c.write(wsServerFrame{Type: "ack", RequestID: frame.RequestID, ConversationID: conversationID})
}

func (c *sessionConn) forwardTurnEvents(conversationID string, subscription fairyruntime.EventSubscription) {
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
	if err := c.server.rt.Companion.ObserveAmbient(frame.ConversationID, *frame.Message); err != nil {
		_ = c.write(wsServerFrame{Type: "error", RequestID: frame.RequestID, Error: err.Error()})
		return
	}
	_ = c.write(wsServerFrame{Type: "ack", RequestID: frame.RequestID, ConversationID: strings.TrimSpace(frame.ConversationID)})
}

func (c *sessionConn) handleParticipate(ctx context.Context, frame wsClientFrame) {
	result, err := c.server.rt.Companion.DecideParticipation(ctx, companion.ParticipationRequest{
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
			if errors.Is(reason, fairyruntime.ErrEventSubscriberOverflow) {
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
		c.watchMu.Unlock()
		for _, unsubscribe := range unsubscribes {
			unsubscribe()
		}
		_ = c.conn.Close()
	})
}

type openSessionResult struct {
	ConversationID string
	CharacterID    string
	MessageCount   int
	Endpoint       interaction.EndpointKind
}

func (s *Server) openSession(ctx context.Context, endpoint interaction.EndpointKind, endpointKey string, interactionContext interaction.Context) (openSessionResult, error) {
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
	binding, err := interaction.NewBinding(endpoint, interactionContext, principalDigest)
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
	return openSessionResult{
		ConversationID: bootstrap.Conversation.ID,
		CharacterID:    bootstrap.Conversation.CharacterID,
		MessageCount:   len(bootstrap.Messages),
		Endpoint:       endpoint,
	}, nil
}
