package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"fairy/character"

	"github.com/cloudwego/hertz/pkg/app"
)

func (s *Server) handleVisualAsset(ctx context.Context, c *app.RequestContext) {
	packID := c.Param("packId")
	assetPath := strings.TrimPrefix(c.Param("assetPath"), "/")
	full, err := character.ResolveAssetFile(character.VisualPacksRoot(s.rt.ConfigRoot), packID+"/"+assetPath)
	if err != nil {
		switch {
		case errors.Is(err, character.ErrInvalidAssetPath):
			writeErr(c, http.StatusBadRequest, err)
		case errors.Is(err, character.ErrAssetNotFound):
			writeErr(c, http.StatusNotFound, err)
		default:
			writeErr(c, http.StatusInternalServerError, err)
		}
		return
	}
	c.Header("Content-Type", "image/png")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Cache-Control", "private, max-age=300")
	c.File(full)
}
