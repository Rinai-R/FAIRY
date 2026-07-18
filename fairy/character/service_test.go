package character

import (
	"os"
	"path/filepath"
	"testing"
)

func writeVisualFixture(t *testing.T, root string, packID string) {
	t.Helper()
	path := filepath.Join(root, "visual-packs", packID, "manifest.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	data := `{"schemaVersion":2,"packId":"` + packID + `","displayName":"Fairy","renderer":"state_images","frame":{"width":128,"height":128},"scale":1,"anchor":{"x":64,"y":127},"states":[{"id":"idle","description":"idle 状态说明","imagePath":"fairy-character://localhost/` + packID + `/idle.png"}]}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
}

func writeCharacterPackageFixture(t *testing.T, dir string, packID string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "images"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "images", "idle.png"), []byte("\x89PNG\r\n\x1a\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(idle) error = %v", err)
	}
	data := `{"schemaVersion":1,"packageId":"` + packID + `","character":{"name":"亚托莉","description":"温柔、敏锐。","dialogueStyle":"短句。","speakingLanguage":"ja"},"visual":{"displayName":"亚托莉","renderer":"state_images","frame":{"width":16,"height":16},"scale":4,"anchor":{"x":8,"y":15},"states":[{"id":"idle","description":"Quiet standing pose.","file":"images/idle.png"}]}}`
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(data), 0o600); err != nil {
		t.Fatalf("WriteFile(manifest) error = %v", err)
	}
}

func TestCharacterServiceListCharactersMissingRoot(t *testing.T) {
	catalog, err := NewCharacterService(t.TempDir()).ListCharacters()
	if err != nil {
		t.Fatalf("ListCharacters() error = %v", err)
	}
	if len(catalog.Characters) != 0 || catalog.Active != nil {
		t.Fatalf("catalog = %#v", catalog)
	}
}

func TestCharacterServiceCreateUpdateAppearanceAndActivate(t *testing.T) {
	root := t.TempDir()
	writeVisualFixture(t, root, "fairy.atri")
	service := NewCharacterService(root)
	created, err := service.CreateCharacter(Brief{Name: "亚托莉", Description: "认真听用户说话。", SpeakingLanguage: "ja"}, "fairy.atri")
	if err != nil {
		t.Fatalf("CreateCharacter() error = %v", err)
	}
	updated, err := service.UpdateCharacter(created.CharacterID, Brief{Name: "亚托莉", Description: "会先听完再回应。", SpeakingLanguage: "zh"})
	if err != nil {
		t.Fatalf("UpdateCharacter() error = %v", err)
	}
	activated, err := service.ActivateCharacter(updated.CharacterID, updated.Revision)
	if err != nil {
		t.Fatalf("ActivateCharacter() error = %v", err)
	}
	if activated.Revision != 2 {
		t.Fatalf("activated = %#v", activated)
	}
}

func TestCharacterServiceImportAndExportPackage(t *testing.T) {
	root := t.TempDir()
	packageDir := t.TempDir()
	writeCharacterPackageFixture(t, packageDir, "fairy.package")
	service := NewCharacterService(root)
	record, err := service.ImportCharacterPackage(packageDir)
	if err != nil {
		t.Fatalf("ImportCharacterPackage() error = %v", err)
	}
	exportPath := filepath.Join(t.TempDir(), "atri.pack")
	if err := service.ExportCharacterPackage(record.CharacterID, exportPath); err != nil {
		t.Fatalf("ExportCharacterPackage() error = %v", err)
	}
}
