package runtime

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

type UserConfigStore struct {
	Path string
	mu   sync.Mutex
}

func NewUserConfigStore(path string) *UserConfigStore {
	return &UserConfigStore{Path: path}
}

func (s *UserConfigStore) Load() (json.RawMessage, bool, error) {
	if s == nil || s.Path == "" {
		return nil, false, errors.New("用户配置路径未设置")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	raw, err := os.ReadFile(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("读取用户配置失败: %w", err)
	}
	if len(raw) == 0 {
		return nil, false, nil
	}
	if err := validateUserConfig(raw); err != nil {
		return nil, false, err
	}
	return json.RawMessage(raw), true, nil
}

func (s *UserConfigStore) Save(raw json.RawMessage) error {
	if s == nil || s.Path == "" {
		return errors.New("用户配置路径未设置")
	}
	if err := validateUserConfig(raw); err != nil {
		return err
	}
	pretty, err := json.MarshalIndent(json.RawMessage(raw), "", "  ")
	if err != nil {
		return fmt.Errorf("格式化用户配置失败: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := os.MkdirAll(filepath.Dir(s.Path), 0o755); err != nil {
		return fmt.Errorf("创建用户配置目录失败: %w", err)
	}
	if err := os.WriteFile(s.Path, append(pretty, '\n'), 0o600); err != nil {
		return fmt.Errorf("写入用户配置失败: %w", err)
	}
	return nil
}

func validateUserConfig(raw []byte) error {
	if len(raw) == 0 {
		return errors.New("用户配置不能为空")
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return fmt.Errorf("用户配置必须是 JSON object: %w", err)
	}
	if payload == nil {
		return errors.New("用户配置必须是 JSON object")
	}
	return nil
}
