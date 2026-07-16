package companion

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fairy/character"
	"fairy/memory"
	"fairy/model"
	"fairy/profile"
	_ "modernc.org/sqlite"
)

func writeVisualFixture(t *testing.T, root string, packID string) {
	t.Helper()
	path := filepath.Join(root, "visual-packs", packID, "manifest.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	data := `{"schemaVersion":2,"packId":"` + packID + `","displayName":"Fairy","renderer":"state_images","frame":{"width":128,"height":128},"scale":1,"anchor":{"x":64,"y":127},"states":[{"id":"idle","description":"idle 状态说明","imagePath":"fairy-character://localhost/` + packID + `/idle.png"}]}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
}

func writeModelConnectionWithEndpoint(t *testing.T, root string, protocol string, endpoint string, authMode string) {
	t.Helper()
	dir := filepath.Join(root, "model")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	document := "{\"schema_version\":1,\"data\":{\"schema_version\":3,\"connection_id\":\"6a129284-6358-47b0-ad64-2a5907d36c91\",\"protocol\":\"" + protocol + "\",\"endpoint\":\"" + endpoint + "\",\"model\":\"deepseek-v4-flash\",\"context_window_tokens\":1048576,\"auth_mode\":\"" + authMode + "\",\"capabilities\":{\"prompt_cache_key\":false,\"cached_tokens_usage\":true,\"explicit_breakpoints\":false,\"cache_retention\":false,\"websocket_continuation\":false}}}"
	if err := os.WriteFile(filepath.Join(dir, "connection.json"), []byte(document), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
}

func seedCompanionRuntime(t *testing.T, root string) (*memory.Store, string) {
	t.Helper()
	writeVisualFixture(t, root, "fairy.atri")
	created, err := character.NewCharacterService(root).CreateCharacter(
		characterBrief("亚托莉", "认真听用户说话。"),
		"fairy.atri",
	)
	if err != nil {
		t.Fatalf("CreateCharacter() error = %v", err)
	}
	if _, err := character.NewCharacterService(root).ActivateCharacter(created.CharacterID, created.Revision); err != nil {
		t.Fatalf("ActivateCharacter() error = %v", err)
	}
	name := "Rinai"
	if _, err := profile.NewProfileService(root).SetPreferredName(&name); err != nil {
		t.Fatalf("SetPreferredName() error = %v", err)
	}
	memoryStore, err := memory.OpenOrCreate(filepath.Join(root, memory.RelativePath))
	if err != nil {
		t.Fatalf("OpenOrCreate() error = %v", err)
	}
	return memoryStore, created.CharacterID
}

func characterBrief(name string, description string) character.Brief {
	return character.Brief{Name: name, Description: description}
}

func insertKnowledgeFixtureForCompanion(t *testing.T, root string, conversationID string, turnID string) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(root, memory.RelativePath))
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	defer db.Close()
	_, err = db.Exec("INSERT INTO knowledge_entries(id, topic, statement, status, verification_basis, confidence_basis_points, source_conversation_id, source_turn_id, created_at_ms, updated_at_ms) VALUES (?1, '主题陈述系统', '主题陈述系统内容', 'verified', 'user_confirmed', 8000, ?2, ?3, 1, 1)", "6a129284-6358-47b0-ad64-2a5907d36c94", conversationID, turnID)
	if err != nil {
		t.Fatalf("insert knowledge fixture error = %v", err)
	}
}

func TestCompanionServiceSubmitTurnReturnsNotMigrated(t *testing.T) {
	service := NewCompanionService()
	_, err := service.SubmitTurn(SubmitTurnRequest{
		ConversationID: "conversation-1",
		Input:          "你好",
		SpeechEnabled:  true,
	})
	if !errors.Is(err, ErrRespondRuntimeNotMigrated) {
		t.Fatalf("SubmitTurn() error = %v, want %v", err, ErrRespondRuntimeNotMigrated)
	}
}

