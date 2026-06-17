package runtime

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestUserConfigStoreSaveAndLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config", "user-config.json")
	store := NewUserConfigStore(path)

	if err := store.Save(json.RawMessage(`{"version":1,"activeCharacterID":"tutor"}`)); err != nil {
		t.Fatalf("save user config: %v", err)
	}

	raw, exists, err := store.Load()
	if err != nil {
		t.Fatalf("load user config: %v", err)
	}
	if !exists {
		t.Fatal("expected saved config to exist")
	}
	var payload struct {
		Version           int    `json:"version"`
		ActiveCharacterID string `json:"activeCharacterID"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("unmarshal saved config: %v", err)
	}
	if payload.Version != 1 || payload.ActiveCharacterID != "tutor" {
		t.Fatalf("unexpected payload: %+v", payload)
	}
}

func TestUserConfigStoreRejectsInvalidJSON(t *testing.T) {
	store := NewUserConfigStore(filepath.Join(t.TempDir(), "user-config.json"))
	if err := store.Save(json.RawMessage(`[]`)); err == nil {
		t.Fatal("expected array config to be rejected")
	}
	if err := store.Save(json.RawMessage(`{"version":`)); err == nil {
		t.Fatal("expected malformed config to be rejected")
	}
}
