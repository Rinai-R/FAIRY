package coreclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"fairy/interaction"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

const (
	sessionWSPath    = "/v1/session/ws"
	wsReadyTimeout   = 15 * time.Second
	wsRequestTimeout = 15 * time.Second
	maxWSFrameBytes  = 4 << 20
)

var (
	ErrTurnEventConsumerOverflow = errors.New("session turn-event consumer overflow")
	errSessionSocketClosed       = errors.New("session websocket closed")
)

type sessionClientFrame struct {
	Type             string                   `json:"type"`
	RequestID        string                   `json:"requestId,omitempty"`
	Endpoint         interaction.EndpointKind `json:"endpoint,omitempty"`
	EndpointKey      string                   `json:"endpointKey,omitempty"`
	Interaction      interaction.Context      `json:"interaction,omitempty"`
	ConversationID   string                   `json:"conversationId,omitempty"`
	EvaluationReason string                   `json:"evaluationReason,omitempty"`
	Messages         []AmbientObservation     `json:"messages,omitempty"`
	Message          *AmbientObservation      `json:"message,omitempty"`
	Input            string                   `json:"input,omitempty"`
	SpeechEnabled    bool                     `json:"speechEnabled,omitempty"`
	TurnID           string                   `json:"turnId,omitempty"`
}

type sessionServerFrame struct {
	Type           string                   `json:"type"`
	RequestID      string                   `json:"requestId,omitempty"`
	ConversationID string                   `json:"conversationId,omitempty"`
	CharacterID    string                   `json:"characterId,omitempty"`
	MessageCount   int                      `json:"messageCount,omitempty"`
	Endpoint       interaction.EndpointKind `json:"endpoint,omitempty"`
	Error          string                   `json:"error,omitempty"`
	Payload        json.RawMessage          `json:"payload,omitempty"`
	Event          *TurnEvent               `json:"event,omitempty"`
	cause          error
}

// SessionSocket is a long-lived session-plane WebSocket.
type SessionSocket struct {
	conn    *websocket.Conn
	writeMu sync.Mutex

	mu         sync.Mutex
	pending    map[string]chan sessionServerFrame
	turnEvents map[string]chan TurnEvent
	done       chan struct{}
	connOnce   sync.Once
	connErr    error
	closing    bool
	closed     bool
	closeErr   error
}

// DialSession upgrades to the Core session WebSocket and waits for ready.
func (c *Client) DialSession(ctx context.Context) (*SessionSocket, error) {
	if c == nil {
		return nil, errors.New("client is not configured")
	}
	if ctx == nil {
		return nil, errors.New("context is required")
	}
	wsURL, err := c.sessionWSURL()
	if err != nil {
		return nil, err
	}
	header := http.Header{}
	if c.token != "" {
		header.Set("Authorization", "Bearer "+c.token)
	}
	dialer := websocket.Dialer{Proxy: http.ProxyFromEnvironment}
	conn, res, err := dialer.DialContext(ctx, wsURL, header)
	if err != nil {
		message := redactClientError(err.Error())
		status := 0
		if res != nil {
			status = res.StatusCode
			_ = res.Body.Close()
		}
		return nil, &Error{Action: "dial session websocket", Endpoint: wsURL, Status: status, Message: message}
	}
	socket := &SessionSocket{
		conn:       conn,
		pending:    make(map[string]chan sessionServerFrame),
		turnEvents: make(map[string]chan TurnEvent),
		done:       make(chan struct{}),
	}
	conn.SetReadLimit(maxWSFrameBytes)
	readyCtx, cancel := context.WithTimeout(ctx, readyTimeoutOr(c.timeout))
	defer cancel()
	if err := socket.waitReady(readyCtx); err != nil {
		_ = conn.Close()
		return nil, &Error{Action: "dial session websocket", Endpoint: wsURL, Message: err.Error()}
	}
	go socket.readLoop()
	return socket, nil
}

func readyTimeoutOr(timeout time.Duration) time.Duration {
	if timeout > 0 {
		return timeout
	}
	return wsReadyTimeout
}

func (c *Client) sessionWSURL() (string, error) {
	cloned := *c.baseURL
	switch cloned.Scheme {
	case "http":
		cloned.Scheme = "ws"
	case "https":
		cloned.Scheme = "wss"
	default:
		return "", errors.New("endpoint must be an absolute http or https URL")
	}
	cloned.Path = sessionWSPath
	cloned.RawQuery = ""
	cloned.Fragment = ""
	return cloned.String(), nil
}

func (s *SessionSocket) waitReady(ctx context.Context) error {
	type result struct {
		frame sessionServerFrame
		err   error
	}
	ch := make(chan result, 1)
	go func() {
		var frame sessionServerFrame
		err := s.conn.ReadJSON(&frame)
		ch <- result{frame: frame, err: err}
	}()
	select {
	case <-ctx.Done():
		return errors.New("websocket ready timeout")
	case result := <-ch:
		if result.err != nil {
			return result.err
		}
		if result.frame.Type != "ready" {
			return fmt.Errorf("first websocket frame is %q, want ready", result.frame.Type)
		}
		return nil
	}
}

