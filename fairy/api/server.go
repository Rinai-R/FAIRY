package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	fairyruntime "fairy/runtime"
	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"go.uber.org/zap"
)

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
	v1.GET("/session/ws", s.handleSessionWebSocket())
	v1.GET("/sessions/:conversationId/messages", s.handleSessionMessages)
	v1.GET("/visual-assets/:packId/*assetPath", s.handleVisualAsset)
	s.registerConfigRoutes()
	s.registerCharacterRoutes()
	s.registerProfileRoutes()
	s.registerIdentityRoutes()
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

func writeErr(c *app.RequestContext, status int, err error) {
	c.JSON(status, map[string]any{"error": err.Error()})
}
