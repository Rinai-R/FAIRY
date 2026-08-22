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

func TestLiveEndpointStrictOpenSERPAvailableAndBlockedIsolation(t *testing.T) {
	origin := strings.TrimSpace(os.Getenv("FAIRY_OPENSERP_TEST_ORIGIN"))
	if origin == "" {
		t.Skip("no explicit live OpenSERP origin")
	}
	applyEdgeSeekDBEnvironment(t)

	root := t.TempDir()
	writeEdgeVisualPack(t, root, "fairy.openserp-live")
	settings := config.NewConfigService(root, nil)
	status, err := settings.SaveEndpointWebSearchSettings(config.WebSearchSettings{Enabled: true, BaseURL: origin})
	if err != nil {
		t.Fatalf("save declared OpenSERP origin: %v", err)
	}
	if !status.Enabled || !status.Ready || status.BaseURL != strings.TrimRight(origin, "/") {
		t.Fatalf("declared OpenSERP status = %#v", status)
	}

	first, err := OpenEndpointStrict(t.Context(), Options{ConfigRoot: root, Profile: ProfileEndpointStrict})
	if err != nil {
		t.Fatal(err)
	}
	search, ok := first.Core().WebSearch.(*knowledge.WebSearchService)
	if !ok || search == nil {
		first.Close(t.Context())
		t.Fatalf("endpoint-strict OpenSERP backend = %T", first.Core().WebSearch)
	}
	hits, err := search.Search(t.Context(), "The Go Programming Language golang.google.cn", 5)
	if err != nil {
		first.Close(t.Context())
		t.Fatalf("search through declared OpenSERP origin: %v", err)
	}
	var source knowledge.IngestSource
	for _, hit := range hits {
		if strings.HasPrefix(hit.URL, "https://golang.google.cn/") {
			source = knowledge.IngestSource{ID: "edge-live-openserp-source", URL: hit.URL, Title: hit.Title}
			break
		}
	}
	if source.ID == "" {
		first.Close(t.Context())
		t.Fatal("declared OpenSERP origin did not return the expected extractable public source")
	}
	if document, err := search.FetchSource(t.Context(), source); err != nil || strings.TrimSpace(document.Content) == "" {
		first.Close(t.Context())
		t.Fatalf("extract through declared OpenSERP origin = (%d bytes, %v)", len(document.Content), err)
	}

	management := first.Management()
	created, err := first.Core().Character.CreateCharacter(character.Brief{
		Name: "OpenSERP", Description: "OpenSERP 隔离验收角色", TextLanguage: "zh", SpeakingLanguage: "zh",
	}, "fairy.openserp-live")
	if err != nil {
		first.Close(t.Context())
		t.Fatal(err)
	}
	if _, err := management.ActivateCharacter(created.CharacterID, created.Revision); err != nil {
		first.Close(t.Context())
		t.Fatal(err)
	}
	opened, err := first.Facade().OpenSession(t.Context(), session.OpenSessionRequest{
		Endpoint:    session.EndpointDesktop,
		EndpointKey: "openserp-live-desktop",
		Interaction: session.Context{
			Audience: session.AudienceSingle, Initiation: session.InitiationDirect, Presentation: session.PresentationChat,
		},
	})
	if err != nil {
		first.Close(t.Context())
		t.Fatal(err)
	}
	turn, err := first.Core().TranscriptStore.BeginTurnContext(t.Context(), opened.ConversationID, "OpenSERP 可用时写入的本地消息。")
	if err != nil {
		first.Close(t.Context())
		t.Fatal(err)
	}
	if _, err := first.Core().TranscriptStore.CompleteTurnContext(t.Context(), opened.ConversationID, turn.ID, "本地消息已完成。即使 OpenSERP 阻断也必须保留。"); err != nil {
		first.Close(t.Context())
		t.Fatal(err)
	}
	memory, err := management.CreateMemory(MemoryWrite{
		Kind: "preference", Scope: personal.Scope{Type: "global"},
		Content: "OpenSERP 阻断时本地记忆与管理仍然可用。", ConfidenceBasisPoints: 8500,
	})
	if err != nil {
		first.Close(t.Context())
		t.Fatal(err)
	}
	if err := first.Close(t.Context()); err != nil {
		t.Fatal(err)
	}

	blockedStatus, err := settings.SaveEndpointWebSearchSettings(config.WebSearchSettings{
		Enabled: true, BaseURL: "http://127.0.0.1:1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !blockedStatus.Enabled || blockedStatus.Ready || blockedStatus.BaseURL != "http://127.0.0.1:1" {
		t.Fatalf("blocked but configured OpenSERP status = %#v", blockedStatus)
	}
	second, err := OpenEndpointStrict(t.Context(), Options{ConfigRoot: root, Profile: ProfileEndpointStrict})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := second.Close(t.Context()); err != nil {
			t.Error(err)
		}
	})
	if _, err := second.Core().WebSearch.Search(t.Context(), "must not fall back", 1); !errors.Is(err, knowledge.ErrSearchEndpointNotConfigured) {
		t.Fatalf("blocked OpenSERP search error = %v, want %v", err, knowledge.ErrSearchEndpointNotConfigured)
	}
	storage, err := second.Core().StorageStatus(t.Context())
	if err != nil || !storage.Ready || storage.Storage != "seekdb" {
		t.Fatalf("blocked OpenSERP local storage = (%#v, %v)", storage, err)
	}
	reopened, err := second.Facade().OpenSession(t.Context(), session.OpenSessionRequest{
		Endpoint:    session.EndpointDesktop,
		EndpointKey: "openserp-live-desktop",
		Interaction: session.Context{
			Audience: session.AudienceSingle, Initiation: session.InitiationDirect, Presentation: session.PresentationChat,
		},
	})
	if err != nil || reopened.ConversationID != opened.ConversationID {
		t.Fatalf("blocked OpenSERP local session = (%#v, %v)", reopened, err)
	}
	page, err := second.Management().Conversation(t.Context(), reopened.ConversationID, 0, 20)
	if err != nil || !messagePageContains(page, "OpenSERP 可用时写入的本地消息。") {
		t.Fatalf("blocked OpenSERP local history = (%#v, %v)", page, err)
	}
	memories, err := second.Management().Memories(created.CharacterID)
	if err != nil || !memoryCatalogContains(memories, memory.ID) {
		t.Fatalf("blocked OpenSERP local memories = (%#v, %v)", memories, err)
	}
}
