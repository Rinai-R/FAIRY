package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"github.com/Rinai-R/FAIRY/internal/app"
	"github.com/Rinai-R/FAIRY/internal/runtime"
	hertzapp "github.com/cloudwego/hertz/pkg/app"
	hertzserver "github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/utils"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

type Options struct {
	AudioDir       string
	ImageDir       string
	UserConfigPath string
	Logger         *slog.Logger
}

type Server struct {
	runtime *runtime.Runtime
	options Options
}

type httpResponse struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data any    `json:"data"`
}

const (
	httpOKCode             = 0
	httpOKMsg              = "ok"
	errCodeInvalidRequest  = 10001
	errCodeUserConfig      = 11001
	errCodeSession         = 12001
	errCodeSessionNotFound = 12004
	errCodeScene           = 13001
	errCodeWorkflow        = 14001
	errCodeDocument        = 15001
	errCodeVoice           = 16001
	errCodeTurn            = 17001
	errCodeWebGAL          = 18001
)

func Register(h *hertzserver.Hertz, runtime *runtime.Runtime, options Options) {
	s := &Server{
		runtime: runtime,
		options: options,
	}

	h.Use(s.cors)
	h.GET("/healthz", s.health)
	registerAPIRoutes(h, s, "/api/v1")
	registerAPIRoutes(h, s, "/api")

	if options.AudioDir != "" {
		h.GET("/audio/*filepath", s.audio)
		h.HEAD("/audio/*filepath", s.audio)
	}
	if options.ImageDir != "" {
		h.GET("/images/*filepath", s.image)
		h.HEAD("/images/*filepath", s.image)
	}
}

func registerAPIRoutes(h *hertzserver.Hertz, s *Server, prefix string) {
	api := h.Group(prefix)
	api.GET("/capabilities", s.capabilities)
	api.GET("/plugins", s.plugins)

	userConfig := api.Group("/user-config")
	userConfig.GET("", s.userConfig)
	userConfig.PUT("", s.saveUserConfig)

	providers := api.Group("/providers")
	providers.GET("/health", s.providerHealth)

	sessions := api.Group("/sessions")
	sessions.GET("", s.sessions)
	sessions.GET("/:id", s.session)
	sessions.POST("/delete", s.deleteSession)
	api.POST("/session/delete", s.deleteSession)

	documents := api.Group("/documents")
	documents.POST("/fetch", s.fetchDocument)
	documents.POST("/upload", s.uploadDocument)

	scenes := api.Group("/scenes")
	scenes.POST("/generate", s.generateScene)
	scenes.POST("/generate-task", s.startSceneGeneration)

	workflows := api.Group("/workflows")
	workflows.POST("/advance", s.advanceWorkflow)

	webgal := api.Group("/webgal")
	webgal.POST("/export", s.exportWebGAL)

	api.POST("/turn", s.turn)
	api.POST("/turn/stream", s.turnStream)

	voices := api.Group("/voices")
	voices.POST("/synthesize", s.synthesizeVoice)
	voices.POST("/clone", s.cloneVoice)
	voices.POST("/clone/status", s.cloneVoiceStatus)
}

func (s *Server) capabilities(_ context.Context, c *hertzapp.RequestContext) {
	writeOK(c, s.runtime.Capabilities())
}

func (s *Server) userConfig(_ context.Context, c *hertzapp.RequestContext) {
	raw, exists, err := runtime.NewUserConfigStore(s.options.UserConfigPath).Load()
	if err != nil {
		writeError(c, consts.StatusBadRequest, errCodeUserConfig, err)
		return
	}
	if !exists {
		writeOK(c, utils.H{"exists": false})
		return
	}
	writeOK(c, utils.H{"exists": true, "config": json.RawMessage(raw)})
}

func (s *Server) saveUserConfig(_ context.Context, c *hertzapp.RequestContext) {
	raw := c.Request.Body()
	if err := runtime.NewUserConfigStore(s.options.UserConfigPath).Save(json.RawMessage(raw)); err != nil {
		writeError(c, consts.StatusBadRequest, errCodeUserConfig, err)
		return
	}
	writeOK(c, utils.H{"ok": true})
}

func (s *Server) plugins(_ context.Context, c *hertzapp.RequestContext) {
	writeOK(c, s.runtime.Plugins())
}

func (s *Server) providerHealth(ctx context.Context, c *hertzapp.RequestContext) {
	writeOK(c, utils.H{"providers": s.runtime.ProviderHealth(ctx)})
}

func (s *Server) sessions(_ context.Context, c *hertzapp.RequestContext) {
	records, err := s.runtime.Sessions()
	if err != nil {
		writeError(c, consts.StatusBadRequest, errCodeSession, err)
		return
	}
	writeOK(c, utils.H{"sessions": records})
}

