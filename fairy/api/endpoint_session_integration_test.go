//go:build integration

package api_test

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fairy/character"
	"fairy/coreclient"
	"fairy/interaction"
	fairyruntime "fairy/runtime"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
)

func TestEndpointSessionIsolatesKeysAndPersistsNoRawKeyIntegration(t *testing.T) {
	root := t.TempDir()
	writeEndpointVisualManifest(t, root)
	characterService := character.NewCharacterService(root)
	record, err := characterService.CreateCharacter(character.Brief{Name: "Endpoint", Description: "Integration character", TextLanguage: "zh", SpeakingLanguage: "zh"}, "fairy.endpoint")
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
	assetResponse, err := http.Get(baseURL + "/v1/visual-assets/fairy.endpoint/idle.png")
	if err != nil {
		t.Fatal(err)
	}
	assetBytes, err := io.ReadAll(assetResponse.Body)
	assetResponse.Body.Close()
	if err != nil || assetResponse.StatusCode != http.StatusOK || string(assetBytes) != "png" {
		t.Fatalf("asset status=%d body=%q err=%v", assetResponse.StatusCode, assetBytes, err)
	}

	client, err := coreclient.New(coreclient.Options{Endpoint: baseURL})
	if err != nil {
		t.Fatal(err)
	}
	imContext := interaction.Context{Audience: interaction.AudienceMulti, Initiation: interaction.InitiationAmbient, Presentation: interaction.PresentationChat}
	open := func(key string) coreclient.OpenSessionResponse {
		result, err := client.OpenSession(context.Background(), coreclient.OpenSessionRequest{Endpoint: interaction.EndpointIM, EndpointKey: key, Interaction: imContext})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	groupA := open("onebot-group:123")
	groupA2 := open("onebot-group:123")
	groupB := open("onebot-group:456")
	if groupA.ConversationID != groupA2.ConversationID || groupA.ConversationID == groupB.ConversationID {
		t.Fatalf("conversation bindings = %#v %#v %#v", groupA, groupA2, groupB)
	}
	desktop, err := client.OpenSession(context.Background(), coreclient.OpenSessionRequest{
		Endpoint: interaction.EndpointDesktop, EndpointKey: "desktop-installation",
		Interaction: interaction.Context{Audience: interaction.AudienceSingle, Initiation: interaction.InitiationDirect, Presentation: interaction.PresentationEmbodied},
	})
	if err != nil || desktop.ConversationID == groupA.ConversationID || desktop.ConversationID == groupB.ConversationID {
		t.Fatalf("desktop binding = %#v, %v", desktop, err)
	}

	pool, err := pgxpool.New(context.Background(), databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var digest string
	if err := pool.QueryRow(context.Background(), "SELECT endpoint_key_digest FROM endpoint_conversations WHERE endpoint = 'im' ORDER BY endpoint_key_digest LIMIT 1").Scan(&digest); err != nil {
		t.Fatal(err)
	}
	if len(digest) != 64 || strings.Contains(digest, "123") || strings.Contains(digest, "456") {
		t.Fatalf("endpoint digest leaked source key: %q", digest)
	}

	const rawOwnerSubject = "owner-user-987654"
	owner, err := client.BindOwnerIdentity(context.Background(), "qq.onebot", rawOwnerSubject)
	if err != nil || owner.Namespace != "qq.onebot" || len(owner.PrincipalDigest) != 64 || strings.Contains(owner.PrincipalDigest, rawOwnerSubject) {
		t.Fatalf("owner bind = %#v, %v", owner, err)
	}
	owners, err := client.ListOwnerIdentities(context.Background())
	if err != nil || len(owners) != 1 || owners[0].PrincipalDigest != owner.PrincipalDigest {
		t.Fatalf("owner list = %#v, %v", owners, err)
	}
	var rawOwnerRows int
	if err := pool.QueryRow(context.Background(), `
SELECT count(*) FROM owner_identities
WHERE namespace = $1 OR subject_digest = $1`, rawOwnerSubject).Scan(&rawOwnerRows); err != nil {
		t.Fatal(err)
	}
	if rawOwnerRows != 0 {
		t.Fatalf("raw owner subject appears in %d rows", rawOwnerRows)
	}
	if err := client.UnbindOwnerIdentity(context.Background(), "qq.onebot", rawOwnerSubject); err != nil {
		t.Fatal(err)
	}
}

func writeEndpointVisualManifest(t *testing.T, root string) {
	t.Helper()
	dir := filepath.Join(root, "visual-packs", "fairy.endpoint")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := `{"schemaVersion":2,"packId":"fairy.endpoint","displayName":"Endpoint","renderer":"state_images","frame":{"width":16,"height":16},"scale":1,"anchor":{"x":8,"y":15},"states":[{"id":"idle","description":"idle","imagePath":"fairy-character://localhost/fairy.endpoint/idle.png"}]}`
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "idle.png"), []byte("png"), 0o600); err != nil {
		t.Fatal(err)
	}
}
