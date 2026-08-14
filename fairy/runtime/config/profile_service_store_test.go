package config

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

func TestNewProfileServiceWithStoreRequiresStore(t *testing.T) {
	service, err := NewProfileServiceWithStore(nil)
	if service != nil || err == nil || err.Error() != "profile store is required" {
		t.Fatalf("NewProfileServiceWithStore(nil) = (%#v, %v)", service, err)
	}
}
