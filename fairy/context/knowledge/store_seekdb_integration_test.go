//go:build integration

package knowledge

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"math"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"fairy/runtime/embedding"
	"fairy/runtime/seekdb"
)

const (
	knowledgeIntegrationConversation = "knowledge-integration-conversation"
	knowledgeIntegrationTurn         = "knowledge-integration-turn"
	knowledgeIntegrationCharacter    = "knowledge-integration-character"
	knowledgeIntegrationSpaceA       = "fairy-knowledge-integration-1024-a"
	knowledgeIntegrationSpaceB       = "fairy-knowledge-integration-1024-b"
)

func TestRealSeekDBKnowledgeStoreIsAtomicAndPersistent(t *testing.T) {
	instance, database, runtimeConfig := openKnowledgeSeekDB(t)
	closed := false
	t.Cleanup(func() {
		if !closed {
			closeKnowledgeSeekDB(t, instance, runtimeConfig.ShutdownLimit)
		}
	})
	if err := seekdb.MigrateSchema(t.Context(), database, seekdb.BuiltinMigrations()); err != nil {
		t.Fatalf("migrate SeekDB knowledge schema: %v", err)
	}
	seedKnowledgeIntegrationAuthority(t, database)

	assertKnowledgeSeekDBConstructor(t, database, runtimeConfig.QueryLimit)
	textStore := newKnowledgeSeekDBStore(t, database, runtimeConfig.QueryLimit, nil)
	embedderA := &knowledgeIntegrationEmbedder{spaceID: knowledgeIntegrationSpaceA}
	vectorStore := newKnowledgeSeekDBStore(t, database, runtimeConfig.QueryLimit, embedderA)
	if !textStore.KnowledgeIngestReady() || !vectorStore.KnowledgeIngestReady() {
		t.Fatal("SeekDB knowledge store is not ingest-ready")
	}

	direct := assertKnowledgeSeekDBVerifiedRecords(t, database, runtimeConfig.QueryLimit, textStore, vectorStore, embedderA)
	assertKnowledgeSeekDBIngestSearchSemantics(t, database, textStore)
	catalog := assertKnowledgeSeekDBCatalogAndStatusIsolation(t, database, runtimeConfig.QueryLimit, textStore, vectorStore)
	actions := assertKnowledgeSeekDBDocumentActionsAreAtomic(t, database, runtimeConfig.QueryLimit, textStore, vectorStore)
	assertKnowledgeSeekDBHybridRetrieval(t, database, textStore, vectorStore, direct, catalog)
	assertKnowledgeSeekDBVectorMaintenance(t, database, runtimeConfig.QueryLimit, direct, catalog)

	if err := seekdb.MigrateSchema(t.Context(), database, seekdb.BuiltinMigrations()); err != nil {
		t.Fatalf("verify idempotent schema before knowledge restart: %v", err)
	}
	if status, err := seekdb.CheckSchema(t.Context(), database, seekdb.CurrentSchemaRevision()); err != nil || status.State != seekdb.SchemaCurrent {
		t.Fatalf("knowledge schema before restart = (%#v, %v)", status, err)
	}
	closeKnowledgeSeekDB(t, instance, runtimeConfig.ShutdownLimit)
	closed = true

	var err error
	instance, err = seekdb.Open(t.Context(), runtimeConfig)
	if err != nil {
		t.Fatalf("restart SeekDB knowledge runtime: %v", err)
	}
	closed = false
	database = instance.SQL()
	if status, err := seekdb.CheckSchema(t.Context(), database, seekdb.CurrentSchemaRevision()); err != nil || status.State != seekdb.SchemaCurrent {
		t.Fatalf("knowledge schema after restart = (%#v, %v)", status, err)
	}
	restarted := newKnowledgeSeekDBStore(
		t, database, runtimeConfig.QueryLimit,
		&knowledgeIntegrationEmbedder{spaceID: knowledgeIntegrationSpaceB},
	)
	assertKnowledgeSeekDBSurvivesRestart(t, database, restarted, direct, catalog, actions)
}

type knowledgeDirectFixture struct {
	sourceFree Record
	sourced    Record
	searchable Record
}

type knowledgeCatalogFixture struct {
	confirmedCandidateID string
	failedCandidateID    string
	tombstoneID          string
}

type knowledgeActionFixture struct {
	addedID       string
	replacementID string
	replacedID    string
	deletedID     string
	noneID        string
}

func assertKnowledgeSeekDBConstructor(t *testing.T, database *sql.DB, queryLimit time.Duration) {
	t.Helper()
	if _, err := NewSeekDBStore(nil, queryLimit, nil); !errors.Is(err, ErrSeekDBConnectionEmpty) {
		t.Fatalf("NewSeekDBStore(nil) error = %v", err)
	}
	if _, err := NewSeekDBStore(database, 0, nil); !errors.Is(err, ErrSeekDBQueryLimitInvalid) {
		t.Fatalf("NewSeekDBStore(zero limit) error = %v", err)
	}
}

func assertKnowledgeSeekDBVerifiedRecords(
	t *testing.T,
	database *sql.DB,
	queryLimit time.Duration,
	textStore, vectorStore *Store,
	embedderA *knowledgeIntegrationEmbedder,
) knowledgeDirectFixture {
	t.Helper()
	ctx := t.Context()
	sourceFree, err := textStore.InsertVerifiedKnowledgeContext(
		ctx,
		"端侧知识",
		"端侧知识条目可以在语义服务关闭时只保存权威文本。",
		knowledgeIntegrationConversation,
		knowledgeIntegrationTurn,
		0,
		nil,
	)
	if err != nil {
		t.Fatalf("insert source-free verified knowledge: %v", err)
	}
	if sourceFree.Status != "verified" || sourceFree.VerificationBasis != "retrieval_ingest" ||
		sourceFree.ConfidenceBasisPoints != 7500 || len(sourceFree.Sources) != 0 {
		t.Fatalf("source-free verified knowledge = %#v", sourceFree)
	}
	assertKnowledgeSourceTupleNull(t, database, sourceFree.ID)
	assertKnowledgeEmbeddingNull(t, database, sourceFree.ID)

	source := AssistantSource{
		Title:           "FAIRY 端侧架构说明",
		URL:             "https://example.test/knowledge/direct",
		Snippet:         "端侧知识由 SeekDB 中的直接记录持久化。",
		Rank:            1,
		FetchedAtUnixMS: 1_786_200_000_200,
	}
	sourced, err := vectorStore.InsertVerifiedKnowledgeContext(
		ctx,
		"直接知识记录",
		"每条公共知识直接拥有自己的来源快照和向量投影。",
		knowledgeIntegrationConversation,
		knowledgeIntegrationTurn,
		9200,
		[]AssistantSource{source},
	)
	if err != nil {
		t.Fatalf("insert sourced verified knowledge: %v", err)
	}
	if sourced.Status != "verified" || sourced.VerificationBasis != "retrieval_ingest" ||
		sourced.ConfidenceBasisPoints != 9200 || len(sourced.Sources) != 1 || sourced.Sources[0] != source {
		t.Fatalf("sourced verified knowledge = %#v", sourced)
	}
	assertKnowledgeDirectSource(t, database, sourced.ID, source)
	assertKnowledgeEmbedding1024(t, database, sourced.ID, sourced.Topic+"\n"+sourced.Statement, knowledgeIntegrationSpaceA)

	callsBeforeDuplicate := embedderA.calls.Load()
	duplicate, err := vectorStore.InsertVerifiedKnowledgeContext(
		ctx,
		"不同标题不会创建重复事实",
		sourced.Statement,
		knowledgeIntegrationConversation,
		knowledgeIntegrationTurn,
		9300,
		nil,
	)
	if err != nil {
		t.Fatalf("insert exact duplicate verified knowledge: %v", err)
	}
	if duplicate.ID != sourced.ID {
		t.Fatalf("duplicate knowledge ID = %q, want %q", duplicate.ID, sourced.ID)
	}
	if embedderA.calls.Load() != callsBeforeDuplicate {
		t.Fatalf("current duplicate invoked provider: calls %d -> %d", callsBeforeDuplicate, embedderA.calls.Load())
	}
	assertKnowledgeDirectSource(t, database, sourced.ID, source)
	assertKnowledgeStatementCount(t, database, sourced.Statement, 1)

	countBefore := knowledgeEntryCount(t, database)
	secondSource := AssistantSource{
		Title: "第二来源", URL: "https://example.test/knowledge/second",
		Snippet: "第二来源不应被静默丢弃。", Rank: 2, FetchedAtUnixMS: 1_786_200_000_201,
	}
	if _, err := textStore.InsertVerifiedKnowledgeContext(
		ctx, "多来源应失败", "单行知识入口不能静默截断多个来源。",
		knowledgeIntegrationConversation, knowledgeIntegrationTurn, 8000,
		[]AssistantSource{source, secondSource},
	); err == nil {
		t.Fatal("InsertVerifiedKnowledgeContext with two sources error = nil")
	}
	assertKnowledgeEntryCount(t, database, countBefore)

	assertKnowledgeProviderFailureWritesNothing(t, database, queryLimit)
	assertKnowledgeCancellationWritesNothing(t, database, vectorStore)
	assertKnowledgeWrongDimensionsWriteNothing(t, database, queryLimit)

	searchable, err := textStore.InsertVerifiedKnowledgeContext(
		ctx,
		"玄蓝星航公开知识",
		"玄蓝星航的公开事实只允许 verified 状态参与 ingest 文本查找。",
		knowledgeIntegrationConversation,
		knowledgeIntegrationTurn,
		8800,
		nil,
	)
	if err != nil {
		t.Fatalf("insert searchable verified knowledge: %v", err)
	}
	return knowledgeDirectFixture{sourceFree: sourceFree, sourced: sourced, searchable: searchable}
}

