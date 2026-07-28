package main

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/spf13/pathologize"
)

const maxVisualCachePackDirectories = 4

var ErrVisualCacheCapacity = errors.New("desktop visual cache capacity reached")

var visualCacheRoots = struct {
	sync.Mutex
	roots map[string]*visualCacheRootState
}{
	roots: make(map[string]*visualCacheRootState),
}

type visualCacheRootState struct {
	active      map[string]int
	staging     map[string]int
	activeFiles map[string]int
}

func beginVisualCacheSync(root, key string) error {
	visualCacheRoots.Lock()
	defer visualCacheRoots.Unlock()
	state := visualCacheRootStateLocked(root)
	if state.active[key] == 0 && state.staging[key] == 0 && visualCacheProtectedKeyCount(state) >= maxVisualCachePackDirectories {
		deleteVisualCacheRootStateIfIdleLocked(root, state)
		return fmt.Errorf("%w: limit %d", ErrVisualCacheCapacity, maxVisualCachePackDirectories)
	}
	state.staging[key]++
	if err := pruneVisualCacheRootLocked(root, state); err != nil {
		decrementVisualCacheReference(state.staging, key)
		deleteVisualCacheRootStateIfIdleLocked(root, state)
		return fmt.Errorf("pruning visual cache before sync: %w", err)
	}
	return nil
}

func abortVisualCacheSync(root, key string) error {
	visualCacheRoots.Lock()
	defer visualCacheRoots.Unlock()
	state := visualCacheRootStateLocked(root)
	decrementVisualCacheReference(state.staging, key)
	var cleanupErr error
	if state.active[key] == 0 && state.staging[key] == 0 {
		cleanupErr = os.RemoveAll(pathologize.Join(root, key))
	}
	pruneErr := pruneVisualCacheRootLocked(root, state)
	deleteVisualCacheRootStateIfIdleLocked(root, state)
	return errors.Join(cleanupErr, pruneErr)
}

func commitVisualCacheSync(c *visualCache, key string, stagedAssets map[string]string) (bool, error) {
	visualCacheRoots.Lock()
	defer visualCacheRoots.Unlock()
	state := visualCacheRootStateLocked(c.root)
	c.mu.Lock()
	if c.visualClosed {
		c.mu.Unlock()
		return false, errors.New("visual cache is closed")
	}
	removeVisualCacheActiveLocked(state, c.activeKey, c.assets)
	decrementVisualCacheReference(state.staging, key)
	state.active[key]++
	for _, path := range stagedAssets {
		state.activeFiles[path]++
	}
	c.activeKey = key
	c.assets = stagedAssets
	c.mu.Unlock()
	if err := pruneVisualCacheRootLocked(c.root, state); err != nil {
		return true, fmt.Errorf("pruning visual cache after sync: %w", err)
	}
	return true, nil
}

func visualCacheRootStateLocked(root string) *visualCacheRootState {
	state := visualCacheRoots.roots[root]
	if state == nil {
		state = &visualCacheRootState{
			active:      make(map[string]int),
			staging:     make(map[string]int),
			activeFiles: make(map[string]int),
		}
		visualCacheRoots.roots[root] = state
	}
	return state
}

func removeVisualCacheActiveLocked(state *visualCacheRootState, key string, assets map[string]string) {
	if state == nil || key == "" {
		return
	}
	decrementVisualCacheReference(state.active, key)
	for _, path := range assets {
		decrementVisualCacheReference(state.activeFiles, path)
	}
}

func decrementVisualCacheReference(references map[string]int, key string) {
	if key == "" || references[key] <= 1 {
		delete(references, key)
		return
	}
	references[key]--
}

func visualCacheProtectedKeyCount(state *visualCacheRootState) int {
	protected := make(map[string]struct{}, len(state.active)+len(state.staging))
	for key := range state.active {
		protected[key] = struct{}{}
	}
	for key := range state.staging {
		protected[key] = struct{}{}
	}
	return len(protected)
}

func deleteVisualCacheRootStateIfIdleLocked(root string, state *visualCacheRootState) {
	if state != nil && len(state.active) == 0 && len(state.staging) == 0 && len(state.activeFiles) == 0 {
		delete(visualCacheRoots.roots, root)
	}
}

type visualCacheDirectory struct {
	name    string
	modTime time.Time
}

func pruneVisualCacheRootLocked(root string, state *visualCacheRootState) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return fmt.Errorf("reading visual cache root: %w", err)
	}
	protected := make(map[string]struct{}, len(state.active)+len(state.staging))
	for key := range state.active {
		protected[key] = struct{}{}
	}
	for key := range state.staging {
		protected[key] = struct{}{}
	}
	if len(protected) > maxVisualCachePackDirectories {
		return fmt.Errorf("%w: protected keys %d exceed limit %d", ErrVisualCacheCapacity, len(protected), maxVisualCachePackDirectories)
	}
	unprotected := make([]visualCacheDirectory, 0)
	for _, entry := range entries {
		if !entry.IsDir() || !isVisualCacheKey(entry.Name()) {
			continue
		}
		if _, ok := protected[entry.Name()]; ok {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("reading visual cache directory %q: %w", entry.Name(), err)
		}
		unprotected = append(unprotected, visualCacheDirectory{name: entry.Name(), modTime: info.ModTime()})
	}
	sort.Slice(unprotected, func(i, j int) bool {
		if unprotected[i].modTime.Equal(unprotected[j].modTime) {
			return unprotected[i].name > unprotected[j].name
		}
		return unprotected[i].modTime.After(unprotected[j].modTime)
	})
	keep := maxVisualCachePackDirectories - len(protected)
	removeFrom := min(keep, len(unprotected))
	for _, directory := range unprotected[removeFrom:] {
		if err := os.RemoveAll(pathologize.Join(root, directory.name)); err != nil {
			return fmt.Errorf("removing visual cache directory %q: %w", directory.name, err)
		}
	}
	for key := range state.active {
		if state.staging[key] > 0 {
			continue
		}
		if err := pruneVisualCacheFilesLocked(root, key, state.activeFiles); err != nil {
			return err
		}
	}
	return nil
}

func pruneVisualCacheFilesLocked(root, key string, activeFiles map[string]int) error {
	directory := pathologize.Join(root, key)
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading active visual cache directory %q: %w", key, err)
	}
	for _, entry := range entries {
		if !entry.Type().IsRegular() || !isGeneratedVisualAssetName(entry.Name()) {
			continue
		}
		path := pathologize.Join(directory, entry.Name())
		if activeFiles[path] > 0 {
			continue
		}
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("removing stale visual cache file %q: %w", entry.Name(), err)
		}
	}
	return nil
}

func isVisualCacheKey(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func isGeneratedVisualAssetName(value string) bool {
	if !strings.HasSuffix(value, ".png") {
		return false
	}
	base := strings.TrimSuffix(value, ".png")
	if len(base) < 64 || !isVisualCacheKey(base[:64]) {
		return false
	}
	suffix := base[64:]
	if suffix == "" {
		return true
	}
	if suffix[0] != '-' || len(suffix) == 1 {
		return false
	}
	for _, character := range suffix[1:] {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}
