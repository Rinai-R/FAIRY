package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	coreclient "fairy/transport/session"
)

var testPNG = []byte("\x89PNG\r\n\x1a\nvisual")

func TestVisualCacheSyncPublishesOnlyLocalAssets(t *testing.T) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/visual-assets/fairy.atri/images/idle.png" && r.URL.Path != "/v1/visual-assets/fairy.atri/images/talk.png" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(testPNG)
	}))
	defer server.Close()
	client, err := coreclient.New(coreclient.Options{Endpoint: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	cache, err := newVisualCacheAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	visual, err := cache.Sync(context.Background(), client, testVisualManifest())
	if err != nil {
		t.Fatal(err)
	}
	if got := visual.States[0].ImagePath; got == "images/idle.png" || got[:12] != "/characters/" {
		t.Fatalf("local image path = %q", got)
	}
	request := httptest.NewRequest(http.MethodGet, visual.States[0].ImagePath, nil)
	response := httptest.NewRecorder()
	cache.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("cached image status = %d, body = %s", response.Code, response.Body.String())
	}
	if got := response.Body.Bytes(); string(got) != string(testPNG) {
		t.Fatalf("cached image = %q", got)
	}
}

func TestVisualCacheSyncRejectsManifestWithoutIdle(t *testing.T) {
	cache, err := newVisualCacheAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	client, err := coreclient.New(coreclient.Options{Endpoint: "http://127.0.0.1:8787"})
	if err != nil {
		t.Fatal(err)
	}
	visual := testVisualManifest()
	visual.States = visual.States[1:]
	if _, err := cache.Sync(context.Background(), client, visual); err == nil {
		t.Fatal("Sync() error = nil, want missing idle error")
	}
}

func TestVisualCacheSyncAcceptsCoreCharacterURI(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/visual-assets/fairy.atri/idle.png" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(testPNG)
	}))
	defer server.Close()
	client, err := coreclient.New(coreclient.Options{Endpoint: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	cache, err := newVisualCacheAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	visual := testVisualManifest()
	visual.States = visual.States[:1]
	visual.States[0].ImagePath = "fairy-character://localhost/fairy.atri/idle.png"
	if _, err := cache.Sync(context.Background(), client, visual); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
}

func TestClosingPreviousVisualCacheKeepsSharedCachedAssets(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(testPNG)
	}))
	defer server.Close()
	client, err := coreclient.New(coreclient.Options{Endpoint: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	previous, err := newVisualCacheAt(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := previous.Sync(context.Background(), client, testVisualManifest()); err != nil {
		t.Fatal(err)
	}
	current, err := newVisualCacheAt(root)
	if err != nil {
		t.Fatal(err)
	}
	visual, err := current.Sync(context.Background(), client, testVisualManifest())
	if err != nil {
		t.Fatal(err)
	}
	if err := previous.Close(); err != nil {
		t.Fatal(err)
	}
	assetRoute := visual.States[0].ImagePath[len("/characters/"):]
	if _, err := os.Stat(filepath.Join(root, assetRoute)); err != nil {
		t.Fatalf("current cache asset disappeared after closing previous cache: %v", err)
	}
}

func TestVisualCacheServesBoundedInMemorySticker(t *testing.T) {
	cache, err := newVisualCacheAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	content := coreclient.StickerContent{MIMEType: "image/gif", Bytes: []byte("GIF89a-content")}
	route, err := cache.PutSticker("beat-1", content)
	if err != nil {
		t.Fatal(err)
	}
	if route == "" || route[:21] != "/characters/stickers/" {
		t.Fatalf("sticker route = %q", route)
	}
	content.Bytes[0] = 'X'
	request := httptest.NewRequest(http.MethodGet, route, nil)
	response := httptest.NewRecorder()
	cache.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("sticker status = %d, body = %s", response.Code, response.Body.String())
	}
	if response.Header().Get("Content-Type") != "image/gif" ||
		response.Header().Get("Cache-Control") != "private, no-store" ||
		response.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("sticker headers = %#v", response.Header())
	}
	if got := response.Body.String(); got != "GIF89a-content" {
		t.Fatalf("sticker content = %q", got)
	}
}

