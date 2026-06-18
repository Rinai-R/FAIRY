package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/Rinai-R/FAIRY/internal/adapters/health"
	"github.com/Rinai-R/FAIRY/internal/app"
	"github.com/Rinai-R/FAIRY/internal/app/bootstrap"
	domainruntime "github.com/Rinai-R/FAIRY/internal/runtime"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

type App struct {
	ctx     context.Context
	runtime *domainruntime.Runtime
	config  bootstrap.Config
}

func New(config bootstrap.Config, logger *slog.Logger) *App {
	return &App{runtime: bootstrap.NewRuntime(config, logger), config: config}
}

func (a *App) Startup(ctx context.Context) {
	a.ctx = ctx
}

func (a *App) Capabilities() app.Capabilities {
	return a.runtime.Capabilities()
}

func (a *App) Plugins() app.PluginCatalog {
	return a.runtime.Plugins()
}

func (a *App) ProviderHealth() []health.Result {
	return a.runtime.ProviderHealth(a.context())
}

func (a *App) Turn(request app.TurnRequest) (app.TurnResponse, error) {
	return a.runtime.Turn(a.context(), request)
}

func (a *App) SynthesizeVoice(request app.VoiceSynthesisRequest) (app.AudioResult, error) {
	return a.runtime.SynthesizeVoice(a.context(), request)
}

func (a *App) CloneVoice(request app.VoiceCloneRequest) (app.VoiceCloneResult, error) {
	return a.runtime.CloneVoice(a.context(), request)
}

func (a *App) CloneVoiceStatus(request app.VoiceCloneRequest) (app.VoiceCloneResult, error) {
	return a.runtime.CloneVoiceStatus(a.context(), request)
}

func (a *App) GenerateScene(request app.SceneGenerateRequest) (app.SceneGenerateResponse, error) {
	return a.runtime.GenerateScene(a.context(), request)
}

func (a *App) StartSceneGeneration(request app.SceneGenerateRequest) (app.SceneGenerationStartResponse, error) {
	return a.runtime.StartSceneGeneration(a.context(), request)
}

func (a *App) ExportWebGAL(request app.WebGALExportRequest) (app.WebGALExportResponse, error) {
	return a.runtime.ExportWebGAL(a.context(), request)
}

func (a *App) AdvanceWorkflow(request app.WorkflowAdvanceRequest) (app.WorkflowAdvanceResponse, error) {
	return a.runtime.AdvanceWorkflow(a.context(), request)
}

func (a *App) FetchDocument(request app.DocumentFetchRequest) (app.DocumentFetchResponse, error) {
	return a.runtime.FetchDocument(a.context(), request)
}

func (a *App) StoreDocumentAsset(request app.DocumentUploadRequest) (app.DocumentAsset, error) {
	return a.runtime.StoreDocumentAsset(a.context(), request)
}

func (a *App) Sessions() ([]app.SessionRecord, error) {
	return a.runtime.Sessions()
}

func (a *App) Session(id string) (app.SessionRecord, error) {
	return a.runtime.Session(id)
}

func (a *App) DeleteSession(id string) (map[string]bool, error) {
	if err := a.runtime.DeleteSession(id); err != nil {
		return nil, err
	}
	return map[string]bool{"deleted": true}, nil
}

func (a *App) SaveCharacterPackage(filename string, content string) (map[string]any, error) {
	filename = strings.TrimSpace(filename)
	if filename == "" {
		filename = "fairy-character.fairy-character.json"
	}
	if strings.TrimSpace(content) == "" {
		return nil, errors.New("角色包内容不能为空")
	}
	path, err := wailsruntime.SaveFileDialog(a.context(), wailsruntime.SaveDialogOptions{
		Title:                "保存 FAIRY 角色包",
		DefaultFilename:      filename,
		CanCreateDirectories: true,
		Filters: []wailsruntime.FileFilter{
			{DisplayName: "FAIRY 角色包 (*.json)", Pattern: "*.json"},
			{DisplayName: "所有文件 (*.*)", Pattern: "*.*"},
		},
	})
	if err != nil {
		return nil, err
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return map[string]any{"cancelled": true}, nil
	}
	if filepath.Ext(path) == "" {
		path += ".json"
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return nil, err
	}
	return map[string]any{"path": path, "cancelled": false}, nil
}

func (a *App) UserConfig() (map[string]any, error) {
	raw, exists, err := domainruntime.NewUserConfigStore(a.config.UserConfigPath).Load()
	if err != nil {
		return nil, err
	}
	if !exists {
		return map[string]any{"exists": false}, nil
	}
	var config map[string]any
	if err := json.Unmarshal(raw, &config); err != nil {
		return nil, err
	}
	return map[string]any{"exists": true, "config": config}, nil
}

func (a *App) SaveUserConfig(config map[string]any) (map[string]bool, error) {
	raw, err := json.Marshal(config)
	if err != nil {
		return nil, err
	}
	if err := domainruntime.NewUserConfigStore(a.config.UserConfigPath).Save(raw); err != nil {
		return nil, err
	}
	return map[string]bool{"ok": true}, nil
}

func (a *App) context() context.Context {
	if a.ctx != nil {
		return a.ctx
	}
	return context.Background()
}
