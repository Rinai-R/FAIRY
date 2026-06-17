package mock

import (
	"context"
	"time"

	"github.com/Rinai-R/FAIRY/internal/adapters/health"
	"github.com/Rinai-R/FAIRY/internal/adapters/voice"
	"github.com/Rinai-R/FAIRY/internal/app"
)

const Format = "mock"

type MockEngine struct{}

func (MockEngine) Synthesize(_ context.Context, input voice.Input) (app.AudioResult, error) {
	return app.AudioResult{
		Format:      Format,
		DurationMS:  estimateDuration(input.Text),
		Placeholder: true,
	}, nil
}

func (MockEngine) Check(_ context.Context) health.Result {
	return health.Result{
		Domain:    "voice",
		Provider:  string(voice.ProviderMock),
		Status:    health.StatusOK,
		Message:   "mock voice 可用",
		CheckedAt: time.Now().UTC(),
	}
}

func estimateDuration(text string) int {
	n := len([]rune(text))
	if n == 0 {
		return 0
	}
	return 800 + n*90
}
