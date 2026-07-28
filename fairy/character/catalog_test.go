package character

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeFile(t testing.TB, path string, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
}

func writeCharacter(t testing.TB, root string, characterID string, revision uint64, name string) {
	t.Helper()
	revisionText := fmt.Sprint(revision)
	writeFile(t, filepath.Join(root, "characters", characterID, "revisions", revisionText+".json"), `{"schema_version":1,"data":{"schema_version":1,"compiler_version":"fairy-character-v1","character_id":"`+characterID+`","revision":`+revisionText+`,"identity":{"name":"`+name+`","description":"认真听用户说话。"},"worldview":"not_specified","attention_biases":["user_explicit_content"],"relationship_stance":"warm_respectful_non_possessive","response_drives":["understand_before_assuming"],"emotional_tendencies":["calm_attunement"],"speech_style":{"character_description_guidance":"认真听用户说话。","fallback":"natural_concise"},"hard_boundaries":["preserve_facts"],"fingerprint":"fixture"}}`)
}

func writeVisual(t testing.TB, root string, packID string) {
	t.Helper()
	writeFile(t, filepath.Join(root, "visual-packs", packID, "manifest.json"), `{"schemaVersion":2,"packId":"`+packID+`","displayName":"Fairy","renderer":"state_images","frame":{"width":128,"height":128},"scale":1,"anchor":{"x":64,"y":127},"states":[{"id":"idle","description":"idle 状态说明","imagePath":"fairy-character://localhost/`+packID+`/idle.png"}]}`)
}

