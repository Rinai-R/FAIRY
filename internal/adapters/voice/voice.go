package voice

import (
	"context"

	"github.com/Rinai-R/FAIRY/internal/app"
)

type Provider string

const (
	ProviderMock       Provider = "mock"
	ProviderMacOS      Provider = "macos"
	ProviderGPTSoVITS  Provider = "gpt-sovits"
	ProviderGPTSoVITS2 Provider = "gptsovits"
	ProviderVolcengine Provider = "volcengine"
)

type Engine interface {
	Synthesize(ctx context.Context, input Input) (app.AudioResult, error)
}

type CloneTrainer interface {
	CloneVoice(ctx context.Context, request app.VoiceCloneRequest) (app.VoiceCloneResult, error)
	CloneStatus(ctx context.Context, request app.VoiceCloneRequest) (app.VoiceCloneResult, error)
}

type Input struct {
	Text      string        `json:"text"`
	Plan      app.VoicePlan `json:"plan"`
	Emotion   string        `json:"emotion"`
	Character app.Character `json:"character"`
	Profile   app.VoiceProfile
}
