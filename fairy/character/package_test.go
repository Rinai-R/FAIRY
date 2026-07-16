package character

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writePackageFixture(t *testing.T, dir string, packID string) {
	t.Helper()
	writeFile(t, filepath.Join(dir, "images", "idle.png"), pngSignature)
	writeFile(t, filepath.Join(dir, "manifest.json"), `{"schemaVersion":1,"packageId":"`+packID+`","character":{"name":"亚托莉","description":"温柔、敏锐。","dialogueStyle":"短句。"},"visual":{"displayName":"亚托莉","renderer":"state_images","frame":{"width":16,"height":16},"scale":4,"anchor":{"x":8,"y":15},"states":[{"id":"idle","description":"Quiet standing pose.","file":"images/idle.png"}]}}`)
}

func TestImportDirectoryPackageCreatesCharacterAndVisualPack(t *testing.T) {
	root := t.TempDir()
	packageDir := t.TempDir()
	writePackageFixture(t, packageDir, "fairy.package")
	record, err := NewStore(root).ImportPackage(packageDir)
	if err != nil {
		t.Fatalf("ImportPackage() error = %v", err)
	}
	if record.Name != "亚托莉" || record.Appearance.Status != "assigned" || record.Appearance.Visual == nil || record.Appearance.Visual.PackID != "fairy.package" {
		t.Fatalf("record = %#v", record)
	}
	if _, err := os.Stat(filepath.Join(root, "visual-packs", "fairy.package", "images", "idle.png")); err != nil {
		t.Fatalf("imported image missing: %v", err)
	}
}

func TestExportPackageRoundTrips(t *testing.T) {
	root := t.TempDir()
	packageDir := t.TempDir()
	writePackageFixture(t, packageDir, "fairy.package")
	record, err := NewStore(root).ImportPackage(packageDir)
	if err != nil {
		t.Fatalf("ImportPackage() error = %v", err)
	}
	exportPath := filepath.Join(t.TempDir(), "atri.pack")
	if err := NewStore(root).ExportPackage(record.CharacterID, exportPath); err != nil {
		t.Fatalf("ExportPackage() error = %v", err)
	}
	archive, err := zip.OpenReader(exportPath)
	if err != nil {
		t.Fatalf("OpenReader() error = %v", err)
	}
	defer archive.Close()
	if _, err := archiveFileBytes(archive.File, "manifest.json"); err != nil {
		t.Fatalf("manifest missing: %v", err)
	}
	if _, err := archiveFileBytes(archive.File, "images/idle.png"); err != nil {
		t.Fatalf("idle missing: %v", err)
	}
}

func TestImportPackageRejectsPathTraversal(t *testing.T) {
	packageDir := t.TempDir()
	writePackageFixture(t, packageDir, "fairy.bad")
	data, err := os.ReadFile(filepath.Join(packageDir, "manifest.json"))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	bad := string(data)
	bad = strings.ReplaceAll(bad, "images/idle.png", "../idle.png")
	if err := os.WriteFile(filepath.Join(packageDir, "manifest.json"), []byte(bad), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if _, err := NewStore(t.TempDir()).ImportPackage(packageDir); err == nil {
		t.Fatal("ImportPackage() error = nil, want traversal error")
	}
}
