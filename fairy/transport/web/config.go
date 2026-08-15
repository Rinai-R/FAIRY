package web

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"fairy/runtime/config"

	"github.com/cloudwego/hertz/pkg/app"
)

var errQQAllowlistLocalOnly = errors.New("QQ allowlist is managed by the local plugin workspace")

func (s *Server) registerConfigRoutes() {
	v1 := s.engine.Group("/v1")
	v1.Use(s.authMiddleware)
	v1.GET("/config/model", s.handleGetModel)
	v1.PUT("/config/model", s.handlePutModel)
	v1.DELETE("/config/model", s.handleDeleteModel)

	v1.GET("/config/web-search", s.handleGetWebSearch)
	v1.PUT("/config/web-search", s.handlePutWebSearch)

	v1.GET("/config/semantic-embedding", s.handleGetSemantic)
	v1.PUT("/config/semantic-embedding", s.handlePutSemantic)
	v1.DELETE("/config/semantic-embedding/credential", s.handleDeleteSemanticCredential)

	v1.GET("/config/qq-onebot", s.handleGetQQOneBot)
	v1.PUT("/config/qq-onebot", s.handlePutQQOneBot)
}

func (s *Server) handleGetQQOneBot(ctx context.Context, c *app.RequestContext) {
	writeErr(c, http.StatusForbidden, errQQAllowlistLocalOnly)
}

func (s *Server) handlePutQQOneBot(ctx context.Context, c *app.RequestContext) {
	writeErr(c, http.StatusForbidden, errQQAllowlistLocalOnly)
}

func (s *Server) handleGetModel(ctx context.Context, c *app.RequestContext) {
	status, err := s.rt.Config.ModelStatus()
	if err != nil {
		writeErr(c, http.StatusInternalServerError, err)
		return
	}
	c.JSON(http.StatusOK, status)
}

type putModelBody struct {
	config.ModelConnectionInput
	APIKey *string `json:"apiKey"`
}

func (s *Server) handlePutModel(ctx context.Context, c *app.RequestContext) {
	var body putModelBody
	if err := c.Bind(&body); err != nil {
		writeErr(c, http.StatusBadRequest, err)
		return
	}
	status, err := s.rt.Config.SaveModelConnection(body.ModelConnectionInput, body.APIKey)
	if err != nil {
		writeErr(c, http.StatusBadRequest, err)
		return
	}
	c.JSON(http.StatusOK, status)
}

func (s *Server) handleDeleteModel(ctx context.Context, c *app.RequestContext) {
	status, err := s.rt.Config.ClearModelConnection()
	if err != nil {
		writeErr(c, http.StatusInternalServerError, err)
		return
	}
	c.JSON(http.StatusOK, status)
}

func (s *Server) handleGetWebSearch(ctx context.Context, c *app.RequestContext) {
	status, err := s.rt.Config.WebSearchStatus()
	if err != nil {
		writeErr(c, http.StatusInternalServerError, err)
		return
	}
	c.JSON(http.StatusOK, status)
}

type putWebSearchBody struct {
	Enabled bool   `json:"enabled"`
	BaseURL string `json:"baseUrl"`
}

func (s *Server) handlePutWebSearch(ctx context.Context, c *app.RequestContext) {
	var body putWebSearchBody
	if err := c.Bind(&body); err != nil {
		writeErr(c, http.StatusBadRequest, err)
		return
	}
	status, err := s.rt.Config.SaveWebSearchSettings(config.WebSearchSettings{
		SchemaVersion: 1,
		Enabled:       body.Enabled,
		BaseURL:       strings.TrimSpace(body.BaseURL),
	})
	if err != nil {
		writeErr(c, http.StatusBadRequest, err)
		return
	}
	c.JSON(http.StatusOK, status)
}

func (s *Server) handleGetSemantic(ctx context.Context, c *app.RequestContext) {
	status, err := s.rt.Config.SemanticEmbeddingStatus()
	if err != nil {
		writeErr(c, http.StatusInternalServerError, err)
		return
	}
	c.JSON(http.StatusOK, status)
}

func (s *Server) handlePutSemantic(ctx context.Context, c *app.RequestContext) {
	var body struct {
		config.SemanticEmbeddingSettings
		APIKey *string `json:"apiKey"`
	}
	if err := c.Bind(&body); err != nil {
		writeErr(c, http.StatusBadRequest, err)
		return
	}
	if body.SchemaVersion == 0 {
		body.SchemaVersion = 2
	}
	status, err := s.rt.Config.SaveSemanticEmbeddingSettings(body.SemanticEmbeddingSettings, body.APIKey)
	if err != nil {
		writeErr(c, http.StatusBadRequest, err)
		return
	}
	c.JSON(http.StatusOK, status)
}

func (s *Server) handleDeleteSemanticCredential(ctx context.Context, c *app.RequestContext) {
	status, err := s.rt.Config.DeleteSemanticEmbeddingCredential()
	if err != nil {
		writeErr(c, http.StatusInternalServerError, err)
		return
	}
	c.JSON(http.StatusOK, status)
}

func (s *Server) enrichStatusPayload(payload map[string]any) {
	if model, err := s.rt.Config.ModelStatus(); err == nil {
		payload["model"] = model
	} else {
		payload["modelError"] = err.Error()
	}
}
