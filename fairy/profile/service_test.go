package profile

import "testing"

func TestProfileServiceSetCurrentAndClear(t *testing.T) {
	service := NewProfileService(t.TempDir())
	name := "Rinai"
	update, err := service.SetPreferredName(&name)
	if err != nil {
		t.Fatalf("SetPreferredName() error = %v", err)
	}
	if !update.Changed || update.Profile == nil {
		t.Fatalf("update = %#v", update)
	}
	current, err := service.Current()
	if err != nil {
		t.Fatalf("Current() error = %v", err)
	}
	if current == nil || current.PreferredName == nil || *current.PreferredName != "Rinai" {
		t.Fatalf("current = %#v", current)
	}
	cleared, err := service.Clear()
	if err != nil {
		t.Fatalf("Clear() error = %v", err)
	}
	if !cleared.Changed || cleared.Profile == nil || cleared.Profile.PreferredName != nil {
		t.Fatalf("cleared = %#v", cleared)
	}
}
