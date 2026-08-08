//go:build integration

package memory

import (
	"context"
	"testing"

	"fairy/context/knowledge"
	coredb "fairy/runtime/database"
	"fairy/runtime/embedding"
)

func TestPostgresDirectKnowledgeDocumentAddUpdateDeleteIntegration(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := coredb.Migrate(ctx, pool.Raw()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	store, err := newMemoryIntegrationStores(pool)
	if err != nil {
		t.Fatal(err)
	}
	bootstrap, err := store.OpenOrCreateCharacterConversationContext(ctx, "direct-knowledge-character")
	if err != nil {
		t.Fatal(err)
	}
	turn, err := store.BeginTurnContext(ctx, bootstrap.Conversation.ID, "搜索项目资料")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompleteTurnContext(ctx, bootstrap.Conversation.ID, turn.ID, "已经找到资料"); err != nil {
		t.Fatal(err)
	}

	task := directKnowledgeTask(bootstrap.Conversation.ID, turn.ID, "direct-task-add", 100)
	firstContent := "FAIRY 使用 PostgreSQL 保存完整知识文档，并由知识条目直接引用文档。"
	first := knowledge.Document{
		SourceID: "direct-source", CanonicalURL: task.Source.URL,
		Title: "FAIRY 架构", Content: firstContent,
		ContentHash: embedding.ContentHash(firstContent), EvidenceID: "direct-evidence-add",
		ContentType: "text/plain", FetchedAtUnixMS: 100,
	}
	changed, err := store.CommitKnowledgeDocumentActionsContext(ctx, task, first, nil, []knowledge.DocumentAction{{
		Operation: knowledge.MutationAdd, Content: "FAIRY 的知识文档完整保存在 PostgreSQL 中。",
		ConfidenceBasisPoints: 9000, Evidence: "PostgreSQL 保存完整知识文档",
	}})
	if err != nil || changed != 1 {
		t.Fatalf("add direct knowledge = %d, %v", changed, err)
	}
	var knowledgeID, sourceURL, evidence string
	if err := pool.Raw().QueryRow(ctx, `
SELECT id, source_url, evidence_text
FROM knowledge_entries
WHERE status = 'verified'`).Scan(
		&knowledgeID, &sourceURL, &evidence,
	); err != nil {
		t.Fatal(err)
	}
	if sourceURL != task.Source.URL || evidence != "PostgreSQL 保存完整知识文档" {
		t.Fatalf("direct add state = source %q evidence %q", sourceURL, evidence)
	}

	updateTask := directKnowledgeTask(bootstrap.Conversation.ID, turn.ID, "direct-task-update", 200)
	secondContent := "FAIRY 现在只用 PostgreSQL 保存完整知识文档，知识条目直接引用当前文档。"
	second := first
	second.Content = secondContent
	second.ContentHash = embedding.ContentHash(secondContent)
	second.EvidenceID = "direct-evidence-update"
	second.FetchedAtUnixMS = 200
	changed, err = store.CommitKnowledgeDocumentActionsContext(ctx, updateTask, second, []string{knowledgeID}, []knowledge.DocumentAction{{
		Operation: knowledge.MutationUpdate, MemoryID: knowledgeID,
		Content:               "FAIRY 只使用 PostgreSQL 保存知识文档和向量。",
		ConfidenceBasisPoints: 9300, Evidence: "只用 PostgreSQL 保存完整知识文档",
	}})
	if err != nil || changed != 1 {
		t.Fatalf("update direct knowledge = %d, %v", changed, err)
	}
	var replacementID, replacementSourceURL, oldStatus string
	if err := pool.Raw().QueryRow(ctx, `
SELECT replacement.id, replacement.source_url, original.status
FROM knowledge_entries AS replacement
JOIN knowledge_entries AS original ON original.id = replacement.supersedes_id
WHERE replacement.status = 'verified' AND original.id = $1`,
		knowledgeID).Scan(&replacementID, &replacementSourceURL, &oldStatus); err != nil {
		t.Fatal(err)
	}
	if replacementSourceURL != task.Source.URL || oldStatus != "superseded" {
		t.Fatalf("direct update state = source %q old %q", replacementSourceURL, oldStatus)
	}

	deleteTask := directKnowledgeTask(bootstrap.Conversation.ID, turn.ID, "direct-task-delete", 300)
	thirdContent := "FAIRY 当前文档明确说明旧知识已经废弃，不应继续召回。"
	third := second
	third.Content = thirdContent
	third.ContentHash = embedding.ContentHash(thirdContent)
	third.EvidenceID = "direct-evidence-delete"
	third.FetchedAtUnixMS = 300
	changed, err = store.CommitKnowledgeDocumentActionsContext(ctx, deleteTask, third, []string{replacementID}, []knowledge.DocumentAction{{
		Operation: knowledge.MutationDelete, MemoryID: replacementID,
		Evidence: "旧知识已经废弃，不应继续召回",
	}})
	if err != nil || changed != 1 {
		t.Fatalf("delete direct knowledge = %d, %v", changed, err)
	}
	var deletedStatus string
	if err := pool.Raw().QueryRow(ctx, "SELECT status FROM knowledge_entries WHERE id = $1", replacementID).Scan(&deletedStatus); err != nil {
		t.Fatal(err)
	}
	if deletedStatus != "tombstone" {
		t.Fatalf("deleted knowledge status = %q", deletedStatus)
	}
	for _, table := range []string{
		"knowledge_sources", "knowledge_document_versions", "knowledge_chunks",
		"knowledge_evidence", "knowledge_ingest_jobs", "knowledge_documents", "feedback_events",
	} {
		var absent bool
		if err := pool.Raw().QueryRow(ctx, "SELECT to_regclass($1) IS NULL", table).Scan(&absent); err != nil {
			t.Fatal(err)
		}
		if !absent {
			t.Fatalf("obsolete table %s still exists", table)
		}
	}
}

func TestPostgresDirectKnowledgeNoneDoesNotAppendDocumentSourceIntegration(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := coredb.Migrate(ctx, pool.Raw()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	store, err := newMemoryIntegrationStores(pool)
	if err != nil {
		t.Fatal(err)
	}
	bootstrap, err := store.OpenOrCreateCharacterConversationContext(ctx, "direct-none-character")
	if err != nil {
		t.Fatal(err)
	}
	turn, err := store.BeginTurnContext(ctx, bootstrap.Conversation.ID, "核对相同事实")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompleteTurnContext(ctx, bootstrap.Conversation.ID, turn.ID, "已核对"); err != nil {
		t.Fatal(err)
	}
	original, err := store.InsertVerifiedKnowledgeContext(
		ctx, "原始来源", "原始文档已经保存这一条稳定事实。",
		bootstrap.Conversation.ID, turn.ID, 9000,
		[]knowledge.AssistantSource{{
			Title: "原始文档", URL: "https://example.com/original",
			Snippet: "原始证据片段", Rank: 1, FetchedAtUnixMS: 100,
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	var originalSourceURL, originalEvidence string
	if err := pool.Raw().QueryRow(ctx, `
SELECT source_url, evidence_text FROM knowledge_entries WHERE id = $1`,
		original.ID).Scan(&originalSourceURL, &originalEvidence); err != nil {
		t.Fatal(err)
	}
	task := directKnowledgeTask(bootstrap.Conversation.ID, turn.ID, "direct-task-none", 200)
	content := "新文档也提到了原始文档已经保存这一条稳定事实，但不应追加来源。"
	document := knowledge.Document{
		SourceID: "direct-source", CanonicalURL: task.Source.URL,
		Title: "新文档", Content: content, ContentHash: embedding.ContentHash(content),
		EvidenceID: "direct-evidence-none", ContentType: "text/plain", FetchedAtUnixMS: 200,
	}
	changed, err := store.CommitKnowledgeDocumentActionsContext(
		ctx, task, document, []string{original.ID},
		[]knowledge.DocumentAction{{
			Operation: knowledge.MutationNone, MemoryID: original.ID,
			Evidence: "原始文档已经保存这一条稳定事实",
		}},
	)
	if err != nil || changed != 0 {
		t.Fatalf("NONE direct knowledge = %d, %v", changed, err)
	}
	var sourceURL, evidence, status string
	if err := pool.Raw().QueryRow(ctx, `
SELECT source_url, evidence_text, status FROM knowledge_entries WHERE id = $1`,
		original.ID).Scan(&sourceURL, &evidence, &status); err != nil {
		t.Fatal(err)
	}
	if sourceURL != originalSourceURL || evidence != originalEvidence || status != "verified" {
		t.Fatalf("NONE changed entry = source %q evidence %q status %q", sourceURL, evidence, status)
	}
}

func directKnowledgeTask(
	conversationID, turnID, taskID string,
	fetchedAt int64,
) knowledge.IngestTask {
	return knowledge.IngestTask{
		ID: taskID, ConversationID: conversationID, TurnID: turnID,
		Source: knowledge.IngestSource{
			ID: "direct-source", Title: "FAIRY 架构",
			URL: "https://example.com/fairy", Snippet: "FAIRY 架构资料",
			Rank: 1, FetchedAtUnixMS: fetchedAt,
		},
	}
}