func (s *Server) session(_ context.Context, c *hertzapp.RequestContext) {
	record, err := s.runtime.Session(c.Param("id"))
	if err != nil {
		writeError(c, consts.StatusNotFound, errCodeSessionNotFound, err)
		return
	}
	writeOK(c, record)
}

func (s *Server) deleteSession(_ context.Context, c *hertzapp.RequestContext) {
	var body struct {
		ID string `json:"id"`
	}
	if err := c.BindJSON(&body); err != nil || body.ID == "" {
		writeError(c, consts.StatusBadRequest, errCodeInvalidRequest, fmt.Errorf("id 不能为空"))
		return
	}
	if err := s.runtime.DeleteSession(body.ID); err != nil {
		writeError(c, consts.StatusNotFound, errCodeSessionNotFound, err)
		return
	}
	writeOK(c, map[string]bool{"deleted": true})
}

func (s *Server) generateScene(ctx context.Context, c *hertzapp.RequestContext) {
	var body app.SceneGenerateRequest
	if err := c.BindJSON(&body); err != nil {
		writeError(c, consts.StatusBadRequest, errCodeInvalidRequest, err)
		return
	}
	resp, err := s.runtime.GenerateScene(ctx, body)
	if err != nil {
		writeError(c, consts.StatusBadRequest, errCodeScene, err)
		return
	}
	writeOK(c, resp)
}

func (s *Server) startSceneGeneration(ctx context.Context, c *hertzapp.RequestContext) {
	var body app.SceneGenerateRequest
	if err := c.BindJSON(&body); err != nil {
		writeError(c, consts.StatusBadRequest, errCodeInvalidRequest, err)
		return
	}
	resp, err := s.runtime.StartSceneGeneration(ctx, body)
	if err != nil {
		writeError(c, consts.StatusBadRequest, errCodeScene, err)
		return
	}
	writeOK(c, resp)
}

func (s *Server) exportWebGAL(ctx context.Context, c *hertzapp.RequestContext) {
	var body app.WebGALExportRequest
	if err := c.BindJSON(&body); err != nil {
		writeError(c, consts.StatusBadRequest, errCodeInvalidRequest, err)
		return
	}
	resp, err := s.runtime.ExportWebGAL(ctx, body)
	if err != nil {
		writeError(c, consts.StatusBadRequest, errCodeWebGAL, err)
		return
	}
	writeOK(c, resp)
}

func (s *Server) advanceWorkflow(ctx context.Context, c *hertzapp.RequestContext) {
	var body app.WorkflowAdvanceRequest
	if err := c.BindJSON(&body); err != nil {
		writeError(c, consts.StatusBadRequest, errCodeInvalidRequest, err)
		return
	}
	resp, err := s.runtime.AdvanceWorkflow(ctx, body)
	if err != nil {
		writeError(c, consts.StatusBadRequest, errCodeWorkflow, err)
		return
	}
	writeOK(c, resp)
}

func (s *Server) fetchDocument(ctx context.Context, c *hertzapp.RequestContext) {
	var body app.DocumentFetchRequest
	if err := c.BindJSON(&body); err != nil {
		writeError(c, consts.StatusBadRequest, errCodeInvalidRequest, err)
		return
	}
	resp, err := s.runtime.FetchDocument(ctx, body)
	if err != nil {
		writeError(c, consts.StatusBadRequest, errCodeDocument, err)
		return
	}
	writeOK(c, resp)
}

func (s *Server) uploadDocument(ctx context.Context, c *hertzapp.RequestContext) {
	file, err := c.FormFile("file")
	if err != nil {
		writeError(c, consts.StatusBadRequest, errCodeInvalidRequest, err)
		return
	}
	if file.Size > runtime.MaxDocumentAssetBytes {
		writeError(c, consts.StatusBadRequest, errCodeDocument, fmt.Errorf("文件过大: %d bytes，当前上限 %d bytes", file.Size, runtime.MaxDocumentAssetBytes))
		return
	}
	opened, err := file.Open()
	if err != nil {
		writeError(c, consts.StatusBadRequest, errCodeDocument, fmt.Errorf("打开上传文件失败: %w", err))
		return
	}
	defer opened.Close()

	data, err := io.ReadAll(io.LimitReader(opened, runtime.MaxDocumentAssetBytes+1))
	if err != nil {
		writeError(c, consts.StatusBadRequest, errCodeDocument, fmt.Errorf("读取上传文件失败: %w", err))
		return
	}
	contentType := file.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	resp, err := s.runtime.StoreDocumentAssetBytes(ctx, file.Filename, contentType, data)
	if err != nil {
		writeError(c, consts.StatusBadRequest, errCodeDocument, err)
		return
	}
	writeOK(c, resp)
}

func (s *Server) cors(ctx context.Context, c *hertzapp.RequestContext) {
	c.Header("Access-Control-Allow-Origin", "*")
	c.Header("Access-Control-Allow-Headers", "content-type")
	c.Header("Access-Control-Allow-Methods", "GET,POST,PUT,OPTIONS")
	if string(c.Method()) == consts.MethodOptions {
		c.AbortWithStatus(consts.StatusNoContent)
		return
	}
	c.Next(ctx)
}

