package companion

import "testing"

func TestRespondRuntimeMigratedRequiresPorts(t *testing.T) {
	if NewCompanionService().RespondRuntimeMigrated() {
		t.Fatal("empty companion must not report migrated")
	}
	root := t.TempDir()
	service := NewCompanionServiceWithRuntime(root, nil, nil, nil)
	if service.RespondRuntimeMigrated() {
		t.Fatal("nil memory/model must not report migrated")
	}
}