func assertKnowledgeProviderFailureWritesNothing(t *testing.T, database *sql.DB, queryLimit time.Duration) {
	t.Helper()
	providerErr := errors.New("knowledge integration provider failed")
	embedder := &knowledgeIntegrationEmbedder{spaceID: knowledgeIntegrationSpaceA, err: providerErr}
	store := newKnowledgeSeekDBStore(t, database, queryLimit, embedder)
	before := knowledgeEntryCount(t, database)
	_, err := store.InsertVerifiedKnowledgeContext(
		t.Context(), "失败知识", "provider 失败时不得产生任何知识行。",
		knowledgeIntegrationConversation, knowledgeIntegrationTurn, 9000, nil,
	)
	if !errors.Is(err, providerErr) {
		t.Fatalf("provider failure error = %v", err)
	}
	assertKnowledgeEntryCount(t, database, before)
	if embedder.calls.Load() != 1 || embedder.legacyCalls.Load() != 0 {
		t.Fatalf("provider calls = context %d legacy %d", embedder.calls.Load(), embedder.legacyCalls.Load())
	}
}

func assertKnowledgeCancellationWritesNothing(t *testing.T, database *sql.DB, store *Store) {
	t.Helper()
	before := knowledgeEntryCount(t, database)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := store.InsertVerifiedKnowledgeContext(
		ctx, "取消知识", "取消的 provider 请求不得产生知识行。",
		knowledgeIntegrationConversation, knowledgeIntegrationTurn, 9000, nil,
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled insert error = %v", err)
	}
	assertKnowledgeEntryCount(t, database, before)
}

func assertKnowledgeWrongDimensionsWriteNothing(t *testing.T, database *sql.DB, queryLimit time.Duration) {
	t.Helper()
	store := newKnowledgeSeekDBStore(t, database, queryLimit, &knowledgeIntegrationEmbedder{
		spaceID: knowledgeIntegrationSpaceA,
		dims:    3,
	})
	before := knowledgeEntryCount(t, database)
	if _, err := store.InsertVerifiedKnowledgeContext(
		t.Context(), "错误维度", "三维向量不得写入一千零二十四维投影。",
		knowledgeIntegrationConversation, knowledgeIntegrationTurn, 9000, nil,
	); err == nil {
		t.Fatal("wrong-dimension insert error = nil")
	}
	assertKnowledgeEntryCount(t, database, before)
}

func assertKnowledgeSeekDBIngestSearchSemantics(
	t *testing.T,
	database *sql.DB,
	store *Store,
) {
	t.Helper()
	const baseTime = int64(1_786_200_000_500)
	seedKnowledgeSearchRecord(
		t, database, "knowledge-search-literal", "特殊标记 qx%_z9",
		"只有包含原样百分号和下划线的记录才能命中。", "verified", "retrieval_ingest", 9000, baseTime,
	)
	seedKnowledgeSearchRecord(
		t, database, "knowledge-search-literal-decoy", "特殊标记 qxAz9",
		"LIKE 通配符不得把这条 decoy 带入结果。", "verified", "retrieval_ingest", 9900, baseTime+1,
	)
	literal, err := store.SearchKnowledgeForIngestContext(t.Context(), "qx%_z9", MaxSearchCandidates)
	if err != nil {
		t.Fatalf("literal special-character knowledge search: %v", err)
	}
	assertKnowledgeSearchIDs(t, literal, []string{"knowledge-search-literal"})

	seedKnowledgeSearchRecord(
		t, database, "knowledge-search-topic-fts", "aurora edge protocol reference",
		"该条正文刻意不包含查询词。", "verified", "retrieval_ingest", 9100, baseTime+2,
	)
	topicOnly, err := store.SearchKnowledgeForIngestContext(t.Context(), "aurora protocol", MaxSearchCandidates)
	if err != nil {
		t.Fatalf("topic-only FTS knowledge search: %v", err)
	}
	if !knowledgeRetrievedContainID(topicOnly, "knowledge-search-topic-fts") {
		t.Fatalf("topic-only FTS search missed record: %#v", topicOnly)
	}

	seedKnowledgeSearchRecord(
		t, database, "knowledge-search-statement-fts", "普通公开知识",
		"orion telemetry calibrator remains available", "verified", "retrieval_ingest", 9100, baseTime+3,
	)
	statementOnly, err := store.SearchKnowledgeForIngestContext(t.Context(), "orion calibrator", MaxSearchCandidates)
	if err != nil {
		t.Fatalf("statement-only FTS knowledge search: %v", err)
	}
	if !knowledgeRetrievedContainID(statementOnly, "knowledge-search-statement-fts") {
		t.Fatalf("statement-only FTS search missed record: %#v", statementOnly)
	}

	seedKnowledgeSearchRecord(
		t, database, "knowledge-search-deduplicated", "nebula observatory",
		"nebula 同时命中 literal 和 FULLTEXT 分支。", "verified", "retrieval_ingest", 9200, baseTime+4,
	)
	deduplicated, err := store.SearchKnowledgeForIngestContext(t.Context(), "nebula", MaxSearchCandidates)
	if err != nil {
		t.Fatalf("deduplicated literal/FTS knowledge search: %v", err)
	}
	if countKnowledgeRetrievedID(deduplicated, "knowledge-search-deduplicated") != 1 {
		t.Fatalf("literal/FTS search did not deduplicate one record: %#v", deduplicated)
	}

	for _, record := range []struct {
		id         string
		confidence uint16
	}{
		{id: "knowledge-search-order-a", confidence: 9000},
		{id: "knowledge-search-order-b", confidence: 9000},
		{id: "knowledge-search-order-c", confidence: 8000},
	} {
		seedKnowledgeSearchRecord(
			t, database, record.id, "stable-token ordering", "稳定排序测试正文。",
			"verified", "retrieval_ingest", record.confidence, baseTime+5,
		)
	}
	ordered, err := store.SearchKnowledgeForIngestContext(t.Context(), "stable-token", MaxSearchCandidates)
	if err != nil {
		t.Fatalf("stable knowledge search: %v", err)
	}
	assertKnowledgeSearchIDs(t, ordered, []string{
		"knowledge-search-order-a", "knowledge-search-order-b", "knowledge-search-order-c",
	})
	repeated, err := store.SearchKnowledgeForIngestContext(t.Context(), "stable-token", MaxSearchCandidates)
	if err != nil {
		t.Fatalf("repeat stable knowledge search: %v", err)
	}
	assertKnowledgeSearchIDs(t, repeated, []string{
		"knowledge-search-order-a", "knowledge-search-order-b", "knowledge-search-order-c",
	})

	seedKnowledgeSearchRecord(
		t, database, "knowledge-search-terminal-candidate", "terminal-isolation",
		"候选状态不得进入 ingest lookup。", "candidate", "unverified", 9000, baseTime+6,
	)
	seedKnowledgeSearchRecord(
		t, database, "knowledge-search-terminal-superseded", "terminal-isolation",
		"已替代状态不得进入 ingest lookup。", "superseded", "retrieval_ingest", 9000, baseTime+6,
	)
	seedKnowledgeSearchRecord(
		t, database, "knowledge-search-terminal-tombstone", "terminal-isolation",
		"墓碑状态不得进入 ingest lookup。", "tombstone", "retrieval_ingest", 9000, baseTime+6,
	)
	terminal, err := store.SearchKnowledgeForIngestContext(t.Context(), "terminal-isolation", MaxSearchCandidates)
	if err != nil {
		t.Fatalf("terminal-isolated knowledge search: %v", err)
	}
	if len(terminal) != 0 {
		t.Fatalf("knowledge ingest lookup leaked terminal records: %#v", terminal)
	}
}

