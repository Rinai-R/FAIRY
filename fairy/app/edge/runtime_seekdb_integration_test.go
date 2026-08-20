//go:build integration

package edge

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"fairy/context/character"
	"fairy/context/knowledge"
	"fairy/context/memory/personal"
	"fairy/runtime/observability"
	"fairy/runtime/seekdb"
	"fairy/transport/session"
)

func TestOpenComposesSeekDBCoreAndSessionFacade(t *testing.T) {
	applyEdgeSeekDBEnvironment(t)
	rt, err := Open(t.Context(), Options{ConfigRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := rt.Close(t.Context()); err != nil {
			t.Error(err)
		}
	})
	if rt.Core() == nil || rt.Core().Foundation == nil {
		t.Fatal("edge did not compose Core SeekDB foundation")
	}
	if rt.Session() == nil || rt.Facade() == nil {
		t.Fatal("edge did not compose Session service and facade")
	}
	host, err := rt.PluginHost()
	if err != nil {
		t.Fatalf("PluginHost() = %v, want configured deny-by-default host", err)
	}
	if _, err := host.Instantiate(t.Context(), "wasi-guest", []byte{
		0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00,
		0x01, 0x09, 0x01, 0x60, 0x04, 0x7f, 0x7f, 0x7f, 0x7f, 0x01, 0x7f,
		0x02, 0x23, 0x01, 0x16, 0x77, 0x61, 0x73, 0x69, 0x5f, 0x73, 0x6e, 0x61, 0x70, 0x73, 0x68, 0x6f, 0x74, 0x5f, 0x70, 0x72, 0x65, 0x76, 0x69, 0x65, 0x77, 0x31,
		0x08, 0x66, 0x64, 0x5f, 0x77, 0x72, 0x69, 0x74, 0x65, 0x00, 0x00,
	}); err == nil {
		t.Fatal("edge plugin host instantiated a WASI guest")
	}
	status, err := rt.Core().Foundation.Status(t.Context())
	if err != nil || status.Storage != "seekdb" || status.Schema.State != seekdb.SchemaCurrent {
		t.Fatalf("foundation status = (%#v, %v)", status, err)
	}
	_, err = rt.Facade().OpenSession(t.Context(), session.OpenSessionRequest{
		Endpoint:    session.EndpointDesktop,
		EndpointKey: "edge-desktop",
		Interaction: session.Context{
			Audience: session.AudienceSingle, Initiation: session.InitiationDirect, Presentation: session.PresentationChat,
		},
	})
	if err != nil && !strings.Contains(err.Error(), "character") {
		t.Fatalf("OpenSession() error = %v", err)
	}
}

