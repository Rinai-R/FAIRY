package companion

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"fairy/character"
	"fairy/config"
	"fairy/memory"
	"fairy/model"
	"fairy/search"
)

type toolCallRecordingTransport struct {
	inner     model.Transport
	mu        sync.Mutex
	drafts    []model.RequestDraft
	allEvents []model.StreamEvent
	calls     int
}

func (t *toolCallRecordingTransport) Execute(ctx context.Context, draft model.RequestDraft, bearerKey string, onEvent func(model.StreamEvent)) error {
	t.mu.Lock()
	t.calls++
	t.drafts = append(t.drafts, draft)
	t.mu.Unlock()
	return t.inner.Execute(ctx, draft, bearerKey, func(event model.StreamEvent) {
		t.mu.Lock()
		t.allEvents = append(t.allEvents, event)
		t.mu.Unlock()
		onEvent(event)
	})
}

func (t *toolCallRecordingTransport) snapshot() (calls int, drafts []model.RequestDraft, events []model.StreamEvent) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.calls, append([]model.RequestDraft(nil), t.drafts...), append([]model.StreamEvent(nil), t.allEvents...)
}

func manualConfigRoot(t *testing.T) string {
	t.Helper()
	if root := os.Getenv("FAIRY_MANUAL_CONFIG_ROOT"); root != "" {
		return root
	}
	if root := os.Getenv("FAIRY_CONFIG_ROOT"); root != "" {
		return root
	}
	t.Skip("set FAIRY_MANUAL_CONFIG_ROOT (or FAIRY_CONFIG_ROOT) to run live tool-call tests with your model key")
	return ""
}

func draftHasTool(draft model.RequestDraft, name string) bool {
	var body map[string]any
	if err := json.Unmarshal([]byte(draft.BodyJSON), &body); err != nil {
		return false
	}
	tools, _ := body["tools"].([]any)
	for _, raw := range tools {
		item, _ := raw.(map[string]any)
		if item == nil {
			continue
		}
		if item["name"] == name {
			return true
		}
		if fn, ok := item["function"].(map[string]any); ok && fn["name"] == name {
			return true
		}
	}
	return false
}

func functionCallNames(events []model.StreamEvent) []string {
	names := make([]string, 0)
	for _, call := range model.FunctionCallsFromEvents(events) {
		names = append(names, call.Name)
	}
	return names
}

func TestManualToolCallAdvertisesMemoryAndWebTools(t *testing.T) {
	root := manualConfigRoot(t)
	if err := config.WriteWebSearchSettings(root, config.WebSearchSettings{SchemaVersion: 1, Enabled: true}); err != nil {
		t.Fatalf("WriteWebSearchSettings() error = %v", err)
	}
	catalog, err := character.NewStore(root).List()
	if err != nil {
		t.Fatalf("ListCharacters() error = %v", err)
	}
	if catalog.Active == nil {
		t.Fatal("active character is required")
	}
	memoryStore, err := memory.OpenOrCreate(filepath.Join(root, memory.RelativePath))
	if err != nil {
		t.Fatalf("OpenOrCreate(memory) error = %v", err)
	}
	bootstrap, err := memoryStore.OpenOrCreateCharacterConversation(catalog.Active.CharacterID)
	if err != nil {
		t.Fatalf("OpenOrCreateCharacterConversation() error = %v", err)
	}
	transport := &toolCallRecordingTransport{inner: model.SDKTransport{}}
	service := NewCompanionServiceWithRuntime(root, memoryStore, model.NewModelServiceWithTransport(root, transport))

	outcome, err := service.SubmitTurn(SubmitTurnRequest{
		ConversationID: bootstrap.Conversation.ID,
		Input:          "随便聊一句就好，不用搜。",
		SpeechEnabled:  false,
	})
	calls, drafts, events := transport.snapshot()
	if err != nil {
		t.Fatalf("SubmitTurn() error = %v calls=%d text=%q", err, calls, collectText(events))
	}
	if calls < 1 || len(drafts) < 1 {
		t.Fatalf("expected at least one model call, got calls=%d", calls)
	}
	if !draftHasTool(drafts[0], toolMemorySearch) {
		t.Fatalf("first draft missing memory.search tools body=%s", drafts[0].BodyJSON)
	}
	if !draftHasTool(drafts[0], toolWebSearch) {
		t.Fatalf("first draft missing web.search tools body=%s", drafts[0].BodyJSON)
	}
	if strings.TrimSpace(outcome.ResponseText) == "" {
		t.Fatalf("empty outcome %#v", outcome)
	}
	t.Logf("advertise-tools ok response=%q modelCalls=%d functionCalls=%v", outcome.ResponseText, calls, functionCallNames(events))
}

