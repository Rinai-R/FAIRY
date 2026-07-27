package coreclient

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestStickerClientLifecycle(t *testing.T) {
	png := append([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, []byte("client")...)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/stickers":
			if err := r.ParseMultipartForm(MaxStickerContentBytes + (1 << 20)); err != nil {
				t.Fatal(err)
			}
			file, header, err := r.FormFile("file")
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			content, err := io.ReadAll(file)
			if err != nil {
				t.Fatal(err)
			}
			if string(content) != string(png) || header.Header.Get("Content-Type") != "image/png" {
				t.Fatalf("upload content=%x content-type=%q", content, header.Header.Get("Content-Type"))
			}
			if r.FormValue("description") != "震惊" || r.FormValue("tags") != `["无语"]` || r.FormValue("status") != "draft" {
				t.Fatalf("multipart fields = %#v", r.MultipartForm.Value)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"s1","contentSha256":"abc","mimeType":"image/png","byteCount":14,"description":"震惊","tags":["无语"],"status":"draft","createdAtUnixMs":1,"updatedAtUnixMs":1}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/stickers":
			if r.URL.Query().Get("status") != "active" || r.URL.Query().Get("limit") != "10" {
				t.Fatalf("list query = %s", r.URL.RawQuery)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"items":[],"offset":0,"limit":10,"total":0}`))
		case r.Method == http.MethodPut && r.URL.Path == "/v1/stickers/s1":
			var body UpdateStickerInput
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body.Status == nil || *body.Status != "active" {
				t.Fatalf("update body = %#v", body)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"s1","contentSha256":"abc","mimeType":"image/png","byteCount":14,"description":"震惊","tags":["无语"],"status":"active","createdAtUnixMs":1,"updatedAtUnixMs":2}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/stickers/s1/content":
			w.Header().Set("Content-Type", "image/png")
			w.Header().Set("X-Content-SHA256", "abc")
			_, _ = w.Write(png)
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/stickers/s1":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"deleted":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client, err := New(Options{Endpoint: server.URL, Token: "test-token"})
	if err != nil {
		t.Fatal(err)
	}
	record, err := client.CreateSticker(t.Context(), CreateStickerInput{
		Filename: "sticker.png", MIMEType: "image/png", Content: png,
		Description: "震惊", Tags: []string{"无语"}, Status: "draft",
	})
	if err != nil || record.ID != "s1" {
		t.Fatalf("CreateSticker() = %#v, %v", record, err)
	}
	page, err := client.ListStickers(t.Context(), "active", 0, 10)
	if err != nil || page.Items == nil {
		t.Fatalf("ListStickers() = %#v, %v", page, err)
	}
	active := "active"
	record, err = client.UpdateSticker(t.Context(), "s1", UpdateStickerInput{Status: &active})
	if err != nil || record.Status != active {
		t.Fatalf("UpdateSticker() = %#v, %v", record, err)
	}
	content, err := client.ReadStickerContent(t.Context(), "s1")
	if err != nil || content.ContentSHA256 != "abc" || string(content.Bytes) != string(png) {
		t.Fatalf("ReadStickerContent() = %#v, %v", content, err)
	}
	if err := client.DeleteSticker(t.Context(), "s1"); err != nil {
		t.Fatal(err)
	}
}

func TestCreateStickerRejectsOversizeBeforeRequest(t *testing.T) {
	client, err := New(Options{Endpoint: "http://127.0.0.1:1"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.CreateSticker(t.Context(), CreateStickerInput{Content: make([]byte, MaxStickerContentBytes+1)})
	if err == nil {
		t.Fatal("CreateSticker(oversize) succeeded")
	}
}
