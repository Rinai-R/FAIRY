package mock

import (
	"context"
	"time"

	"github.com/Rinai-R/FAIRY/internal/adapters/health"
	"github.com/Rinai-R/FAIRY/internal/adapters/image"
	"github.com/Rinai-R/FAIRY/internal/app"
)

type Engine struct{}

func (Engine) Generate(_ context.Context, input image.Input) (app.ImageResult, error) {
	prompt := input.Request.Prompt
	if prompt == "" {
		prompt = input.Turn.Scene.Title
	}
	return app.ImageResult{
		Format:      "mock",
		Prompt:      prompt,
		Placeholder: true,
	}, nil
}

func (Engine) Check(_ context.Context) health.Result {
	return health.Result{
		Domain:    "image",
		Provider:  string(image.ProviderMock),
		Status:    health.StatusOK,
		Message:   "mock image 可用",
		CheckedAt: time.Now().UTC(),
	}
}
