//go:build integration

package memory

import (
	"context"
	"testing"

	"fairy/context/memory/personal"
	coredb "fairy/runtime/database"
)

func TestPostgresCompanionPortraitIsScopedAndBounded(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := coredb.Migrate(ctx, pool.Raw()); err != nil {
		t.Fatal(err)
	}
	store, err := newMemoryIntegrationStores(pool)
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.OpenOrCreateCharacterConversationContext(ctx, "portrait-character-1")
	if err != nil {
		t.Fatal(err)
	}
	turn, err := store.BeginTurnContext(ctx, first.Conversation.ID, "portrait source")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompleteTurnContext(ctx, first.Conversation.ID, turn.ID, "portrait reply"); err != nil {
		t.Fatal(err)
	}
	second, err := store.OpenOrCreateCharacterConversationContext(ctx, "portrait-character-2")
	if err != nil {
		t.Fatal(err)
	}
	secondTurn, err := store.BeginTurnContext(ctx, second.Conversation.ID, "other portrait source")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompleteTurnContext(ctx, second.Conversation.ID, secondTurn.ID, "other portrait reply"); err != nil {
		t.Fatal(err)
	}
	for _, input := range []struct {
		kind    string
		scope   personal.Scope
		content string
	}{
		{kind: "profile", scope: personal.Scope{Type: "global"}, content: "用户喜欢先听完再回应"},
		{kind: "preference", scope: personal.Scope{Type: "global"}, content: "情绪低落时先陪伴"},
		{kind: "preference", scope: personal.Scope{Type: "global"}, content: "不喜欢被催促"},
		{kind: "preference", scope: personal.Scope{Type: "global"}, content: "第三条偏好不应进入每 kind 两条上限"},
		{kind: "relationship", scope: personal.Scope{Type: "character", CharacterID: "portrait-character-1"}, content: "和角色一有稳定信任"},
		{kind: "relationship", scope: personal.Scope{Type: "character", CharacterID: "portrait-character-2"}, content: "和角色二的关系不得混入"},
	} {
		if _, err := store.CreatePersonalMemoryContext(ctx, input.kind, input.scope, input.content, 9000); err != nil {
			t.Fatal(err)
		}
	}
	portrait, err := store.CompanionPortraitContext(ctx, "portrait-character-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(portrait.PersonalMemories) != 4 {
		t.Fatalf("portrait = %#v", portrait)
	}
	for _, item := range portrait.PersonalMemories {
		if item.ID == "" || item.Scope.Type == "unassigned_legacy" || item.Scope.CharacterID == "portrait-character-2" {
			t.Fatalf("portrait leaked out-of-scope item: %#v", item)
		}
	}
}
