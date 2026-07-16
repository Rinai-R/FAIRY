package visual

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ValidateRelativePNGPath accepts only relative PNG paths under visual-packs,
// matching the Tauri fairy-character protocol guardrails.
func ValidateRelativePNGPath(value string) (string, error) {
	if value == "" ||
		!strings.HasSuffix(value, ".png") ||
		strings.Contains(value, "://") ||
		strings.ContainsAny(value, `\\?#`) ||
		strings.HasPrefix(value, "/") {
		return "", errors.New("invalid character asset path")
	}
	parts := strings.Split(value, "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return "", errors.New("invalid character asset path")
		}
	}
	return value, nil
}

// ResolveAssetFile joins a validated relative PNG path onto the visual-packs root.
func ResolveAssetFile(visualPacksRoot, relative string) (string, error) {
	safe, err := ValidateRelativePNGPath(relative)
	if err != nil {
		return "", err
	}
	root := filepath.Clean(visualPacksRoot)
	full := filepath.Clean(filepath.Join(root, filepath.FromSlash(safe)))
	rel, err := filepath.Rel(root, full)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", errors.New("invalid character asset path")
	}
	info, err := os.Stat(full)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("character asset not found")
		}
		return "", err
	}
	if info.IsDir() {
		return "", errors.New("character asset not found")
	}
	return full, nil
}

// VisualPacksRoot returns configRoot/visual-packs.
func VisualPacksRoot(configRoot string) string {
	return filepath.Join(configRoot, "visual-packs")
}
