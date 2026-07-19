package api

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"fairy/character"
	"fairy/visual"
	"github.com/cloudwego/hertz/pkg/app"
)

func (s *Server) registerCharacterRoutes() {
	v1 := s.engine.Group("/v1")
	v1.Use(s.authMiddleware)
	v1.GET("/characters", s.handleListCharacters)
	v1.POST("/characters", s.handleCreateCharacter)
	v1.PUT("/characters/:characterId", s.handleUpdateCharacter)
	v1.POST("/characters/:characterId/activate", s.handleActivateCharacter)
	v1.POST("/characters/:characterId/appearance", s.handleSetAppearance)
	v1.POST("/characters/import", s.handleImportCharacter)
	v1.GET("/characters/:characterId/export", s.handleExportCharacter)
	v1.GET("/visual-packs", s.handleListVisualPacks)
}

func (s *Server) handleListCharacters(ctx context.Context, c *app.RequestContext) {
	catalog, err := s.rt.Character.ListCharacters()
	if err != nil {
		writeErr(c, http.StatusInternalServerError, err)
		return
	}
	c.JSON(http.StatusOK, catalog)
}

type createCharacterBody struct {
	character.Brief
	VisualPackID string `json:"visualPackId"`
}

func (s *Server) handleCreateCharacter(ctx context.Context, c *app.RequestContext) {
	var body createCharacterBody
	if err := c.Bind(&body); err != nil {
		writeErr(c, http.StatusBadRequest, err)
		return
	}
	record, err := s.rt.Character.CreateCharacter(body.Brief, body.VisualPackID)
	if err != nil {
		writeErr(c, http.StatusBadRequest, err)
		return
	}
	c.JSON(http.StatusOK, record)
}

type updateCharacterBody struct {
	character.Brief
}

func (s *Server) handleUpdateCharacter(ctx context.Context, c *app.RequestContext) {
	id := c.Param("characterId")
	var body updateCharacterBody
	if err := c.Bind(&body); err != nil {
		writeErr(c, http.StatusBadRequest, err)
		return
	}
	record, err := s.rt.Character.UpdateCharacter(id, body.Brief)
	if err != nil {
		writeErr(c, http.StatusBadRequest, err)
		return
	}
	c.JSON(http.StatusOK, record)
}

type activateCharacterBody struct {
	Revision uint64 `json:"revision"`
}

func (s *Server) handleActivateCharacter(ctx context.Context, c *app.RequestContext) {
	id := c.Param("characterId")
	var body activateCharacterBody
	if err := c.Bind(&body); err != nil {
		writeErr(c, http.StatusBadRequest, err)
		return
	}
	record, err := s.rt.Character.ActivateCharacter(id, body.Revision)
	if err != nil {
		writeErr(c, http.StatusBadRequest, err)
		return
	}
	c.JSON(http.StatusOK, record)
}

type appearanceBody struct {
	VisualPackID string `json:"visualPackId"`
}

func (s *Server) handleSetAppearance(ctx context.Context, c *app.RequestContext) {
	id := c.Param("characterId")
	var body appearanceBody
	if err := c.Bind(&body); err != nil {
		writeErr(c, http.StatusBadRequest, err)
		return
	}
	record, err := s.rt.Character.SetCharacterAppearance(id, body.VisualPackID)
	if err != nil {
		writeErr(c, http.StatusBadRequest, err)
		return
	}
	c.JSON(http.StatusOK, record)
}

func (s *Server) handleImportCharacter(ctx context.Context, c *app.RequestContext) {
	fileHeader, err := c.FormFile("file")
	if err != nil {
		writeErr(c, http.StatusBadRequest, err)
		return
	}
	src, err := fileHeader.Open()
	if err != nil {
		writeErr(c, http.StatusBadRequest, err)
		return
	}
	defer src.Close()

	tmp, err := os.CreateTemp("", "fairy-character-*.pack")
	if err != nil {
		writeErr(c, http.StatusInternalServerError, err)
		return
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := io.Copy(tmp, src); err != nil {
		tmp.Close()
		writeErr(c, http.StatusInternalServerError, err)
		return
	}
	if err := tmp.Close(); err != nil {
		writeErr(c, http.StatusInternalServerError, err)
		return
	}
	record, err := s.rt.Character.ImportCharacterPackage(tmpPath)
	if err != nil {
		writeErr(c, http.StatusBadRequest, err)
		return
	}
	c.JSON(http.StatusOK, record)
}

func (s *Server) handleExportCharacter(ctx context.Context, c *app.RequestContext) {
	id := strings.TrimSpace(c.Param("characterId"))
	if id == "" {
		writeErr(c, http.StatusBadRequest, errMissingCharacterID)
		return
	}
	tmpDir, err := os.MkdirTemp("", "fairy-export-*")
	if err != nil {
		writeErr(c, http.StatusInternalServerError, err)
		return
	}
	defer os.RemoveAll(tmpDir)
	outPath := filepath.Join(tmpDir, id+".pack")
	if err := s.rt.Character.ExportCharacterPackage(id, outPath); err != nil {
		writeErr(c, http.StatusBadRequest, err)
		return
	}
	data, err := os.ReadFile(outPath)
	if err != nil {
		writeErr(c, http.StatusInternalServerError, err)
		return
	}
	c.Header("Content-Disposition", `attachment; filename="`+id+`.pack"`)
	c.Data(http.StatusOK, "application/octet-stream", data)
}

func (s *Server) handleListVisualPacks(ctx context.Context, c *app.RequestContext) {
	catalog, err := visual.ListManifests(visual.VisualPacksRoot(s.rt.ConfigRoot))
	if err != nil {
		writeErr(c, http.StatusInternalServerError, err)
		return
	}
	c.JSON(http.StatusOK, catalog)
}
