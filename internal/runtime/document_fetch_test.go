package runtime_test

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Rinai-R/FAIRY/internal/app"
	"github.com/Rinai-R/FAIRY/internal/runtime"
)

func TestFetchDocumentDownloadsHTTPDocument(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="lesson.md"`)
		_, _ = w.Write([]byte("# 课程\n注意力机制用于关注重要信息。"))
	}))
	defer server.Close()

	rt := runtime.NewRuntime(runtime.Dependencies{})
	resp, err := rt.FetchDocument(context.Background(), app.DocumentFetchRequest{URL: server.URL + "/doc"})
	if err != nil {
		t.Fatalf("FetchDocument() error = %v", err)
	}
	if resp.Filename != "lesson.md" {
		t.Fatalf("Filename = %q, want lesson.md", resp.Filename)
	}
	if !strings.HasPrefix(resp.ContentType, "text/markdown") {
		t.Fatalf("ContentType = %q", resp.ContentType)
	}
	raw, err := base64.StdEncoding.DecodeString(resp.DataBase64)
	if err != nil {
		t.Fatalf("DecodeString() error = %v", err)
	}
	if string(raw) != "# 课程\n注意力机制用于关注重要信息。" {
		t.Fatalf("document body = %q", raw)
	}
}

func TestFetchDocumentRejectsUnsupportedScheme(t *testing.T) {
	rt := runtime.NewRuntime(runtime.Dependencies{})
	_, err := rt.FetchDocument(context.Background(), app.DocumentFetchRequest{URL: "file:///etc/passwd"})
	if err == nil {
		t.Fatal("FetchDocument() error = nil, want scheme error")
	}
	if !strings.Contains(err.Error(), "仅支持 http/https") {
		t.Fatalf("error = %q", err)
	}
}
