//go:build integration && live

package edge

import (
	"errors"
	"os"
	"strings"
	"testing"

	"fairy/context/character"
	"fairy/context/knowledge"
	"fairy/context/memory/personal"
	"fairy/runtime/config"
	"fairy/transport/session"
)

type endpointLiveSettings struct {
	chatProtocol  string
	chatOrigin    string
	chatModel     string
	chatAPIKey    string
	embedProvider string
	embedOrigin   string
	embedModel    string
	embedAPIKey   string
	openSERP      string
}

func TestLiveEndpointStrictConversationSemanticMemoryIsolationAndRestart(t *testing.T) {
	settings := requireEndpointLiveSettings(t)
	applyEdgeSeekDBEnvironment(t)

	root := t.TempDir()
	writeEdgeVisualPack(t, root, "fairy.endpoint-live")

	// First launch starts from a clean profile. Settings and credentials are
	// saved through the same management API used by Desktop; the test-only
	// environment is only an injection source and is not read by production.
	bootstrap := openEndpointLiveRuntime(t, root)
	management := bootstrap.Management()
	assertEndpointLiveModelSaved(t, management, settings, settings.chatAPIKey)
	assertEndpointLiveSemanticSaved(t, management, settings, settings.embedAPIKey)
	web, err := management.SaveWebSearch(WebSearchWrite{Enabled: true, BaseURL: settings.openSERP})
	if err != nil {
		t.Fatalf("save explicit OpenSERP settings: %v", err)
	}
	if !web.Enabled || !web.Ready || web.BaseURL != strings.TrimRight(settings.openSERP, "/") {
		t.Fatalf("saved OpenSERP status = %#v", web)
	}
	created, err := bootstrap.Core().Character.CreateCharacter(character.Brief{
		Name: "Endpoint Live", Description: "第三方 provider 端侧验收角色", TextLanguage: "zh", SpeakingLanguage: "zh",
	}, "fairy.endpoint-live")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := management.ActivateCharacter(created.CharacterID, created.Revision); err != nil {
		t.Fatal(err)
	}
	closeEndpointLiveRuntime(t, bootstrap)

	full := openEndpointLiveRuntime(t, root)
	overview, err := full.Management().Overview(t.Context())
	if err != nil {
		closeEndpointLiveRuntime(t, full)
		t.Fatal(err)
	}
	if !overview.Storage.Ready || !overview.Model.Ready || !overview.Model.CredentialConfigured ||
		!overview.Semantic.Configured || !overview.Semantic.CredentialConfigured || !overview.WebSearch.Ready {
		closeEndpointLiveRuntime(t, full)
		t.Fatalf("full endpoint readiness = %#v", overview)
	}

	opened, err := full.Facade().OpenSession(t.Context(), session.OpenSessionRequest{
		Endpoint: session.EndpointDesktop, EndpointKey: "endpoint-live-desktop",
		Interaction: session.Context{Audience: session.AudienceSingle, Initiation: session.InitiationDirect, Presentation: session.PresentationChat},
	})
	if err != nil {
		closeEndpointLiveRuntime(t, full)
		t.Fatal(err)
	}
	firstTurn, err := full.Facade().SubmitTurn(t.Context(), opened.ConversationID, session.SubmitTurnRequest{
		Input: "请用一句自然的话确认第三方聊天 provider 已经接入。",
	})
	if err != nil {
		closeEndpointLiveRuntime(t, full)
		t.Fatalf("submit live third-party turn: %v", err)
	}
	if firstTurn.Outcome.ConversationID != opened.ConversationID || firstTurn.Outcome.TurnID == "" || strings.TrimSpace(firstTurn.Outcome.ResponseText) == "" {
		closeEndpointLiveRuntime(t, full)
		t.Fatalf("live turn outcome = %#v", firstTurn.Outcome)
	}

	memoryContent := "endpoint-live-semantic-aurora：第三方 1024 维向量记忆必须在重启后保留。"
	memory, err := full.Management().CreateMemory(MemoryWrite{
		Kind: "preference", Scope: personal.Scope{Type: "global"}, Content: memoryContent, ConfidenceBasisPoints: 9100,
	})
	if err != nil {
		closeEndpointLiveRuntime(t, full)
		t.Fatalf("write live semantic memory: %v", err)
	}
	assertEndpointLiveMemoryTuple(t, full, memory.ID)
	retrieved, err := full.Core().MemoryStore.RetrieveContext(t.Context(), created.CharacterID, "semantic aurora 1024 vector memory")
	if err != nil || !endpointLiveRetrievalContains(retrieved, memory.ID) {
		closeEndpointLiveRuntime(t, full)
		t.Fatalf("retrieve live semantic memory = (%#v, %v)", retrieved, err)
	}

	search, ok := full.Core().WebSearch.(*knowledge.WebSearchService)
	if !ok || search == nil {
		closeEndpointLiveRuntime(t, full)
		t.Fatalf("endpoint OpenSERP backend = %T", full.Core().WebSearch)
	}
	hits, err := search.Search(t.Context(), "The Go Programming Language golang.google.cn", 5)
	if err != nil || len(hits) == 0 {
		closeEndpointLiveRuntime(t, full)
		t.Fatalf("search through live OpenSERP = (%d hits, %v)", len(hits), err)
	}
	if _, err := search.FetchSource(t.Context(), knowledge.IngestSource{ID: "endpoint-live-source", URL: hits[0].URL, Title: hits[0].Title}); err != nil {
		closeEndpointLiveRuntime(t, full)
		t.Fatalf("extract through live OpenSERP: %v", err)
	}
	closeEndpointLiveRuntime(t, full)

	// A normal restart must restore the exact local conversation and memory
	// before either optional capability is degraded.
	restarted := openEndpointLiveRuntime(t, root)
	reopened, err := restarted.Facade().OpenSession(t.Context(), session.OpenSessionRequest{
		Endpoint: session.EndpointDesktop, EndpointKey: "endpoint-live-desktop",
		Interaction: session.Context{Audience: session.AudienceSingle, Initiation: session.InitiationDirect, Presentation: session.PresentationChat},
	})
	if err != nil || reopened.ConversationID != opened.ConversationID {
		closeEndpointLiveRuntime(t, restarted)
		t.Fatalf("restart session = (%#v, %v)", reopened, err)
	}
	page, err := restarted.Facade().ListMessages(t.Context(), reopened.ConversationID, 0, 50)
	if err != nil || !messagePageContains(page, "请用一句自然的话确认第三方聊天 provider 已经接入。") ||
		!messagePageContains(page, firstTurn.Outcome.ResponseText) {
		closeEndpointLiveRuntime(t, restarted)
		t.Fatalf("restart history = (%#v, %v)", page, err)
	}
	memories, err := restarted.Management().Memories(created.CharacterID)
	if err != nil || !memoryCatalogContains(memories, memory.ID) {
		closeEndpointLiveRuntime(t, restarted)
		t.Fatalf("restart memories = (%#v, %v)", memories, err)
	}
	assertEndpointLiveMemoryTuple(t, restarted, memory.ID)

	// Persist a blocked OpenSERP origin, then reopen. Search alone becomes
	// unavailable; the local database, management, history and memories remain.
	blockedWeb, err := restarted.Management().SaveWebSearch(WebSearchWrite{Enabled: true, BaseURL: "http://127.0.0.1:1"})
	if err != nil || !blockedWeb.Enabled || blockedWeb.Ready {
		closeEndpointLiveRuntime(t, restarted)
		t.Fatalf("save blocked OpenSERP = (%#v, %v)", blockedWeb, err)
	}
	closeEndpointLiveRuntime(t, restarted)

	degraded := openEndpointLiveRuntime(t, root)
	if _, err := degraded.Core().WebSearch.Search(t.Context(), "must not fall back", 1); !errors.Is(err, knowledge.ErrSearchEndpointNotConfigured) {
		closeEndpointLiveRuntime(t, degraded)
		t.Fatalf("blocked OpenSERP error = %v, want %v", err, knowledge.ErrSearchEndpointNotConfigured)
	}
	assertEndpointLiveLocalState(t, degraded, created.CharacterID, opened.ConversationID, memory.ID)

	// Replacing only the saved credential makes each third-party model
	// capability fail closed. It must not select a local model or damage local
	// state. Restore the valid credentials before the final restart.
	assertEndpointLiveModelSaved(t, degraded.Management(), settings, "fairy-intentionally-invalid-chat-key")
	if _, err := degraded.Facade().SubmitTurn(t.Context(), opened.ConversationID, session.SubmitTurnRequest{Input: "这条请求必须因聊天 provider 被阻断而失败。"}); err == nil {
		closeEndpointLiveRuntime(t, degraded)
		t.Fatal("blocked chat provider unexpectedly completed a turn")
	}
	assertEndpointLiveLocalState(t, degraded, created.CharacterID, opened.ConversationID, memory.ID)
	assertEndpointLiveModelSaved(t, degraded.Management(), settings, settings.chatAPIKey)

	assertEndpointLiveSemanticSaved(t, degraded.Management(), settings, "fairy-intentionally-invalid-embedding-key")
	failedMemoryContent := "endpoint-live-blocked-embedding-must-not-persist"
	if _, err := degraded.Management().CreateMemory(MemoryWrite{
		Kind: "preference", Scope: personal.Scope{Type: "global"}, Content: failedMemoryContent, ConfidenceBasisPoints: 9000,
	}); err == nil {
		closeEndpointLiveRuntime(t, degraded)
		t.Fatal("blocked embedding provider unexpectedly wrote a memory")
	}
	if catalog, err := degraded.Management().Memories(created.CharacterID); err != nil || endpointLiveCatalogContainsContent(catalog, failedMemoryContent) {
		closeEndpointLiveRuntime(t, degraded)
		t.Fatalf("blocked embedding local catalog = (%#v, %v)", catalog, err)
	}
	assertEndpointLiveSemanticSaved(t, degraded.Management(), settings, settings.embedAPIKey)
	closeEndpointLiveRuntime(t, degraded)

	finalRuntime := openEndpointLiveRuntime(t, root)
	t.Cleanup(func() { closeEndpointLiveRuntime(t, finalRuntime) })
	assertEndpointLiveLocalState(t, finalRuntime, created.CharacterID, opened.ConversationID, memory.ID)
	finalOverview, err := finalRuntime.Management().Overview(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !finalOverview.Model.Ready || !finalOverview.Semantic.Configured || !finalOverview.Semantic.CredentialConfigured || finalOverview.WebSearch.Ready {
		t.Fatalf("final isolated readiness = %#v", finalOverview)
	}
	finalTurn, err := finalRuntime.Facade().SubmitTurn(t.Context(), opened.ConversationID, session.SubmitTurnRequest{
		Input: "OpenSERP 不可用时，请仍用一句话回复。",
	})
	if err != nil || strings.TrimSpace(finalTurn.Outcome.ResponseText) == "" {
		t.Fatalf("chat after isolated OpenSERP failure = (%#v, %v)", finalTurn.Outcome, err)
	}
}

func requireEndpointLiveSettings(t *testing.T) endpointLiveSettings {
	t.Helper()
	settings := endpointLiveSettings{
		chatProtocol:  strings.TrimSpace(os.Getenv("FAIRY_CHAT_TEST_PROTOCOL")),
		chatOrigin:    strings.TrimSpace(os.Getenv("FAIRY_CHAT_TEST_BASE_URL")),
		chatModel:     strings.TrimSpace(os.Getenv("FAIRY_CHAT_TEST_MODEL")),
		chatAPIKey:    strings.TrimSpace(os.Getenv("FAIRY_CHAT_TEST_API_KEY")),
		embedProvider: strings.TrimSpace(os.Getenv("FAIRY_EMBEDDING_TEST_PROVIDER")),
		embedOrigin:   strings.TrimSpace(os.Getenv("FAIRY_EMBEDDING_TEST_BASE_URL")),
		embedModel:    strings.TrimSpace(os.Getenv("FAIRY_EMBEDDING_TEST_MODEL")),
		embedAPIKey:   strings.TrimSpace(os.Getenv("FAIRY_EMBEDDING_TEST_API_KEY")),
		openSERP:      strings.TrimSpace(os.Getenv("FAIRY_OPENSERP_TEST_ORIGIN")),
	}
	if settings.chatProtocol == "" {
		settings.chatProtocol = "chat_completions"
	}
	if settings.embedProvider == "" {
		settings.embedProvider = config.SemanticEmbeddingProviderOpenAICompatible
	}
	mandatory := map[string]string{
		"FAIRY_CHAT_TEST_BASE_URL":      settings.chatOrigin,
		"FAIRY_CHAT_TEST_MODEL":         settings.chatModel,
		"FAIRY_CHAT_TEST_API_KEY":       settings.chatAPIKey,
		"FAIRY_EMBEDDING_TEST_BASE_URL": settings.embedOrigin,
		"FAIRY_EMBEDDING_TEST_MODEL":    settings.embedModel,
		"FAIRY_EMBEDDING_TEST_API_KEY":  settings.embedAPIKey,
		"FAIRY_OPENSERP_TEST_ORIGIN":    settings.openSERP,
	}
	configured := 0
	missing := make([]string, 0, len(mandatory))
	for name, value := range mandatory {
		if value == "" {
			missing = append(missing, name)
		} else {
			configured++
		}
	}
	if configured == 0 {
		t.Skip("no explicit endpoint live acceptance settings")
	}
	if len(missing) != 0 {
		t.Fatalf("endpoint live acceptance settings are incomplete; missing %s", strings.Join(missing, ", "))
	}
	if err := config.ValidateEndpointStrictProviderURL(settings.chatOrigin); err != nil {
		t.Fatalf("chat origin is not endpoint-strict: %v", err)
	}
	if err := config.ValidateEndpointStrictProviderURL(settings.embedOrigin); err != nil {
		t.Fatalf("embedding origin is not endpoint-strict: %v", err)
	}
	return settings
}

func openEndpointLiveRuntime(t *testing.T, root string) *Runtime {
	t.Helper()
	runtime, err := OpenEndpointStrict(t.Context(), Options{ConfigRoot: root, Profile: ProfileEndpointStrict})
	if err != nil {
		t.Fatal(err)
	}
	return runtime
}

func closeEndpointLiveRuntime(t *testing.T, runtime *Runtime) {
	t.Helper()
	if runtime == nil {
		return
	}
	if err := runtime.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func assertEndpointLiveModelSaved(t *testing.T, management *Management, settings endpointLiveSettings, apiKey string) {
	t.Helper()
	status, err := management.SaveModel(ModelWrite{
		ModelConnectionInput: config.ModelConnectionInput{
			Protocol: settings.chatProtocol, Endpoint: settings.chatOrigin, Model: settings.chatModel,
			ContextWindowTokens: 32768, AuthMode: "bearer_key",
		},
		APIKey: apiKey,
	})
	if err != nil {
		t.Fatalf("save explicit chat provider: %v", err)
	}
	if !status.Configured || !status.Ready || !status.CredentialConfigured || status.Endpoint != strings.TrimRight(settings.chatOrigin, "/") {
		t.Fatalf("saved chat status = %#v", status)
	}
}

func assertEndpointLiveSemanticSaved(t *testing.T, management *Management, settings endpointLiveSettings, apiKey string) {
	t.Helper()
	status, err := management.SaveSemantic(SemanticWrite{
		Provider: settings.embedProvider, Enabled: true, Endpoint: settings.embedOrigin,
		Model: settings.embedModel, APIKey: apiKey,
	})
	if err != nil {
		t.Fatalf("save explicit embedding provider: %v", err)
	}
	if !status.Enabled || !status.Configured || !status.CredentialConfigured || status.Dimensions != config.SemanticEmbeddingDimensions ||
		status.Endpoint != strings.TrimRight(settings.embedOrigin, "/") {
		t.Fatalf("saved embedding status = %#v", status)
	}
}

func assertEndpointLiveMemoryTuple(t *testing.T, runtime *Runtime, memoryID string) {
	t.Helper()
	database, err := runtime.Core().Foundation.SQL()
	if err != nil {
		t.Fatal(err)
	}
	var spaceID string
	var hashBytes int
	var hasEmbedding int
	if err := database.QueryRowContext(t.Context(), `
SELECT embedding_space_id, OCTET_LENGTH(embedding_content_hash), IF(embedding IS NULL, 0, 1)
FROM personal_memories
WHERE id = ?
`, memoryID).Scan(&spaceID, &hashBytes, &hasEmbedding); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(spaceID) == "" || hashBytes != 32 || hasEmbedding != 1 {
		t.Fatalf("memory embedding tuple = (space=%q hashBytes=%d vector=%d)", spaceID, hashBytes, hasEmbedding)
	}
}

func assertEndpointLiveLocalState(t *testing.T, runtime *Runtime, characterID, conversationID, memoryID string) {
	t.Helper()
	storage, err := runtime.Core().StorageStatus(t.Context())
	if err != nil || !storage.Ready || storage.Storage != "seekdb" {
		t.Fatalf("local storage status = (%#v, %v)", storage, err)
	}
	page, err := runtime.Facade().ListMessages(t.Context(), conversationID, 0, 50)
	if err != nil || len(page.Messages) == 0 {
		t.Fatalf("local history = (%#v, %v)", page, err)
	}
	catalog, err := runtime.Management().Memories(characterID)
	if err != nil || !memoryCatalogContains(catalog, memoryID) {
		t.Fatalf("local memories = (%#v, %v)", catalog, err)
	}
}

func endpointLiveRetrievalContains(retrieval personal.Retrieval, memoryID string) bool {
	for _, record := range retrieval.PersonalMemories {
		if record.ID == memoryID {
			return true
		}
	}
	return false
}

func endpointLiveCatalogContainsContent(catalog personal.Catalog, content string) bool {
	for _, records := range [][]personal.Record{catalog.Global, catalog.Character, catalog.NeedsReview} {
		for _, record := range records {
			if record.Content == content {
				return true
			}
		}
	}
	return false
}