func writeCharacterLibrary(t testing.TB, root string, count int) string {
	t.Helper()
	if count < 1 {
		t.Fatal("character fixture count must be positive")
	}
	const packID = "fairy.shared"
	writeVisual(t, root, packID)
	for index := range count {
		characterID := fmt.Sprintf("character-%03d", index)
		writeCharacter(t, root, characterID, 1, fmt.Sprintf("角色 %03d", index))
		writeFile(t, filepath.Join(root, "character-appearances", characterID+".json"), `{"schema_version":1,"data":{"character_id":"`+characterID+`","revision":1,"visual_pack_id":"`+packID+`"}}`)
	}
	targetID := "character-000"
	writeFile(t, filepath.Join(root, "active-character.json"), `{"schema_version":1,"data":{"character_id":"`+targetID+`","revision":1}}`)
	return targetID
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
	if catalog.Characters[0].SpeakingLanguage != DefaultSpeakingLanguage || catalog.Active.SpeakingLanguage != DefaultSpeakingLanguage {
		t.Fatalf("speaking language = %#v active=%#v", catalog.Characters[0].SpeakingLanguage, catalog.Active.SpeakingLanguage)
	}
	if catalog.Characters[0].TextLanguage != DefaultTextLanguage || catalog.Active.TextLanguage != DefaultTextLanguage {
		t.Fatalf("text language = %#v active=%#v", catalog.Characters[0].TextLanguage, catalog.Active.TextLanguage)
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

func TestStoreLookupMatchesCatalogRecord(t *testing.T) {
	root := t.TempDir()
	targetID := writeCharacterLibrary(t, root, 2)
	store := NewStore(root)

	got, found, err := store.Lookup(targetID)
	if err != nil {
		t.Fatalf("Lookup() error = %v", err)
	}
	if !found {
		t.Fatal("Lookup() found = false, want true")
	}
	catalog, err := store.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(catalog.Characters) != 2 {
		t.Fatalf("List() returned %d characters, want 2", len(catalog.Characters))
	}
	if !reflect.DeepEqual(got, catalog.Characters[0]) {
		t.Fatalf("Lookup() = %#v, catalog target = %#v", got, catalog.Characters[0])
	}
}

func TestStoreLookupRejectsInvalidOrMissingCharacter(t *testing.T) {
	store := NewStore(t.TempDir())
	for _, characterID := range []string{"", " character", "../character", `character\\nested`} {
		if _, _, err := store.Lookup(characterID); err == nil {
			t.Errorf("Lookup(%q) error = nil, want validation error", characterID)
		}
	}

	if record, found, err := store.Lookup("missing-character"); err != nil || found || !reflect.DeepEqual(record, Record{}) {
		t.Fatalf("Lookup(missing) = (%#v, %v, %v), want zero, false, nil", record, found, err)
	}

	root := t.TempDir()
	writeFile(t, filepath.Join(root, "characters", "broken-character", "revisions", "1.json"), `{broken`)
	if record, found, err := NewStore(root).Lookup("broken-character"); err != nil || found || !reflect.DeepEqual(record, Record{}) {
		t.Fatalf("Lookup(broken) = (%#v, %v, %v), want zero, false, nil", record, found, err)
	}
}

func TestStoreLookupFallsBackToLatestValidRevision(t *testing.T) {
	root := t.TempDir()
	const characterID = "character-fallback"
	writeCharacter(t, root, characterID, 1, "有效角色")
	writeFile(t, filepath.Join(root, "characters", characterID, "revisions", "2.json"), `{broken`)

	record, found, err := NewStore(root).Lookup(characterID)
	if err != nil {
		t.Fatalf("Lookup() error = %v", err)
	}
	if !found || record.Revision != 1 || record.Name != "有效角色" {
		t.Fatalf("Lookup() = (%#v, %v), want revision 1 fallback", record, found)
	}
}

func TestStoreLookupOrdersRevisionsNumerically(t *testing.T) {
	root := t.TempDir()
	const characterID = "character-numeric-revision"
	writeCharacter(t, root, characterID, 2, "旧角色")
	writeCharacter(t, root, characterID, 10, "新角色")

	record, found, err := NewStore(root).Lookup(characterID)
	if err != nil {
		t.Fatalf("Lookup() error = %v", err)
	}
	if !found || record.Revision != 10 || record.Name != "新角色" {
		t.Fatalf("Lookup() = (%#v, %v), want numeric revision 10", record, found)
	}
}

func TestStoreLookupFallsBackAcrossRevisionCandidateBatches(t *testing.T) {
	root := t.TempDir()
	const characterID = "character-batched-fallback"
	writeCharacter(t, root, characterID, 1, "有效角色")
	for revision := uint64(2); revision <= revisionCandidateBatchSize+2; revision++ {
		writeFile(t, filepath.Join(root, "characters", characterID, "revisions", fmt.Sprintf("%d.json", revision)), `{broken`)
	}

	record, found, err := NewStore(root).Lookup(characterID)
	if err != nil {
		t.Fatalf("Lookup() error = %v", err)
	}
	if !found || record.Revision != 1 || record.Name != "有效角色" {
		t.Fatalf("Lookup() = (%#v, %v), want cross-batch revision 1 fallback", record, found)
	}
	candidates, _, err := scanRevisionCandidates(filepath.Join(root, "characters", characterID, "revisions"), characterID, nil, revisionCandidateBatchSize, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != revisionCandidateBatchSize {
		t.Fatalf("retained candidates = %d, want %d", len(candidates), revisionCandidateBatchSize)
	}
}

func TestCharacterRevisionProductionScanRemainsBounded(t *testing.T) {
	source, err := os.ReadFile("catalog.go")
	if err != nil {
		t.Fatal(err)
	}
	production := string(source)
	for _, forbidden := range []string{
		"os.ReadDir(revisionsDir)",
		"entries[i].Name() > entries[j].Name()",
	} {
		if strings.Contains(production, forbidden) {
			t.Fatalf("character revision scan contains full-directory marker %q", forbidden)
		}
	}
	for _, required := range []string{
		"directory.ReadDir(128)",
		"revisionCandidateBatchSize = 64",
		"revisionCandidateNewer",
	} {
		if !strings.Contains(production, required) {
			t.Fatalf("character revision scan is missing %q", required)
		}
	}
}

func TestStoreLookupKeepsUnavailableAppearance(t *testing.T) {
	root := t.TempDir()
	const characterID = "character-unavailable-appearance"
	writeCharacter(t, root, characterID, 1, "外观损坏角色")
	writeFile(t, filepath.Join(root, "character-appearances", characterID+".json"), `{broken`)

	record, found, err := NewStore(root).Lookup(characterID)
	if err != nil {
		t.Fatalf("Lookup() error = %v", err)
	}
	if !found || record.Appearance.Status != "unavailable" || record.Appearance.Visual != nil {
		t.Fatalf("Lookup() = (%#v, %v), want unavailable appearance", record, found)
	}
}

func TestStoreLookupIgnoresActivePointerAndUnrelatedCharacters(t *testing.T) {
	root := t.TempDir()
	const targetID = "character-target"
	writeCharacter(t, root, targetID, 1, "目标角色")
	writeFile(t, filepath.Join(root, "characters", "character-unrelated", "revisions", "1.json"), `{broken`)
	writeFile(t, filepath.Join(root, "active-character.json"), `{broken`)

	record, found, err := NewStore(root).Lookup(targetID)
	if err != nil {
		t.Fatalf("Lookup() error = %v", err)
	}
	if !found || record.CharacterID != targetID || record.Name != "目标角色" {
		t.Fatalf("Lookup() = (%#v, %v), want target record", record, found)
	}
	if _, err := NewStore(root).List(); err == nil {
		t.Fatal("List() error = nil, fixture must prove corrupt active pointer is observable to catalog only")
	}
}

func TestStoreCreateUpdateAppearanceAndActivate(t *testing.T) {
	root := t.TempDir()
	writeVisual(t, root, "fairy.atri")
	writeVisual(t, root, "fairy.alt")
	style := "短句、自然。"
	store := NewStore(root)

	created, err := store.Create(Brief{Name: " 亚托莉 ", Description: " 认真听用户说话。 ", DialogueStyle: &style, TextLanguage: "en", SpeakingLanguage: "zh"}, "fairy.atri")
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if created.Revision != 1 || created.Name != "亚托莉" || created.DialogueStyle == nil || *created.DialogueStyle != style || created.TextLanguage != "en" || created.SpeakingLanguage != "zh" || created.Appearance.Status != "assigned" {
		t.Fatalf("created = %#v", created)
	}

	updated, err := store.Update(created.CharacterID, Brief{Name: "亚托莉", Description: "会先听完再回应。", TextLanguage: "zh", SpeakingLanguage: "en"})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if updated.Revision != 2 || updated.Description != "会先听完再回应。" || updated.TextLanguage != "zh" || updated.SpeakingLanguage != "en" {
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

func TestStoreCreateRejectsUnsupportedSpeakingLanguage(t *testing.T) {
	root := t.TempDir()
	writeVisual(t, root, "fairy.atri")
	_, err := NewStore(root).Create(Brief{Name: "亚托莉", Description: "认真听用户说话。", SpeakingLanguage: "ko"}, "fairy.atri")
	if err == nil {
		t.Fatal("Create() error = nil, want unsupported speaking language error")
	}
}

func TestStoreCreateRejectsUnsupportedTextLanguage(t *testing.T) {
	root := t.TempDir()
	writeVisual(t, root, "fairy.atri")
	_, err := NewStore(root).Create(Brief{Name: "亚托莉", Description: "认真听用户说话。", TextLanguage: "ko", SpeakingLanguage: "ja"}, "fairy.atri")
	if err == nil {
		t.Fatal("Create() error = nil, want unsupported text language error")
	}
}

func TestStoreCreateRejectsMissingVisualPack(t *testing.T) {
	_, err := NewStore(t.TempDir()).Create(Brief{Name: "亚托莉", Description: "认真听用户说话。"}, "missing.pack")
	if err == nil {
		t.Fatal("Create() error = nil, want missing visual pack error")
	}
}
