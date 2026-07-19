package api_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"fairy/api"
	"fairy/character"
	fairyruntime "fairy/runtime"
	"go.uber.org/zap"
)

func startTestServer(t *testing.T, root string) (addr string, rt *fairyruntime.Runtime) {
	t.Helper()
	var err error
	rt, err = fairyruntime.Open(fairyruntime.Options{ConfigRoot: root, Logger: zap.NewNop()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rt.Close() })

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr = ln.Addr().String()
	_ = ln.Close()
	srv, err := api.NewServer(rt, api.Options{Addr: addr, Logger: zap.NewNop()})
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Run() }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})
	waitHTTP(t, "http://"+addr+"/v1/status")
	return addr, rt
}

func writeVisualPack(t *testing.T, root, packID string) {
	t.Helper()
	path := filepath.Join(root, "visual-packs", packID, "manifest.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	data := `{"schemaVersion":2,"packId":"` + packID + `","displayName":"Demo","renderer":"state_images","frame":{"width":16,"height":16},"scale":1,"anchor":{"x":8,"y":15},"states":[{"id":"idle","description":"idle","imagePath":"fairy-character://localhost/` + packID + `/idle.png"}]}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(path), "idle.png"), []byte("\x89PNG\r\n\x1a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestAdminCharactersProfileIntelligenceUsage(t *testing.T) {
	root := t.TempDir()
	writeVisualPack(t, root, "demo.pack")
	addr, _ := startTestServer(t, root)

	createBody, _ := json.Marshal(map[string]any{
		"name":             "亚托莉",
		"description":      "温柔敏锐。",
		"textLanguage":     "zh",
		"speakingLanguage": "ja",
		"visualPackId":     "demo.pack",
	})
	res, err := http.Post("http://"+addr+"/v1/characters", "application/json", bytes.NewReader(createBody))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(res.Body)
		t.Fatalf("create character = %d %s", res.StatusCode, raw)
	}
	var created map[string]any
	if err := json.NewDecoder(res.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	characterID, _ := created["characterId"].(string)
	revision := uint64(created["revision"].(float64))
	if characterID == "" {
		t.Fatalf("created = %#v", created)
	}

	actBody, _ := json.Marshal(map[string]any{"revision": revision})
	req, _ := http.NewRequest(http.MethodPost, "http://"+addr+"/v1/characters/"+characterID+"/activate", bytes.NewReader(actBody))
	req.Header.Set("Content-Type", "application/json")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("activate = %d", res.StatusCode)
	}

	res, err = http.Get("http://" + addr + "/v1/characters")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var catalog map[string]any
	if err := json.NewDecoder(res.Body).Decode(&catalog); err != nil {
		t.Fatal(err)
	}
	if catalog["active"] == nil {
		t.Fatalf("catalog missing active: %#v", catalog)
	}

	res, err = http.Get("http://" + addr + "/v1/visual-packs")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("visual-packs = %d", res.StatusCode)
	}

	name := "Rinai"
	profBody, _ := json.Marshal(map[string]any{"preferredName": name})
	req, _ = http.NewRequest(http.MethodPut, "http://"+addr+"/v1/profile", bytes.NewReader(profBody))
	req.Header.Set("Content-Type", "application/json")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(res.Body)
		t.Fatalf("put profile = %d %s", res.StatusCode, raw)
	}

	res, err = http.Get("http://" + addr + "/v1/profile")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var profile map[string]any
	if err := json.NewDecoder(res.Body).Decode(&profile); err != nil {
		t.Fatal(err)
	}
	if profile["preferredName"] != name {
		t.Fatalf("profile = %#v", profile)
	}

	res, err = http.Get("http://" + addr + "/v1/intelligence")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(res.Body)
		t.Fatalf("intelligence = %d %s", res.StatusCode, raw)
	}

	res, err = http.Get("http://" + addr + "/v1/memories/personal?characterId=" + characterID)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(res.Body)
		t.Fatalf("list memories = %d %s", res.StatusCode, raw)
	}

	res, err = http.Get("http://" + addr + "/v1/usage")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(res.Body)
		t.Fatalf("usage = %d %s", res.StatusCode, raw)
	}
}

func TestAdminCharacterPackageExportAndImport(t *testing.T) {
	sourceRoot := t.TempDir()
	writeVisualPack(t, sourceRoot, "demo.pack")
	sourceAddr, sourceRuntime := startTestServer(t, sourceRoot)

	record, err := sourceRuntime.Character.CreateCharacter(characterBrief(), "demo.pack")
	if err != nil {
		t.Fatalf("CreateCharacter() error = %v", err)
	}
	res, err := http.Get("http://" + sourceAddr + "/v1/characters/" + record.CharacterID + "/export")
	if err != nil {
		t.Fatal(err)
	}
	packageBytes, readErr := io.ReadAll(res.Body)
	res.Body.Close()
	if readErr != nil {
		t.Fatal(readErr)
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("export = %d %s", res.StatusCode, packageBytes)
	}
	if disposition := res.Header.Get("Content-Disposition"); disposition == "" {
		t.Fatal("export response missing Content-Disposition")
	}
	archive, err := zip.NewReader(bytes.NewReader(packageBytes), int64(len(packageBytes)))
	if err != nil {
		t.Fatalf("export response is not a zip archive: %v", err)
	}
	if len(archive.File) == 0 {
		t.Fatal("export archive is empty")
	}

	targetAddr, _ := startTestServer(t, t.TempDir())
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "demo.pack")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(packageBytes); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, "http://"+targetAddr+"/v1/characters/import", &body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(res.Body)
		t.Fatalf("import = %d %s", res.StatusCode, raw)
	}
	var imported map[string]any
	if err := json.NewDecoder(res.Body).Decode(&imported); err != nil {
		t.Fatal(err)
	}
	if imported["name"] != "亚托莉" || imported["characterId"] == "" {
		t.Fatalf("imported = %#v", imported)
	}
}

func characterBrief() character.Brief {
	return character.Brief{
		Name:             "亚托莉",
		Description:      "温柔敏锐。",
		TextLanguage:     "zh",
		SpeakingLanguage: "ja",
	}
}
