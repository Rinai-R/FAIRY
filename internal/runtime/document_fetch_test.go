package runtime_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Rinai-R/FAIRY/internal/app"
	"github.com/Rinai-R/FAIRY/internal/runtime"
)

func TestFetchDocumentDownloadsHTTPDocument(t *testing.T) {
	rt := runtime.NewRuntime(runtime.Dependencies{
		DocumentHTTP: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.String() != "https://example.com/doc" {
				t.Fatalf("request URL = %s", req.URL.String())
			}
			return &http.Response{
				StatusCode:    http.StatusOK,
				Status:        "200 OK",
				Header:        http.Header{"Content-Type": {"text/markdown; charset=utf-8"}, "Content-Disposition": {`attachment; filename="lesson.md"`}},
				Body:          io.NopCloser(bytes.NewBufferString("# 课程\n注意力机制用于关注重要信息。")),
				ContentLength: int64(len("# 课程\n注意力机制用于关注重要信息。")),
				Request:       req,
			}, nil
		})},
	})
	resp, err := rt.FetchDocument(context.Background(), app.DocumentFetchRequest{URL: "https://example.com/doc"})
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

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
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
