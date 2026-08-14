package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	instanceLockName   = "runtime.write.lock"
	fairyProfileVendor = "dev.rinai.fairy"
	fairyProfileApp    = "session-core"
	fairyProfileRev    = "v1"
)

var (
	ErrInstanceHeld            = errors.New("another FAIRY runtime already holds this user profile")
	ErrInstanceLockUnsupported = errors.New("user profile write locks are not supported on this platform")
)

type instanceGuard interface {
	Close() error
}

func desktopProfileDir() (string, error) {
	if root := strings.TrimSpace(os.Getenv("FAIRY_CONFIG_ROOT")); root != "" {
		abs, err := filepath.Abs(root)
		if err != nil {
			return "", fmt.Errorf("resolve FAIRY_CONFIG_ROOT: %w", err)
		}
		return abs, nil
	}
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve user profile directory: %w", err)
	}
	return filepath.Join(configDir, fairyProfileVendor, fairyProfileApp, fairyProfileRev), nil
}

func focusSocketPath(dir string) string {
	sum := sha256.Sum256([]byte(dir))
	return filepath.Join("/tmp", fmt.Sprintf("fairy-%d-focus-%s.sock", os.Geteuid(), hex.EncodeToString(sum[:8])))
}

func ensureProfileDir(dir string) error {
	if dir == "" || dir != filepath.Clean(dir) || !filepath.IsAbs(dir) {
		return errors.New("user profile directory must be an absolute clean path")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create user profile directory: %w", err)
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("inspect user profile directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("user profile directory must be a real directory")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("user profile directory permissions %04o are wider than 0700", info.Mode().Perm())
	}
	return nil
}
