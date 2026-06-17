package runtime_test

import (
	"context"
	"encoding/base64"
	"os"
	"strings"
	"testing"

	"github.com/Rinai-R/FAIRY/internal/app"
	"github.com/Rinai-R/FAIRY/internal/runtime"
)

func TestStoreDocumentAssetWritesUploadedFile(t *testing.T) {
	rt := runtime.NewRuntime(runtime.Dependencies{MaterialDir: t.TempDir()})
	body := []byte("image-or-handwritten-note")

	asset, err := rt.StoreDocumentAsset(context.Background(), app.DocumentUploadRequest{
		Filename:    "../手写 笔记.png",
		ContentType: "image/png",
		DataBase64:  base64.StdEncoding.EncodeToString(body),
	})
	if err != nil {
		t.Fatalf("StoreDocumentAsset() error = %v", err)
	}
	if strings.Contains(asset.Filename, "..") || strings.Contains(asset.Filename, "/") {
		t.Fatalf("Filename was not sanitized: %q", asset.Filename)
	}
	if asset.ContentType != "image/png" {
		t.Fatalf("ContentType = %q", asset.ContentType)
	}
	raw, err := os.ReadFile(asset.Path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(raw) != string(body) {
		t.Fatalf("stored body = %q", raw)
	}
}

func TestStoreDocumentAssetRejectsMissingContent(t *testing.T) {
	rt := runtime.NewRuntime(runtime.Dependencies{MaterialDir: t.TempDir()})
	_, err := rt.StoreDocumentAsset(context.Background(), app.DocumentUploadRequest{
		Filename: "lesson.png",
	})
	if err == nil {
		t.Fatal("StoreDocumentAsset() error = nil")
	}
	if !strings.Contains(err.Error(), "data_base64 不能为空") {
		t.Fatalf("error = %q", err)
	}
}
