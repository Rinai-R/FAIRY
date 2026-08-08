package api

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"fairy/character"
	"fairy/observability"

	"github.com/cloudwego/hertz/pkg/common/ut"
	"go.uber.org/zap"
)

func TestCharacterDeleteRouteRequiresAuthAndMapsResults(t *testing.T) {
	root := t.TempDir()
	writeCharacterVisualFixture(t, root, "fairy.delete-test")
	characters := character.NewCharacterService(root)
	record, err := characters.CreateCharacter(character.Brief{
		Name: "待删除角色", Description: "用于验证角色删除接口。",
		TextLanguage: "zh", SpeakingLanguage: "zh",
	}, "fairy.delete-test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := characters.ActivateCharacter(record.CharacterID, record.Revision); err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(&Dependencies{
		Character: characters, HTTPMetrics: observability.NewHTTPMetrics(), Logger: zap.NewNop(),
	}, Options{Addr: "127.0.0.1:0", Token: "core-token", Logger: zap.NewNop()})
	if err != nil {
		t.Fatal(err)
	}
	path := "/v1/characters/" + record.CharacterID

	unauthorized := ut.PerformRequest(server.Engine().Engine, http.MethodDelete, path, nil)
	if unauthorized.Result().StatusCode() != http.StatusUnauthorized {
		t.Fatalf("unauthorized DELETE status = %d", unauthorized.Result().StatusCode())
	}
	if _, found, err := characters.CatalogStore().Lookup(record.CharacterID); err != nil || !found {
		t.Fatalf("character after unauthorized DELETE = found %v, error %v", found, err)
	}

	deleted := ut.PerformRequest(server.Engine().Engine, http.MethodDelete, path, nil,
		ut.Header{Key: "Authorization", Value: "Bearer core-token"},
	)
	if deleted.Result().StatusCode() != http.StatusNoContent || len(deleted.Result().Body()) != 0 {
		t.Fatalf("DELETE status = %d body=%q", deleted.Result().StatusCode(), deleted.Result().Body())
	}
	catalog, err := characters.ListCharacters()
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Characters) != 0 || catalog.Active != nil {
		t.Fatalf("catalog after DELETE = %#v", catalog)
	}

	missing := ut.PerformRequest(server.Engine().Engine, http.MethodDelete, path, nil,
		ut.Header{Key: "Authorization", Value: "Bearer core-token"},
	)
	if missing.Result().StatusCode() != http.StatusNotFound {
		t.Fatalf("repeated DELETE status = %d body=%s", missing.Result().StatusCode(), missing.Result().Body())
	}
	invalid := ut.PerformRequest(server.Engine().Engine, http.MethodDelete, "/v1/characters/%20invalid", nil,
		ut.Header{Key: "Authorization", Value: "Bearer core-token"},
	)
	if invalid.Result().StatusCode() != http.StatusBadRequest {
		t.Fatalf("invalid DELETE status = %d body=%s", invalid.Result().StatusCode(), invalid.Result().Body())
	}
}

func writeCharacterVisualFixture(t *testing.T, root, packID string) {
	t.Helper()
	path := filepath.Join(root, "visual-packs", packID, "manifest.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	data := `{"schemaVersion":2,"packId":"` + packID + `","displayName":"Delete test","renderer":"state_images","frame":{"width":16,"height":16},"scale":1,"anchor":{"x":8,"y":15},"states":[{"id":"idle","description":"idle","imagePath":"fairy-character://localhost/` + packID + `/idle.png"}]}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}