func (s *SessionSocket) readLoop() {
	var terminalErr error
	defer func() { s.finish(terminalErr) }()
	for {
		var frame sessionServerFrame
		err := s.conn.ReadJSON(&frame)
		if err != nil {
			terminalErr = err
			return
		}
		switch frame.Type {
		case "turn.event":
			if frame.Event == nil {
				continue
			}
			conversationID := frame.ConversationID
			if conversationID == "" {
				conversationID = frame.Event.ConversationID
			}
			s.mu.Lock()
			ch := s.turnEvents[conversationID]
			if ch == nil {
				s.mu.Unlock()
				continue
			}
			select {
			case ch <- *frame.Event:
			default:
				s.mu.Unlock()
				terminalErr = fmt.Errorf("%w: conversation %s", ErrTurnEventConsumerOverflow, conversationID)
				return
			}
			s.mu.Unlock()
		default:
			if frame.RequestID == "" {
				continue
			}
			s.mu.Lock()
			ch := s.pending[frame.RequestID]
			s.mu.Unlock()
			if ch == nil {
				continue
			}
			select {
			case ch <- frame:
			default:
			}
		}
	}
}

// finish is called only by readLoop and owns all business-channel closure.
func (s *SessionSocket) finish(err error) {
	_ = s.closeConn()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	if errors.Is(err, ErrTurnEventConsumerOverflow) {
		s.closeErr = err
	} else if s.closing {
		s.closeErr = errSessionSocketClosed
	} else if err != nil {
		s.closeErr = err
	} else {
		s.closeErr = errSessionSocketClosed
	}
	for id, ch := range s.pending {
		select {
		case ch <- sessionServerFrame{Type: "error", RequestID: id, Error: s.closeErr.Error(), cause: s.closeErr}:
		default:
		}
		delete(s.pending, id)
	}
	for id, ch := range s.turnEvents {
		close(ch)
		delete(s.turnEvents, id)
	}
	close(s.done)
}

func (s *SessionSocket) Close() error {
	if s == nil || s.conn == nil {
		return nil
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closing = true
	done := s.done
	s.mu.Unlock()
	err := s.closeConn()
	<-done
	return err
}

func (s *SessionSocket) closeConn() error {
	s.connOnce.Do(func() {
		s.connErr = s.conn.Close()
	})
	return s.connErr
}

func (s *SessionSocket) request(ctx context.Context, frame sessionClientFrame, expectTypes ...string) (sessionServerFrame, error) {
	if s == nil || s.conn == nil {
		return sessionServerFrame{}, errors.New("session websocket is not open")
	}
	if ctx == nil {
		return sessionServerFrame{}, errors.New("context is required")
	}
	if strings.TrimSpace(frame.RequestID) == "" {
		frame.RequestID = uuid.NewString()
	}
	ch := make(chan sessionServerFrame, 1)
	s.mu.Lock()
	if s.closed || s.closing {
		err := s.closeErr
		s.mu.Unlock()
		if err == nil {
			err = errSessionSocketClosed
		}
		return sessionServerFrame{}, err
	}
	s.pending[frame.RequestID] = ch
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.pending, frame.RequestID)
		s.mu.Unlock()
	}()

	s.writeMu.Lock()
	err := s.conn.WriteJSON(frame)
	s.writeMu.Unlock()
	if err != nil {
		return sessionServerFrame{}, err
	}

	select {
	case <-ctx.Done():
		return sessionServerFrame{}, ctx.Err()
	case reply := <-ch:
		if reply.Type == "error" {
			if reply.cause != nil {
				return sessionServerFrame{}, reply.cause
			}
			message := reply.Error
			if message == "" {
				message = "session websocket error"
			}
			return sessionServerFrame{}, errors.New(message)
		}
		if len(expectTypes) > 0 {
			ok := false
			for _, want := range expectTypes {
				if reply.Type == want {
					ok = true
					break
				}
			}
			if !ok {
				return sessionServerFrame{}, fmt.Errorf("unexpected websocket frame %q", reply.Type)
			}
		}
		return reply, nil
	}
}

func (s *SessionSocket) OpenSession(ctx context.Context, request OpenSessionRequest) (OpenSessionResponse, error) {
	reply, err := s.request(ctx, sessionClientFrame{
		Type: "session.open", Endpoint: request.Endpoint, EndpointKey: request.EndpointKey, Interaction: request.Interaction,
	}, "session.opened")
	if err != nil {
		return OpenSessionResponse{}, err
	}
	result := OpenSessionResponse{
		ConversationID: reply.ConversationID,
		CharacterID:    reply.CharacterID,
		MessageCount:   reply.MessageCount,
		Endpoint:       reply.Endpoint,
	}
	if result.ConversationID == "" || result.CharacterID == "" || result.Endpoint == "" {
		return OpenSessionResponse{}, errors.New("open session response is missing required fields")
	}
	return result, nil
}

