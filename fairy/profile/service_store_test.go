package profile

import "testing"

func TestProfileServiceReusesProfileStore(t *testing.T) {
	service := NewProfileService(t.TempDir())
	first := service.ProfileStore()
	second := service.ProfileStore()
	if first == nil || first != second {
		t.Fatalf("ProfileStore() instances differ: %p vs %p", first, second)
	}
	if _, err := service.Current(); err != nil {
		t.Fatalf("Current() error = %v", err)
	}
	if service.ProfileStore() != first {
		t.Fatal("ProfileStore() changed after Current")
	}
}
