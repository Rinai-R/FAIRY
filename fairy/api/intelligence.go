package api

import (
	"context"
	"net/http"
	"strings"

	"fairy/memory"
	"github.com/cloudwego/hertz/pkg/app"
)

func (s *Server) registerIntelligenceRoutes() {
	v1 := s.engine.Group("/v1")
	v1.Use(s.authMiddleware)
	v1.GET("/intelligence", s.handleIntelligence)
	v1.GET("/memories/personal", s.handlePersonalMemories)
	v1.POST("/memories/personal", s.handleCreatePersonalMemory)
	v1.PUT("/memories/personal/:id", s.handleRevisePersonalMemory)
	v1.DELETE("/memories/personal/:id", s.handleTombstonePersonalMemory)
}

func (s *Server) handleIntelligence(ctx context.Context, c *app.RequestContext) {
	summary, err := s.rt.Memory.Summary()
	if err != nil {
		summary = memory.Summary{}
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
	c.JSON(http.StatusOK, map[string]any{
		"summary":              summary,
		"activeBackgroundJobs": s.rt.Companion.ActiveBackgroundJobs(),
		"webSearch":            web,
		"semanticEmbedding":    semantic,
		"ready":                true,
	})
}

func (s *Server) handlePersonalMemories(ctx context.Context, c *app.RequestContext) {
	characterID := strings.TrimSpace(string(c.Query("characterId")))
	if characterID == "" {
		catalog, err := s.rt.Character.ListCharacters()
		if err != nil {
			writeErr(c, http.StatusInternalServerError, err)
			return
		}
		if catalog.Active == nil {
			writeErr(c, http.StatusConflict, errMissingCharacterID)
			return
		}
		characterID = catalog.Active.CharacterID
	}
	out, err := s.rt.Memory.PersonalMemoryCatalog(characterID)
	if err != nil {
		writeErr(c, http.StatusBadRequest, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

type createPersonalMemoryBody struct {
	Kind                  string             `json:"kind"`
	Scope                 memory.MemoryScope `json:"scope"`
	Content               string             `json:"content"`
	ConfidenceBasisPoints uint16             `json:"confidenceBasisPoints"`
}

func (s *Server) handleCreatePersonalMemory(ctx context.Context, c *app.RequestContext) {
	var body createPersonalMemoryBody
	if err := c.Bind(&body); err != nil {
		writeErr(c, http.StatusBadRequest, err)
		return
	}
	if body.ConfidenceBasisPoints == 0 {
		body.ConfidenceBasisPoints = 8000
	}
	record, err := s.rt.Memory.CreatePersonalMemory(body.Kind, body.Scope, body.Content, body.ConfidenceBasisPoints)
	if err != nil {
		writeErr(c, http.StatusBadRequest, err)
		return
	}
	c.JSON(http.StatusOK, record)
}

type revisePersonalMemoryBody struct {
	Content               string `json:"content"`
	ConfidenceBasisPoints uint16 `json:"confidenceBasisPoints"`
}

func (s *Server) handleRevisePersonalMemory(ctx context.Context, c *app.RequestContext) {
	id := c.Param("id")
	var body revisePersonalMemoryBody
	if err := c.Bind(&body); err != nil {
		writeErr(c, http.StatusBadRequest, err)
		return
	}
	if body.ConfidenceBasisPoints == 0 {
		body.ConfidenceBasisPoints = 8000
	}
	record, err := s.rt.Memory.RevisePersonalMemory(id, body.Content, body.ConfidenceBasisPoints)
	if err != nil {
		writeErr(c, http.StatusBadRequest, err)
		return
	}
	c.JSON(http.StatusOK, record)
}

func (s *Server) handleTombstonePersonalMemory(ctx context.Context, c *app.RequestContext) {
	id := c.Param("id")
	if err := s.rt.Memory.TombstonePersonalMemory(id); err != nil {
		writeErr(c, http.StatusBadRequest, err)
		return
	}
	c.JSON(http.StatusOK, map[string]any{"ok": true})
}
