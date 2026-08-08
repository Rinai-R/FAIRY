//go:build integration

package memory

import (
	"context"
	"testing"

	coredb "fairy/runtime/database"
)

func TestPostgresExpressionPartsPersistAtomicallyIntegration(t *testing.T) {
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
	bootstrap, err := store.OpenOrCreateCharacterConversationContext(ctx, "character-expression-history")
	if err != nil {
		t.Fatal(err)
	}
	turn, err := store.BeginTurnContext(ctx, bootstrap.Conversation.ID, "怎么了")
	if err != nil {
		t.Fatal(err)
	}
	parts := []ExpressionPart{
		{Kind: ExpressionUtterance, Text: "前一句。", VisualState: "idle"},
		{Kind: ExpressionSticker, VisualState: "surprised", Sticker: &StickerSnapshot{
			ID: "sticker-deleted-later", Description: "发送时的震惊描述", MIMEType: "image/gif",
		}},
		{Kind: ExpressionUtterance, Text: "后一句。", VisualState: "happy"},
	}
	completed, err := store.CompleteExpressionTurnContext(
		ctx, bootstrap.Conversation.ID, turn.ID, "前一句。\n后一句。", parts,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(completed.Parts) != 3 || completed.Parts[1].Sticker.Description != "发送时的震惊描述" {
		t.Fatalf("completed message = %#v", completed)
	}

	reloaded, err := store.LoadConversationContext(ctx, bootstrap.Conversation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.Messages) != 2 || len(reloaded.Messages[1].Parts) != 3 ||
		reloaded.Messages[1].Parts[1].Sticker.ID != "sticker-deleted-later" {
		t.Fatalf("reloaded messages = %#v", reloaded.Messages)
	}
	prompt, err := store.LoadConversationPromptContext(ctx, bootstrap.Conversation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := PromptMessageText(prompt.Messages[1]); got != "前一句。\n[表情包：发送时的震惊描述]\n后一句。" {
		t.Fatalf("prompt history = %q", got)
	}
	page, err := store.ListConversationMessagesBeforeContext(ctx, bootstrap.Conversation.ID, 0, 10)
	if err != nil || len(page.Messages) != 2 || len(page.Messages[1].Parts) != 3 {
		t.Fatalf("message page = %#v, %v", page, err)
	}
}

func TestPostgresPureStickerAssistantMessageIntegration(t *testing.T) {
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
	bootstrap, err := store.OpenOrCreateCharacterConversationContext(ctx, "character-pure-sticker")
	if err != nil {
		t.Fatal(err)
	}
	turn, err := store.BeginTurnContext(ctx, bootstrap.Conversation.ID, "点个头")
	if err != nil {
		t.Fatal(err)
	}
	parts := []ExpressionPart{{Kind: ExpressionSticker, VisualState: "idle", Sticker: &StickerSnapshot{
		ID: "sticker-1", Description: "安静点头", MIMEType: "image/png",
	}}}
	if _, err := store.CompleteExpressionTurnContext(ctx, bootstrap.Conversation.ID, turn.ID, "", parts); err != nil {
		t.Fatal(err)
	}
	var content string
	var storedParts []byte
	if err := pool.Raw().QueryRow(ctx,
		"SELECT content, expression_parts FROM conversation_messages WHERE turn_id = $1 AND role = 'assistant'",
		turn.ID,
	).Scan(&content, &storedParts); err != nil {
		t.Fatal(err)
	}
	if content != "" || len(storedParts) == 0 {
		t.Fatalf("content = %q, expression_parts = %s", content, storedParts)
	}
}
