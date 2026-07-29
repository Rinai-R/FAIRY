//go:build integration

package memory

import (
	"context"
	"testing"

	"fairy/coredb"
)

func TestPostgresDirectKnowledgeDocumentAddUpdateDeleteIntegration(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := coredb.Migrate(ctx, pool.Raw()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	store, err := NewStoreFromPool(pool)
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

	claim := enqueueAndClaimDirectKnowledge(t, ctx, store, bootstrap.Conversation.ID, turn.ID, "direct-task-add", 100)
	firstContent := "FAIRY 使用 PostgreSQL 保存完整知识文档，并由知识条目直接引用文档。"
	first := KnowledgeDocument{
		SourceID: "direct-source", CanonicalURL: claim.Task.Source.URL,
		Title: "FAIRY 架构", Content: firstContent,
		ContentHash: semanticContentHash(firstContent), EvidenceID: "direct-evidence-add",
		ContentType: "text/plain", FetchedAtUnixMS: 100,
	}
	needs, err := store.KnowledgeDocumentNeedsExtractionContext(ctx, claim.JobID, claim.Task.ID, first)
	if err != nil || !needs {
		t.Fatalf("first document needs extraction = %v, %v", needs, err)
	}
	changed, err := store.CommitKnowledgeDocumentActionsContext(ctx, claim.JobID, claim.Task.ID, first, nil, []KnowledgeDocumentAction{{
		Operation: KnowledgeMutationAdd, Content: "FAIRY 的知识文档完整保存在 PostgreSQL 中。",
		ConfidenceBasisPoints: 9000, Evidence: "PostgreSQL 保存完整知识文档",
	}})
	if err != nil || changed != 1 {
		t.Fatalf("add direct knowledge = %d, %v", changed, err)
	}
	var knowledgeID, documentID, evidence, eventStatus string
	if err := pool.Raw().QueryRow(ctx, `
SELECT entry.id, entry.document_id, entry.evidence_text, event.status
FROM knowledge_entries AS entry
JOIN feedback_events AS event ON event.id = $1
WHERE entry.status = 'verified'`, claim.JobID).Scan(
		&knowledgeID, &documentID, &evidence, &eventStatus,
	); err != nil {
		t.Fatal(err)
	}
	if documentID == "" || evidence != "PostgreSQL 保存完整知识文档" || eventStatus != "succeeded" {
		t.Fatalf("direct add state = document %q evidence %q event %q", documentID, evidence, eventStatus)
	}

	updateClaim := enqueueAndClaimDirectKnowledge(t, ctx, store, bootstrap.Conversation.ID, turn.ID, "direct-task-update", 200)
	secondContent := "FAIRY 现在只用 PostgreSQL 保存完整知识文档，知识条目直接引用当前文档。"
	second := first
	second.Content = secondContent
	second.ContentHash = semanticContentHash(secondContent)
	second.EvidenceID = "direct-evidence-update"
	second.FetchedAtUnixMS = 200
	changed, err = store.CommitKnowledgeDocumentActionsContext(ctx, updateClaim.JobID, updateClaim.Task.ID, second, []string{knowledgeID}, []KnowledgeDocumentAction{{
		Operation: KnowledgeMutationUpdate, MemoryID: knowledgeID,
		Content:               "FAIRY 只使用 PostgreSQL 保存知识文档和向量。",
		ConfidenceBasisPoints: 9300, Evidence: "只用 PostgreSQL 保存完整知识文档",
	}})
	if err != nil || changed != 1 {
		t.Fatalf("update direct knowledge = %d, %v", changed, err)
	}
	var replacementID, replacementDocumentID, oldStatus string
	if err := pool.Raw().QueryRow(ctx, `
SELECT replacement.id, replacement.document_id, original.status
FROM knowledge_entries AS replacement
JOIN knowledge_entries AS original ON original.id = replacement.supersedes_id
WHERE replacement.status = 'verified' AND original.id = $1`,
		knowledgeID).Scan(&replacementID, &replacementDocumentID, &oldStatus); err != nil {
		t.Fatal(err)
	}
	if replacementDocumentID != documentID || oldStatus != "superseded" {
		t.Fatalf("direct update state = document %q old %q", replacementDocumentID, oldStatus)
	}

	deleteClaim := enqueueAndClaimDirectKnowledge(t, ctx, store, bootstrap.Conversation.ID, turn.ID, "direct-task-delete", 300)
	thirdContent := "FAIRY 当前文档明确说明旧知识已经废弃，不应继续召回。"
	third := second
	third.Content = thirdContent
	third.ContentHash = semanticContentHash(thirdContent)
	third.EvidenceID = "direct-evidence-delete"
	third.FetchedAtUnixMS = 300
	changed, err = store.CommitKnowledgeDocumentActionsContext(ctx, deleteClaim.JobID, deleteClaim.Task.ID, third, []string{replacementID}, []KnowledgeDocumentAction{{
		Operation: KnowledgeMutationDelete, MemoryID: replacementID,
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
		"knowledge_evidence", "knowledge_ingest_jobs",
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
	store, err := NewStoreFromPool(pool)
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
		[]AssistantSource{{
			Title: "原始文档", URL: "https://example.com/original",
			Snippet: "原始证据片段", Rank: 1, FetchedAtUnixMS: 100,
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	var originalDocumentID, originalEvidence string
	if err := pool.Raw().QueryRow(ctx, `
SELECT document_id, evidence_text FROM knowledge_entries WHERE id = $1`,
		original.ID).Scan(&originalDocumentID, &originalEvidence); err != nil {
		t.Fatal(err)
	}
	claim := enqueueAndClaimDirectKnowledge(t, ctx, store, bootstrap.Conversation.ID, turn.ID, "direct-task-none", 200)
	content := "新文档也提到了原始文档已经保存这一条稳定事实，但不应追加来源。"
	document := KnowledgeDocument{
		SourceID: "direct-source", CanonicalURL: claim.Task.Source.URL,
		Title: "新文档", Content: content, ContentHash: semanticContentHash(content),
		EvidenceID: "direct-evidence-none", ContentType: "text/plain", FetchedAtUnixMS: 200,
	}
	changed, err := store.CommitKnowledgeDocumentActionsContext(
		ctx, claim.JobID, claim.Task.ID, document, []string{original.ID},
		[]KnowledgeDocumentAction{{
			Operation: KnowledgeMutationNone, MemoryID: original.ID,
			Evidence: "原始文档已经保存这一条稳定事实",
		}},
	)
	if err != nil || changed != 0 {
		t.Fatalf("NONE direct knowledge = %d, %v", changed, err)
	}
	var documentID, evidence, status string
	if err := pool.Raw().QueryRow(ctx, `
SELECT document_id, evidence_text, status FROM knowledge_entries WHERE id = $1`,
		original.ID).Scan(&documentID, &evidence, &status); err != nil {
		t.Fatal(err)
	}
	if documentID != originalDocumentID || evidence != originalEvidence || status != "verified" {
		t.Fatalf("NONE changed entry = document %q evidence %q status %q", documentID, evidence, status)
	}
}

func enqueueAndClaimDirectKnowledge(
	t *testing.T,
	ctx context.Context,
	store *Store,
	conversationID, turnID, taskID string,
	fetchedAt int64,
) KnowledgeIngestClaim {
	t.Helper()
	task := KnowledgeIngestTask{
		ID: taskID, ConversationID: conversationID, TurnID: turnID,
		Source: KnowledgeIngestSource{
			ID: "direct-source", Title: "FAIRY 架构",
			URL: "https://example.com/fairy", Snippet: "FAIRY 架构资料",
			Rank: 1, FetchedAtUnixMS: fetchedAt,
		},
	}
	if err := store.EnqueueKnowledgeIngestTasksContext(ctx, []KnowledgeIngestTask{task}); err != nil {
		t.Fatal(err)
	}
	claims, err := store.ClaimKnowledgeIngestTasksContext(ctx, 1)
	if err != nil || len(claims) != 1 {
		t.Fatalf("claim direct knowledge task = %#v, %v", claims, err)
	}
	return claims[0]
}
