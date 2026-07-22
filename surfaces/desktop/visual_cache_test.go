package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"fairy/coreclient"
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
