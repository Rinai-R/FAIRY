package edge

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"fairy/app/core"
	"fairy/context/character"
)

func TestNewSessionAndCatalogFailClosedWithoutComposition(t *testing.T) {
	var rt *Runtime
	if rt.NewSession() != nil {
		t.Fatal("nil runtime exposed a session facade")
	}
	if err := rt.CancelTurn("c1", "t1"); !errors.Is(err, ErrSessionUnavailable) {
		t.Fatalf("CancelTurn() = %v, want %v", err, ErrSessionUnavailable)
	}
	if _, err := rt.ListCharacters(t.Context()); !errors.Is(err, ErrCharacterCatalogUnavailable) {
		t.Fatalf("ListCharacters() = %v, want %v", err, ErrCharacterCatalogUnavailable)
	}
	if _, err := rt.ListMessages(t.Context(), "c1", 0, 20); !errors.Is(err, ErrSessionUnavailable) {
		t.Fatalf("ListMessages() = %v, want %v", err, ErrSessionUnavailable)
	}
	if _, err := rt.ReadStickerContent(t.Context(), "sticker-1"); !errors.Is(err, ErrStickerStoreUnavailable) {
		t.Fatalf("ReadStickerContent() = %v, want %v", err, ErrStickerStoreUnavailable)
	}
	if _, err := rt.VisualAsset(t.Context(), "fairy.test", "images/idle.png"); !errors.Is(err, ErrVisualPacksRootUnavailable) {
		t.Fatalf("VisualAsset() = %v, want %v", err, ErrVisualPacksRootUnavailable)
	}
}

func TestProjectCharacterCatalogKeepsSessionContract(t *testing.T) {
	catalog := character.Catalog{
		Characters: []character.Record{testCharacterRecord("character-1", "Atri")},
		Active:     ptr(testCharacterRecord("character-1", "Atri")),
	}
	projected := projectCharacterCatalog(catalog)
	if len(projected.Characters) != 1 || projected.Active == nil {
		t.Fatalf("projected catalog = %#v", projected)
	}
	got := projected.Characters[0]
	if got.CharacterID != "character-1" || got.Name != "Atri" || got.Revision != 2 || got.Appearance.Status != "assigned" {
		t.Fatalf("projected record = %#v", got)
	}
	if got.Appearance.Visual == nil || got.Appearance.Visual.PackID != "fairy.test" || got.Appearance.Visual.Frame.Width != 16 {
		t.Fatalf("projected visual = %#v", got.Appearance.Visual)
	}
	if projected.Active.CharacterID != got.CharacterID {
		t.Fatalf("active record = %#v", projected.Active)
	}
}

func TestVisualAssetReadsLocalPackAndRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	assetDir := filepath.Join(root, "visual-packs", "fairy.test", "images")
	if err := os.MkdirAll(assetDir, 0o700); err != nil {
		t.Fatal(err)
	}
	png := []byte("\x89PNG\r\n\x1a\nidle")
	if err := os.WriteFile(filepath.Join(assetDir, "idle.png"), png, 0o600); err != nil {
		t.Fatal(err)
	}
	rt := &Runtime{core: &core.Runtime{ConfigRoot: root}}
	got, err := rt.VisualAsset(t.Context(), "fairy.test", "images/idle.png")
	if err != nil {
		t.Fatalf("VisualAsset() error = %v", err)
	}
	if string(got) != string(png) {
		t.Fatalf("VisualAsset() = %q", got)
	}
	if _, err := rt.VisualAsset(t.Context(), "fairy.test", "../idle.png"); err == nil {
		t.Fatal("VisualAsset() allowed path traversal")
	}
	if _, err := rt.VisualAsset(t.Context(), "fairy.test/../secret", "images/idle.png"); err == nil {
		t.Fatal("VisualAsset() allowed pack ID traversal")
	}
}

func TestVisualAssetRejectsMissingFile(t *testing.T) {
	rt := &Runtime{core: &core.Runtime{ConfigRoot: t.TempDir()}}
	_, err := rt.VisualAsset(t.Context(), "fairy.test", "images/idle.png")
	if !errors.Is(err, character.ErrAssetNotFound) && !errors.Is(err, character.ErrInvalidAssetPath) {
		t.Fatalf("VisualAsset() error = %v, want missing asset", err)
	}
}

func testCharacterRecord(id, name string) character.Record {
	return character.Record{
		CharacterID: id,
		Revision:    2,
		Name:        name,
		Appearance: character.Appearance{
			Status: "assigned",
			Visual: &character.Manifest{
				SchemaVersion: 2,
				PackID:        "fairy.test",
				DisplayName:   name,
				Renderer:      "state_images",
				Frame:         character.Frame{Width: 16, Height: 16},
				Scale:         1,
				Anchor:        character.Point{X: 8, Y: 16},
				States:        []character.State{{ID: "idle", ImagePath: "images/idle.png"}},
			},
		},
	}
}

func ptr[T any](value T) *T { return &value }
