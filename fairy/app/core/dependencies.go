package core

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"fairy/app/foundation"
)

func openFoundation(lifetime context.Context, configRoot string) (*foundation.Foundation, error) {
	characterRoot, err := filepath.Abs(configRoot)
	if err != nil {
		return nil, fmt.Errorf("resolving character root: %w", err)
	}
	if err := os.MkdirAll(characterRoot, 0o700); err != nil {
		return nil, fmt.Errorf("creating character root: %w", err)
	}
	opened, err := foundation.Open(lifetime, foundation.Options{CharacterRoot: characterRoot})
	if err != nil {
		return nil, err
	}
	return opened, nil
}
