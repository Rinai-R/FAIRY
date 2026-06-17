package image

import (
	"context"

	"github.com/Rinai-R/FAIRY/internal/app"
)

type Provider string

const (
	ProviderMock    Provider = "mock"
	ProviderComfyUI Provider = "comfyui"
)

type Engine interface {
	Generate(ctx context.Context, input Input) (app.ImageResult, error)
}

type Input struct {
	Request   app.ImageRequest `json:"request"`
	Turn      app.TurnRequest  `json:"turn"`
	Character app.Character    `json:"character"`
}
