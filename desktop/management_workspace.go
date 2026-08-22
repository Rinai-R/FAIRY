package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"fairy/runtime/observability"
)

const managementWorkspaceFileName = "management-workspace.json"

var managementTaskIDs = map[string]struct{}{
	"overview": {}, "character": {}, "profile": {}, "model": {}, "stickers": {},
	"integrations": {}, "intelligence": {}, "plugins": {}, "conversation-debug": {},
	"metrics": {}, "tracing": {}, "logs": {}, "backup": {},
}

type ManagementWorkspaceState struct {
	Section          string `json:"section"`
	Width            int    `json:"width"`
	Height           int    `json:"height"`
	X                int    `json:"x"`
	Y                int    `json:"y"`
	HasLayout        bool   `json:"hasLayout"`
	TraceID          string `json:"traceId"`
	MessageID        string `json:"messageId"`
	LogLevel         string `json:"logLevel"`
	PluginInstanceID string `json:"pluginInstanceId"`
}

type ManagementWorkspaceWrite struct {
	Section          string `json:"section"`
	TraceID          string `json:"traceId"`
	MessageID        string `json:"messageId"`
	LogLevel         string `json:"logLevel"`
	PluginInstanceID string `json:"pluginInstanceId"`
}

func (s *CoreService) ManagementWorkspaceState() (ManagementWorkspaceState, error) {
	return s.loadManagementWorkspace()
}

func (s *CoreService) SaveManagementWorkspaceState(write ManagementWorkspaceWrite) (ManagementWorkspaceState, error) {
	state, err := s.loadManagementWorkspace()
	if err != nil {
		return ManagementWorkspaceState{}, err
	}
	next, err := normalizeManagementWorkspaceWrite(state, write)
	if err != nil {
		return ManagementWorkspaceState{}, err
	}
	if err := s.persistManagementWorkspace(next); err != nil {
		return ManagementWorkspaceState{}, err
	}
	return next, nil
}

func (s *CoreService) loadManagementWorkspace() (ManagementWorkspaceState, error) {
	if s == nil {
		return ManagementWorkspaceState{}, errors.New("desktop core service is unavailable")
	}
	s.mu.Lock()
	if s.workspaceLoaded {
		state := s.workspace
		s.mu.Unlock()
		return state, nil
	}
	s.mu.Unlock()
	dir, err := s.resolveProfileDir()
	if err != nil {
		return ManagementWorkspaceState{}, err
	}
	state, err := readManagementWorkspaceFile(filepath.Join(dir, managementWorkspaceFileName))
	if err != nil {
		return ManagementWorkspaceState{}, err
	}
	s.mu.Lock()
	s.workspace = state
	s.workspaceLoaded = true
	s.mu.Unlock()
	return state, nil
}

func (s *CoreService) persistManagementWorkspace(state ManagementWorkspaceState) error {
	dir, err := s.resolveProfileDir()
	if err != nil {
		return err
	}
	if err := writeManagementWorkspaceFile(filepath.Join(dir, managementWorkspaceFileName), state); err != nil {
		return err
	}
	s.mu.Lock()
	s.workspace = state
	s.workspaceLoaded = true
	s.mu.Unlock()
	return nil
}

func (s *CoreService) snapshotManagementLayout() error {
	s.mu.Lock()
	window := s.management
	state := s.workspace
	s.mu.Unlock()
	if window == nil {
		return nil
	}
	state.Width, state.Height = window.Size()
	state.X, state.Y = window.Position()
	state.HasLayout = true
	return s.persistManagementWorkspace(state)
}

func (s *CoreService) restoreManagementLayout() {
	state, err := s.loadManagementWorkspace()
	if err != nil {
		return
	}
	s.mu.Lock()
	window := s.management
	s.mu.Unlock()
	if window == nil || !state.HasLayout {
		return
	}
	if state.Width >= managementMinWidth && state.Height >= managementMinHeight {
		window.SetSize(state.Width, state.Height)
	}
	window.SetPosition(state.X, state.Y)
}

func normalizeManagementWorkspaceWrite(current ManagementWorkspaceState, write ManagementWorkspaceWrite) (ManagementWorkspaceState, error) {
	section := strings.TrimSpace(write.Section)
	if section == "" {
		section = current.Section
	}
	if section == "" {
		section = "overview"
	}
	if _, ok := managementTaskIDs[section]; !ok {
		return ManagementWorkspaceState{}, fmt.Errorf("management task %q is invalid", section)
	}
	if err := validateWorkspaceCorrelation(write.TraceID); err != nil {
		return ManagementWorkspaceState{}, err
	}
	if err := validateWorkspaceCorrelation(write.MessageID); err != nil {
		return ManagementWorkspaceState{}, err
	}
	level := strings.TrimSpace(write.LogLevel)
	switch level {
	case "", "debug", "info", "warn", "error":
	default:
		return ManagementWorkspaceState{}, fmt.Errorf("log level %q is invalid", write.LogLevel)
	}
	current.Section = section
	current.TraceID = strings.TrimSpace(write.TraceID)
	current.MessageID = strings.TrimSpace(write.MessageID)
	current.LogLevel = level
	pluginInstanceID, err := normalizeWorkspacePluginInstanceID(write.PluginInstanceID)
	if err != nil {
		return ManagementWorkspaceState{}, err
	}
	current.PluginInstanceID = pluginInstanceID
	return current, nil
}

func normalizeWorkspacePluginInstanceID(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	if len(value) > 128 {
		return "", fmt.Errorf("plugin instance id is invalid")
	}
	if value[0] == '.' || value[0] == '-' || value[len(value)-1] == '.' || value[len(value)-1] == '-' {
		return "", fmt.Errorf("plugin instance id is invalid")
	}
	for _, r := range value {
		letter := r >= 'a' && r <= 'z'
		digit := r >= '0' && r <= '9'
		if !letter && !digit && r != '.' && r != '-' {
			return "", fmt.Errorf("plugin instance id is invalid")
		}
	}
	return value, nil
}

func validateWorkspaceCorrelation(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	if !observability.ValidCorrelationID(value) {
		return fmt.Errorf("correlation id is invalid")
	}
	return nil
}

func readManagementWorkspaceFile(path string) (ManagementWorkspaceState, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return ManagementWorkspaceState{Section: "overview"}, nil
		}
		return ManagementWorkspaceState{}, fmt.Errorf("reading management workspace state: %w", err)
	}
	var state ManagementWorkspaceState
	if err := json.Unmarshal(raw, &state); err != nil {
		return ManagementWorkspaceState{}, fmt.Errorf("parsing management workspace state: %w", err)
	}
	if state.Section == "" {
		state.Section = "overview"
	}
	if _, ok := managementTaskIDs[state.Section]; !ok {
		return ManagementWorkspaceState{}, fmt.Errorf("management task %q is invalid", state.Section)
	}
	return state, nil
}

func writeManagementWorkspaceFile(path string, state ManagementWorkspaceState) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("creating management workspace directory: %w", err)
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("serializing management workspace state: %w", err)
	}
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".management-workspace-*.tmp")
	if err != nil {
		return fmt.Errorf("creating management workspace temporary file: %w", err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return fmt.Errorf("securing management workspace temporary file: %w", err)
	}
	if _, err := temporary.Write(raw); err != nil {
		temporary.Close()
		return fmt.Errorf("writing management workspace temporary file: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("syncing management workspace temporary file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("closing management workspace temporary file: %w", err)
	}
	if err := os.Rename(temporaryName, path); err != nil {
		return fmt.Errorf("replacing management workspace state: %w", err)
	}
	return nil
}
