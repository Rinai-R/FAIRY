package companion

import (
	"context"
	"errors"
	"strings"
	"testing"

	"fairy/model"
	"fairy/session"
	"fairy/sticker"
)

type stickerSearchStub struct {
	active       bool
	activeErr    error
	activeCalls  int
	searchErr    error
	searchCalls  int
	searchQuery  string
	searchLimit  int
	searchResult []sticker.Candidate
}

func (stub *stickerSearchStub) HasActive(context.Context) (bool, error) {
	stub.activeCalls++
	return stub.active, stub.activeErr
}

func (stub *stickerSearchStub) Search(_ context.Context, query string, limit int) ([]sticker.Candidate, error) {
	stub.searchCalls++
	stub.searchQuery = query
	stub.searchLimit = limit
	return append([]sticker.Candidate(nil), stub.searchResult...), stub.searchErr
}

func TestStickerToolRequiresExplicitCapabilityAndActiveLibrary(t *testing.T) {
	stub := &stickerSearchStub{active: true}
	enabled, err := stickerToolAvailable(t.Context(), session.OutputCapabilities{}, stub)
	if err != nil || enabled || stub.activeCalls != 0 {
		t.Fatalf("unsupported session availability = %v, %v; active calls = %d", enabled, err, stub.activeCalls)
	}
	enabled, err = stickerToolAvailable(t.Context(), session.OutputCapabilities{Sticker: true}, stub)
	if err != nil || !enabled || stub.activeCalls != 1 {
		t.Fatalf("supported session availability = %v, %v; active calls = %d", enabled, err, stub.activeCalls)
	}
	stub.active = false
	enabled, err = stickerToolAvailable(t.Context(), session.OutputCapabilities{Sticker: true}, stub)
	if err != nil || enabled {
		t.Fatalf("empty library availability = %v, %v", enabled, err)
	}
	stub.activeErr = errors.New("db unavailable")
	if _, err := stickerToolAvailable(t.Context(), session.OutputCapabilities{Sticker: true}, stub); err == nil {
		t.Fatal("library availability error was hidden")
	}
}

func TestStickerToolIsAvailableForPrivateAndPublicInteractions(t *testing.T) {
	for name, resolved := range map[string]session.Resolved{
		"private": desktopResolved(),
		"public":  publicAmbientResolved(),
	} {
		t.Run(name, func(t *testing.T) {
			tools := respondToolSpecsForRuntime(false, resolved, false, true)
			if tools[len(tools)-1].Name != toolStickerSearch {
				t.Fatalf("runtime tools = %#v", tools)
			}
		})
	}
}

func TestStickerInstructionsReplaceLegacyTextOnlySchemaOnlyWhenEnabled(t *testing.T) {
	plain := respondInstructionsForInteraction(true, desktopResolved())
	if got := stickerExpressionInstructions(plain, false); got != plain {
		t.Fatal("unsupported session instructions changed")
	}
	withSticker := stickerExpressionInstructions(plain, true)
	for _, required := range []string{`"kind":"utterance"`, `"kind":"sticker"`, `"stickerId"`, "at most one chain may be sticker"} {
		if !strings.Contains(withSticker, required) {
			t.Fatalf("sticker instructions missing %q: %s", required, withSticker)
		}
	}
	if strings.Contains(withSticker, "each chain only visualState/text") {
		t.Fatal("sticker instructions retained conflicting text-only schema")
	}
}

func TestStickerCandidatesAreBoundedToCurrentTurn(t *testing.T) {
	stub := &stickerSearchStub{searchResult: []sticker.Candidate{
		{ID: "sticker-1", Description: "震惊", Tags: []string{"震惊"}, MIMEType: "image/png"},
		{ID: "sticker-2", Description: "无语", Tags: []string{"无语"}, MIMEType: "image/gif"},
	}}
	set := make(stickerCandidateSet)
	candidates, err := searchStickerCandidates(t.Context(), stub, set, "震惊 无语")
	if err != nil {
		t.Fatal(err)
	}
	if stub.searchCalls != 1 || stub.searchQuery != "震惊 无语" || stub.searchLimit != stickerCandidatesLimit {
		t.Fatalf("search = calls:%d query:%q limit:%d", stub.searchCalls, stub.searchQuery, stub.searchLimit)
	}
	if len(candidates) != 2 || !set.contains("sticker-1") || !set.contains("sticker-2") {
		t.Fatalf("candidates = %#v, set = %#v", candidates, set)
	}
	if set.contains("sticker-not-returned") {
		t.Fatal("candidate set authorized an ID not returned during this turn")
	}

	items := stickerToolPromptItems("call-1", `{"query":"震惊 无语"}`, candidates, nil)
	if len(items) != 2 || items[0].Type != model.PromptItemToolCall || items[1].Type != model.PromptItemToolResult {
		t.Fatalf("tool prompt items = %#v", items)
	}
	result := (*items[1].Parts)[0].Text
	if !strings.Contains(result, `"id":"sticker-1"`) || !strings.Contains(result, `"description":"震惊"`) || strings.Contains(result, "image/png") || strings.Contains(result, "mimeType") {
		t.Fatalf("unsafe or incomplete sticker result = %s", result)
	}

	freshTurn := make(stickerCandidateSet)
	if freshTurn.contains("sticker-1") {
		t.Fatal("candidate authorization leaked across turns")
	}
}

func TestStickerCandidateSetHasHardLimit(t *testing.T) {
	set := make(stickerCandidateSet)
	for index := 0; index < maxStickerCandidates-1; index++ {
		id := string(rune('a' + index))
		set[id] = sticker.Candidate{ID: id}
	}
	stub := &stickerSearchStub{searchResult: []sticker.Candidate{{ID: "last"}, {ID: "overflow"}}}
	added, err := searchStickerCandidates(t.Context(), stub, set, "reaction")
	if err != nil {
		t.Fatal(err)
	}
	if stub.searchLimit != 1 || len(added) != 1 || added[0].ID != "last" || set.contains("overflow") {
		t.Fatalf("added = %#v, set size = %d, limit = %d", added, len(set), stub.searchLimit)
	}
}
