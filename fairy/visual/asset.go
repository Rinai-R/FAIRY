package visual

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var (
	ErrInvalidAssetPath = errors.New("invalid character asset path")
	ErrAssetNotFound    = errors.New("character asset not found")
)

// ValidateRelativePNGPath accepts only relative PNG paths under visual-packs,
// matching the Tauri fairy-character protocol guardrails.
func ValidateRelativePNGPath(value string) (string, error) {
	if value == "" ||
		!strings.HasSuffix(value, ".png") ||
		strings.Contains(value, "://") ||
		strings.ContainsAny(value, `\\?#`) ||
		strings.HasPrefix(value, "/") {
		return "", ErrInvalidAssetPath
	}
	parts := strings.Split(value, "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return "", ErrInvalidAssetPath
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
		return "", ErrInvalidAssetPath
	}
	info, err := os.Stat(full)
	if err != nil {
		if os.IsNotExist(err) {
			return "", ErrAssetNotFound
		}
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", ErrAssetNotFound
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("resolving visual packs root: %w", err)
	}
	realFile, err := filepath.EvalSymlinks(full)
	if err != nil {
		if os.IsNotExist(err) {
			return "", ErrAssetNotFound
		}
		return "", fmt.Errorf("resolving character asset: %w", err)
	}
	realRelative, err := filepath.Rel(realRoot, realFile)
	if err != nil || realRelative == ".." || strings.HasPrefix(realRelative, ".."+string(filepath.Separator)) || filepath.IsAbs(realRelative) {
		return "", ErrInvalidAssetPath
	}
	return realFile, nil
}

// VisualPacksRoot returns configRoot/visual-packs.
func VisualPacksRoot(configRoot string) string {
	return filepath.Join(configRoot, "visual-packs")
}
