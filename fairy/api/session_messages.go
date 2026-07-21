package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"fairy/memory"
	"github.com/cloudwego/hertz/pkg/app"
)

func (s *Server) handleSessionMessages(ctx context.Context, c *app.RequestContext) {
	conversationID := c.Param("conversationId")
	limit := memory.DefaultMessagePageLimit
	if raw := string(c.Query("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 || parsed > memory.MaxMessagePageLimit {
			writeErr(c, http.StatusBadRequest, errors.New("limit must be between 1 and 200"))
			return
		}
		limit = parsed
	}
	var before uint64
	if raw := string(c.Query("beforeSequence")); raw != "" {
		parsed, err := strconv.ParseUint(raw, 10, 64)
		if err != nil || parsed == 0 {
			writeErr(c, http.StatusBadRequest, errors.New("beforeSequence must be a positive integer"))
			return
		}
		before = parsed
	}
	page, err := s.rt.MemoryStore.ListConversationMessagesBeforeContext(ctx, conversationID, before, limit)
	if err != nil {
		writeErr(c, http.StatusBadRequest, err)
		return
	}
	c.JSON(http.StatusOK, page)
}
