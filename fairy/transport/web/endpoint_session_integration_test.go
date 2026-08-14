//go:build integration

package web_test

import (
	"context"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	fairycore "fairy/app/core"
	"fairy/context/character"

	"go.uber.org/zap"

	"fairy/transport/session"
)

func TestEndpointSessionIsolatesKeysAndPersistsNoRawKeyIntegration(t *testing.T) {
	applySeekDBAPIEnv(t)
	root := t.TempDir()
	writeEndpointVisualManifest(t, root)
	rt, err := fairycore.Open(fairycore.RuntimeOptions{ConfigRoot: root, Logger: zap.NewNop()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rt.Close() })
	record, err := rt.Character.CreateCharacter(character.Brief{Name: "Endpoint", Description: "Integration character", TextLanguage: "zh", SpeakingLanguage: "zh"}, "fairy.endpoint")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rt.Character.ActivateCharacter(record.CharacterID, record.Revision); err != nil {
		t.Fatal(err)
	}
	target, err := rt.Character.CreateCharacter(character.Brief{Name: "Debug Target", Description: "Evaluation-only character", TextLanguage: "zh", SpeakingLanguage: "zh"}, "fairy.endpoint")
	if err != nil {
		t.Fatal(err)
	}
	baseURL, token := startProductionAPIServer(t, rt)
	assetResponse := doRequest(t, http.MethodGet, baseURL+"/v1/visual-assets/fairy.endpoint/idle.png", token)
	assetBytes, err := io.ReadAll(assetResponse.Body)
	assetResponse.Body.Close()
	if err != nil || assetResponse.StatusCode != http.StatusOK || string(assetBytes) != "png" {
		t.Fatalf("asset status=%d body=%q err=%v", assetResponse.StatusCode, assetBytes, err)
	}

	client, err := session.New(session.Options{Endpoint: baseURL, Token: token})
	if err != nil {
		t.Fatal(err)
	}
	sessionSocket, err := client.DialSession(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sessionSocket.Close() })
	imContext := session.Context{Audience: session.AudienceMulti, Initiation: session.InitiationAmbient, Presentation: session.PresentationChat}
	open := func(key string) session.OpenSessionResponse {
		result, err := sessionSocket.OpenSession(context.Background(), session.OpenSessionRequest{
			Endpoint: session.EndpointIM, EndpointKey: key, Interaction: imContext,
			OutputCapabilities: session.OutputCapabilities{Sticker: true},
		})
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
	if !rt.Turn.OutputCapabilities(groupA.ConversationID).Sticker {
		t.Fatal("session.open did not bind advertised sticker capability")
	}
	desktop, err := sessionSocket.OpenSession(context.Background(), session.OpenSessionRequest{
		Endpoint: session.EndpointDesktop, EndpointKey: "desktop-installation",
		Interaction: session.Context{Audience: session.AudienceSingle, Initiation: session.InitiationDirect, Presentation: session.PresentationEmbodied},
	})
	if err != nil || desktop.ConversationID == groupA.ConversationID || desktop.ConversationID == groupB.ConversationID {
		t.Fatalf("desktop binding = %#v, %v", desktop, err)
	}
	if rt.Turn.OutputCapabilities(desktop.ConversationID).Sticker {
		t.Fatal("missing outputCapabilities defaulted to sticker support")
	}
	evaluation, err := sessionSocket.OpenSession(context.Background(), session.OpenSessionRequest{
		Endpoint: session.EndpointDesktop, EndpointKey: "browser-debug-session", CharacterID: target.CharacterID,
		Interaction: session.Context{
			Audience: session.AudienceSingle, Initiation: session.InitiationDirect,
			Presentation: session.PresentationChat, Evaluation: true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if evaluation.CharacterID != target.CharacterID {
		t.Fatalf("evaluation session character = %q, want %q", evaluation.CharacterID, target.CharacterID)
	}
	resolved, err := rt.Turn.ResolveInteraction(evaluation.ConversationID)
	if err != nil || !resolved.IsEvaluation() || !resolved.AllowsPersonalMemory() {
		t.Fatalf("evaluation interaction = %#v, %v", resolved, err)
	}
	catalog, err := client.ListCharacters(context.Background())
	if err != nil || catalog.Active == nil || catalog.Active.CharacterID != record.CharacterID {
		t.Fatalf("evaluation session changed active character: %#v, %v", catalog.Active, err)
	}
	if err := sessionSocket.Close(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) &&
		(rt.Turn.OutputCapabilities(groupA.ConversationID).Sticker ||
			rt.Turn.OutputCapabilities(groupB.ConversationID).Sticker) {
		time.Sleep(10 * time.Millisecond)
	}
	if rt.Turn.OutputCapabilities(groupA.ConversationID).Sticker || rt.Turn.OutputCapabilities(groupB.ConversationID).Sticker {
		t.Fatal("closed SessionSocket retained advertised sticker capability")
	}

	database, err := rt.Foundation.SQL()
	if err != nil {
		t.Fatal(err)
	}
	var digestBytes []byte
	if err := database.QueryRowContext(context.Background(), "SELECT endpoint_key_digest FROM endpoint_conversations WHERE endpoint = 'im' ORDER BY endpoint_key_digest LIMIT 1").Scan(&digestBytes); err != nil {
		t.Fatal(err)
	}
	digest := hex.EncodeToString(digestBytes)
	if len(digest) != 64 || strings.Contains(digest, "123") || strings.Contains(digest, "456") {
		t.Fatalf("endpoint digest leaked source key: %q", digest)
	}
	var evaluationPersisted bool
	var evaluationCharacterID string
	if err := database.QueryRowContext(context.Background(), `
SELECT evaluation, character_id
FROM endpoint_conversations
WHERE conversation_id = ?`, evaluation.ConversationID).Scan(&evaluationPersisted, &evaluationCharacterID); err != nil {
		t.Fatal(err)
	}
	if !evaluationPersisted || evaluationCharacterID != target.CharacterID {
		t.Fatalf("evaluation endpoint row = evaluation:%t character:%q", evaluationPersisted, evaluationCharacterID)
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
	if err := database.QueryRowContext(context.Background(), `
SELECT count(*) FROM owner_identities
WHERE namespace = ? OR subject_digest = ?`, rawOwnerSubject, rawOwnerSubject).Scan(&rawOwnerRows); err != nil {
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
