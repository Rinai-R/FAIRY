package desktop

import (
	"context"
	"log/slog"

	"github.com/Rinai-R/FAIRY/internal/adapters/health"
	"github.com/Rinai-R/FAIRY/internal/app"
	"github.com/Rinai-R/FAIRY/internal/app/bootstrap"
	domainruntime "github.com/Rinai-R/FAIRY/internal/runtime"
)

type App struct {
	ctx     context.Context
	runtime *domainruntime.Runtime
}

func New(config bootstrap.Config, logger *slog.Logger) *App {
	return &App{runtime: bootstrap.NewRuntime(config, logger)}
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

func (a *App) context() context.Context {
	if a.ctx != nil {
		return a.ctx
	}
	return context.Background()
}