func TestVisualCacheRejectsUnsupportedStickerAndEvictsOldest(t *testing.T) {
	cache, err := newVisualCacheAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cache.PutSticker("bad", coreclient.StickerContent{MIMEType: "image/svg+xml", Bytes: []byte("<svg/>")}); err == nil {
		t.Fatal("unsupported sticker MIME accepted")
	}
	var firstRoute string
	for index := 0; index <= maxLiveStickerAssets; index++ {
		route, err := cache.PutSticker(
			string(rune('a'+index)),
			coreclient.StickerContent{MIMEType: "image/png", Bytes: append([]byte(nil), testPNG...)},
		)
		if err != nil {
			t.Fatal(err)
		}
		if index == 0 {
			firstRoute = route
		}
	}
	response := httptest.NewRecorder()
	cache.ServeHTTP(response, httptest.NewRequest(http.MethodGet, firstRoute, nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("evicted sticker status = %d, want 404", response.Code)
	}
}

func TestVisualCacheBoundsGeneratedPackDirectoriesAndPreservesUnknownObjects(t *testing.T) {
	root := t.TempDir()
	for index := 0; index < 6; index++ {
		key := fmt.Sprintf("%064x", index+1)
		path := filepath.Join(root, key)
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
		timestamp := time.Unix(int64(index+1), 0)
		if err := os.Chtimes(path, timestamp, timestamp); err != nil {
			t.Fatal(err)
		}
	}
	unknownDir := filepath.Join(root, "operator-files")
	if err := os.Mkdir(unknownDir, 0o700); err != nil {
		t.Fatal(err)
	}
	unknownFile := filepath.Join(root, strings.Repeat("a", 64))
	if err := os.WriteFile(unknownFile, []byte("keep-file"), 0o600); err != nil {
		t.Fatal(err)
	}
	unknownSymlink := filepath.Join(root, strings.Repeat("b", 64))
	if err := os.Symlink(unknownDir, unknownSymlink); err != nil {
		t.Fatal(err)
	}

	server := visualAssetServer(t, func() []byte { return append([]byte(nil), testPNG...) })
	defer server.Close()
	client, err := coreclient.New(coreclient.Options{Endpoint: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	cache, err := newVisualCacheAt(root)
	if err != nil {
		t.Fatal(err)
	}
	visual, err := cache.Sync(t.Context(), client, visualManifestVariant(100))
	if err != nil {
		t.Fatal(err)
	}
	if got := len(generatedVisualCacheDirectories(t, root)); got > maxVisualCachePackDirectories {
		t.Fatalf("generated visual cache directories = %d, want <= %d", got, maxVisualCachePackDirectories)
	}
	response := httptest.NewRecorder()
	cache.ServeHTTP(response, httptest.NewRequest(http.MethodGet, visual.States[0].ImagePath, nil))
	if response.Code != http.StatusOK {
		t.Fatalf("current cached asset status = %d", response.Code)
	}
	if _, err := os.Stat(unknownDir); err != nil {
		t.Fatalf("unknown directory was removed: %v", err)
	}
	if content, err := os.ReadFile(unknownFile); err != nil || string(content) != "keep-file" {
		t.Fatalf("unknown file content = %q, err=%v", content, err)
	}
	if info, err := os.Lstat(unknownSymlink); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("unknown symlink changed: info=%v err=%v", info, err)
	}
	if err := cache.Close(); err != nil {
		t.Fatal(err)
	}
	assertVisualCacheRootStateReleased(t, root)
}

func TestVisualCacheProtectsActiveKeysAndRecoversProtectedCapacity(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(testPNG)
	}))
	defer server.Close()
	client, err := coreclient.New(coreclient.Options{Endpoint: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	caches := make([]*visualCache, maxVisualCachePackDirectories)
	manifests := make([]coreclient.VisualManifest, maxVisualCachePackDirectories)
	for index := range caches {
		cache, err := newVisualCacheAt(root)
		if err != nil {
			t.Fatal(err)
		}
		manifest := visualManifestVariant(index)
		if _, err := cache.Sync(t.Context(), client, manifest); err != nil {
			t.Fatal(err)
		}
		caches[index] = cache
		manifests[index] = manifest
	}
	beforeRejected := requests.Load()
	rejected, err := newVisualCacheAt(root)
	if err != nil {
		t.Fatal(err)
	}
	fifthManifest := visualManifestVariant(maxVisualCachePackDirectories)
	if _, err := rejected.Sync(t.Context(), client, fifthManifest); !errors.Is(err, ErrVisualCacheCapacity) {
		t.Fatalf("fifth protected key error = %v, want ErrVisualCacheCapacity", err)
	}
	if got := requests.Load(); got != beforeRejected {
		t.Fatalf("capacity rejection performed %d HTTP requests", got-beforeRejected)
	}
	for _, manifest := range manifests {
		key, err := visualCacheKey(manifest)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(root, key)); err != nil {
			t.Fatalf("protected key %s was removed: %v", key, err)
		}
	}

	releasedKey, err := visualCacheKey(manifests[0])
	if err != nil {
		t.Fatal(err)
	}
	old := time.Unix(1, 0)
	if err := os.Chtimes(filepath.Join(root, releasedKey), old, old); err != nil {
		t.Fatal(err)
	}
	if err := caches[0].Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := rejected.Sync(t.Context(), client, fifthManifest); err != nil {
		t.Fatalf("Sync after active lease release error = %v", err)
	}
	if got := len(generatedVisualCacheDirectories(t, root)); got != maxVisualCachePackDirectories {
		t.Fatalf("generated directories after recovery = %d, want %d", got, maxVisualCachePackDirectories)
	}
	if _, err := os.Stat(filepath.Join(root, releasedKey)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("released oldest key still exists, err=%v", err)
	}
	for index := 1; index < len(caches); index++ {
		key, err := visualCacheKey(manifests[index])
		if err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(root, key)); err != nil {
			t.Fatalf("still-active key %s was removed: %v", key, err)
		}
	}
	for index := 1; index < len(caches); index++ {
		if err := caches[index].Close(); err != nil {
			t.Fatal(err)
		}
	}
	if err := rejected.Close(); err != nil {
		t.Fatal(err)
	}
	assertVisualCacheRootStateReleased(t, root)
}