func (s *SessionSocket) Watch(ctx context.Context, conversationID string) (<-chan TurnEvent, error) {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return nil, errors.New("conversation ID is required")
	}
	s.mu.Lock()
	ch := s.turnEvents[conversationID]
	if ch == nil {
		ch = make(chan TurnEvent, 64)
		s.turnEvents[conversationID] = ch
	}
	s.mu.Unlock()
	_, err := s.request(ctx, sessionClientFrame{Type: "session.watch", ConversationID: conversationID}, "ack")
	if err != nil {
		s.mu.Lock()
		if s.turnEvents[conversationID] == ch {
			delete(s.turnEvents, conversationID)
		}
		s.mu.Unlock()
		return nil, err
	}
	return ch, nil
}

func (s *SessionSocket) ObserveAmbient(ctx context.Context, conversationID string, message AmbientObservation) error {
	_, err := s.request(ctx, sessionClientFrame{
		Type: "ambient.observe", ConversationID: conversationID, Message: &message,
	}, "ack")
	return err
}

func (s *SessionSocket) DecideParticipation(ctx context.Context, conversationID string, request ParticipationRequest) (ParticipationResponse, error) {
	reply, err := s.request(ctx, sessionClientFrame{
		Type: "participation.decide", ConversationID: conversationID,
		EvaluationReason: request.EvaluationReason, Messages: request.Messages,
	}, "result")
	if err != nil {
		return ParticipationResponse{}, err
	}
	var result ParticipationResponse
	if err := json.Unmarshal(reply.Payload, &result); err != nil {
		return ParticipationResponse{}, err
	}
	if err := validateParticipationResponse(result); err != nil {
		return ParticipationResponse{}, err
	}
	return result, nil
}

func (s *SessionSocket) SubmitTurn(ctx context.Context, conversationID string, request SubmitTurnRequest) (SubmitTurnResponse, error) {
	// Turn execution follows caller context; do not impose the short request timeout.
	reply, err := s.request(ctx, sessionClientFrame{
		Type: "turn.submit", ConversationID: conversationID,
		Input: request.Input, SpeechEnabled: request.SpeechEnabled,
	}, "result")
	if err != nil {
		return SubmitTurnResponse{}, err
	}
	var result SubmitTurnResponse
	if err := json.Unmarshal(reply.Payload, &result); err != nil {
		return SubmitTurnResponse{}, err
	}
	if result.Outcome.ConversationID == "" || result.Outcome.TurnID == "" {
		return SubmitTurnResponse{}, errors.New("submit turn response is missing required fields")
	}
	return result, nil
}

func (s *SessionSocket) CancelTurn(ctx context.Context, conversationID, turnID string) error {
	_, err := s.request(ctx, sessionClientFrame{
		Type: "turn.cancel", ConversationID: conversationID, TurnID: turnID,
	}, "result")
	return err
}

type wsEventStream struct {
	ctx    context.Context
	socket *SessionSocket
	ch     <-chan TurnEvent
}

func (s *wsEventStream) Next() (SSEEvent, error) {
	if s == nil || s.ch == nil {
		return SSEEvent{}, errors.New("stream is not open")
	}
	select {
	case <-s.ctx.Done():
		return SSEEvent{}, s.ctx.Err()
	case event, ok := <-s.ch:
		if !ok {
			return SSEEvent{}, io.EOF
		}
		payload, err := json.Marshal(event)
		if err != nil {
			return SSEEvent{}, err
		}
		return SSEEvent{Event: "turn.event", Data: payload}, nil
	}
}

func (s *wsEventStream) Close() error {
	if s == nil || s.socket == nil {
		return nil
	}
	return s.socket.Close()
}

func (c *Client) withSession(ctx context.Context, fn func(*SessionSocket) error) error {
	socket, err := c.DialSession(ctx)
	if err != nil {
		return err
	}
	defer socket.Close()
	return fn(socket)
}

func (c *Client) sessionRPC(ctx context.Context, timeout time.Duration, fn func(context.Context, *SessionSocket) error) error {
	if timeout <= 0 {
		timeout = c.timeout
	}
	if timeout <= 0 {
		timeout = wsRequestTimeout
	}
	requestCtx := ctx
	cancel := func() {}
	if _, ok := ctx.Deadline(); !ok {
		requestCtx, cancel = context.WithTimeout(ctx, timeout)
	}
	defer cancel()
	return c.withSession(requestCtx, func(socket *SessionSocket) error {
		return fn(requestCtx, socket)
	})
}