func TestCleanDirEdgeRuntimePersistsConversationMemoryKnowledgeAndLogs(t *testing.T) {
	applyEdgeSeekDBEnvironment(t)
	root := t.TempDir()
	writeEdgeVisualPack(t, root, "fairy.clean-dir")

	first, err := Open(t.Context(), Options{ConfigRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	management := first.Management()
	if management == nil {
		first.Close(t.Context())
		t.Fatal("clean-dir edge runtime did not expose management")
	}
	created, err := first.Core().Character.CreateCharacter(character.Brief{
		Name: "CleanDir", Description: "干净目录验收角色", TextLanguage: "zh", SpeakingLanguage: "zh",
	}, "fairy.clean-dir")
	if err != nil {
		first.Close(t.Context())
		t.Fatal(err)
	}
	if _, err := management.ActivateCharacter(created.CharacterID, created.Revision); err != nil {
		first.Close(t.Context())
		t.Fatal(err)
	}
	preferredName := "Rinai"
	if _, err := management.SaveProfile(&preferredName); err != nil {
		first.Close(t.Context())
		t.Fatal(err)
	}
	opened, err := first.Facade().OpenSession(t.Context(), session.OpenSessionRequest{
		Endpoint:    session.EndpointDesktop,
		EndpointKey: "clean-dir-desktop",
		Interaction: session.Context{
			Audience: session.AudienceSingle, Initiation: session.InitiationDirect, Presentation: session.PresentationChat,
		},
	})
	if err != nil {
		first.Close(t.Context())
		t.Fatalf("OpenSession() error = %v", err)
	}
	turn, err := first.Core().TranscriptStore.BeginTurnContext(t.Context(), opened.ConversationID, "你好，记住这次干净安装。")
	if err != nil {
		first.Close(t.Context())
		t.Fatal(err)
	}
	if _, err := first.Core().TranscriptStore.CompleteTurnContext(
		t.Context(), opened.ConversationID, turn.ID, "已经记住这次干净安装。",
	); err != nil {
		first.Close(t.Context())
		t.Fatal(err)
	}
	memory, err := management.CreateMemory(MemoryWrite{
		Kind: "preference", Scope: personal.Scope{Type: "global"},
		Content: "用户希望干净安装后仍能召回个人偏好。", ConfidenceBasisPoints: 8000,
	})
	if err != nil {
		first.Close(t.Context())
		t.Fatal(err)
	}
	knowledgeRecord, err := first.Core().KnowledgeStore.InsertVerifiedKnowledgeContext(
		t.Context(), "干净安装", "干净目录启动后知识必须跨重启可召回。",
		opened.ConversationID, turn.ID, 9000, nil,
	)
	if err != nil {
		first.Close(t.Context())
		t.Fatal(err)
	}
	first.Core().Logs.Append(observability.EntryInput{
		Time: time.Now(), Level: "info", Logger: "edge.clean-dir", Message: "clean-dir-ready",
	})
	waitEdgeCondition(t, "observability history persist", func() bool {
		logs, err := first.Core().History.RecentLogs(t.Context(), 20)
		if err != nil {
			return false
		}
		for _, entry := range logs {
			if entry.Message == "clean-dir-ready" {
				return true
			}
		}
		return false
	})
	if err := first.Close(t.Context()); err != nil {
		t.Fatal(err)
	}

	second, err := Open(t.Context(), Options{ConfigRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := second.Close(t.Context()); err != nil {
			t.Error(err)
		}
	})
	restoredManagement := second.Management()
	if restoredManagement == nil {
		t.Fatal("restarted edge runtime did not expose management")
	}
	catalog, err := restoredManagement.Characters()
	if err != nil || catalog.Active == nil || catalog.Active.CharacterID != created.CharacterID {
		t.Fatalf("restarted character catalog = %#v, %v", catalog, err)
	}
	profile, err := restoredManagement.Profile()
	if err != nil || profile.PreferredName == nil || *profile.PreferredName != preferredName {
		t.Fatalf("restarted profile = %#v, %v", profile, err)
	}
	reopened, err := second.Facade().OpenSession(t.Context(), session.OpenSessionRequest{
		Endpoint:    session.EndpointDesktop,
		EndpointKey: "clean-dir-desktop",
		Interaction: session.Context{
			Audience: session.AudienceSingle, Initiation: session.InitiationDirect, Presentation: session.PresentationChat,
		},
	})
	if err != nil || reopened.ConversationID != opened.ConversationID {
		t.Fatalf("restarted session = %#v, %v", reopened, err)
	}
	page, err := restoredManagement.Conversation(t.Context(), reopened.ConversationID, 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	if !messagePageContains(page, "你好，记住这次干净安装。") || !messagePageContains(page, "已经记住这次干净安装。") {
		t.Fatalf("restarted conversation = %#v", page)
	}
	memories, err := restoredManagement.Memories(created.CharacterID)
	if err != nil || !memoryCatalogContains(memories, memory.ID) {
		t.Fatalf("restarted memories = %#v, %v", memories, err)
	}
	knowledgeCatalog, err := restoredManagement.Knowledge(t.Context())
	if err != nil || !knowledgeRecordsContainID(knowledgeCatalog.Verified, knowledgeRecord.ID) {
		t.Fatalf("restarted knowledge = %#v, %v", knowledgeCatalog, err)
	}
	hits, err := second.Core().KnowledgeStore.SearchKnowledgeForIngestContext(t.Context(), "干净目录", knowledge.MaxSearchCandidates)
	if err != nil || !knowledgeRetrievedContainID(hits, knowledgeRecord.ID) {
		t.Fatalf("restarted knowledge recall = %#v, %v", hits, err)
	}
	logs, err := restoredManagement.Logs(observability.LogFilter{LoggerPrefix: "edge.clean-dir", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	foundLog := false
	for _, entry := range logs.Entries {
		if entry.Message == "clean-dir-ready" {
			foundLog = true
		}
		payload := strings.ToLower(entry.Message)
		if strings.Contains(payload, "bearer") || strings.Contains(payload, "credential") {
			t.Fatalf("restored log leaked credential: %#v", entry)
		}
	}
	if !foundLog {
		t.Fatalf("restarted logs missing clean-dir-ready: %#v", logs)
	}
	if _, err := restoredManagement.Metrics(t.Context()); err != nil {
		t.Fatalf("restarted metrics: %v", err)
	}
	if _, err := restoredManagement.TurnRuntime(t.Context(), reopened.ConversationID, turn.ID); err != nil {
		t.Fatalf("restarted turn debug: %v", err)
	}
}

func applyEdgeSeekDBEnvironment(t *testing.T) {
	t.Helper()
	library := os.Getenv(seekdb.EnvLibrary)
	if library == "" {
		t.Skip(seekdb.EnvLibrary + " is not set")
	}
	t.Setenv(seekdb.EnvLibrary, library)
	t.Setenv(seekdb.EnvDataDir, edgeProcessDataDir(t))
	t.Setenv(seekdb.EnvDatabase, edgeUniqueDatabase(t))
	t.Setenv(seekdb.EnvConnectLimit, "5s")
	t.Setenv(seekdb.EnvStartLimit, "90s")
	t.Setenv(seekdb.EnvQueryLimit, "15s")
	t.Setenv(seekdb.EnvShutdownLimit, "20s")
	t.Setenv("FAIRY_DATABASE_URL", "postgres://invalid-legacy-sentinel")
}

var (
	edgeDataDirOnce sync.Once
	edgeDataDir     string
)

func edgeProcessDataDir(t *testing.T) string {
	t.Helper()
	edgeDataDirOnce.Do(func() {
		dir, err := os.MkdirTemp("", "fairy-edge-seekdb-")
		if err != nil {
			t.Fatal(err)
		}
		edgeDataDir = dir
	})
	return edgeDataDir
}

func edgeUniqueDatabase(t *testing.T) string {
	t.Helper()
	sum := sha256.Sum256([]byte(t.Name()))
	return "db" + hex.EncodeToString(sum[:10])
}

func writeEdgeVisualPack(t *testing.T, root, packID string) {
	t.Helper()
	dir := filepath.Join(root, "visual-packs", packID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := `{"schemaVersion":2,"packId":"` + packID + `","displayName":"CleanDir","renderer":"state_images","frame":{"width":16,"height":16},"scale":1,"anchor":{"x":8,"y":15},"states":[{"id":"idle","description":"idle","imagePath":"fairy-character://localhost/` + packID + `/idle.png"}]}`
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "idle.png"), []byte("png"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func waitEdgeCondition(t *testing.T, name string, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ready() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s did not become ready", name)
}

func messagePageContains(page session.MessagePage, text string) bool {
	for _, item := range page.Messages {
		if strings.Contains(item.Content, text) {
			return true
		}
	}
	return false
}

func memoryCatalogContains(catalog personal.Catalog, id string) bool {
	for _, record := range append(append(catalog.Global, catalog.Character...), catalog.NeedsReview...) {
		if record.ID == id {
			return true
		}
	}
	return false
}

func knowledgeRecordsContainID(records []knowledge.Record, id string) bool {
	for _, record := range records {
		if record.ID == id {
			return true
		}
	}
	return false
}

func knowledgeRetrievedContainID(records []knowledge.Retrieved, id string) bool {
	for _, record := range records {
		if record.ID == id {
			return true
		}
	}
	return false
}
