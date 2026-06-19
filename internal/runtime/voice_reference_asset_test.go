package runtime_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Rinai-R/FAIRY/internal/app"
	"github.com/Rinai-R/FAIRY/internal/runtime"
)

func TestStoreVoiceReferenceAudioBytesWritesAudioFile(t *testing.T) {
	materialDir := t.TempDir()
	rt := runtime.NewRuntime(runtime.Dependencies{MaterialDir: materialDir})
	body := []byte("riff-wav-placeholder")

	asset, err := rt.StoreVoiceReferenceAudioBytes("voice ref.wav", "audio/wav", body)
	if err != nil {
		t.Fatalf("StoreVoiceReferenceAudioBytes() error = %v", err)
	}
	if asset.ContentType != "audio/wav" {
		t.Fatalf("ContentType = %q", asset.ContentType)
	}
	if !strings.HasPrefix(asset.Path, filepath.Join(materialDir, runtime.DefaultVoiceReferenceDirName)) {
		t.Fatalf("Path = %q, want voice reference directory under %q", asset.Path, materialDir)
	}
	raw, err := os.ReadFile(asset.Path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(raw) != string(body) {
		t.Fatalf("stored body = %q", raw)
	}
}

func TestStoreVoiceReferenceAudioBytesRejectsNonAudioFile(t *testing.T) {
	rt := runtime.NewRuntime(runtime.Dependencies{MaterialDir: t.TempDir()})
	_, err := rt.StoreVoiceReferenceAudioBytes("notes.txt", "text/plain", []byte("not audio"))
	if err == nil {
		t.Fatal("StoreVoiceReferenceAudioBytes() error = nil")
	}
	if !strings.Contains(err.Error(), "参考音频必须是音频文件") {
		t.Fatalf("error = %q", err)
	}
}

func TestStoreVoiceReferenceAudioRejectsMissingBase64(t *testing.T) {
	rt := runtime.NewRuntime(runtime.Dependencies{MaterialDir: t.TempDir()})
	_, err := rt.StoreVoiceReferenceAudio(context.Background(), app.DocumentUploadRequest{Filename: "voice.wav"})
	if err == nil {
		t.Fatal("StoreVoiceReferenceAudio() error = nil")
	}
	if !strings.Contains(err.Error(), "data_base64 不能为空") {
		t.Fatalf("error = %q", err)
	}
}
