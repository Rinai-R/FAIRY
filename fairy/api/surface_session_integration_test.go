//go:build integration

package api_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fairy/character"
	fairyruntime "fairy/runtime"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
)

func TestSurfaceSessionAPIIsolatesKeysAndRequiresIMKeyIntegration(t *testing.T) {
	root := t.TempDir()
	writeSurfaceVisualManifest(t, root)
	characterService := character.NewCharacterService(root)
	record, err := characterService.CreateCharacter(character.Brief{Name: "Surface", Description: "Integration character", TextLanguage: "zh", SpeakingLanguage: "zh"}, "fairy.surface")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := characterService.ActivateCharacter(record.CharacterID, record.Revision); err != nil {
		t.Fatal(err)
	}
	databaseURL, cleanup := isolatedAPISchema(t)
	defer cleanup()
	qdrantURL := apiTestQdrantURL()
	ensureAPIQdrantCollection(t, qdrantURL)
	setAPIProductionEnv(t, databaseURL, qdrantURL, base64.StdEncoding.EncodeToString([]byte("abcdef0123456789abcdef0123456789")))
	rt, err := fairyruntime.Open(fairyruntime.Options{ConfigRoot: root, Logger: zap.NewNop()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rt.Close() })
	baseURL := startProductionAPIServer(t, rt)
	assetResponse, err := http.Get(baseURL + "/v1/visual-assets/fairy.surface/idle.png")
	if err != nil {
		t.Fatal(err)
	}
	assetBytes, err := io.ReadAll(assetResponse.Body)
	assetResponse.Body.Close()
	if err != nil || assetResponse.StatusCode != http.StatusOK || assetResponse.Header.Get("Content-Type") != "image/png" || string(assetBytes) != "png" {
		t.Fatalf("asset status=%d type=%q body=%q err=%v", assetResponse.StatusCode, assetResponse.Header.Get("Content-Type"), assetBytes, err)
	}
	missingAsset, err := http.Get(baseURL + "/v1/visual-assets/fairy.surface/missing.png")
	if err != nil {
		t.Fatal(err)
	}
	missingAsset.Body.Close()
	if missingAsset.StatusCode != http.StatusNotFound {
		t.Fatalf("missing asset status=%d", missingAsset.StatusCode)
	}

	missing := postJSON(t, baseURL+"/v1/sessions", `{"surface":"im_group"}`)
	if missing.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(missing.Body)
		missing.Body.Close()
		t.Fatalf("missing surface key status=%d body=%s", missing.StatusCode, body)
	}
	missing.Body.Close()

	groupA := postJSON(t, baseURL+"/v1/sessions", `{"surface":"im_group","surfaceKey":"onebot-group:123"}`)
	groupABody := decodeResponse(t, groupA)
	groupA2 := postJSON(t, baseURL+"/v1/sessions", `{"surface":"im_group","surfaceKey":"onebot-group:123"}`)
	groupA2Body := decodeResponse(t, groupA2)
	groupB := postJSON(t, baseURL+"/v1/sessions", `{"surface":"im_group","surfaceKey":"onebot-group:456"}`)
	groupBBody := decodeResponse(t, groupB)
	if groupABody["conversationId"] != groupA2Body["conversationId"] || groupABody["conversationId"] == groupBBody["conversationId"] {
		t.Fatalf("conversation bindings = %#v %#v %#v", groupABody, groupA2Body, groupBBody)
	}

	desktop := postJSON(t, baseURL+"/v1/sessions", `{"surface":"desktop"}`)
	desktopBody := decodeResponse(t, desktop)
	if desktopBody["conversationId"] == groupABody["conversationId"] || desktopBody["conversationId"] == groupBBody["conversationId"] {
		t.Fatalf("desktop conversation was keyed group: %#v", desktopBody)
	}

	pool, err := pgxpool.New(context.Background(), databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var digest string
	if err := pool.QueryRow(context.Background(), "SELECT surface_key_digest FROM surface_conversations WHERE surface = 'im_group' ORDER BY surface_key_digest LIMIT 1").Scan(&digest); err != nil {
		t.Fatal(err)
	}
	if len(digest) != 64 || strings.Contains(digest, "123") || strings.Contains(digest, "456") {
		t.Fatalf("surface digest leaked source key: %q", digest)
	}
}

func writeSurfaceVisualManifest(t *testing.T, root string) {
	t.Helper()
	dir := filepath.Join(root, "visual-packs", "fairy.surface")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := `{"schemaVersion":2,"packId":"fairy.surface","displayName":"Surface","renderer":"state_images","frame":{"width":16,"height":16},"scale":1,"anchor":{"x":8,"y":15},"states":[{"id":"idle","description":"idle","imagePath":"fairy-character://localhost/fairy.surface/idle.png"}]}`
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "idle.png"), []byte("png"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func postJSON(t *testing.T, rawURL, payload string) *http.Response {
	t.Helper()
	response, err := http.Post(rawURL, "application/json", bytes.NewBufferString(payload))
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func decodeResponse(t *testing.T, response *http.Response) map[string]any {
	t.Helper()
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("response status=%d body=%s", response.StatusCode, body)
	}
	var value map[string]any
	if err := json.Unmarshal(body, &value); err != nil {
		t.Fatal(err)
	}
	return value
}
