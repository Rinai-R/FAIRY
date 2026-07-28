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
const maxLiveStickerAssets = 8

type cachedStickerAsset struct {
	mimeType string
	content  []byte
}

type visualCache struct {
	root         string
	mu           sync.RWMutex
	assets       map[string]string
	stickers     map[string]cachedStickerAsset
	stickerOrder []string
	activeKey    string
	visualClosed bool
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
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("normalizing visual cache root: %w", err)
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("creating visual cache root: %w", err)
	}
	return &visualCache{
		root:     root,
		assets:   make(map[string]string),
		stickers: make(map[string]cachedStickerAsset),
	}, nil
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
	c.mu.RLock()
	closed := c.visualClosed
	c.mu.RUnlock()
	if closed {
		return coreclient.VisualManifest{}, errors.New("visual cache is closed")
	}
	if err := beginVisualCacheSync(c.root, key); err != nil {
		return coreclient.VisualManifest{}, err
	}
	abortSync := func(cause error) error {
		return errors.Join(cause, abortVisualCacheSync(c.root, key))
	}
	local := visual
	localStates := make([]coreclient.VisualState, 0, len(visual.States))
	stagedAssets := make(map[string]string, len(visual.States))
	var total int

	for _, state := range visual.States {
		assetPath, err := visualAssetPath(visual.PackID, state.ImagePath)
		if err != nil {
			return coreclient.VisualManifest{}, abortSync(fmt.Errorf("normalizing %q state image: %w", state.ID, err))
		}
		image, err := client.VisualAsset(ctx, visual.PackID, assetPath)
		if err != nil {
			return coreclient.VisualManifest{}, abortSync(fmt.Errorf("downloading %q state image: %w", state.ID, err))
		}
		if len(image) < 8 || string(image[:8]) != "\x89PNG\r\n\x1a\n" {
			return coreclient.VisualManifest{}, abortSync(fmt.Errorf("downloading %q state image: response is not a PNG", state.ID))
		}
		total += len(image)
		if total > maxVisualPackBytes {
			return coreclient.VisualManifest{}, abortSync(fmt.Errorf("visual pack exceeds %d bytes", maxVisualPackBytes))
		}
		filename := visualStateFilename(state.ID)
		target := pathologize.Join(c.root, key, filename)
		finalPath, err := moveVisualAsset(image, target)
		if err != nil {
			return coreclient.VisualManifest{}, abortSync(fmt.Errorf("caching %q state image: %w", state.ID, err))
		}
		route := "/" + key + "/" + filename
		stagedAssets[route] = finalPath
		state.ImagePath = "/characters" + route
		localStates = append(localStates, state)
	}
	local.States = localStates

	committed, err := commitVisualCacheSync(c, key, stagedAssets)
	if !committed {
		return coreclient.VisualManifest{}, abortSync(err)
	}
	if err != nil {
		return coreclient.VisualManifest{}, err
	}
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
	if strings.HasPrefix(r.URL.Path, "/characters/stickers/") {
		c.serveSticker(w, r)
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

func (c *visualCache) PutSticker(beatID string, content coreclient.StickerContent) (string, error) {
	if c == nil || strings.TrimSpace(beatID) == "" {
		return "", errors.New("sticker beat ID is required")
	}
	if len(content.Bytes) == 0 || len(content.Bytes) > coreclient.MaxStickerContentBytes {
		return "", errors.New("sticker content size is invalid")
	}
	if !desktopStickerMIME(content.MIMEType) {
		return "", errors.New("sticker MIME is unsupported")
	}
	digest := sha256.New()
	_, _ = digest.Write([]byte(beatID))
	_, _ = digest.Write([]byte{0})
	_, _ = digest.Write(content.Bytes)
	routeKey := hex.EncodeToString(digest.Sum(nil))
	route := "/characters/stickers/" + routeKey

	c.mu.Lock()
	if _, exists := c.stickers[routeKey]; !exists {
		c.stickerOrder = append(c.stickerOrder, routeKey)
	}
	c.stickers[routeKey] = cachedStickerAsset{
		mimeType: content.MIMEType,
		content:  append([]byte(nil), content.Bytes...),
	}
	for len(c.stickerOrder) > maxLiveStickerAssets {
		evicted := c.stickerOrder[0]
		c.stickerOrder = c.stickerOrder[1:]
		delete(c.stickers, evicted)
	}
	c.mu.Unlock()
	return route, nil
}

func (c *visualCache) serveSticker(w http.ResponseWriter, r *http.Request) {
	routeKey := strings.TrimPrefix(r.URL.Path, "/characters/stickers/")
	if routeKey == "" || strings.Contains(routeKey, "/") {
		http.NotFound(w, r)
		return
	}
	c.mu.RLock()
	asset, ok := c.stickers[routeKey]
	if ok {
		asset.content = append([]byte(nil), asset.content...)
	}
	c.mu.RUnlock()
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", asset.mimeType)
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Length", fmt.Sprint(len(asset.content)))
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	_, _ = w.Write(asset.content)
}

func desktopStickerMIME(value string) bool {
	switch value {
	case "image/jpeg", "image/png", "image/gif", "image/webp":
		return true
	default:
		return false
	}
}

func (c *visualCache) Close() error {
	if c == nil {
		return nil
	}
	visualCacheRoots.Lock()
	c.mu.Lock()
	if c.visualClosed {
		c.mu.Unlock()
		visualCacheRoots.Unlock()
		return nil
	}
	c.visualClosed = true
	state := visualCacheRootStateLocked(c.root)
	removeVisualCacheActiveLocked(state, c.activeKey, c.assets)
	c.assets = make(map[string]string)
	c.stickers = make(map[string]cachedStickerAsset)
	c.stickerOrder = nil
	c.activeKey = ""
	c.mu.Unlock()
	err := pruneVisualCacheRootLocked(c.root, state)
	deleteVisualCacheRootStateIfIdleLocked(c.root, state)
	visualCacheRoots.Unlock()
	return err
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

func moveVisualAsset(image []byte, target string) (string, error) {
	temporary, err := os.CreateTemp("", "fairy-visual-*.png")
	if err != nil {
		return "", fmt.Errorf("creating temporary visual asset: %w", err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if _, err := temporary.Write(image); err != nil {
		_ = temporary.Close()
		return "", fmt.Errorf("writing temporary visual asset: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return "", fmt.Errorf("closing temporary visual asset: %w", err)
	}
	flow := fileflow.Flow{DirMode: 0o700}
	return flow.Move(temporaryName, target)
}
