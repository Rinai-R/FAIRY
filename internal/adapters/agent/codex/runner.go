package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	DefaultBin     = "codex"
	DefaultWorkDir = "data/codex-runtime"
)

type ExecRequest struct {
	Prompt    string
	Schema    string
	SessionID string
}

type Runner struct {
	Bin     string
	Model   string
	WorkDir string
	Timeout time.Duration
}

func NewRunner(bin string, model string, workDir string, timeout time.Duration) *Runner {
	if bin == "" {
		bin = DefaultBin
	}
	if workDir == "" {
		workDir = DefaultWorkDir
	}
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	return &Runner{Bin: bin, Model: model, WorkDir: workDir, Timeout: timeout}
}

func (r *Runner) ExecJSON(ctx context.Context, req ExecRequest, target any) (string, error) {
	if strings.TrimSpace(req.Prompt) == "" {
		return "", errors.New("codex prompt 不能为空")
	}
	if strings.TrimSpace(req.Schema) == "" {
		return "", errors.New("codex output schema 不能为空")
	}

	ctx, cancel := context.WithTimeout(ctx, r.Timeout)
	defer cancel()

	tempDir, err := os.MkdirTemp("", "fairy-codex-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tempDir)

	schemaPath := filepath.Join(tempDir, "schema.json")
	outputPath := filepath.Join(tempDir, "output.json")
	if err := os.WriteFile(schemaPath, []byte(req.Schema), 0o600); err != nil {
		return "", err
	}

	cmd := exec.CommandContext(ctx, r.Bin, r.buildArgs(req, schemaPath, outputPath)...)
	if err := os.MkdirAll(r.WorkDir, 0o755); err != nil {
		return "", fmt.Errorf("创建 codex workdir 失败: %w", err)
	}
	cmd.Dir = r.WorkDir
	cmd.Stdin = strings.NewReader(req.Prompt)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		return "", fmt.Errorf("codex exec 失败: %w: %s", err, msg)
	}

	raw, err := os.ReadFile(outputPath)
	if err != nil {
		raw = bytes.TrimSpace(stdout.Bytes())
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return "", errors.New("codex 返回为空")
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return "", fmt.Errorf("解析 codex JSON 失败: %w: %s", err, string(raw))
	}
	return extractSessionID(stdout.Bytes()), nil
}

func (r *Runner) buildArgs(req ExecRequest, schemaPath string, outputPath string) []string {
	args := []string{
		"exec",
		"--sandbox", "read-only",
		"--output-schema", schemaPath,
		"--output-last-message", outputPath,
		"--json",
	}
	if r.Model != "" {
		args = append(args, "--model", r.Model)
	}
	if req.SessionID != "" {
		return append(args, "resume", req.SessionID, "-")
	}
	return append(args, "-")
}

func extractSessionID(raw []byte) string {
	lines := bytes.Split(raw, []byte{'\n'})
	for _, line := range lines {
		var event any
		if err := json.Unmarshal(bytes.TrimSpace(line), &event); err != nil {
			continue
		}
		if sessionID := findSessionID(event); sessionID != "" {
			return sessionID
		}
	}
	return ""
}

func findSessionID(value any) string {
	switch typed := value.(type) {
	case map[string]any:
		for _, key := range []string{"session_id", "thread_id", "conversation_id"} {
			if raw, ok := typed[key].(string); ok && raw != "" {
				return raw
			}
		}
		for _, child := range typed {
			if sessionID := findSessionID(child); sessionID != "" {
				return sessionID
			}
		}
	case []any:
		for _, child := range typed {
			if sessionID := findSessionID(child); sessionID != "" {
				return sessionID
			}
		}
	}
	return ""
}
