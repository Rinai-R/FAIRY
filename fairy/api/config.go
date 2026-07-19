package api

import (
	"context"
	"net/http"
	"strings"

	"fairy/config"
	"fairy/speech"
	"github.com/cloudwego/hertz/pkg/app"
)

func (s *Server) registerConfigRoutes() {
	v1 := s.engine.Group("/v1")
	v1.Use(s.authMiddleware)
	v1.GET("/config/model", s.handleGetModel)
	v1.PUT("/config/model", s.handlePutModel)
	v1.DELETE("/config/model", s.handleDeleteModel)

	v1.GET("/config/speech", s.handleGetSpeech)
	v1.PUT("/config/speech", s.handlePutSpeech)
	v1.DELETE("/config/speech", s.handleDeleteSpeech)

	v1.GET("/config/web-search", s.handleGetWebSearch)
	v1.PUT("/config/web-search", s.handlePutWebSearch)

	v1.GET("/config/semantic-embedding", s.handleGetSemantic)
	v1.PUT("/config/semantic-embedding", s.handlePutSemantic)
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

func (s *Server) handleGetSpeech(ctx context.Context, c *app.RequestContext) {
	status, err := s.rt.Speech.Status()
	if err != nil {
		writeErr(c, http.StatusInternalServerError, err)
		return
	}
	c.JSON(http.StatusOK, status)
}

func (s *Server) handlePutSpeech(ctx context.Context, c *app.RequestContext) {
	var body speech.SaveSettingsRequest
	if err := c.Bind(&body); err != nil {
		writeErr(c, http.StatusBadRequest, err)
		return
	}
	status, err := s.rt.Speech.SaveSettings(body)
	if err != nil {
		writeErr(c, http.StatusBadRequest, err)
		return
	}
	c.JSON(http.StatusOK, status)
}

func (s *Server) handleDeleteSpeech(ctx context.Context, c *app.RequestContext) {
	status, err := s.rt.Speech.ClearSettings()
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
	var body config.SemanticEmbeddingSettings
	if err := c.Bind(&body); err != nil {
		writeErr(c, http.StatusBadRequest, err)
		return
	}
	if body.SchemaVersion == 0 {
		body.SchemaVersion = 1
	}
	status, err := s.rt.Config.SaveSemanticEmbeddingSettings(body)
	if err != nil {
		writeErr(c, http.StatusBadRequest, err)
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
	if speechStatus, err := s.rt.Speech.Status(); err == nil {
		payload["speech"] = speechStatus
	} else {
		payload["speechError"] = err.Error()
	}
}