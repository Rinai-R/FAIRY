package macos

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Rinai-R/FAIRY/internal/adapters/health"
	"github.com/Rinai-R/FAIRY/internal/adapters/voice"
	"github.com/Rinai-R/FAIRY/internal/app"
)

const (
	DefaultOutputDir = "tmp/audio"
	DefaultBaseURL   = "/audio/"
	DefaultVoiceName = "Tingting"
)

type MacOSEngine struct {
	OutputDir string
	BaseURL   string
	VoiceName string
}

func NewMacOSEngine(outputDir string, baseURL string, voiceName string) *MacOSEngine {
	if outputDir == "" {
		outputDir = DefaultOutputDir
	}
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	if voiceName == "" {
		voiceName = DefaultVoiceName
	}
	return &MacOSEngine{OutputDir: outputDir, BaseURL: baseURL, VoiceName: voiceName}
}

func (e *MacOSEngine) Synthesize(ctx context.Context, input voice.Input) (app.AudioResult, error) {
	text := strings.TrimSpace(input.Text)
	if text == "" {
		return app.AudioResult{Format: "m4a", Placeholder: true}, nil
	}
	if err := os.MkdirAll(e.OutputDir, 0o755); err != nil {
		return app.AudioResult{}, err
	}

	name := fmt.Sprintf("%d.m4a", time.Now().UnixNano())
	path := filepath.Join(e.OutputDir, name)
	voice := e.VoiceName
	if input.Plan.VoiceID != "" && !strings.Contains(input.Plan.VoiceID, "_") {
		voice = input.Plan.VoiceID
	}

	cmd := exec.CommandContext(ctx, "say", "-v", voice, "-o", path, "--file-format=m4af", text)
	if err := cmd.Run(); err != nil {
		return app.AudioResult{}, err
	}
	return app.AudioResult{
		URL:         e.BaseURL + name,
		Format:      "m4a",
		DurationMS:  estimateDuration(text),
		Placeholder: false,
	}, nil
}

func (e *MacOSEngine) Check(_ context.Context) health.Result {
	start := time.Now()
	_, err := exec.LookPath("say")
	status := health.StatusOK
	message := "macOS say 可用"
	if err != nil {
		status = health.StatusDown
		message = err.Error()
	}
	return health.Result{
		Domain:    "voice",
		Provider:  string(voice.ProviderMacOS),
		Status:    status,
		LatencyMS: time.Since(start).Milliseconds(),
		Message:   message,
		CheckedAt: time.Now().UTC(),
		Metadata:  map[string]string{"voice": e.VoiceName},
	}
}

func estimateDuration(text string) int {
	n := len([]rune(text))
	if n == 0 {
		return 0
	}
	return 800 + n*90
}
