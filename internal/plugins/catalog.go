package plugins

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"

	"github.com/Rinai-R/FAIRY/internal/app"
)

type Catalog struct {
	ManifestPath string
	PluginDir    string
}

func NewCatalog(manifestPath string, pluginDir string) *Catalog {
	if manifestPath == "" {
		manifestPath = "configs/plugins.json"
	}
	if pluginDir == "" {
		pluginDir = "plugins"
	}
	return &Catalog{ManifestPath: manifestPath, PluginDir: pluginDir}
}

func (c *Catalog) Load() app.PluginCatalog {
	catalog := app.PluginCatalog{Version: "0.1.0"}
	c.loadFile(c.ManifestPath, &catalog)
	for _, path := range c.pluginManifestPaths(&catalog) {
		c.loadFile(path, &catalog)
	}
	return catalog
}

func (c *Catalog) loadFile(path string, catalog *app.PluginCatalog) {
	if path == "" {
		return
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		if path != c.ManifestPath {
			catalog.Errors = append(catalog.Errors, path+": "+err.Error())
		}
		return
	}
	if err != nil {
		catalog.Errors = append(catalog.Errors, path+": "+err.Error())
		return
	}
	var manifest app.PluginManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		catalog.Errors = append(catalog.Errors, path+": "+err.Error())
		return
	}
	manifest.Path = path
	catalog.Manifests = append(catalog.Manifests, manifest)
}

func (c *Catalog) pluginManifestPaths(catalog *app.PluginCatalog) []string {
	entries, err := os.ReadDir(c.PluginDir)
	if errors.Is(err, os.ErrNotExist) {
		return []string{}
	}
	if err != nil {
		catalog.Errors = append(catalog.Errors, c.PluginDir+": "+err.Error())
		return []string{}
	}
	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			candidate := filepath.Join(c.PluginDir, entry.Name(), "plugin.json")
			if _, err := os.Stat(candidate); err == nil {
				paths = append(paths, candidate)
			}
			continue
		}
		if filepath.Ext(entry.Name()) == ".json" {
			paths = append(paths, filepath.Join(c.PluginDir, entry.Name()))
		}
	}
	sort.Strings(paths)
	return paths
}
