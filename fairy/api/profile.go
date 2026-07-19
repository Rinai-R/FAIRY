package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/cloudwego/hertz/pkg/app"
)

var errMissingCharacterID = errors.New("characterId is required")

func (s *Server) registerProfileRoutes() {
	v1 := s.engine.Group("/v1")
	v1.Use(s.authMiddleware)
	v1.GET("/profile", s.handleGetProfile)
	v1.PUT("/profile", s.handlePutProfile)
	v1.DELETE("/profile", s.handleDeleteProfile)
}

func (s *Server) handleGetProfile(ctx context.Context, c *app.RequestContext) {
	snap, err := s.rt.Profile.Current()
	if err != nil {
		writeErr(c, http.StatusInternalServerError, err)
		return
	}
	if snap == nil {
		c.JSON(http.StatusOK, map[string]any{"revision": 0, "preferredName": nil})
		return
	}
	c.JSON(http.StatusOK, snap)
}

type putProfileBody struct {
	PreferredName *string `json:"preferredName"`
}

func (s *Server) handlePutProfile(ctx context.Context, c *app.RequestContext) {
	var body putProfileBody
	if err := c.Bind(&body); err != nil {
		writeErr(c, http.StatusBadRequest, err)
		return
	}
	update, err := s.rt.Profile.SetPreferredName(body.PreferredName)
	if err != nil {
		writeErr(c, http.StatusBadRequest, err)
		return
	}
	c.JSON(http.StatusOK, update)
}

func (s *Server) handleDeleteProfile(ctx context.Context, c *app.RequestContext) {
	update, err := s.rt.Profile.Clear()
	if err != nil {
		writeErr(c, http.StatusInternalServerError, err)
		return
	}
	c.JSON(http.StatusOK, update)
}
