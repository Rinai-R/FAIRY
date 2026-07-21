package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"fairy/companion"
	"fairy/memory"
	fairyruntime "fairy/runtime"
	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/protocol/sse"
	"go.uber.org/zap"
)

const sessionHarnessEventName = "harness"

// Options configures the Hertz Core HTTP API.
type Options struct {
	Addr   string // e.g. 127.0.0.1:8787
	Token  string // if non-empty, require Authorization: Bearer <token>
	Logger *zap.Logger
}

// Server wraps Hertz + Session Core runtime.
type Server struct {
	rt     *fairyruntime.Runtime
	engine *server.Hertz
	token  string
	logger *zap.Logger
}

func NewServer(rt *fairyruntime.Runtime, options Options) (*Server, error) {
	if rt == nil {
		return nil, errors.New("runtime is required")
	}
	addr := strings.TrimSpace(options.Addr)
	if addr == "" {
		addr = "127.0.0.1:8787"
	}
	logger := options.Logger
	if logger == nil {
		logger = rt.Logger
	}
	if options.Token != strings.TrimSpace(options.Token) {
		return nil, errors.New("API token must not contain leading or trailing whitespace")
	}
	engine := server.Default(server.WithHostPorts(addr), server.WithSenseClientDisconnection(true))
	s := &Server{rt: rt, engine: engine, token: options.Token, logger: logger}
	engine.Use(s.metricsMiddleware)
	s.routes()
	return s, nil
}

func (s *Server) Engine() *server.Hertz { return s.engine }

func (s *Server) Run() error {
	return s.engine.Run()
}

func (s *Server) Shutdown(ctx context.Context) error {
	return s.engine.Shutdown(ctx)
}

func (s *Server) routes() {
	v1 := s.engine.Group("/v1")
	v1.Use(s.authMiddleware)
	v1.GET("/status", s.handleStatus)
	v1.POST("/sessions", s.handleOpenSession)
	v1.GET("/sessions/:conversationId/messages", s.handleSessionMessages)
	v1.GET("/visual-assets/:packId/*assetPath", s.handleVisualAsset)
	v1.POST("/sessions/:conversationId/turns", s.handleSubmitTurn)
	v1.POST("/sessions/:conversationId/group-participation", s.handleGroupParticipation)
	v1.GET("/sessions/:conversationId/events", s.handleSessionEvents)
	v1.POST("/sessions/:conversationId/turns/:turnId/cancel", s.handleCancelTurn)
	s.registerConfigRoutes()
	s.registerCharacterRoutes()
	s.registerProfileRoutes()
	s.registerIntelligenceRoutes()
	s.registerUsageRoutes()
	s.registerObservabilityRoutes()
	s.registerConsoleRoutes()
}

