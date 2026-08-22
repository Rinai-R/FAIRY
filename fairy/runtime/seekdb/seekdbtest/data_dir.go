//go:build integration

// Package seekdbtest owns process-scoped filesystem fixtures for real embedded
// SeekDB integration tests.
package seekdbtest

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// EnvDataRoot lets the integration-suite parent own cleanup after the child
// test process exits. Embedded SeekDB intentionally remains live until process
// exit, so testing.T.TempDir cleanup can race its background log writers.
const EnvDataRoot = "FAIRY_SEEKDB_INTEGRATION_DATA_ROOT"

var (
	processDataDirOnce sync.Once
	processDataDir     string
	processDataDirErr  error
)

// DataDir returns the one embedded SeekDB data directory owned by this test
// process. The directory deliberately has no testing cleanup: the external
// isolated runner removes its parent only after the process has exited.
func DataDir(t testing.TB) string {
	t.Helper()
	processDataDirOnce.Do(func() {
		processDataDir, processDataDirErr = makeProcessDataDir()
	})
	if processDataDirErr != nil {
		t.Fatalf("prepare process-scoped SeekDB integration data dir: %v", processDataDirErr)
	}
	return processDataDir
}

func makeProcessDataDir() (string, error) {
	root := os.Getenv(EnvDataRoot)
	if root == "" {
		var err error
		root, err = os.MkdirTemp("", "fairy-seekdb-integration-")
		if err != nil {
			return "", err
		}
	} else {
		if !filepath.IsAbs(root) || filepath.Clean(root) != root {
			return "", fmt.Errorf("%s must be an absolute clean path", EnvDataRoot)
		}
		if err := os.MkdirAll(root, 0o700); err != nil {
			return "", fmt.Errorf("create integration data root: %w", err)
		}
	}
	if err := requirePrivateDirectory(root); err != nil {
		return "", err
	}

	dataDir := filepath.Join(root, "seekdb")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return "", fmt.Errorf("create SeekDB data dir: %w", err)
	}
	if err := requirePrivateDirectory(dataDir); err != nil {
		return "", err
	}
	return dataDir, nil
}

func requirePrivateDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("SeekDB integration data path must be a real directory")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("SeekDB integration data directory permissions %04o are wider than 0700", info.Mode().Perm())
	}
	return nil
}