func assertKnowledgeSeekDBCatalogAndStatusIsolation(
	t *testing.T,
	database *sql.DB,
	queryLimit time.Duration,
	textStore, vectorStore *Store,
) knowledgeCatalogFixture {
	t.Helper()
	confirmedID := "knowledge-candidate-confirmed"
	failedID := "knowledge-candidate-provider-failure"
	searchCandidateID := "knowledge-candidate-search-isolation"
	seedKnowledgeCandidate(t, database, confirmedID, "候选主题", "候选知识可以由用户确认。", 1_786_200_001_000)
	seedKnowledgeCandidate(t, database, failedID, "失败候选主题", "provider 失败的候选必须保持未确认。", 1_786_200_001_001)
	seedKnowledgeCandidate(t, database, searchCandidateID, "玄蓝星航候选", "玄蓝星航候选事实不能出现在 verified 查找。", 1_786_200_001_002)

	catalog, err := textStore.CatalogContext(t.Context())
	if err != nil {
		t.Fatalf("load knowledge catalog: %v", err)
	}
	if !knowledgeRecordsContain(catalog.Candidates, confirmedID) ||
		!knowledgeRecordsContain(catalog.Candidates, failedID) ||
		knowledgeRecordsContain(catalog.Verified, confirmedID) {
		t.Fatalf("initial knowledge catalog = %#v", catalog)
	}

	confirmed, err := vectorStore.ConfirmCandidateContext(t.Context(), confirmedID)
	if err != nil {
		t.Fatalf("confirm knowledge candidate: %v", err)
	}
	if confirmed.ID != confirmedID || confirmed.Status != "verified" || confirmed.VerificationBasis != "user_confirmed" {
		t.Fatalf("confirmed knowledge = %#v", confirmed)
	}
	assertKnowledgeEmbedding1024(t, database, confirmed.ID, confirmed.Topic+"\n"+confirmed.Statement, knowledgeIntegrationSpaceA)

	providerErr := errors.New("candidate confirmation provider failed")
	failingStore := newKnowledgeSeekDBStore(
		t, database, queryLimit,
		&knowledgeIntegrationEmbedder{spaceID: knowledgeIntegrationSpaceA, err: providerErr},
	)
	if _, err := failingStore.ConfirmCandidateContext(t.Context(), failedID); !errors.Is(err, providerErr) {
		t.Fatalf("failed candidate confirmation error = %v", err)
	}
	assertKnowledgeStatusAndBasis(t, database, failedID, "candidate", "unverified")
	assertKnowledgeEmbeddingNull(t, database, failedID)

	tombstone, err := vectorStore.InsertVerifiedKnowledgeContext(
		t.Context(), "玄蓝星航废弃知识", "玄蓝星航已经废弃的事实不得参与 ingest 查找。",
		knowledgeIntegrationConversation, knowledgeIntegrationTurn, 8700, nil,
	)
	if err != nil {
		t.Fatalf("insert tombstone search fixture: %v", err)
	}
	if err := textStore.TombstoneContext(t.Context(), tombstone.ID); err != nil {
		t.Fatalf("tombstone search fixture: %v", err)
	}

	results, err := textStore.SearchKnowledgeForIngestContext(t.Context(), "玄蓝星航", MaxSearchCandidates)
	if err != nil {
		t.Fatalf("verified-only knowledge ingest lookup: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("verified-only knowledge ingest lookup returned no verified result")
	}
	for _, result := range results {
		if result.ID == searchCandidateID || result.ID == tombstone.ID {
			t.Fatalf("ingest lookup leaked non-verified record: %#v", result)
		}
	}
	if !knowledgeRetrievedContain(results, "玄蓝星航公开知识") {
		t.Fatalf("ingest lookup missed verified topic: %#v", results)
	}

	catalog, err = textStore.CatalogContext(t.Context())
	if err != nil {
		t.Fatalf("reload knowledge catalog: %v", err)
	}
	if !knowledgeRecordsContain(catalog.Verified, confirmedID) ||
		knowledgeRecordsContain(catalog.Candidates, confirmedID) ||
		knowledgeRecordsContain(catalog.Verified, tombstone.ID) {
		t.Fatalf("status-isolated knowledge catalog = %#v", catalog)
	}
	stats, err := textStore.StatsContext(t.Context())
	if err != nil {
		t.Fatalf("knowledge stats: %v", err)
	}
	if stats.Candidates < 1 || stats.Verified < 1 {
		t.Fatalf("knowledge stats = %#v", stats)
	}
	assertKnowledgeSeekDBConfirmLosesToConcurrentTombstone(
		t, database, queryLimit, textStore,
	)
	return knowledgeCatalogFixture{
		confirmedCandidateID: confirmedID,
		failedCandidateID:    failedID,
		tombstoneID:          tombstone.ID,
	}
}

func assertKnowledgeSeekDBConfirmLosesToConcurrentTombstone(
	t *testing.T,
	database *sql.DB,
	queryLimit time.Duration,
	tombstoneStore *Store,
) {
	t.Helper()
	const candidateID = "knowledge-confirm-tombstone-race"
	seedKnowledgeCandidate(
		t, database, candidateID, "并发确认候选", "确认期间被墓碑化的候选不得写入向量。",
		1_786_200_001_100,
	)
	barrier := newKnowledgeBarrierEmbedder(knowledgeIntegrationSpaceA, 1)
	t.Cleanup(barrier.releaseAll)
	confirmStore := newKnowledgeSeekDBStore(t, database, queryLimit, barrier)
	confirmed := make(chan error, 1)
	ctx := t.Context()
	go func() {
		_, err := confirmStore.ConfirmCandidateContext(ctx, candidateID)
		confirmed <- err
	}()
	barrier.waitForCalls(t, 1)
	if err := tombstoneStore.TombstoneContext(t.Context(), candidateID); err != nil {
		barrier.releaseAll()
		t.Fatalf("tombstone candidate while confirmation provider is blocked: %v", err)
	}
	barrier.releaseAll()
	if err := receiveKnowledgeConcurrentError(t, confirmed, "candidate confirmation"); err == nil {
		t.Fatal("candidate confirmation won after concurrent tombstone")
	}
	assertKnowledgeStatusAndBasis(t, database, candidateID, "tombstone", "unverified")
	assertKnowledgeEmbeddingNull(t, database, candidateID)
}

func assertKnowledgeSeekDBDocumentActionsAreAtomic(
	t *testing.T,
	database *sql.DB,
	queryLimit time.Duration,
	textStore, vectorStore *Store,
) knowledgeActionFixture {
	t.Helper()
	replaceTarget := insertKnowledgeActionTarget(t, textStore, "替换目标", "旧的直接知识将被新事实替代。")
	deleteTarget := insertKnowledgeActionTarget(t, textStore, "删除目标", "已经失效的直接知识将被墓碑化。")
	noneTarget := insertKnowledgeActionTarget(t, textStore, "保持目标", "无需修改的知识必须保持原始来源和正文。")
	noneBefore := loadKnowledgeSnapshot(t, database, noneTarget.ID)

	task, document := knowledgeIntegrationDocument(
		"knowledge-actions", 1_786_200_002_000,
		"文档说明新增事实已经生效。旧的直接知识由新事实完整替代。已经失效的直接知识不应继续召回。无需修改的知识仍然保持原样。",
	)
	actions := []DocumentAction{
		{Operation: MutationAdd, Content: "新增事实已经生效并保存完整来源。", ConfidenceBasisPoints: 9100, Evidence: "新增事实已经生效"},
		{Operation: MutationReplace, MemoryID: replaceTarget.ID, Content: "新的直接知识完整替代旧事实。", ConfidenceBasisPoints: 9300, Evidence: "旧的直接知识由新事实完整替代"},
		{Operation: MutationDelete, MemoryID: deleteTarget.ID, Evidence: "已经失效的直接知识不应继续召回"},
		{Operation: MutationNone, MemoryID: noneTarget.ID, Evidence: "无需修改的知识仍然保持原样"},
	}
	changed, err := vectorStore.CommitKnowledgeDocumentActionsContext(
		t.Context(), task, document,
		[]string{replaceTarget.ID, deleteTarget.ID, noneTarget.ID}, actions,
	)
	if err != nil {
		t.Fatalf("commit direct knowledge actions: %v", err)
	}
	if changed != 3 {
		t.Fatalf("direct knowledge changed count = %d, want 3", changed)
	}

	addedID := knowledgeIDByStatement(t, database, actions[0].Content)
	replacementID := knowledgeIDByStatement(t, database, actions[1].Content)
	assertKnowledgeStatusAndBasis(t, database, replaceTarget.ID, "superseded", "retrieval_ingest")
	assertKnowledgeStatusAndBasis(t, database, deleteTarget.ID, "tombstone", "retrieval_ingest")
	var supersedesID sql.NullString
	if err := database.QueryRowContext(t.Context(),
		"SELECT supersedes_id FROM knowledge_entries WHERE id = ?", replacementID,
	).Scan(&supersedesID); err != nil {
		t.Fatalf("load replacement lineage: %v", err)
	}
	if !supersedesID.Valid || supersedesID.String != replaceTarget.ID {
		t.Fatalf("replacement supersedes_id = %#v, want %q", supersedesID, replaceTarget.ID)
	}
	for _, record := range []struct {
		id, statement, evidence string
	}{
		{id: addedID, statement: actions[0].Content, evidence: actions[0].Evidence},
		{id: replacementID, statement: actions[1].Content, evidence: actions[1].Evidence},
	} {
		assertKnowledgeDocumentSource(t, database, record.id, document, record.evidence)
		assertKnowledgeEmbedding1024(t, database, record.id, document.Title+"\n"+record.statement, knowledgeIntegrationSpaceA)
	}
	if noneAfter := loadKnowledgeSnapshot(t, database, noneTarget.ID); noneAfter != noneBefore {
		t.Fatalf("NONE changed knowledge entry:\n before=%#v\n after=%#v", noneBefore, noneAfter)
	}

	beforeEmpty := knowledgeEntryCount(t, database)
	emptyTask, emptyDocument := knowledgeIntegrationDocument(
		"knowledge-actions-empty", 1_786_200_002_050,
		"空 action 批次不得改写任何知识行，也不得把当前网页正文当成独立状态保存。",
	)
	emptyChanged, err := textStore.CommitKnowledgeDocumentActionsContext(
		t.Context(), emptyTask, emptyDocument, nil, nil,
	)
	if err != nil {
		t.Fatalf("empty knowledge actions: %v", err)
	}
	if emptyChanged != 0 {
		t.Fatalf("empty knowledge actions changed = %d, want 0", emptyChanged)
	}
	assertKnowledgeEntryCount(t, database, beforeEmpty)

	assertKnowledgeInvalidLaterActionRollsBack(t, database, vectorStore)
	assertKnowledgeActionProviderFailureRollsBack(t, database, queryLimit, noneTarget.ID)
	assertKnowledgeSeekDBConcurrentDocumentTargetHasOneWinner(
		t, database, queryLimit, textStore,
	)
	return knowledgeActionFixture{
		addedID: addedID, replacementID: replacementID, replacedID: replaceTarget.ID,
		deletedID: deleteTarget.ID, noneID: noneTarget.ID,
	}
}

func assertKnowledgeSeekDBConcurrentDocumentTargetHasOneWinner(
	t *testing.T,
	database *sql.DB,
	queryLimit time.Duration,
	textStore *Store,
) {
	t.Helper()
	target := insertKnowledgeActionTarget(
		t, textStore, "并发替换目标", "同一个 verified target 只能被一个并发替换赢得。",
	)
	barrier := newKnowledgeBarrierEmbedder(knowledgeIntegrationSpaceA, 2)
	t.Cleanup(barrier.releaseAll)
	store := newKnowledgeSeekDBStore(t, database, queryLimit, barrier)
	taskA, documentA := knowledgeIntegrationDocument(
		"knowledge-concurrent-action-a", 1_786_200_002_300,
		"并发替换甲拥有合法证据，并尝试替代同一个目标。",
	)
	taskB, documentB := knowledgeIntegrationDocument(
		"knowledge-concurrent-action-b", 1_786_200_002_301,
		"并发替换乙拥有合法证据，并尝试替代同一个目标。",
	)
	outcomes := make(chan knowledgeConcurrentOutcome, 2)
	ctx := t.Context()
	for _, input := range []struct {
		task     IngestTask
		document Document
		content  string
		evidence string
	}{
		{
			task: taskA, document: documentA,
			content: "并发替换甲生成的新知识正文。", evidence: "并发替换甲拥有合法证据",
		},
		{
			task: taskB, document: documentB,
			content: "并发替换乙生成的新知识正文。", evidence: "并发替换乙拥有合法证据",
		},
	} {
		input := input
		go func() {
			changed, err := store.CommitKnowledgeDocumentActionsContext(
				ctx, input.task, input.document, []string{target.ID},
				[]DocumentAction{{
					Operation: MutationReplace, MemoryID: target.ID,
					Content: input.content, ConfidenceBasisPoints: 9000,
					Evidence: input.evidence,
				}},
			)
			outcomes <- knowledgeConcurrentOutcome{changed: changed, err: err}
		}()
	}
	barrier.waitForCalls(t, 2)
	barrier.releaseAll()
	succeeded := 0
	failed := 0
	for range 2 {
		result := receiveKnowledgeConcurrentOutcome(t, outcomes, "concurrent document action")
		if result.err == nil {
			if result.changed != 1 {
				t.Fatalf("winning concurrent action changed = %d, want 1", result.changed)
			}
			succeeded++
		} else {
			if result.changed != 0 {
				t.Fatalf("losing concurrent action changed = %d, want 0", result.changed)
			}
			failed++
		}
	}
	if succeeded != 1 || failed != 1 {
		t.Fatalf("concurrent document action outcomes = success %d failure %d", succeeded, failed)
	}
	assertKnowledgeStatusAndBasis(t, database, target.ID, "superseded", "retrieval_ingest")
	var replacements int
	if err := database.QueryRowContext(t.Context(), `
SELECT COUNT(*) FROM knowledge_entries
WHERE supersedes_id = ? AND status = 'verified'`, target.ID).Scan(&replacements); err != nil {
		t.Fatalf("count concurrent knowledge replacements: %v", err)
	}
	if replacements != 1 {
		t.Fatalf("concurrent verified replacements = %d, want 1", replacements)
	}
}

func assertKnowledgeInvalidLaterActionRollsBack(t *testing.T, database *sql.DB, store *Store) {
	t.Helper()
	before := knowledgeEntryCount(t, database)
	task, document := knowledgeIntegrationDocument(
		"knowledge-actions-invalid", 1_786_200_002_100,
		"第一个新增事实本身合法，但后续未知 alias 必须使整批失败。未知目标不允许被猜测修复。",
	)
	_, err := store.CommitKnowledgeDocumentActionsContext(
		t.Context(), task, document, nil,
		[]DocumentAction{
			{Operation: MutationAdd, Content: "第一个新增事实本身合法且有充分证据。", ConfidenceBasisPoints: 9000, Evidence: "第一个新增事实本身合法"},
			{Operation: MutationReplace, MemoryID: "unknown-knowledge-alias", Content: "未知目标不允许生成替换记录。", ConfidenceBasisPoints: 9000, Evidence: "未知目标不允许被猜测修复"},
		},
	)
	if err == nil {
		t.Fatal("invalid later knowledge action error = nil")
	}
	assertKnowledgeEntryCount(t, database, before)
	assertKnowledgeStatementCount(t, database, "第一个新增事实本身合法且有充分证据。", 0)
}

func assertKnowledgeActionProviderFailureRollsBack(t *testing.T, database *sql.DB, queryLimit time.Duration, targetID string) {
	t.Helper()
	providerErr := errors.New("knowledge action provider failed")
	store := newKnowledgeSeekDBStore(t, database, queryLimit, &knowledgeIntegrationEmbedder{
		spaceID: knowledgeIntegrationSpaceA,
		err:     providerErr,
	})
	before := knowledgeEntryCount(t, database)
	targetBefore := loadKnowledgeSnapshot(t, database, targetID)
	task, document := knowledgeIntegrationDocument(
		"knowledge-actions-provider-failure", 1_786_200_002_200,
		"provider 失败前的替换动作拥有合法证据，但不得留下 superseded 或新增记录。",
	)
	_, err := store.CommitKnowledgeDocumentActionsContext(
		t.Context(), task, document, []string{targetID},
		[]DocumentAction{{
			Operation: MutationReplace, MemoryID: targetID,
			Content: "provider 失败不得提交这条替换知识。", ConfidenceBasisPoints: 9000,
			Evidence: "provider 失败前的替换动作拥有合法证据",
		}},
	)
	if !errors.Is(err, providerErr) {
		t.Fatalf("knowledge action provider failure = %v", err)
	}
	assertKnowledgeEntryCount(t, database, before)
	if targetAfter := loadKnowledgeSnapshot(t, database, targetID); targetAfter != targetBefore {
		t.Fatalf("provider failure changed target:\n before=%#v\n after=%#v", targetBefore, targetAfter)
	}
}

func assertKnowledgeSeekDBVectorMaintenance(
	t *testing.T,
	database *sql.DB,
	queryLimit time.Duration,
	direct knowledgeDirectFixture,
	catalog knowledgeCatalogFixture,
) {
	t.Helper()
	embedderA := &knowledgeIntegrationEmbedder{spaceID: knowledgeIntegrationSpaceA}
	storeA := newKnowledgeSeekDBStore(t, database, queryLimit, embedderA)
	first, err := storeA.RebuildVectors(t.Context(), 1)
	if err != nil {
		t.Fatalf("rebuild missing knowledge vectors: %v", err)
	}
	if first.UpdatedItems < 1 || first.FailedItems != 0 {
		t.Fatalf("first knowledge vector rebuild = %#v", first)
	}
	assertKnowledgeEmbedding1024(t, database, direct.sourceFree.ID, direct.sourceFree.Topic+"\n"+direct.sourceFree.Statement, knowledgeIntegrationSpaceA)
	assertKnowledgeEmbeddingNull(t, database, catalog.failedCandidateID)

	second, err := storeA.RebuildVectors(t.Context(), 1)
	if err != nil {
		t.Fatalf("repeat knowledge vector rebuild: %v", err)
	}
	if second.UpdatedItems != 0 || second.FailedItems != 0 || second.SkippedItems != second.ScannedItems {
		t.Fatalf("idempotent knowledge vector rebuild = %#v", second)
	}

	embedderB := &knowledgeIntegrationEmbedder{spaceID: knowledgeIntegrationSpaceB}
	storeB := newKnowledgeSeekDBStore(t, database, queryLimit, embedderB)
	switched, err := storeB.RebuildVectors(t.Context(), 1)
	if err != nil {
		t.Fatalf("switch knowledge embedding space: %v", err)
	}
	if switched.UpdatedItems < 1 || switched.FailedItems != 0 {
		t.Fatalf("knowledge embedding space switch = %#v", switched)
	}
	assertKnowledgeEmbedding1024(t, database, direct.sourceFree.ID, direct.sourceFree.Topic+"\n"+direct.sourceFree.Statement, knowledgeIntegrationSpaceB)
	assertKnowledgeEmbeddingSpace(t, database, catalog.tombstoneID, knowledgeIntegrationSpaceA)

	if _, err := database.ExecContext(t.Context(), `
UPDATE knowledge_entries
SET embedding_space_id = NULL, embedding_content_hash = NULL, embedding = NULL
WHERE id = ? AND status = 'verified'`, direct.sourceFree.ID); err != nil {
		t.Fatalf("make knowledge vector stale: %v", err)
	}
	providerErr := errors.New("knowledge rebuild provider failed")
	failingStore := newKnowledgeSeekDBStore(t, database, queryLimit, &knowledgeIntegrationEmbedder{
		spaceID: knowledgeIntegrationSpaceB,
		err:     providerErr,
	})
	if _, err := failingStore.RebuildVectors(t.Context(), 1); !errors.Is(err, providerErr) {
		t.Fatalf("failed knowledge vector rebuild error = %v", err)
	}
	assertKnowledgeEmbeddingNull(t, database, direct.sourceFree.ID)
	if _, err := storeB.RebuildVectors(t.Context(), 1); err != nil {
		t.Fatalf("restore knowledge vector after failed rebuild: %v", err)
	}
	assertKnowledgeEmbedding1024(t, database, direct.sourceFree.ID, direct.sourceFree.Topic+"\n"+direct.sourceFree.Statement, knowledgeIntegrationSpaceB)
	assertKnowledgeSeekDBVectorRebuildSkipsConcurrentDrift(
		t, database, queryLimit, storeB,
	)
}

func assertKnowledgeSeekDBVectorRebuildSkipsConcurrentDrift(
	t *testing.T,
	database *sql.DB,
	queryLimit time.Duration,
	repairStore *Store,
) {
	t.Helper()
	textStore := newKnowledgeSeekDBStore(t, database, queryLimit, nil)
	statusDrift, err := textStore.InsertVerifiedKnowledgeContext(
		t.Context(), "向量状态漂移", "provider 阻塞期间墓碑化的知识不得获得旧向量。",
		knowledgeIntegrationConversation, knowledgeIntegrationTurn, 9000, nil,
	)
	if err != nil {
		t.Fatalf("insert status-drift vector fixture: %v", err)
	}
	contentDrift, err := textStore.InsertVerifiedKnowledgeContext(
		t.Context(), "向量正文漂移", "provider 阻塞期间变化的正文不得获得旧正文向量。",
		knowledgeIntegrationConversation, knowledgeIntegrationTurn, 9000, nil,
	)
	if err != nil {
		t.Fatalf("insert content-drift vector fixture: %v", err)
	}
	barrier := newKnowledgeBarrierEmbedder(knowledgeIntegrationSpaceB, 1)
	t.Cleanup(barrier.releaseAll)
	store := newKnowledgeSeekDBStore(t, database, queryLimit, barrier)
	rebuilt := make(chan knowledgeVectorOutcome, 1)
	ctx := t.Context()
	go func() {
		result, err := store.RebuildVectors(ctx, maxVectorMaintenancePageSize)
		rebuilt <- knowledgeVectorOutcome{result: result, err: err}
	}()
	calls := barrier.waitForCalls(t, 1)
	if len(calls[0]) != 2 ||
		!stringSliceContains(calls[0], statusDrift.Topic+"\n"+statusDrift.Statement) ||
		!stringSliceContains(calls[0], contentDrift.Topic+"\n"+contentDrift.Statement) {
		barrier.releaseAll()
		t.Fatalf("vector rebuild pending contents = %#v", calls[0])
	}
	if _, err := database.ExecContext(t.Context(), `
UPDATE knowledge_entries SET status = 'tombstone'
WHERE id = ? AND status = 'verified'`, statusDrift.ID); err != nil {
		barrier.releaseAll()
		t.Fatalf("apply status drift during vector provider: %v", err)
	}
	const changedStatement = "provider 返回前正文已经改变，旧内容向量必须被 CAS 丢弃。"
	if _, err := database.ExecContext(t.Context(), `
UPDATE knowledge_entries SET statement = ?
WHERE id = ? AND status = 'verified'`, changedStatement, contentDrift.ID); err != nil {
		barrier.releaseAll()
		t.Fatalf("apply content drift during vector provider: %v", err)
	}
	barrier.releaseAll()
	result := receiveKnowledgeVectorOutcome(t, rebuilt)
	if result.err != nil {
		t.Fatalf("rebuild vectors after concurrent drift: %v", result.err)
	}
	if result.result.UpdatedItems != 0 || result.result.SkippedItems < 2 {
		t.Fatalf("vector rebuild drift result = %#v", result.result)
	}
	assertKnowledgeStatusAndBasis(t, database, statusDrift.ID, "tombstone", "retrieval_ingest")
	assertKnowledgeEmbeddingNull(t, database, statusDrift.ID)
	assertKnowledgeEmbeddingNull(t, database, contentDrift.ID)
	var persistedStatement string
	if err := database.QueryRowContext(t.Context(),
		"SELECT statement FROM knowledge_entries WHERE id = ?", contentDrift.ID,
	).Scan(&persistedStatement); err != nil {
		t.Fatalf("load content-drift statement: %v", err)
	}
	if persistedStatement != changedStatement {
		t.Fatalf("content-drift statement = %q", persistedStatement)
	}
	if _, err := repairStore.RebuildVectors(t.Context(), maxVectorMaintenancePageSize); err != nil {
		t.Fatalf("repair current knowledge vector after drift: %v", err)
	}
	assertKnowledgeEmbeddingNull(t, database, statusDrift.ID)
	assertKnowledgeEmbedding1024(
		t, database, contentDrift.ID, contentDrift.Topic+"\n"+changedStatement,
		knowledgeIntegrationSpaceB,
	)
}

func assertKnowledgeSeekDBSurvivesRestart(
	t *testing.T,
	database *sql.DB,
	store *Store,
	direct knowledgeDirectFixture,
	catalog knowledgeCatalogFixture,
	actions knowledgeActionFixture,
) {
	t.Helper()
	for id, wantStatus := range map[string]string{
		direct.sourceFree.ID:         "verified",
		direct.sourced.ID:            "verified",
		direct.searchable.ID:         "verified",
		catalog.confirmedCandidateID: "verified",
		catalog.failedCandidateID:    "candidate",
		catalog.tombstoneID:          "tombstone",
		actions.addedID:              "verified",
		actions.replacementID:        "verified",
		actions.replacedID:           "superseded",
		actions.deletedID:            "tombstone",
		actions.noneID:               "verified",
	} {
		var status string
		if err := database.QueryRowContext(t.Context(),
			"SELECT status FROM knowledge_entries WHERE id = ?", id,
		).Scan(&status); err != nil {
			t.Fatalf("load knowledge %q after restart: %v", id, err)
		}
		if status != wantStatus {
			t.Fatalf("knowledge %q status after restart = %q, want %q", id, status, wantStatus)
		}
	}
	assertKnowledgeEmbedding1024(t, database, direct.sourceFree.ID, direct.sourceFree.Topic+"\n"+direct.sourceFree.Statement, knowledgeIntegrationSpaceB)
	catalogAfterRestart, err := store.CatalogContext(t.Context())
	if err != nil {
		t.Fatalf("catalog after knowledge restart: %v", err)
	}
	if !knowledgeRecordsContain(catalogAfterRestart.Verified, direct.sourceFree.ID) ||
		!knowledgeRecordsContain(catalogAfterRestart.Verified, actions.replacementID) ||
		!knowledgeRecordsContain(catalogAfterRestart.Candidates, catalog.failedCandidateID) ||
		knowledgeRecordsContain(catalogAfterRestart.Verified, actions.deletedID) {
		t.Fatalf("knowledge catalog after restart = %#v", catalogAfterRestart)
	}
	results, err := store.SearchKnowledgeForIngestContext(t.Context(), "玄蓝星航", MaxSearchCandidates)
	if err != nil {
		t.Fatalf("knowledge ingest lookup after restart: %v", err)
	}
	if !knowledgeRetrievedContain(results, "玄蓝星航公开知识") {
		t.Fatalf("knowledge ingest lookup after restart = %#v", results)
	}
}

func insertKnowledgeActionTarget(t *testing.T, store *Store, topic, statement string) Record {
	t.Helper()
	record, err := store.InsertVerifiedKnowledgeContext(
		t.Context(), topic, statement,
		knowledgeIntegrationConversation, knowledgeIntegrationTurn, 8500, nil,
	)
	if err != nil {
		t.Fatalf("insert knowledge action target %q: %v", topic, err)
	}
	return record
}

func knowledgeIntegrationDocument(prefix string, fetchedAt int64, content string) (IngestTask, Document) {
	url := "https://example.test/knowledge/" + prefix
	task := IngestTask{
		ID:             prefix + "-task",
		ConversationID: knowledgeIntegrationConversation,
		TurnID:         knowledgeIntegrationTurn,
		Source: IngestSource{
			ID: prefix + "-source", Title: "端侧知识文档", URL: url,
			Snippet: "端侧知识文档摘要", Rank: 1, FetchedAtUnixMS: fetchedAt,
		},
	}
	document := Document{
		SourceID: task.Source.ID, CanonicalURL: url, Title: task.Source.Title,
		Content: content, ContentHash: embedding.ContentHash(content),
		EvidenceID: prefix + "-evidence", ContentType: "text/plain",
		ETag: "etag-" + prefix, LastModified: "Fri, 14 Aug 2026 10:00:00 GMT",
		FetchedAtUnixMS:    fetchedAt,
		ReconcilerRevision: embedding.ContentHash("reconciler-" + prefix),
	}
	return task, document
}

func assertKnowledgeSeekDBHybridRetrieval(
	t *testing.T,
	database *sql.DB,
	textStore, vectorStore *Store,
	direct knowledgeDirectFixture,
	catalog knowledgeCatalogFixture,
) {
	t.Helper()
	ctx := t.Context()
	seedKnowledgePrivacyLeakRecords(t, database)

	text, err := textStore.RetrieveContext(ctx, "玄蓝星航")
	if err != nil {
		t.Fatalf("text-only public retrieve: %v", err)
	}
	if text.SemanticStatus != string(embedding.SemanticStatusUnavailable) {
		t.Fatalf("text-only semantic status = %q, want unavailable", text.SemanticStatus)
	}
	if !knowledgeRetrievedContain(text.Entries, "玄蓝星航公开知识") {
		t.Fatalf("text-only retrieve missed verified knowledge: %#v", text)
	}
	assertKnowledgeRetrievalExcludesPrivateAndTerminal(t, text.Entries, catalog)

	hybrid, err := vectorStore.RetrieveContext(ctx, "玄蓝星航")
	if err != nil {
		t.Fatalf("hybrid public retrieve: %v", err)
	}
	if hybrid.SemanticStatus != string(embedding.SemanticStatusUsed) {
		t.Fatalf("hybrid semantic status = %q, want used", hybrid.SemanticStatus)
	}
	if !knowledgeRetrievedContain(hybrid.Entries, "玄蓝星航公开知识") {
		t.Fatalf("hybrid retrieve missed verified knowledge: %#v", hybrid)
	}
	assertKnowledgeRetrievalExcludesPrivateAndTerminal(t, hybrid.Entries, catalog)

	textVectorQuery, err := textStore.RetrieveContext(ctx, "zzzzvectorquery")
	if err != nil {
		t.Fatalf("text-only vector query: %v", err)
	}
	if textVectorQuery.SemanticStatus != string(embedding.SemanticStatusUnavailable) {
		t.Fatalf("text-only vector-query status = %q, want unavailable", textVectorQuery.SemanticStatus)
	}
	if knowledgeRetrievedContainID(textVectorQuery.Entries, direct.sourced.ID) {
		t.Fatalf("text-only mode invented a vector hit: %#v", textVectorQuery)
	}

	vectorOnly, err := vectorStore.RetrieveContext(ctx, "zzzzvectorquery")
	if err != nil {
		t.Fatalf("vector-only public retrieve: %v", err)
	}
	if vectorOnly.SemanticStatus != string(embedding.SemanticStatusUsed) {
		t.Fatalf("vector-only semantic status = %q, want used", vectorOnly.SemanticStatus)
	}
	if !knowledgeRetrievedContainID(vectorOnly.Entries, direct.sourced.ID) &&
		!knowledgeRetrievedContainID(vectorOnly.Entries, direct.sourceFree.ID) {
		t.Fatalf("vector-only retrieve missed embedded verified knowledge: %#v", vectorOnly)
	}
	assertKnowledgeRetrievalExcludesPrivateAndTerminal(t, vectorOnly.Entries, catalog)
}

func assertKnowledgeRetrievalExcludesPrivateAndTerminal(
	t *testing.T,
	entries []Retrieved,
	catalog knowledgeCatalogFixture,
) {
	t.Helper()
	for _, entry := range entries {
		switch entry.ID {
		case "privacy-leak-personal", "privacy-leak-social",
			catalog.failedCandidateID, catalog.tombstoneID,
			"knowledge-candidate-search-isolation":
			t.Fatalf("public retrieval leaked private or terminal record: %#v", entry)
		}
	}
}

func seedKnowledgePrivacyLeakRecords(t *testing.T, database *sql.DB) {
	t.Helper()
	now := int64(1_786_200_002_000)
	personalContent := "玄蓝星航公开知识只是伪装成公共事实的个人记忆。"
	personalHash := sha256.Sum256([]byte(personalContent))
	if _, err := database.ExecContext(t.Context(), `
INSERT INTO personal_memories(
  id, kind, scope_kind, character_id, review_status, content, status,
  confidence_basis_points, source_conversation_id, source_turn_id, evidence_ids,
  supersedes_id, embedding_space_id, embedding_content_hash, embedding,
  created_at_ms, updated_at_ms, normalized_content_hash
) VALUES (
  'privacy-leak-personal', 'profile', 'global', NULL, 'ready', ?, 'active',
  9900, ?, ?, CAST('[]' AS JSON), NULL, ?, ?, ?, ?, ?, ?
)`,
		personalContent, knowledgeIntegrationConversation, knowledgeIntegrationTurn,
		knowledgeIntegrationSpaceA, personalHash[:], knowledgeIntegrationVectorLiteral(),
		now, now, personalHash[:],
	); err != nil {
		t.Fatalf("seed privacy-leak personal memory: %v", err)
	}

	socialContent := "社交侧的相似向量不得进入公共召回。"
	socialHash := sha256.Sum256([]byte("privacy-leak-social"))
	embeddingHash := sha256.Sum256([]byte(socialContent))
	if _, err := database.ExecContext(t.Context(), `
INSERT INTO social_memory_entries(
  id, character_id, conversation_id, kind, situation, content, recall_cue,
  content_hash, sender_id, sender_name, status, source_start_ms, source_end_ms,
  feedback_evaluation_count, feedback_adopted_count, feedback_positive_count,
  feedback_partial_count, feedback_negative_count, feedback_score_basis_points,
  embedding_space_id, embedding_content_hash, embedding, created_at_ms, updated_at_ms
) VALUES (
  'privacy-leak-social', ?, ?, 'episode', '玄蓝星航公开知识', ?, '玄蓝星航',
  ?, NULL, '', 'active', 1, 1,
  0, 0, 0, 0, 0, 0,
  ?, ?, ?, ?, ?
)`,
		knowledgeIntegrationCharacter, knowledgeIntegrationConversation, socialContent,
		socialHash[:], knowledgeIntegrationSpaceA, embeddingHash[:],
		knowledgeIntegrationVectorLiteral(), now, now,
	); err != nil {
		t.Fatalf("seed privacy-leak social memory: %v", err)
	}
}

func seedKnowledgeIntegrationAuthority(t testing.TB, database *sql.DB) {
	t.Helper()
	createdAt := int64(1_786_200_000_000)
	if _, err := database.ExecContext(t.Context(), `
INSERT INTO conversations(id, character_id, kind, created_at_ms, updated_at_ms)
VALUES (?, ?, 'character', ?, ?)`,
		knowledgeIntegrationConversation, knowledgeIntegrationCharacter, createdAt, createdAt,
	); err != nil {
		t.Fatalf("seed knowledge conversation: %v", err)
	}
	if _, err := database.ExecContext(t.Context(), `
INSERT INTO conversation_turns(
  id, conversation_id, message_id, sequence, status, origin,
  error_code, error_message, error_retryable,
  extraction_state, extraction_claim_id, extraction_lease_owner, extraction_lease_expires_at_ms,
  extraction_attempt_count, extraction_next_attempt_at_ms,
  extraction_error_code, extraction_error_message, created_at_ms, updated_at_ms
) VALUES (?, ?, NULL, 1, 'completed', 'user', NULL, NULL, NULL,
          'pending', NULL, NULL, NULL, 0, 0, NULL, NULL, ?, ?)`,
		knowledgeIntegrationTurn, knowledgeIntegrationConversation, createdAt+1, createdAt+1,
	); err != nil {
		t.Fatalf("seed knowledge turn: %v", err)
	}
}

func seedKnowledgeCandidate(t *testing.T, database *sql.DB, id, topic, statement string, now int64) {
	t.Helper()
	if _, err := database.ExecContext(t.Context(), `
INSERT INTO knowledge_entries(
  id, topic, statement, status, verification_basis, confidence_basis_points,
  source_conversation_id, source_turn_id, created_at_ms, updated_at_ms
) VALUES (?, ?, ?, 'candidate', 'unverified', 7000, ?, ?, ?, ?)`,
		id, topic, statement, knowledgeIntegrationConversation, knowledgeIntegrationTurn, now, now,
	); err != nil {
		t.Fatalf("seed knowledge candidate %q: %v", id, err)
	}
}

func seedKnowledgeSearchRecord(
	t *testing.T,
	database *sql.DB,
	id, topic, statement, status, basis string,
	confidence uint16,
	now int64,
) {
	t.Helper()
	if _, err := database.ExecContext(t.Context(), `
INSERT INTO knowledge_entries(
  id, topic, statement, status, verification_basis, confidence_basis_points,
  source_conversation_id, source_turn_id, created_at_ms, updated_at_ms
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, topic, statement, status, basis, int(confidence),
		knowledgeIntegrationConversation, knowledgeIntegrationTurn, now, now,
	); err != nil {
		t.Fatalf("seed knowledge search record %q: %v", id, err)
	}
}

type knowledgePersistedSnapshot struct {
	topic, statement, status, basis, sourceURL, sourceTitle, sourceHash string
	sourceType, sourceETag, sourceLastModified, reconciler, evidence    string
	supersedesID, embeddingSpace, embeddingHash                         string
	confidence, sourceFetchedAt, createdAt, updatedAt                   int64
	embeddingNull                                                       bool
}

func loadKnowledgeSnapshot(t *testing.T, database *sql.DB, id string) knowledgePersistedSnapshot {
	t.Helper()
	var snapshot knowledgePersistedSnapshot
	if err := database.QueryRowContext(t.Context(), `
SELECT topic, statement, status, verification_basis, confidence_basis_points,
       COALESCE(source_url, '<NULL>'), COALESCE(source_title, '<NULL>'),
       COALESCE(HEX(source_content_hash), '<NULL>'), COALESCE(source_content_type, '<NULL>'),
       COALESCE(source_fetched_at_ms, 0), COALESCE(source_etag, '<NULL>'),
       COALESCE(source_last_modified, '<NULL>'), COALESCE(reconciler_revision, '<NULL>'),
       COALESCE(evidence_text, '<NULL>'), COALESCE(supersedes_id, '<NULL>'),
       COALESCE(embedding_space_id, '<NULL>'), COALESCE(HEX(embedding_content_hash), '<NULL>'),
       embedding IS NULL, created_at_ms, updated_at_ms
FROM knowledge_entries WHERE id = ?`, id).Scan(
		&snapshot.topic, &snapshot.statement, &snapshot.status, &snapshot.basis, &snapshot.confidence,
		&snapshot.sourceURL, &snapshot.sourceTitle, &snapshot.sourceHash, &snapshot.sourceType,
		&snapshot.sourceFetchedAt, &snapshot.sourceETag, &snapshot.sourceLastModified,
		&snapshot.reconciler, &snapshot.evidence, &snapshot.supersedesID,
		&snapshot.embeddingSpace, &snapshot.embeddingHash, &snapshot.embeddingNull,
		&snapshot.createdAt, &snapshot.updatedAt,
	); err != nil {
		t.Fatalf("load knowledge snapshot %q: %v", id, err)
	}
	return snapshot
}

func assertKnowledgeDirectSource(t *testing.T, database *sql.DB, id string, source AssistantSource) {
	t.Helper()
	var url, title, hashHex, contentType, evidence string
	var fetchedAt int64
	var optionalNull bool
	if err := database.QueryRowContext(t.Context(), `
SELECT source_url, source_title, HEX(source_content_hash), source_content_type,
       source_fetched_at_ms, evidence_text,
       source_etag IS NULL AND source_last_modified IS NULL AND reconciler_revision IS NULL
FROM knowledge_entries WHERE id = ?`, id).Scan(
		&url, &title, &hashHex, &contentType, &fetchedAt, &evidence, &optionalNull,
	); err != nil {
		t.Fatalf("load direct knowledge source %q: %v", id, err)
	}
	if url != source.URL || title != source.Title ||
		hashHex != strings.ToUpper(embedding.ContentHash(source.Snippet)) ||
		contentType != "text/plain" || fetchedAt != source.FetchedAtUnixMS ||
		evidence != source.Snippet || !optionalNull {
		t.Fatalf("direct knowledge source %q = (%q, %q, %q, %q, %d, %q, %v)",
			id, url, title, hashHex, contentType, fetchedAt, evidence, optionalNull)
	}
}

func assertKnowledgeDocumentSource(t *testing.T, database *sql.DB, id string, document Document, evidence string) {
	t.Helper()
	var url, title, hashHex, contentType, etag, lastModified, reconciler, gotEvidence string
	var fetchedAt int64
	if err := database.QueryRowContext(t.Context(), `
SELECT source_url, source_title, HEX(source_content_hash), source_content_type,
       source_fetched_at_ms, source_etag, source_last_modified, reconciler_revision, evidence_text
FROM knowledge_entries WHERE id = ?`, id).Scan(
		&url, &title, &hashHex, &contentType, &fetchedAt,
		&etag, &lastModified, &reconciler, &gotEvidence,
	); err != nil {
		t.Fatalf("load document knowledge source %q: %v", id, err)
	}
	if url != document.CanonicalURL || title != document.Title ||
		hashHex != strings.ToUpper(document.ContentHash) || contentType != document.ContentType ||
		fetchedAt != document.FetchedAtUnixMS || etag != document.ETag ||
		lastModified != document.LastModified || reconciler != document.ReconcilerRevision ||
		gotEvidence != evidence {
		t.Fatalf("document knowledge source %q = (%q, %q, %q, %q, %d, %q, %q, %q, %q)",
			id, url, title, hashHex, contentType, fetchedAt, etag, lastModified, reconciler, gotEvidence)
	}
}

func assertKnowledgeSourceTupleNull(t *testing.T, database *sql.DB, id string) {
	t.Helper()
	var allNull bool
	if err := database.QueryRowContext(t.Context(), `
SELECT source_url IS NULL AND source_title IS NULL AND source_content_hash IS NULL AND
       source_content_type IS NULL AND source_fetched_at_ms IS NULL AND source_etag IS NULL AND
       source_last_modified IS NULL AND reconciler_revision IS NULL AND evidence_text IS NULL
FROM knowledge_entries WHERE id = ?`, id).Scan(&allNull); err != nil {
		t.Fatalf("load source-free knowledge %q: %v", id, err)
	}
	if !allNull {
		t.Fatalf("knowledge %q source tuple is not fully NULL", id)
	}
}

func assertKnowledgeEmbeddingNull(t *testing.T, database *sql.DB, id string) {
	t.Helper()
	var allNull bool
	if err := database.QueryRowContext(t.Context(), `
SELECT embedding_space_id IS NULL AND embedding_content_hash IS NULL AND embedding IS NULL
FROM knowledge_entries WHERE id = ?`, id).Scan(&allNull); err != nil {
		t.Fatalf("load nullable knowledge embedding %q: %v", id, err)
	}
	if !allNull {
		t.Fatalf("knowledge %q embedding tuple is not fully NULL", id)
	}
}

func assertKnowledgeEmbedding1024(t *testing.T, database *sql.DB, id, content, spaceID string) {
	t.Helper()
	var gotSpace, hashHex string
	var present bool
	var distance float64
	if err := database.QueryRowContext(t.Context(), `
SELECT embedding_space_id, HEX(embedding_content_hash), embedding IS NOT NULL,
       COSINE_DISTANCE(embedding, ?)
FROM knowledge_entries WHERE id = ?`, knowledgeIntegrationVectorLiteral(), id).Scan(
		&gotSpace, &hashHex, &present, &distance,
	); err != nil {
		t.Fatalf("load knowledge embedding %q: %v", id, err)
	}
	if gotSpace != spaceID || hashHex != strings.ToUpper(embedding.ContentHash(content)) ||
		!present || math.Abs(distance) > 1e-6 {
		t.Fatalf("knowledge %q embedding = (%q, %q, %v, %f)", id, gotSpace, hashHex, present, distance)
	}
}

func assertKnowledgeEmbeddingSpace(t *testing.T, database *sql.DB, id, want string) {
	t.Helper()
	var got sql.NullString
	if err := database.QueryRowContext(t.Context(),
		"SELECT embedding_space_id FROM knowledge_entries WHERE id = ?", id,
	).Scan(&got); err != nil {
		t.Fatalf("load knowledge embedding space %q: %v", id, err)
	}
	if !got.Valid || got.String != want {
		t.Fatalf("knowledge %q embedding space = %#v, want %q", id, got, want)
	}
}

func assertKnowledgeStatusAndBasis(t *testing.T, database *sql.DB, id, wantStatus, wantBasis string) {
	t.Helper()
	var status, basis string
	if err := database.QueryRowContext(t.Context(),
		"SELECT status, verification_basis FROM knowledge_entries WHERE id = ?", id,
	).Scan(&status, &basis); err != nil {
		t.Fatalf("load knowledge state %q: %v", id, err)
	}
	if status != wantStatus || basis != wantBasis {
		t.Fatalf("knowledge %q state = (%q, %q), want (%q, %q)", id, status, basis, wantStatus, wantBasis)
	}
}

func knowledgeIDByStatement(t *testing.T, database *sql.DB, statement string) string {
	t.Helper()
	var id string
	if err := database.QueryRowContext(t.Context(),
		"SELECT id FROM knowledge_entries WHERE statement = ?", statement,
	).Scan(&id); err != nil {
		t.Fatalf("load knowledge by statement %q: %v", statement, err)
	}
	return id
}

func knowledgeEntryCount(t *testing.T, database *sql.DB) int {
	t.Helper()
	var count int
	if err := database.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM knowledge_entries").Scan(&count); err != nil {
		t.Fatalf("count knowledge entries: %v", err)
	}
	return count
}

func assertKnowledgeEntryCount(t *testing.T, database *sql.DB, want int) {
	t.Helper()
	if got := knowledgeEntryCount(t, database); got != want {
		t.Fatalf("knowledge entry count = %d, want %d", got, want)
	}
}

func assertKnowledgeStatementCount(t *testing.T, database *sql.DB, statement string, want int) {
	t.Helper()
	var got int
	if err := database.QueryRowContext(t.Context(),
		"SELECT COUNT(*) FROM knowledge_entries WHERE statement = ?", statement,
	).Scan(&got); err != nil {
		t.Fatalf("count knowledge statement %q: %v", statement, err)
	}
	if got != want {
		t.Fatalf("knowledge statement count for %q = %d, want %d", statement, got, want)
	}
}

func knowledgeRecordsContain(records []Record, id string) bool {
	for _, record := range records {
		if record.ID == id {
			return true
		}
	}
	return false
}

func knowledgeRetrievedContain(records []Retrieved, topic string) bool {
	for _, record := range records {
		if record.Topic == topic {
			return true
		}
	}
	return false
}

func knowledgeRetrievedContainID(records []Retrieved, id string) bool {
	return countKnowledgeRetrievedID(records, id) > 0
}

func countKnowledgeRetrievedID(records []Retrieved, id string) int {
	count := 0
	for _, record := range records {
		if record.ID == id {
			count++
		}
	}
	return count
}

func assertKnowledgeSearchIDs(t *testing.T, records []Retrieved, want []string) {
	t.Helper()
	got := make([]string, len(records))
	for index, record := range records {
		got[index] = record.ID
	}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("knowledge search IDs = %#v, want %#v", got, want)
	}
}

func stringSliceContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

type knowledgeConcurrentOutcome struct {
	changed int
	err     error
}

type knowledgeVectorOutcome struct {
	result VectorRebuildResult
	err    error
}

func receiveKnowledgeConcurrentError(t *testing.T, result <-chan error, operation string) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(10 * time.Second):
		t.Fatalf("%s did not finish", operation)
		return nil
	}
}

func receiveKnowledgeConcurrentOutcome(
	t *testing.T,
	result <-chan knowledgeConcurrentOutcome,
	operation string,
) knowledgeConcurrentOutcome {
	t.Helper()
	select {
	case outcome := <-result:
		return outcome
	case <-time.After(10 * time.Second):
		t.Fatalf("%s did not finish", operation)
		return knowledgeConcurrentOutcome{}
	}
}

func receiveKnowledgeVectorOutcome(
	t *testing.T,
	result <-chan knowledgeVectorOutcome,
) knowledgeVectorOutcome {
	t.Helper()
	select {
	case outcome := <-result:
		return outcome
	case <-time.After(10 * time.Second):
		t.Fatal("knowledge vector rebuild did not finish")
		return knowledgeVectorOutcome{}
	}
}

type knowledgeBarrierEmbedder struct {
	spaceID string
	arrived chan []string
	release chan struct{}
	once    sync.Once
}

func newKnowledgeBarrierEmbedder(spaceID string, expectedCalls int) *knowledgeBarrierEmbedder {
	return &knowledgeBarrierEmbedder{
		spaceID: spaceID,
		arrived: make(chan []string, expectedCalls),
		release: make(chan struct{}),
	}
}

func (embedder *knowledgeBarrierEmbedder) Ready() bool { return true }

func (embedder *knowledgeBarrierEmbedder) Status() embedding.SemanticStatus {
	return embedding.SemanticStatusReady
}

func (embedder *knowledgeBarrierEmbedder) ModelID() string { return embedder.spaceID }

func (embedder *knowledgeBarrierEmbedder) Dims() int { return embedding.Dimensions }

func (*knowledgeBarrierEmbedder) Embed([]string) ([][]float32, error) {
	return nil, errors.New("knowledge barrier legacy Embed must not run")
}

func (embedder *knowledgeBarrierEmbedder) EmbedContext(
	ctx context.Context,
	texts []string,
) ([][]float32, error) {
	copyOfTexts := append([]string(nil), texts...)
	select {
	case embedder.arrived <- copyOfTexts:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case <-embedder.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	results := make([][]float32, len(texts))
	for index := range results {
		results[index] = make([]float32, embedding.Dimensions)
		results[index][0] = 1
	}
	return results, nil
}

func (embedder *knowledgeBarrierEmbedder) waitForCalls(t *testing.T, count int) [][]string {
	t.Helper()
	results := make([][]string, 0, count)
	for len(results) < count {
		select {
		case texts := <-embedder.arrived:
			results = append(results, texts)
		case <-time.After(10 * time.Second):
			embedder.releaseAll()
			t.Fatalf("knowledge provider calls = %d, want %d", len(results), count)
		}
	}
	return results
}

func (embedder *knowledgeBarrierEmbedder) releaseAll() {
	embedder.once.Do(func() { close(embedder.release) })
}

type knowledgeIntegrationEmbedder struct {
	spaceID     string
	dims        int
	err         error
	calls       atomic.Int64
	legacyCalls atomic.Int64
}

func (embedder *knowledgeIntegrationEmbedder) Ready() bool { return true }

func (embedder *knowledgeIntegrationEmbedder) Status() embedding.SemanticStatus {
	return embedding.SemanticStatusReady
}

func (embedder *knowledgeIntegrationEmbedder) ModelID() string { return embedder.spaceID }

func (embedder *knowledgeIntegrationEmbedder) Dims() int {
	if embedder.dims > 0 {
		return embedder.dims
	}
	return embedding.Dimensions
}

func (embedder *knowledgeIntegrationEmbedder) Embed([]string) ([][]float32, error) {
	embedder.legacyCalls.Add(1)
	return nil, errors.New("knowledge integration legacy Embed must not run")
}

func (embedder *knowledgeIntegrationEmbedder) EmbedContext(ctx context.Context, texts []string) ([][]float32, error) {
	embedder.calls.Add(1)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if embedder.err != nil {
		return nil, embedder.err
	}
	results := make([][]float32, len(texts))
	for index := range results {
		results[index] = make([]float32, embedder.Dims())
		results[index][0] = 1
	}
	return results, nil
}

func knowledgeIntegrationVectorLiteral() string {
	values := make([]string, embedding.Dimensions)
	values[0] = "1"
	for index := 1; index < len(values); index++ {
		values[index] = "0"
	}
	return "[" + strings.Join(values, ",") + "]"
}

func newKnowledgeSeekDBStore(
	t testing.TB,
	database *sql.DB,
	queryLimit time.Duration,
	embedder embedding.SemanticEmbedder,
) *Store {
	t.Helper()
	store, err := NewSeekDBStore(database, queryLimit, embedder)
	if err != nil {
		t.Fatalf("new SeekDB knowledge store: %v", err)
	}
	return store
}

func openKnowledgeSeekDB(t testing.TB) (*seekdb.Runtime, *sql.DB, seekdb.Config) {
	t.Helper()
	binary := os.Getenv(seekdb.EnvBinaryPath)
	if binary == "" {
		t.Skip(seekdb.EnvBinaryPath + " is not set")
	}
	config := seekdb.Config{
		BinaryPath:    binary,
		LibraryDirs:   filepath.SplitList(os.Getenv(seekdb.EnvLibraryPath)),
		DataDir:       filepath.Join(t.TempDir(), "seekdb-knowledge"),
		Address:       reserveKnowledgeLoopbackAddress(t),
		Database:      seekdb.DefaultDatabase,
		User:          seekdb.DefaultUser,
		ConnectLimit:  5 * time.Second,
		StartLimit:    90 * time.Second,
		QueryLimit:    15 * time.Second,
		ShutdownLimit: 20 * time.Second,
		MaxOpenConns:  16,
		MaxIdleConns:  8,
	}
	instance, err := seekdb.Open(t.Context(), config)
	if err != nil {
		t.Fatalf("open real SeekDB knowledge runtime: %v", err)
	}
	return instance, instance.SQL(), config
}

func reserveKnowledgeLoopbackAddress(t testing.TB) string {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return address
}

func closeKnowledgeSeekDB(t testing.TB, instance *seekdb.Runtime, timeout time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := instance.Close(ctx); err != nil {
		t.Errorf("close real SeekDB knowledge runtime: %v", err)
	}
}
