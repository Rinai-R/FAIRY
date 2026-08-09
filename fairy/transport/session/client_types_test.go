package session

import (
	"encoding/json"
	"testing"
)

func TestVisualStateMotionSurvivesSessionJSONProjection(t *testing.T) {
	raw, err := json.Marshal(CharacterCatalog{Characters: []CharacterRecord{{
		CharacterID: "character-1",
		Appearance: CharacterAppearance{Status: "assigned", Visual: &VisualManifest{
			SchemaVersion: 2,
			PackID:        "fairy.test",
			States:        []VisualState{{ID: "idle", ImagePath: "images/idle.png", Motion: "float"}},
		}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	var restored CharacterCatalog
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	if got := restored.Characters[0].Appearance.Visual.States[0].Motion; got != "float" {
		t.Fatalf("restored motion = %q, want float", got)
	}
}
