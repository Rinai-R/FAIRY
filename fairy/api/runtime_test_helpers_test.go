//go:build sqlite_legacy

package api_test

import (
	"testing"

	"fairy/memory"
	fairyruntime "fairy/runtime"
	"fairy/secret"

	"go.uber.org/zap"
)

func testRuntimeOptions(t *testing.T, root string) fairyruntime.Options {
	t.Helper()
	memoryPath, err := memory.DatabasePath(root)
	if err != nil {
		t.Fatal(err)
	}
	memoryStore, err := memory.OpenOrCreate(memoryPath)
	if err != nil {
		t.Fatal(err)
	}
	secretPath, err := secret.DatabasePath(root)
	if err != nil {
		t.Fatal(err)
	}
	return fairyruntime.Options{
		ConfigRoot: root,
		Logger:     zap.NewNop(),
		Dependencies: &fairyruntime.Dependencies{
			MemoryStore: memoryStore,
			SecretStore: secret.NewStore(secretPath),
		},
	}
}
