package visual

import (
	"os"
	"path/filepath"
	"testing"
)

func writeManifest(t *testing.T, root string, packID string) {
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

func TestListManifestsSorted(t *testing.T) {
	root := t.TempDir()
	writeManifest(t, root, "fairy.second")
	writeManifest(t, root, "fairy.first")
	catalog, err := ListManifests(root)
	if err != nil {
		t.Fatalf("ListManifests() error = %v", err)
	}
	if len(catalog.VisualPacks) != 2 || catalog.VisualPacks[0].PackID != "fairy.first" || catalog.VisualPacks[1].PackID != "fairy.second" {
		t.Fatalf("catalog = %#v", catalog)
	}
}

func TestListManifestsMissingRootReturnsEmptyCatalog(t *testing.T) {
	catalog, err := ListManifests(t.TempDir())
	if err != nil {
		t.Fatalf("ListManifests() error = %v", err)
	}
	if len(catalog.VisualPacks) != 0 {
		t.Fatalf("catalog = %#v", catalog)
	}
}