func (s *Server) authMiddleware(ctx context.Context, c *app.RequestContext) {
	if s.token == "" {
		c.Next(ctx)
		return
	}
	header := string(c.GetHeader("Authorization"))
	if header != "Bearer "+s.token {
		c.AbortWithStatusJSON(http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	c.Next(ctx)
}

func (s *Server) handleStatus(ctx context.Context, c *app.RequestContext) {
	bootstrap, err := s.rt.Bootstrap.Status()
	if err != nil {
		writeErr(c, http.StatusInternalServerError, err)
		return
	}
	web, err := s.rt.Config.WebSearchStatus()
	if err != nil {
		writeErr(c, http.StatusInternalServerError, err)
		return
	}
	semantic, err := s.rt.Config.SemanticEmbeddingStatus()
	if err != nil {
		writeErr(c, http.StatusInternalServerError, err)
		return
	}
	payload := map[string]any{
		"bootstrap":            bootstrap,
		"configRoot":           s.rt.ConfigRoot,
		"webSearch":            web,
		"semanticEmbedding":    semantic,
		"activeBackgroundJobs": s.rt.Companion.ActiveBackgroundJobs(),
	}
	database, qdrant, secretKey := s.infrastructureStatus(ctx)
	payload["database"] = database
	payload["qdrant"] = qdrant
	payload["secretKey"] = secretKey
	s.enrichStatusPayload(payload)
	c.JSON(http.StatusOK, payload)
}

func (s *Server) handleOpenSession(ctx context.Context, c *app.RequestContext) {
	var body openSessionBody
	_ = c.Bind(&body) // empty body is allowed; invalid JSON still yields zero surface → desktop
	surface, err := companion.NormalizeSurface(body.Surface)
	if err != nil {
		writeErr(c, http.StatusBadRequest, err)
		return
	}
	var surfaceKeyDigest string
	if body.SurfaceKey != "" {
		surfaceKeyDigest, err = s.rt.Secret.DigestSurfaceKey(string(surface), body.SurfaceKey)
		if err != nil {
			writeErr(c, http.StatusBadRequest, err)
			return
		}
	} else if surface == companion.SurfaceIMPrivate || surface == companion.SurfaceIMGroup {
		writeErr(c, http.StatusBadRequest, errors.New("surfaceKey is required for IM sessions"))
		return
	}
	catalog, err := s.rt.Character.ListCharacters()
	if err != nil {
		writeErr(c, http.StatusInternalServerError, err)
		return
	}
	if catalog.Active == nil {
		writeErr(c, http.StatusConflict, errors.New("no active character"))
		return
	}
	var bootstrap memory.ConversationBootstrap
	if surfaceKeyDigest != "" {
		bootstrap, err = s.rt.MemoryStore.OpenOrCreateSurfaceConversationContext(ctx, catalog.Active.CharacterID, string(surface), surfaceKeyDigest)
	} else {
		bootstrap, err = s.rt.MemoryStore.OpenOrCreateCharacterConversationContext(ctx, catalog.Active.CharacterID)
	}
	if err != nil {
		writeErr(c, http.StatusInternalServerError, err)
		return
	}
	if err := s.rt.Companion.BindSurface(bootstrap.Conversation.ID, surface); err != nil {
		writeErr(c, http.StatusBadRequest, err)
		return
	}
	c.JSON(http.StatusOK, map[string]any{
		"conversationId": bootstrap.Conversation.ID,
		"characterId":    bootstrap.Conversation.CharacterID,
		"messageCount":   len(bootstrap.Messages),
		"surface":        surface,
	})
}

type openSessionBody struct {
	Surface    string `json:"surface"`
	SurfaceKey string `json:"surfaceKey"`
}

type submitTurnBody struct {
	Input         string `json:"input"`
	SpeechEnabled bool   `json:"speechEnabled"`
	Surface       string `json:"surface"`
}

type groupParticipationBody struct {
	EvaluationReason companion.GroupParticipationEvaluationReason `json:"evaluationReason"`
	Messages         []companion.GroupObservation                 `json:"messages"`
}

func (s *Server) handleGroupParticipation(ctx context.Context, c *app.RequestContext) {
	conversationID := c.Param("conversationId")
	var body groupParticipationBody
	if err := c.Bind(&body); err != nil {
		writeErr(c, http.StatusBadRequest, err)
		return
	}
	result, err := s.rt.Companion.DecideGroupParticipation(ctx, companion.GroupParticipationRequest{
		ConversationID:   conversationID,
		EvaluationReason: body.EvaluationReason,
		Messages:         body.Messages,
	})
	if err != nil {
		writeErr(c, http.StatusBadRequest, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (s *Server) handleSubmitTurn(ctx context.Context, c *app.RequestContext) {
	conversationID := c.Param("conversationId")
	var body submitTurnBody
	if err := c.Bind(&body); err != nil {
		writeErr(c, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(body.Input) == "" {
		writeErr(c, http.StatusBadRequest, errors.New("input is required"))
		return
	}
	surface, err := companion.NormalizeSurface(body.Surface)
	if err != nil {
		writeErr(c, http.StatusBadRequest, err)
		return
	}
	// Empty surface on turn means "use session binding / desktop" — pass "" so ResolveSurface can fall back.
	var turnSurface companion.SurfaceKind
	if strings.TrimSpace(body.Surface) != "" {
		turnSurface = surface
	}
	outcome, err := s.rt.Companion.SubmitTurn(companion.SubmitTurnRequest{
		ConversationID: conversationID,
		Input:          body.Input,
		SpeechEnabled:  body.SpeechEnabled,
		Surface:        turnSurface,
	})
	if err != nil {
		writeErr(c, http.StatusBadRequest, err)
		return
	}
	resolved, _ := s.rt.Companion.ResolveSurface(conversationID, turnSurface)
	c.JSON(http.StatusOK, map[string]any{"outcome": outcome, "surface": resolved})
}

func (s *Server) handleCancelTurn(ctx context.Context, c *app.RequestContext) {
	conversationID := c.Param("conversationId")
	turnID := c.Param("turnId")
	if err := s.rt.Companion.CancelTurn(conversationID, turnID); err != nil {
		writeErr(c, http.StatusBadRequest, err)
		return
	}
	c.JSON(http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleSessionEvents(ctx context.Context, c *app.RequestContext) {
	conversationID := c.Param("conversationId")
	if conversationID == "" {
		writeErr(c, http.StatusBadRequest, errors.New("conversationId is required"))
		return
	}
	ch, unsub := s.rt.Events.Subscribe(conversationID)
	defer unsub()

	w := sse.NewWriter(c)
	defer w.Close()

	// Initial comment/ping so clients know the stream is live.
	_ = w.WriteEvent("0", "ready", []byte(`{"ok":true}`))

	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			payload, err := json.Marshal(ev)
			if err != nil {
				s.logger.Warn("sse marshal", zap.Error(err))
				continue
			}
			id := fmt.Sprintf("%s-%d", ev.TurnID, ev.Sequence)
			if err := w.WriteEvent(id, sessionHarnessEventName, payload); err != nil {
				return
			}
		case <-time.After(15 * time.Second):
			if err := w.WriteEvent("", "ping", []byte(`{}`)); err != nil {
				return
			}
		}
	}
}

func writeErr(c *app.RequestContext, status int, err error) {
	c.JSON(status, map[string]any{"error": err.Error()})
}
