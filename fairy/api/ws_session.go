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
	"fairy/memory"
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
	CheckOrigin:     func(r *http.Request) bool { return true },
}

type wsClientFrame struct {
	Type             string                                   `json:"type"`
	RequestID        string                                   `json:"requestId,omitempty"`
	Surface          string                                   `json:"surface,omitempty"`
	SurfaceKey       string                                   `json:"surfaceKey,omitempty"`
	ConversationID   string                                   `json:"conversationId,omitempty"`
	EvaluationReason companion.GroupParticipationEvaluationReason `json:"evaluationReason,omitempty"`
	Messages         []companion.GroupObservation             `json:"messages,omitempty"`
	Message          *companion.GroupObservation              `json:"message,omitempty"`
	Input            string                                   `json:"input,omitempty"`
	SpeechEnabled    bool                                     `json:"speechEnabled,omitempty"`
	TurnID           string                                   `json:"turnId,omitempty"`
}

type wsServerFrame struct {
	Type           string                  `json:"type"`
	RequestID      string                  `json:"requestId,omitempty"`
	ConversationID string                  `json:"conversationId,omitempty"`
	CharacterID    string                  `json:"characterId,omitempty"`
	MessageCount   int                     `json:"messageCount,omitempty"`
	Surface        companion.SurfaceKind   `json:"surface,omitempty"`
	Error          string                  `json:"error,omitempty"`
	Payload        json.RawMessage         `json:"payload,omitempty"`
	Event          *companion.HarnessEvent `json:"event,omitempty"`
}

func (s *Server) handleSessionWebSocket() app.HandlerFunc {
	return adaptor.HertzHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.token != "" {
			header := r.Header.Get("Authorization")
			if header != "Bearer "+s.token {
				http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
				return
			}
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
	server  *Server
	conn    *websocket.Conn
	writeMu sync.Mutex
	watchMu sync.Mutex
	watches map[string]func()
}

func (c *sessionConn) run(parent context.Context) {
	defer c.close()
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
	case "group.observe":
		c.handleObserve(frame)
	case "group.participate":
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
	result, err := c.server.openSession(ctx, frame.Surface, frame.SurfaceKey)
	if err != nil {
		_ = c.write(wsServerFrame{Type: "error", RequestID: frame.RequestID, Error: err.Error()})
		return
	}
	_ = c.write(wsServerFrame{
		Type: "session.opened", RequestID: frame.RequestID,
		ConversationID: result.ConversationID, CharacterID: result.CharacterID,
		MessageCount: result.MessageCount, Surface: result.Surface,
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
	ch, unsub := c.server.rt.Events.Subscribe(conversationID)
	c.watches[conversationID] = unsub
	c.watchMu.Unlock()
	go c.forwardHarness(conversationID, ch, unsub)
	_ = c.write(wsServerFrame{Type: "ack", RequestID: frame.RequestID, ConversationID: conversationID})
}

func (c *sessionConn) forwardHarness(conversationID string, ch <-chan companion.HarnessEvent, unsub func()) {
	for event := range ch {
		ev := event
		if err := c.write(wsServerFrame{Type: "harness", ConversationID: conversationID, Event: &ev}); err != nil {
			unsub()
			return
		}
	}
}

func (c *sessionConn) handleObserve(frame wsClientFrame) {
	if frame.Message == nil {
		_ = c.write(wsServerFrame{Type: "error", RequestID: frame.RequestID, Error: "message is required"})
		return
	}
	if err := c.server.rt.Companion.ObserveGroupMessage(frame.ConversationID, *frame.Message); err != nil {
		_ = c.write(wsServerFrame{Type: "error", RequestID: frame.RequestID, Error: err.Error()})
		return
	}
	_ = c.write(wsServerFrame{Type: "ack", RequestID: frame.RequestID, ConversationID: strings.TrimSpace(frame.ConversationID)})
}

func (c *sessionConn) handleParticipate(ctx context.Context, frame wsClientFrame) {
	result, err := c.server.rt.Companion.DecideGroupParticipation(ctx, companion.GroupParticipationRequest{
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
	surface, err := companion.NormalizeSurface(frame.Surface)
	if err != nil {
		_ = c.write(wsServerFrame{Type: "error", RequestID: frame.RequestID, Error: err.Error()})
		return
	}
	var turnSurface companion.SurfaceKind
	if strings.TrimSpace(frame.Surface) != "" {
		turnSurface = surface
	}
	if strings.TrimSpace(frame.Input) == "" {
		_ = c.write(wsServerFrame{Type: "error", RequestID: frame.RequestID, Error: "input is required"})
		return
	}
	go func() {
		outcome, err := c.server.rt.Companion.SubmitTurn(companion.SubmitTurnRequest{
			ConversationID: frame.ConversationID,
			Input:          frame.Input,
			SpeechEnabled:  frame.SpeechEnabled,
			Surface:        turnSurface,
		})
		if err != nil {
			_ = c.write(wsServerFrame{Type: "error", RequestID: frame.RequestID, Error: err.Error()})
			return
		}
		resolved, _ := c.server.rt.Companion.ResolveSurface(frame.ConversationID, turnSurface)
		payload, err := json.Marshal(map[string]any{"outcome": outcome, "surface": resolved})
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

func (c *sessionConn) close() {
	c.watchMu.Lock()
	for id, unsub := range c.watches {
		unsub()
		delete(c.watches, id)
	}
	c.watchMu.Unlock()
	_ = c.conn.Close()
}

type openSessionResult struct {
	ConversationID string
	CharacterID    string
	MessageCount   int
	Surface        companion.SurfaceKind
}

func (s *Server) openSession(ctx context.Context, surfaceRaw, surfaceKey string) (openSessionResult, error) {
	surface, err := companion.NormalizeSurface(surfaceRaw)
	if err != nil {
		return openSessionResult{}, err
	}
	var surfaceKeyDigest string
	if surfaceKey != "" {
		surfaceKeyDigest, err = s.rt.Secret.DigestSurfaceKey(string(surface), surfaceKey)
		if err != nil {
			return openSessionResult{}, err
		}
	} else if surface == companion.SurfaceIMPrivate || surface == companion.SurfaceIMGroup {
		return openSessionResult{}, errors.New("surfaceKey is required for IM sessions")
	}
	catalog, err := s.rt.Character.ListCharacters()
	if err != nil {
		return openSessionResult{}, err
	}
	if catalog.Active == nil {
		return openSessionResult{}, errors.New("no active character")
	}
	var bootstrap memory.ConversationBootstrap
	if surfaceKeyDigest != "" {
		bootstrap, err = s.rt.MemoryStore.OpenOrCreateSurfaceConversationContext(ctx, catalog.Active.CharacterID, string(surface), surfaceKeyDigest)
	} else {
		bootstrap, err = s.rt.MemoryStore.OpenOrCreateCharacterConversationContext(ctx, catalog.Active.CharacterID)
	}
	if err != nil {
		return openSessionResult{}, err
	}
	if err := s.rt.Companion.BindSurface(bootstrap.Conversation.ID, surface); err != nil {
		return openSessionResult{}, err
	}
	return openSessionResult{
		ConversationID: bootstrap.Conversation.ID,
		CharacterID:    bootstrap.Conversation.CharacterID,
		MessageCount:   len(bootstrap.Messages),
		Surface:        surface,
	}, nil
}
