package character

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

type VisualCatalog struct {
	VisualPacks []Manifest `json:"visualPacks"`
}

func ListManifests(root string) (VisualCatalog, error) {
	if root == "" {
		return VisualCatalog{}, errors.New("visual pack root is required")
	}
	dir := filepath.Join(root, "visual-packs")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return VisualCatalog{VisualPacks: []Manifest{}}, nil
		}
		return VisualCatalog{}, fmt.Errorf("reading visual pack directory: %w", err)
	}
	manifests := make([]Manifest, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		manifest, err := LoadManifestFromFile(filepath.Join(dir, entry.Name(), "manifest.json"))
		if err != nil {
			return VisualCatalog{}, err
		}
		manifests = append(manifests, manifest)
	}
	sort.Slice(manifests, func(i, j int) bool { return manifests[i].PackID < manifests[j].PackID })
	return VisualCatalog{VisualPacks: manifests}, nil
}
