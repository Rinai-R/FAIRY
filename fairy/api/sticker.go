package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"fairy/sticker"

	"github.com/cloudwego/hertz/pkg/app"
)

func (s *Server) registerStickerRoutes() {
	v1 := s.engine.Group("/v1")
	v1.Use(s.authMiddleware)
	v1.GET("/stickers", s.handleListStickers)
	v1.POST("/stickers", s.handleCreateSticker)
	v1.PUT("/stickers/:stickerId", s.handleUpdateSticker)
	v1.DELETE("/stickers/:stickerId", s.handleDeleteSticker)
	v1.GET("/stickers/:stickerId/content", s.handleStickerContent)
}

func (s *Server) handleListStickers(ctx context.Context, c *app.RequestContext) {
	if s.rt.Stickers == nil {
		writeErr(c, http.StatusServiceUnavailable, sticker.ErrDatabasePoolRequired)
		return
	}
	input := sticker.ListInput{}
	if rawStatus := strings.TrimSpace(c.Query("status")); rawStatus != "" {
		status := sticker.Status(rawStatus)
		input.Status = &status
	}
	var err error
	if rawOffset := strings.TrimSpace(c.Query("offset")); rawOffset != "" {
		input.Offset, err = strconv.Atoi(rawOffset)
		if err != nil {
			writeErr(c, http.StatusBadRequest, sticker.ErrPageInvalid)
			return
		}
	}
	if rawLimit := strings.TrimSpace(c.Query("limit")); rawLimit != "" {
		input.Limit, err = strconv.Atoi(rawLimit)
		if err != nil {
			writeErr(c, http.StatusBadRequest, sticker.ErrPageInvalid)
			return
		}
	}
	page, err := s.rt.Stickers.List(ctx, input)
	if err != nil {
		s.writeStickerError(c, err)
		return
	}
	c.JSON(http.StatusOK, page)
}

func (s *Server) handleCreateSticker(ctx context.Context, c *app.RequestContext) {
	if s.rt.Stickers == nil {
		writeErr(c, http.StatusServiceUnavailable, sticker.ErrDatabasePoolRequired)
		return
	}
	header, err := c.FormFile("file")
	if err != nil {
		writeErr(c, http.StatusBadRequest, sticker.ErrContentRequired)
		return
	}
	if header.Size <= 0 {
		writeErr(c, http.StatusBadRequest, sticker.ErrContentRequired)
		return
	}
	if header.Size > sticker.MaxContentBytes {
		writeErr(c, http.StatusRequestEntityTooLarge, sticker.ErrContentTooLarge)
		return
	}
	file, err := header.Open()
	if err != nil {
		writeErr(c, http.StatusBadRequest, err)
		return
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, sticker.MaxContentBytes+1))
	if err != nil {
		writeErr(c, http.StatusBadRequest, err)
		return
	}
	if len(content) > sticker.MaxContentBytes {
		writeErr(c, http.StatusRequestEntityTooLarge, sticker.ErrContentTooLarge)
		return
	}
	var tags []string
	rawTags := strings.TrimSpace(string(c.FormValue("tags")))
	if rawTags != "" {
		if err := json.Unmarshal([]byte(rawTags), &tags); err != nil {
			writeErr(c, http.StatusBadRequest, sticker.ErrTagInvalid)
			return
		}
	}
	record, err := s.rt.Stickers.Create(ctx, sticker.CreateInput{
		Content:          content,
		DeclaredMIMEType: header.Header.Get("Content-Type"),
		Description:      string(c.FormValue("description")),
		Tags:             tags,
		Status:           sticker.Status(strings.TrimSpace(string(c.FormValue("status")))),
	})
	if err != nil {
		s.writeStickerError(c, err)
		return
	}
	c.JSON(http.StatusCreated, record)
}

type updateStickerBody struct {
	Description *string         `json:"description"`
	Tags        *[]string       `json:"tags"`
	Status      *sticker.Status `json:"status"`
}

func (s *Server) handleUpdateSticker(ctx context.Context, c *app.RequestContext) {
	if s.rt.Stickers == nil {
		writeErr(c, http.StatusServiceUnavailable, sticker.ErrDatabasePoolRequired)
		return
	}
	var body updateStickerBody
	if err := c.Bind(&body); err != nil {
		writeErr(c, http.StatusBadRequest, err)
		return
	}
	record, err := s.rt.Stickers.Update(ctx, c.Param("stickerId"), sticker.UpdateInput{
		Description: body.Description,
		Tags:        body.Tags,
		Status:      body.Status,
	})
	if err != nil {
		s.writeStickerError(c, err)
		return
	}
	c.JSON(http.StatusOK, record)
}

func (s *Server) handleDeleteSticker(ctx context.Context, c *app.RequestContext) {
	if s.rt.Stickers == nil {
		writeErr(c, http.StatusServiceUnavailable, sticker.ErrDatabasePoolRequired)
		return
	}
	if err := s.rt.Stickers.Delete(ctx, c.Param("stickerId")); err != nil {
		s.writeStickerError(c, err)
		return
	}
	c.JSON(http.StatusOK, map[string]bool{"deleted": true})
}

func (s *Server) handleStickerContent(ctx context.Context, c *app.RequestContext) {
	if s.rt.Stickers == nil {
		writeErr(c, http.StatusServiceUnavailable, sticker.ErrDatabasePoolRequired)
		return
	}
	content, err := s.rt.Stickers.Content(ctx, c.Param("stickerId"))
	if err != nil {
		s.writeStickerError(c, err)
		return
	}
	c.Header("Content-Type", content.MIMEType)
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Cache-Control", "private, max-age=300")
	c.Header("X-Content-SHA256", content.ContentSHA256)
	c.Header("ETag", `"`+content.ContentSHA256+`"`)
	c.Data(http.StatusOK, content.MIMEType, content.Bytes)
}

func (s *Server) writeStickerError(c *app.RequestContext, err error) {
	switch {
	case errors.Is(err, sticker.ErrNotFound):
		writeErr(c, http.StatusNotFound, err)
	case errors.Is(err, sticker.ErrDuplicateContent):
		writeErr(c, http.StatusConflict, err)
	case errors.Is(err, sticker.ErrContentTooLarge):
		writeErr(c, http.StatusRequestEntityTooLarge, err)
	case errors.Is(err, sticker.ErrContentRequired),
		errors.Is(err, sticker.ErrUnsupportedMIME),
		errors.Is(err, sticker.ErrMIMEMismatch),
		errors.Is(err, sticker.ErrDescriptionRequired),
		errors.Is(err, sticker.ErrDescriptionTooLong),
		errors.Is(err, sticker.ErrTooManyTags),
		errors.Is(err, sticker.ErrTagInvalid),
		errors.Is(err, sticker.ErrStatusInvalid),
		errors.Is(err, sticker.ErrPageInvalid):
		writeErr(c, http.StatusBadRequest, err)
	default:
		writeErr(c, http.StatusInternalServerError, err)
	}
}