func TestCompanionServiceSubmitTurnResolvesVisualStates(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
			MaxTokens uint32 `json:"max_tokens"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("request body is not JSON: %v", err)
		}
		if strings.Contains(body.Messages[0].Content, "existing personal memories") {
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"{\\\"mutations\\\":[]}\"}}]}\n\n")
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
			return
		}
		if body.MaxTokens != RespondMaxOutputTokens {
			t.Fatalf("max_tokens = %d, want %d", body.MaxTokens, RespondMaxOutputTokens)
		}
		joined := ""
		for _, message := range body.Messages {
			joined += message.Content + "\n"
		}
		if !strings.Contains(joined, "idle 状态说明") {
			t.Fatalf("visual states missing from prompt: %#v", body.Messages)
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"{\\\"chains\\\":[{\\\"visualState\\\":\\\"idle\\\",\\\"text\\\":\\\"好。\\\"}]}\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
	}))
	t.Cleanup(server.Close)

	root := t.TempDir()
	writeModelConnectionWithEndpoint(t, root, "chat_completions", server.URL, "no_auth")
	memoryStore, characterID := seedCompanionRuntime(t, root)
	bootstrap, err := memoryStore.OpenOrCreateCharacterConversation(characterID)
	if err != nil {
		t.Fatalf("OpenOrCreateCharacterConversation() error = %v", err)
	}
	service := NewCompanionServiceWithRuntime(root, memoryStore, model.NewModelServiceWithTransport(root, model.HTTPTransport{Client: server.Client()}))
	outcome, err := service.SubmitTurn(SubmitTurnRequest{
		ConversationID: bootstrap.Conversation.ID,
		Input:          "你好",
		SpeechEnabled:  false,
	})
	if err != nil {
		t.Fatalf("SubmitTurn() error = %v", err)
	}
	if outcome.ResponseText != "好。" || outcome.VisualState != "idle" || !outcome.RespondMigrated {
		t.Fatalf("outcome = %#v", outcome)
	}
}

func TestCompanionServiceRespondRuntimeMigratedFalse(t *testing.T) {
	if NewCompanionService().RespondRuntimeMigrated() {
		t.Fatal("RespondRuntimeMigrated() = true, want false")
	}
}

func TestNilCompanionServiceSubmitTurnReturnsNotMigrated(t *testing.T) {
	var service *CompanionService
	_, err := service.SubmitTurn(SubmitTurnRequest{ConversationID: "conversation-1", Input: "你好"})
	if !errors.Is(err, ErrRespondRuntimeNotMigrated) {
		t.Fatalf("SubmitTurn() error = %v", err)
	}
}

func TestCompanionServiceSubmitCompiledTurnPersistsCompletedAssistant(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("request body is not JSON: %v", err)
		}
		if len(body.Messages) == 0 {
			t.Fatal("messages empty")
		}
		// Background extract may race after the respond turn; answer it without failing respond assertions.
		if strings.Contains(body.Messages[0].Content, "existing personal memories") {
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"{\\\"mutations\\\":[]}\"}}]}\n\n")
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
			return
		}
		if len(body.Messages) < 5 {
			t.Fatalf("messages too short: %#v", body.Messages)
		}
		if body.Messages[0].Role != "system" || !strings.Contains(body.Messages[0].Content, "严格 JSON object") || strings.Contains(body.Messages[0].Content, "亚托莉") {
			t.Fatalf("system instructions must stay stable and persona-free: %#v", body.Messages[0])
		}
		joined := ""
		for _, message := range body.Messages {
			joined += message.Content + "\n"
		}
		if !strings.Contains(joined, "亚托莉") || !strings.Contains(joined, "Rinai") {
			t.Fatalf("messages missing persona/profile context data: %#v", body.Messages)
		}
		last := body.Messages[len(body.Messages)-1]
		if last.Role != "user" || !strings.Contains(last.Content, "fairy_context_data") || !strings.Contains(last.Content, "太甜的饮料") || !strings.Contains(last.Content, "主题陈述系统") {
			t.Fatalf("retrieval context missing from tail: %#v", body.Messages)
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"{\\\"chains\\\":[{\\\"visualState\\\":\\\"happy\\\",\\\"text\\\":\\\"我在。\\\"}]}\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
	}))
	t.Cleanup(server.Close)

	root := t.TempDir()
	writeModelConnectionWithEndpoint(t, root, "chat_completions", server.URL, "no_auth")
	memoryStore, characterID := seedCompanionRuntime(t, root)
	bootstrap, err := memoryStore.OpenOrCreateCharacterConversation(characterID)
	if err != nil {
		t.Fatalf("OpenOrCreateCharacterConversation() error = %v", err)
	}
	seedTurn, err := memoryStore.BeginTurn(bootstrap.Conversation.ID, "前一轮")
	if err != nil {
		t.Fatalf("BeginTurn(seed) error = %v", err)
	}
	if _, err := memoryStore.CompleteTurn(bootstrap.Conversation.ID, seedTurn.ID, "上一句。"); err != nil {
		t.Fatalf("CompleteTurn(seed) error = %v", err)
	}
	if _, err := memoryStore.CreatePersonalMemory("preference", memory.MemoryScope{Type: "global"}, "用户不喜欢太甜的饮料", 9000); err != nil {
		t.Fatalf("CreatePersonalMemory() error = %v", err)
	}
	insertKnowledgeFixtureForCompanion(t, root, bootstrap.Conversation.ID, seedTurn.ID)
	service := NewCompanionServiceWithRuntime(root, memoryStore, model.NewModelServiceWithTransport(root, model.HTTPTransport{Client: server.Client()}))

	outcome, err := service.SubmitCompiledTurn(SubmitCompiledTurnRequest{
		ConversationID:        bootstrap.Conversation.ID,
		Input:                 "太甜的饮料推荐 主题陈述系统",
		SpeechEnabled:         true,
		MaxOutputTokens:       160,
		AvailableVisualStates: []VisualState{{ID: "idle", Description: "idle 状态说明"}, {ID: "happy", Description: "happy 状态说明"}},
	})
	if err != nil {
		t.Fatalf("SubmitCompiledTurn() error = %v", err)
	}
	if outcome.ResponseText != "我在。" || outcome.SpeechText != "我在。" || outcome.VisualState != "happy" || !outcome.RespondMigrated {
		t.Fatalf("outcome = %#v", outcome)
	}
	reloaded, err := memoryStore.LoadConversation(bootstrap.Conversation.ID)
	if err != nil {
		t.Fatalf("LoadConversation() error = %v", err)
	}
	if len(reloaded.Messages) != 4 || reloaded.Messages[2].Role != "user" || reloaded.Messages[3].Role != "assistant" {
		t.Fatalf("messages = %#v", reloaded.Messages)
	}
}

func TestCompanionServiceSubmitCompiledTurnFailureKeepsOnlyUserMessage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "provider failed", http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)

	root := t.TempDir()
	writeModelConnectionWithEndpoint(t, root, "chat_completions", server.URL, "no_auth")
	memoryStore, characterID := seedCompanionRuntime(t, root)
	bootstrap, err := memoryStore.OpenOrCreateCharacterConversation(characterID)
	if err != nil {
		t.Fatalf("OpenOrCreateCharacterConversation() error = %v", err)
	}
	service := NewCompanionServiceWithRuntime(root, memoryStore, model.NewModelServiceWithTransport(root, model.HTTPTransport{Client: server.Client()}))

	_, err = service.SubmitCompiledTurn(SubmitCompiledTurnRequest{
		ConversationID:        bootstrap.Conversation.ID,
		Input:                 "你好",
		SpeechEnabled:         true,
		MaxOutputTokens:       160,
		AvailableVisualStates: []VisualState{{ID: "idle", Description: "idle 状态说明"}},
	})
	if err == nil {
		t.Fatal("SubmitCompiledTurn() error = nil, want provider error")
	}
	reloaded, err := memoryStore.LoadConversation(bootstrap.Conversation.ID)
	if err != nil {
		t.Fatalf("LoadConversation() error = %v", err)
	}
	if len(reloaded.Messages) != 1 || reloaded.Messages[0].Role != "user" {
		t.Fatalf("messages = %#v", reloaded.Messages)
	}
}

func TestCompanionServiceCompactConversationCommitsPromptWindow(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"用户问候，角色回应在场。\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
	}))
	t.Cleanup(server.Close)

	root := t.TempDir()
	writeModelConnectionWithEndpoint(t, root, "chat_completions", server.URL, "no_auth")
	memoryStore, characterID := seedCompanionRuntime(t, root)
	bootstrap, err := memoryStore.OpenOrCreateCharacterConversation(characterID)
	if err != nil {
		t.Fatalf("OpenOrCreateCharacterConversation() error = %v", err)
	}
	turn, err := memoryStore.BeginTurn(bootstrap.Conversation.ID, "你好")
	if err != nil {
		t.Fatalf("BeginTurn() error = %v", err)
	}
	if _, err := memoryStore.CompleteTurn(bootstrap.Conversation.ID, turn.ID, "我在。"); err != nil {
		t.Fatalf("CompleteTurn() error = %v", err)
	}
	service := NewCompanionServiceWithRuntime(root, memoryStore, model.NewModelServiceWithTransport(root, model.HTTPTransport{Client: server.Client()}))
	result, err := service.CompactConversation(bootstrap.Conversation.ID)
	if err != nil {
		t.Fatalf("CompactConversation() error = %v", err)
	}
	if result.WindowRevision != 2 {
		t.Fatalf("result = %#v", result)
	}
	reloaded, err := memoryStore.LoadConversation(bootstrap.Conversation.ID)
	if err != nil {
		t.Fatalf("LoadConversation() error = %v", err)
	}
	if reloaded.PromptWindow.Summary == nil || *reloaded.PromptWindow.Summary != "用户问候，角色回应在场。" || reloaded.PromptWindow.CutoffMessageSequence != 2 {
		t.Fatalf("prompt window = %#v", reloaded.PromptWindow)
	}
}
