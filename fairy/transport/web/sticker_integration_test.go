//go:build integration

package web_test

import (
	"encoding/base64"
	"errors"
	"testing"

	fairycore "fairy/app/core"
	coreclient "fairy/transport/session"

	"go.uber.org/zap"
)

func TestProductionStickerManagementLifecycle(t *testing.T) {
	databaseURL, cleanup := isolatedAPISchema(t)
	defer cleanup()
	masterKey := base64.StdEncoding.EncodeToString([]byte("abcdef0123456789abcdef0123456789"))
	setAPIProductionEnv(t, databaseURL, masterKey)

	rt, err := fairycore.Open(fairycore.RuntimeOptions{ConfigRoot: t.TempDir(), Logger: zap.NewNop()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rt.Close() })
	baseURL, token := startProductionAPIServer(t, rt)
	client, err := coreclient.New(coreclient.Options{Endpoint: baseURL, Token: token})
	if err != nil {
		t.Fatal(err)
	}
	png := append([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, []byte("api")...)
	record, err := client.CreateSticker(t.Context(), coreclient.CreateStickerInput{
		Filename: "manual.png", MIMEType: "image/png", Content: png,
		Description: "震惊和无语", Tags: []string{"震惊", "无语"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if record.Status != "draft" || record.Description != "震惊和无语" {
		t.Fatalf("created sticker = %#v", record)
	}
	if _, err := client.CreateSticker(t.Context(), coreclient.CreateStickerInput{
		Filename: "duplicate.png", MIMEType: "image/png", Content: png,
	}); err == nil {
		t.Fatal("duplicate sticker upload succeeded")
	} else {
		var clientErr *coreclient.Error
		if !errors.As(err, &clientErr) || clientErr.Status != 409 {
			t.Fatalf("duplicate error = %v", err)
		}
	}
	active := "active"
	record, err = client.UpdateSticker(t.Context(), record.ID, coreclient.UpdateStickerInput{Status: &active})
	if err != nil || record.Status != active {
		t.Fatalf("UpdateSticker(active) = %#v, %v", record, err)
	}
	page, err := client.ListStickers(t.Context(), "active", 0, 10)
	if err != nil || page.Total != 1 || len(page.Items) != 1 || page.Items[0].ID != record.ID {
		t.Fatalf("ListStickers(active) = %#v, %v", page, err)
	}
	content, err := client.ReadStickerContent(t.Context(), record.ID)
	if err != nil || content.MIMEType != "image/png" || string(content.Bytes) != string(png) {
		t.Fatalf("ReadStickerContent() = %#v, %v", content, err)
	}
	if err := client.DeleteSticker(t.Context(), record.ID); err != nil {
		t.Fatal(err)
	}
}
