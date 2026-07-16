package profile

import "testing"

func TestStoreSetRestartAndClear(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
	name := "  Rinai  "
	update, err := store.SetPreferredName(&name)
	if err != nil {
		t.Fatalf("SetPreferredName() error = %v", err)
	}
	if !update.Changed || update.Profile == nil || update.Profile.Revision != 1 || *update.Profile.PreferredName != "Rinai" {
		t.Fatalf("update = %#v", update)
	}
	same, err := NewStore(root).SetPreferredName(&name)
	if err != nil {
		t.Fatalf("SetPreferredName(same) error = %v", err)
	}
	if same.Changed || same.Profile.Revision != 1 {
		t.Fatalf("same = %#v", same)
	}
	cleared, err := NewStore(root).Clear()
	if err != nil {
		t.Fatalf("Clear() error = %v", err)
	}
	if !cleared.Changed || cleared.Profile == nil || cleared.Profile.Revision != 2 || cleared.Profile.PreferredName != nil {
		t.Fatalf("cleared = %#v", cleared)
	}
}

func TestStoreClearAbsentDoesNotFabricateProfile(t *testing.T) {
	update, err := NewStore(t.TempDir()).Clear()
	if err != nil {
		t.Fatalf("Clear() error = %v", err)
	}
	if update.Changed || update.Profile != nil || update.RecoveredCorruption {
		t.Fatalf("update = %#v", update)
	}
}

func TestStoreRejectsInvalidPreferredName(t *testing.T) {
	name := "名字\n第二行"
	_, err := NewStore(t.TempDir()).SetPreferredName(&name)
	if err == nil {
		t.Fatal("SetPreferredName() error = nil, want invalid preferred name error")
	}
}