func (s *Server) health(_ context.Context, c *hertzapp.RequestContext) {
	writeOK(c, utils.H{
		"ok":   true,
		"time": time.Now().UTC().Format(time.RFC3339),
	})
}

func (s *Server) turn(ctx context.Context, c *hertzapp.RequestContext) {
	var body app.TurnRequest
	if err := c.BindJSON(&body); err != nil {
		writeError(c, consts.StatusBadRequest, errCodeInvalidRequest, err)
		return
	}

	resp, err := s.runtime.Turn(ctx, body)
	if err != nil {
		writeError(c, consts.StatusBadRequest, errCodeTurn, err)
		return
	}
	writeOK(c, resp)
}

func (s *Server) turnStream(ctx context.Context, c *hertzapp.RequestContext) {
	var body app.TurnRequest
	if err := c.BindJSON(&body); err != nil {
		writeError(c, consts.StatusBadRequest, errCodeInvalidRequest, err)
		return
	}

	resp, err := s.runtime.Turn(ctx, body)
	if err != nil {
		writeError(c, consts.StatusBadRequest, errCodeTurn, err)
		return
	}

	c.Header("Content-Type", "text/event-stream; charset=utf-8")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	writer := c.Response.BodyWriter()
	for _, token := range splitTokens(resp.DisplayText) {
		if err := writeSSE(writer, "token", utils.H{"text": token}); err != nil {
			return
		}
	}
	_ = writeSSE(writer, "done", resp)
}

func (s *Server) cloneVoice(ctx context.Context, c *hertzapp.RequestContext) {
	var body app.VoiceCloneRequest
	if err := c.BindJSON(&body); err != nil {
		writeError(c, consts.StatusBadRequest, errCodeInvalidRequest, err)
		return
	}
	resp, err := s.runtime.CloneVoice(ctx, body)
	if err != nil {
		writeError(c, consts.StatusBadRequest, errCodeVoice, err)
		return
	}
	writeOK(c, resp)
}

func (s *Server) synthesizeVoice(ctx context.Context, c *hertzapp.RequestContext) {
	var body app.VoiceSynthesisRequest
	if err := c.BindJSON(&body); err != nil {
		writeError(c, consts.StatusBadRequest, errCodeInvalidRequest, err)
		return
	}
	resp, err := s.runtime.SynthesizeVoice(ctx, body)
	if err != nil {
		writeError(c, consts.StatusBadRequest, errCodeVoice, err)
		return
	}
	writeOK(c, resp)
}

func (s *Server) cloneVoiceStatus(ctx context.Context, c *hertzapp.RequestContext) {
	var body app.VoiceCloneRequest
	if err := c.BindJSON(&body); err != nil {
		writeError(c, consts.StatusBadRequest, errCodeInvalidRequest, err)
		return
	}
	resp, err := s.runtime.CloneVoiceStatus(ctx, body)
	if err != nil {
		writeError(c, consts.StatusBadRequest, errCodeVoice, err)
		return
	}
	writeOK(c, resp)
}

func (s *Server) audio(_ context.Context, c *hertzapp.RequestContext) {
	s.serveStaticFile(c, s.options.AudioDir, c.Param("filepath"))
}

func (s *Server) image(_ context.Context, c *hertzapp.RequestContext) {
	s.serveStaticFile(c, s.options.ImageDir, c.Param("filepath"))
}

func (s *Server) serveStaticFile(c *hertzapp.RequestContext, root string, raw string) {
	name := strings.TrimPrefix(raw, "/")
	if name == "" || strings.Contains(name, "..") {
		c.Status(consts.StatusNotFound)
		return
	}
	hertzapp.ServeFile(c, filepath.Join(root, name))
}

func writeOK(c *hertzapp.RequestContext, data any) {
	c.JSON(consts.StatusOK, httpResponse{
		Code: httpOKCode,
		Msg:  httpOKMsg,
		Data: data,
	})
}

func writeError(c *hertzapp.RequestContext, status int, code int, err error) {
	c.JSON(status, httpResponse{
		Code: code,
		Msg:  err.Error(),
		Data: nil,
	})
}

func writeSSE(writer interface{ Write([]byte) (int, error) }, event string, payload any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(writer, "event: %s\ndata: %s\n\n", event, raw)
	return err
}

func splitTokens(text string) []string {
	runes := []rune(text)
	if len(runes) == 0 {
		return []string{}
	}
	tokens := make([]string, 0, len(runes)/2+1)
	for start := 0; start < len(runes); start += 2 {
		end := start + 2
		if end > len(runes) {
			end = len(runes)
		}
		tokens = append(tokens, string(runes[start:end]))
	}
	return tokens
}
