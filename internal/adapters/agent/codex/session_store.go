package codex

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

const DefaultSessionPath = "data/codex_runtime_sessions.json"

type SessionStore struct {
	Path string
	mu   sync.Mutex
}

func NewSessionStore(path string) *SessionStore {
	if path == "" {
		path = DefaultSessionPath
	}
	return &SessionStore{Path: path}
}

func (s *SessionStore) Get(key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	items, err := s.load()
	if err != nil {
		return "", err
	}
	return items[key], nil
}

func (s *SessionStore) Set(key string, sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	items, err := s.load()
	if err != nil {
		return err
	}
	items[key] = sessionID

	if err := os.MkdirAll(filepath.Dir(s.Path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(items, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.Path, raw, 0o644)
}

func (s *SessionStore) load() (map[string]string, error) {
	raw, err := os.ReadFile(s.Path)
	if os.IsNotExist(err) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}

	items := map[string]string{}
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, err
	}
	return items, nil
}
