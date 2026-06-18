package desktop

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/Rinai-R/FAIRY/internal/app/bootstrap"
)

func TestRuntimeAssetHandlerServesAudio(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "voice.mp3"), []byte("mp3"), 0o644); err != nil {
		t.Fatalf("write audio fixture: %v", err)
	}

	handler := runtimeAssetHandler(bootstrap.Config{AudioDir: dir})
	req := httptest.NewRequest(http.MethodGet, "/audio/voice.mp3", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Body.String(); got != "mp3" {
		t.Fatalf("body = %q, want %q", got, "mp3")
	}
}

func TestRuntimeAssetHandlerRejectsUnknownPath(t *testing.T) {
	handler := runtimeAssetHandler(bootstrap.Config{AudioDir: t.TempDir()})
	req := httptest.NewRequest(http.MethodGet, "/unknown/file.txt", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}
