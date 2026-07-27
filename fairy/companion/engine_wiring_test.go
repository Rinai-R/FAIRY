package companion

import "testing"

func TestEnginesWiredOnConstruct(t *testing.T) {
	service := NewCompanionService()
	if service.turns == nil {
		t.Fatal("turn engine was not wired")
	}
	if service.turns.host != service {
		t.Fatal("turn engine must reference host service")
	}
}