func TestManualToolCallWebSearchAgentLoop(t *testing.T) {
	root := manualConfigRoot(t)
	if err := config.WriteWebSearchSettings(root, config.WebSearchSettings{SchemaVersion: 1, Enabled: true}); err != nil {
		t.Fatalf("WriteWebSearchSettings() error = %v", err)
	}
	if _, found := search.ResolveBinary(root); !found {
		t.Skip("openserp binary not found under config bin/ or FAIRY_OPENSERP_PATH")
	}
	catalog, err := character.NewStore(root).List()
	if err != nil {
		t.Fatalf("ListCharacters() error = %v", err)
	}
	if catalog.Active == nil {
		t.Fatal("active character is required")
	}
	memoryStore, err := memory.OpenOrCreate(filepath.Join(root, memory.RelativePath))
	if err != nil {
		t.Fatalf("OpenOrCreate(memory) error = %v", err)
	}
	bootstrap, err := memoryStore.OpenOrCreateCharacterConversation(catalog.Active.CharacterID)
	if err != nil {
		t.Fatalf("OpenOrCreateCharacterConversation() error = %v", err)
	}
	transport := &toolCallRecordingTransport{inner: model.SDKTransport{}}
	service := NewCompanionServiceWithRuntime(root, memoryStore, model.NewModelServiceWithTransport(root, transport))

	outcome, err := service.SubmitTurn(SubmitTurnRequest{
		ConversationID: bootstrap.Conversation.ID,
		Input:          "现在 Go 语言最新稳定版本号是多少？不要用训练记忆猜，必须先查公开资料再回答。",
		SpeechEnabled:  false,
	})
	calls, drafts, events := transport.snapshot()
	names := functionCallNames(events)
	if err != nil {
		t.Fatalf("SubmitTurn() error = %v calls=%d fn=%v text=%q", err, calls, names, collectText(events))
	}
	if len(drafts) == 0 || !draftHasTool(drafts[0], toolWebSearch) {
		t.Fatalf("web.search not advertised on first request")
	}
	hasWeb := false
	for _, name := range names {
		if name == toolWebSearch {
			hasWeb = true
			break
		}
	}
	if !hasWeb {
		t.Fatalf("expected web.search function_call, got names=%v modelCalls=%d response=%q", names, calls, outcome.ResponseText)
	}
	if calls < 2 {
		t.Fatalf("expected tool then reply (>=2 model calls), got %d response=%q", calls, outcome.ResponseText)
	}
	if strings.TrimSpace(outcome.ResponseText) == "" || len(outcome.Chains) == 0 {
		t.Fatalf("outcome = %#v", outcome)
	}
	ledger, err := memoryStore.ListTurnRuntimeEvents(outcome.ConversationID, outcome.TurnID)
	if err != nil {
		t.Fatalf("ListTurnRuntimeEvents() error = %v", err)
	}
	if !runtimeLedgerContainsType(ledger, runtimeLedgerEventTool) {
		t.Fatalf("runtime ledger missing tool event fn=%v response=%q", names, outcome.ResponseText)
	}
	if !runtimeLedgerMetadataContains(ledger, runtimeLedgerEventTool, toolWebSearch) {
		t.Fatalf("runtime ledger missing web_search tool metadata fn=%v", names)
	}
	t.Logf("web.search loop ok modelCalls=%d fn=%v response=%q", calls, names, outcome.ResponseText)
}

func TestManualToolCallMemorySearchAgentLoop(t *testing.T) {
	root := manualConfigRoot(t)
	catalog, err := character.NewStore(root).List()
	if err != nil {
		t.Fatalf("ListCharacters() error = %v", err)
	}
	if catalog.Active == nil {
		t.Fatal("active character is required")
	}
	memoryStore, err := memory.OpenOrCreate(filepath.Join(root, memory.RelativePath))
	if err != nil {
		t.Fatalf("OpenOrCreate(memory) error = %v", err)
	}
	marker := fmt.Sprintf("FAIRY_TOOLCALL_MARKER_%d", time.Now().Unix())
	fact := "用户测试暗号是" + marker + "，仅用于 tool call 集成测试。"
	if _, err := memoryStore.CreatePersonalMemory("preference", memory.MemoryScope{Type: "global"}, fact, 9500); err != nil {
		t.Fatalf("CreatePersonalMemory() error = %v", err)
	}
	bootstrap, err := memoryStore.OpenOrCreateCharacterConversation(catalog.Active.CharacterID)
	if err != nil {
		t.Fatalf("OpenOrCreateCharacterConversation() error = %v", err)
	}
	transport := &toolCallRecordingTransport{inner: model.SDKTransport{}}
	service := NewCompanionServiceWithRuntime(root, memoryStore, model.NewModelServiceWithTransport(root, transport))

	outcome, err := service.SubmitTurn(SubmitTurnRequest{
		ConversationID: bootstrap.Conversation.ID,
		Input:          "我的测试暗号是什么？不要瞎猜，先查个人记忆再回答，把暗号原文念出来。",
		SpeechEnabled:  false,
	})
	calls, drafts, events := transport.snapshot()
	names := functionCallNames(events)
	if err != nil {
		t.Fatalf("SubmitTurn() error = %v calls=%d fn=%v text=%q", err, calls, names, collectText(events))
	}
	if len(drafts) == 0 || !draftHasTool(drafts[0], toolMemorySearch) {
		t.Fatalf("memory.search not advertised on first request")
	}
	hasMemory := false
	for _, name := range names {
		if name == toolMemorySearch {
			hasMemory = true
			break
		}
	}
	if !hasMemory {
		t.Fatalf("expected memory.search function_call, got names=%v modelCalls=%d response=%q", names, calls, outcome.ResponseText)
	}
	if calls < 2 {
		t.Fatalf("expected tool then reply (>=2 model calls), got %d response=%q", calls, outcome.ResponseText)
	}
	if !strings.Contains(outcome.ResponseText, marker) {
		t.Fatalf("response missing memory marker %q response=%q", marker, outcome.ResponseText)
	}
	t.Logf("memory.search loop ok modelCalls=%d fn=%v response=%q", calls, names, outcome.ResponseText)
}
