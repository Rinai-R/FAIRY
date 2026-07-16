package character

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, path string, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
}

func writeCharacter(t *testing.T, root string, characterID string, revision uint64, name string) {
	t.Helper()
	writeFile(t, filepath.Join(root, "characters", characterID, "revisions", "1.json"), `{"schema_version":1,"data":{"schema_version":1,"compiler_version":"fairy-character-v1","character_id":"`+characterID+`","revision":`+"1"+`,"identity":{"name":"`+name+`","description":"认真听用户说话。"},"worldview":"not_specified","attention_biases":["user_explicit_content"],"relationship_stance":"warm_respectful_non_possessive","response_drives":["understand_before_assuming"],"emotional_tendencies":["calm_attunement"],"speech_style":{"character_description_guidance":"认真听用户说话。","fallback":"natural_concise"},"hard_boundaries":["preserve_facts"],"fingerprint":"fixture"}}`)
}

func writeVisual(t *testing.T, root string, packID string) {
	t.Helper()
	writeFile(t, filepath.Join(root, "visual-packs", packID, "manifest.json"), `{"schemaVersion":2,"packId":"`+packID+`","displayName":"Fairy","renderer":"state_images","frame":{"width":128,"height":128},"scale":1,"anchor":{"x":64,"y":127},"states":[{"id":"idle","description":"idle 状态说明","imagePath":"fairy-character://localhost/`+packID+`/idle.png"}]}`)
}

func TestStoreListReturnsActiveAssignedCharacter(t *testing.T) {
	root := t.TempDir()
	characterID := "6a129284-6358-47b0-ad64-2a5907d36c91"
	writeCharacter(t, root, characterID, 1, "亚托莉")
	writeVisual(t, root, "fairy.atri")
	writeFile(t, filepath.Join(root, "character-appearances", characterID+".json"), `{"schema_version":1,"data":{"character_id":"`+characterID+`","revision":1,"visual_pack_id":"fairy.atri"}}`)
	writeFile(t, filepath.Join(root, "active-character.json"), `{"schema_version":1,"data":{"character_id":"`+characterID+`","revision":1}}`)

	catalog, err := NewStore(root).List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(catalog.Characters) != 1 || catalog.Active == nil {
		t.Fatalf("catalog = %#v", catalog)
	}
	if catalog.Characters[0].Name != "亚托莉" || catalog.Characters[0].Appearance.Status != "assigned" {
		t.Fatalf("character = %#v", catalog.Characters[0])
	}
	if catalog.Characters[0].Appearance.Visual == nil || catalog.Characters[0].Appearance.Visual.PackID != "fairy.atri" {
		t.Fatalf("appearance = %#v", catalog.Characters[0].Appearance)
	}
}

func TestStoreListMissingRootReturnsEmptyCatalog(t *testing.T) {
	catalog, err := NewStore(t.TempDir()).List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(catalog.Characters) != 0 || catalog.Active != nil || len(catalog.Diagnostics) != 0 {
		t.Fatalf("catalog = %#v", catalog)
	}
}

func TestStoreListReportsCorruptCharacter(t *testing.T) {
	root := t.TempDir()
	characterID := "6a129284-6358-47b0-ad64-2a5907d36c91"
	writeFile(t, filepath.Join(root, "characters", characterID, "revisions", "1.json"), `{broken`)
	catalog, err := NewStore(root).List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(catalog.Characters) != 0 || len(catalog.Diagnostics) != 1 {
		t.Fatalf("catalog = %#v", catalog)
	}
}

func TestStoreCreateUpdateAppearanceAndActivate(t *testing.T) {
	root := t.TempDir()
	writeVisual(t, root, "fairy.atri")
	writeVisual(t, root, "fairy.alt")
	style := "短句、自然。"
	store := NewStore(root)

	created, err := store.Create(Brief{Name: " 亚托莉 ", Description: " 认真听用户说话。 ", DialogueStyle: &style}, "fairy.atri")
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if created.Revision != 1 || created.Name != "亚托莉" || created.DialogueStyle == nil || *created.DialogueStyle != style || created.Appearance.Status != "assigned" {
		t.Fatalf("created = %#v", created)
	}

	updated, err := store.Update(created.CharacterID, Brief{Name: "亚托莉", Description: "会先听完再回应。"})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if updated.Revision != 2 || updated.Description != "会先听完再回应。" {
		t.Fatalf("updated = %#v", updated)
	}

	assigned, err := store.SetAppearance(created.CharacterID, "fairy.alt")
	if err != nil {
		t.Fatalf("SetAppearance() error = %v", err)
	}
	if assigned.Appearance.Visual == nil || assigned.Appearance.Visual.PackID != "fairy.alt" || assigned.Appearance.BindingRevision != 2 {
		t.Fatalf("assigned = %#v", assigned)
	}

	active, err := store.Activate(created.CharacterID, updated.Revision)
	if err != nil {
		t.Fatalf("Activate() error = %v", err)
	}
	if active.CharacterID != created.CharacterID || active.Revision != updated.Revision {
		t.Fatalf("active = %#v", active)
	}
	catalog, err := store.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if catalog.Active == nil || catalog.Active.CharacterID != created.CharacterID || catalog.Active.Revision != updated.Revision {
		t.Fatalf("catalog active = %#v", catalog.Active)
	}
}

func TestStoreCreateRejectsMissingVisualPack(t *testing.T) {
	_, err := NewStore(t.TempDir()).Create(Brief{Name: "亚托莉", Description: "认真听用户说话。"}, "missing.pack")
	if err == nil {
		t.Fatal("Create() error = nil, want missing visual pack error")
	}
}
