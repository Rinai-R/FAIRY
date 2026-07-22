package companion

import "testing"

func TestEnginesWiredOnConstruct(t *testing.T) {
	service := NewCompanionService()
	if service.turns == nil || service.participation == nil {
		t.Fatalf("engines = turns:%v participation:%v", service.turns != nil, service.participation != nil)
	}
	if service.turns.host != service || service.participation.host != service {
		t.Fatal("engines must reference host service")
	}
}
