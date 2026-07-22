package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"fairy/coreclient"
	"github.com/spf13/fileflow"
	"github.com/spf13/pathologize"
)

const maxVisualPackBytes = 64 << 20

type visualCache struct {
	root   string
	mu     sync.RWMutex
	assets map[string]string
}

func newVisualCache() (*visualCache, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return nil, fmt.Errorf("locating user cache directory: %w", err)
	}
	return newVisualCacheAt(filepath.Join(base, "FAIRY", "visual-runtime"))
}

func newVisualCacheAt(root string) (*visualCache, error) {
	if root == "" {
		return nil, errors.New("visual cache root is required")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("creating visual cache root: %w", err)
	}
	return &visualCache{root: root, assets: make(map[string]string)}, nil
}

func (c *visualCache) Sync(ctx context.Context, client *coreclient.Client, visual coreclient.VisualManifest) (coreclient.VisualManifest, error) {
	if client == nil {
		return coreclient.VisualManifest{}, errors.New("Core client is required")
	}
	if err := validateVisualManifest(visual); err != nil {
		return coreclient.VisualManifest{}, err
	}
	key, err := visualCacheKey(visual)
	if err != nil {
		return coreclient.VisualManifest{}, err
	}
	local := visual
	localStates := make([]coreclient.VisualState, 0, len(visual.States))
	stagedAssets := make(map[string]string, len(visual.States))
	var total int

	for _, state := range visual.States {
		assetPath, err := visualAssetPath(visual.PackID, state.ImagePath)
		if err != nil {
			return coreclient.VisualManifest{}, fmt.Errorf("normalizing %q state image: %w", state.ID, err)
		}
		image, err := client.VisualAsset(ctx, visual.PackID, assetPath)
		if err != nil {
			return coreclient.VisualManifest{}, fmt.Errorf("downloading %q state image: %w", state.ID, err)
		}
		if len(image) < 8 || string(image[:8]) != "\x89PNG\r\n\x1a\n" {
			return coreclient.VisualManifest{}, fmt.Errorf("downloading %q state image: response is not a PNG", state.ID)
		}
		total += len(image)
		if total > maxVisualPackBytes {
			return coreclient.VisualManifest{}, fmt.Errorf("visual pack exceeds %d bytes", maxVisualPackBytes)
		}
		filename := visualStateFilename(state.ID)
		target := pathologize.Join(c.root, key, filename)
		if err := moveVisualAsset(image, target); err != nil {
			return coreclient.VisualManifest{}, fmt.Errorf("caching %q state image: %w", state.ID, err)
		}
		route := "/" + key + "/" + filename
		stagedAssets[route] = target
		state.ImagePath = "/characters" + route
		localStates = append(localStates, state)
	}
	local.States = localStates

	c.mu.Lock()
	c.assets = stagedAssets
	c.mu.Unlock()
	return local, nil
}

func visualAssetPath(packID, imagePath string) (string, error) {
	parsed, err := url.Parse(imagePath)
	if err != nil {
		return "", fmt.Errorf("parsing visual image path: %w", err)
	}
	if parsed.Scheme == "fairy-character" {
		prefix := "/" + packID + "/"
		if parsed.Host != "localhost" || parsed.RawQuery != "" || parsed.Fragment != "" || !strings.HasPrefix(parsed.Path, prefix) {
			return "", errors.New("visual image path does not match pack")
		}
		return strings.TrimPrefix(parsed.Path, prefix), nil
	}
	if parsed.Scheme != "" || strings.TrimSpace(imagePath) == "" {
		return "", errors.New("visual image path is invalid")
	}
	return imagePath, nil
}

func (c *visualCache) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	c.mu.RLock()
	route := strings.TrimPrefix(r.URL.Path, "/characters")
	asset, ok := c.assets[route]
	c.mu.RUnlock()
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	http.ServeFile(w, r, asset)
}

func (c *visualCache) Close() error {
	if c == nil {
		return nil
	}
	// The visual cache root is shared by sequential session instances. It is
	// intentionally retained so reconnecting (including React StrictMode's
	// development remount) cannot remove a newer instance's character assets.
	c.mu.Lock()
	c.assets = make(map[string]string)
	c.mu.Unlock()
	return nil
}

func validateVisualManifest(visual coreclient.VisualManifest) error {
	if visual.PackID == "" {
		return errors.New("active character visual pack ID is required")
	}
	if len(visual.States) == 0 {
		return errors.New("active character visual pack has no states")
	}
	seen := make(map[string]struct{}, len(visual.States))
	hasIdle := false
	for _, state := range visual.States {
		if state.ID == "" || state.ImagePath == "" || !strings.HasSuffix(state.ImagePath, ".png") {
			return errors.New("active character visual state is invalid")
		}
		if _, duplicate := seen[state.ID]; duplicate {
			return fmt.Errorf("active character visual state %q is duplicated", state.ID)
		}
		seen[state.ID] = struct{}{}
		hasIdle = hasIdle || state.ID == "idle"
	}
	if !hasIdle {
		return errors.New("active character visual pack is missing idle")
	}
	return nil
}

func visualCacheKey(visual coreclient.VisualManifest) (string, error) {
	raw, err := json.Marshal(visual)
	if err != nil {
		return "", fmt.Errorf("encoding visual manifest cache key: %w", err)
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}

func visualStateFilename(stateID string) string {
	digest := sha256.Sum256([]byte(stateID))
	return hex.EncodeToString(digest[:]) + ".png"
}

func moveVisualAsset(image []byte, target string) error {
	temporary, err := os.CreateTemp("", "fairy-visual-*.png")
	if err != nil {
		return fmt.Errorf("creating temporary visual asset: %w", err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if _, err := temporary.Write(image); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("writing temporary visual asset: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("closing temporary visual asset: %w", err)
	}
	flow := fileflow.Flow{DirMode: 0o700}
	if _, err := flow.Move(temporaryName, target); err != nil {
		return err
	}
	return nil
}
