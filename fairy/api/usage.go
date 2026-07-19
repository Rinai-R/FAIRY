package api

import (
	"context"
	"net/http"

	"github.com/cloudwego/hertz/pkg/app"
)

func (s *Server) registerUsageRoutes() {
	v1 := s.engine.Group("/v1")
	v1.Use(s.authMiddleware)
	v1.GET("/usage", s.handleUsage)
}

func (s *Server) handleUsage(ctx context.Context, c *app.RequestContext) {
	report, err := s.rt.Memory.TokenUsageReport()
	if err != nil {
		writeErr(c, http.StatusInternalServerError, err)
		return
	}
	c.JSON(http.StatusOK, report)
}
