package scene

import (
	"context"

	"github.com/Rinai-R/FAIRY/internal/app"
)

type Provider string

const (
	ProviderMock  Provider = "mock"
	ProviderCodex Provider = "codex"
)

type Engine interface {
	Generate(ctx context.Context, input Input) (app.SceneGenerateResponse, error)
}

type Input struct {
	Request app.SceneGenerateRequest `json:"request"`
}