func TestVisualCacheSyncFailureRemovesUnleasedPartialDirectory(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) == 1 {
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(testPNG)
			return
		}
		http.Error(w, "fixture failure", http.StatusBadGateway)
	}))
	defer server.Close()
	client, err := coreclient.New(coreclient.Options{Endpoint: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	cache, err := newVisualCacheAt(root)
	if err != nil {
		t.Fatal(err)
	}
	manifest := visualManifestVariant(200)
	key, err := visualCacheKey(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cache.Sync(t.Context(), client, manifest); err == nil {
		t.Fatal("partial Sync error = nil")
	}
	if _, err := os.Stat(filepath.Join(root, key)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed Sync retained partial key, err=%v", err)
	}
	if err := cache.Close(); err != nil {
		t.Fatal(err)
	}
	assertVisualCacheRootStateReleased(t, root)
}

func TestVisualCacheUsesFileflowFinalPathForConflictingContent(t *testing.T) {
	var current atomic.Value
	oldPNG := append(append([]byte(nil), testPNG...), []byte("-old")...)
	newPNG := append(append([]byte(nil), testPNG...), []byte("-new")...)
	current.Store(oldPNG)
	server := visualAssetServer(t, func() []byte {
		return append([]byte(nil), current.Load().([]byte)...)
	})
	defer server.Close()
	client, err := coreclient.New(coreclient.Options{Endpoint: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	oldCache, err := newVisualCacheAt(root)
	if err != nil {
		t.Fatal(err)
	}
	manifest := visualManifestVariant(300)
	oldVisual, err := oldCache.Sync(t.Context(), client, manifest)
	if err != nil {
		t.Fatal(err)
	}
	current.Store(newPNG)
	newCache, err := newVisualCacheAt(root)
	if err != nil {
		t.Fatal(err)
	}
	newVisual, err := newCache.Sync(t.Context(), client, manifest)
	if err != nil {
		t.Fatal(err)
	}
	assertVisualCacheContent(t, oldCache, oldVisual.States[0].ImagePath, oldPNG)
	assertVisualCacheContent(t, newCache, newVisual.States[0].ImagePath, newPNG)
	if err := oldCache.Close(); err != nil {
		t.Fatal(err)
	}
	assertVisualCacheContent(t, newCache, newVisual.States[0].ImagePath, newPNG)
	if err := newCache.Close(); err != nil {
		t.Fatal(err)
	}
	assertVisualCacheRootStateReleased(t, root)
}

func TestVisualCacheCloseDuringSyncDoesNotReactivateOrRetainPartialKey(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var once atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if once.CompareAndSwap(false, true) {
			close(started)
			<-release
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(testPNG)
	}))
	defer server.Close()
	client, err := coreclient.New(coreclient.Options{Endpoint: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	cache, err := newVisualCacheAt(root)
	if err != nil {
		t.Fatal(err)
	}
	manifest := visualManifestVariant(400)
	key, err := visualCacheKey(manifest)
	if err != nil {
		t.Fatal(err)
	}
	syncResult := make(chan error, 1)
	go func() {
		_, syncErr := cache.Sync(t.Context(), client, manifest)
		syncResult <- syncErr
	}()
	<-started
	if err := cache.Close(); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-syncResult; err == nil || !strings.Contains(err.Error(), "visual cache is closed") {
		t.Fatalf("Sync/Close race error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, key)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Sync/Close race retained partial key, err=%v", err)
	}
	assertVisualCacheRootStateReleased(t, root)
}

func testVisualManifest() coreclient.VisualManifest {
	return coreclient.VisualManifest{
		SchemaVersion: 2,
		PackID:        "fairy.atri",
		Renderer:      "state_images",
		Frame:         coreclient.VisualFrame{Width: 128, Height: 192},
		Scale:         1,
		Anchor:        coreclient.VisualAnchor{X: 64, Y: 190},
		States: []coreclient.VisualState{
			{ID: "idle", ImagePath: "images/idle.png"},
			{ID: "talk", ImagePath: "images/talk.png"},
		},
	}
}

func visualManifestVariant(index int) coreclient.VisualManifest {
	manifest := testVisualManifest()
	manifest.DisplayName = fmt.Sprintf("fixture-%d", index)
	return manifest
}

func visualAssetServer(t *testing.T, content func() []byte) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(content())
	}))
}

func generatedVisualCacheDirectories(t *testing.T, root string) []string {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	var result []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() && len(name) == 64 && strings.Trim(name, "0123456789abcdef") == "" {
			result = append(result, name)
		}
	}
	return result
}

func assertVisualCacheContent(t *testing.T, cache *visualCache, route string, want []byte) {
	t.Helper()
	response := httptest.NewRecorder()
	cache.ServeHTTP(response, httptest.NewRequest(http.MethodGet, route, nil))
	if response.Code != http.StatusOK {
		t.Fatalf("visual cache status = %d, body=%q", response.Code, response.Body.String())
	}
	if got := response.Body.Bytes(); string(got) != string(want) {
		t.Fatalf("visual cache content = %q, want %q", got, want)
	}
}

func assertVisualCacheRootStateReleased(t *testing.T, root string) {
	t.Helper()
	absolute, err := filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	visualCacheRoots.Lock()
	_, retained := visualCacheRoots.roots[absolute]
	visualCacheRoots.Unlock()
	if retained {
		t.Fatalf("visual cache root state %q was retained", absolute)
	}
}
