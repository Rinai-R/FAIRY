//go:build integration

package foundation

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"fairy/context/character"
	historycompaction "fairy/context/history/compaction"
	historyexpr "fairy/context/history/expression"
	historyruntime "fairy/context/history/runtime"
	"fairy/runtime/config"
	"fairy/runtime/ledger"
	"fairy/runtime/model"
	"fairy/runtime/observability"
	"fairy/runtime/seekdb"
	"fairy/transport/session"
)

func TestRealSeekDBFoundationStoresPersistAcrossRestartWithoutLegacyFallback(t *testing.T) {
	environment := newFoundationIntegrationEnvironment(t)
	characterRoot := filepath.Join(t.TempDir(), "characters")
	writeFoundationIntegrationVisual(t, characterRoot, "fairy.atri")
	writeFoundationIntegrationLegacyCharacter(t, characterRoot, "legacy-only")

	first := openFoundationIntegration(t, environment, characterRoot)
	defer closeFoundationIntegration(t, first)
	assertFoundationIntegrationCurrent(t, first)

	// The local character files are an import concern, not a runtime fallback.
	// A fresh authoritative SeekDB must therefore remain empty even when a valid
	// legacy record is present below the visual root.
	catalog, err := first.Characters.ListContext(t.Context())
	if err != nil {
		t.Fatalf("list fresh foundation characters: %v", err)
	}
	if len(catalog.Characters) != 0 || catalog.Active != nil || len(catalog.Diagnostics) != 0 {
		t.Fatalf("fresh SeekDB character catalog used a legacy fallback: %#v", catalog)
	}

	document, err := first.Documents.CompareAndSwap(
		t.Context(),
		"integration",
		"foundation",
		1,
		0,
		json.RawMessage(`{"authority":"seekdb"}`),
	)
	if err != nil || document.Revision != 1 {
		t.Fatalf("write foundation config document = (%#v, %v)", document, err)
	}

	preferredName := "Rinai"
	profileUpdate, err := first.Profile.SetPreferredName(&preferredName)
	if err != nil || !profileUpdate.Changed || profileUpdate.Profile == nil || profileUpdate.Profile.Revision != 1 {
		t.Fatalf("write foundation profile = (%#v, %v)", profileUpdate, err)
	}

	const (
		secretName  = "foundation-integration"
		secretPlain = "sk-foundation-integration-secret"
	)
	secretValue, err := config.NewSecretValue(secretPlain)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Secrets.SaveContext(t.Context(), secretName, secretValue); err != nil {
		t.Fatalf("write foundation secret: %v", err)
	}

	const (
		ownerNamespace = "qq.onebot"
		ownerDigest    = "abababababababababababababababababababababababababababababababab"
	)
	if err := first.Identity.BindOwnerContext(t.Context(), ownerNamespace, ownerDigest); err != nil {
		t.Fatalf("write foundation identity: %v", err)
	}

	created, err := first.Characters.CreateContext(t.Context(), character.Brief{
		Name:             "亚托莉",
		Description:      "认真听完再回应。",
		TextLanguage:     "zh",
		SpeakingLanguage: "ja",
	}, "fairy.atri")
	if err != nil {
		t.Fatalf("write foundation character: %v", err)
	}
	if _, err := first.Characters.ActivateContext(t.Context(), created.CharacterID, created.Revision); err != nil {
		t.Fatalf("activate foundation character: %v", err)
	}

	closeFoundationIntegration(t, first)

	second := openFoundationIntegration(t, environment, characterRoot)
	defer closeFoundationIntegration(t, second)
	assertFoundationIntegrationCurrent(t, second)

	restoredDocument, found, err := second.Documents.Get(t.Context(), "integration", "foundation")
	var restoredDocumentValue struct {
		Authority string `json:"authority"`
	}
	decodeErr := json.Unmarshal(restoredDocument.Document, &restoredDocumentValue)
	if err != nil || decodeErr != nil || !found || restoredDocument.Revision != 1 || restoredDocumentValue.Authority != "seekdb" {
		t.Fatalf("read restarted config document = (%#v, found %v, read error %v, decode error %v)", restoredDocument, found, err, decodeErr)
	}
	restoredProfile, err := second.Profile.Current()
	if err != nil || restoredProfile == nil || restoredProfile.Revision != 1 || restoredProfile.PreferredName == nil || *restoredProfile.PreferredName != preferredName {
		t.Fatalf("read restarted profile = (%#v, %v)", restoredProfile, err)
	}
	restoredSecret, found, err := second.Secrets.LoadContext(t.Context(), secretName)
	if err != nil || !found || restoredSecret.Expose() != secretPlain {
		t.Fatalf("read restarted secret = (%v, found %v, error %v)", restoredSecret, found, err)
	}
	isOwner, err := second.Identity.IsOwnerContext(t.Context(), ownerNamespace, ownerDigest)
	if err != nil || !isOwner {
		t.Fatalf("read restarted identity = (%v, %v)", isOwner, err)
	}
	restoredCharacter, found, err := second.Characters.LookupContext(t.Context(), created.CharacterID)
	if err != nil || !found || restoredCharacter.Revision != created.Revision || restoredCharacter.Name != "亚托莉" {
		t.Fatalf("read restarted character = (%#v, found %v, error %v)", restoredCharacter, found, err)
	}
	restartedCatalog, err := second.Characters.ListContext(t.Context())
	if err != nil || restartedCatalog.Active == nil || restartedCatalog.Active.CharacterID != created.CharacterID || len(restartedCatalog.Characters) != 1 {
		t.Fatalf("read restarted active character = (%#v, %v)", restartedCatalog, err)
	}

	environment.assertNoLegacyReads(t)
}

func TestRealSeekDBFoundationConversationStoresShareAuthorityAndPersistAcrossRestart(t *testing.T) {
	environment := newFoundationIntegrationEnvironment(t)
	characterRoot := filepath.Join(t.TempDir(), "characters")
	first := openFoundationIntegration(t, environment, characterRoot)
	defer closeFoundationIntegration(t, first)

	binding := session.Binding{
		Endpoint: session.EndpointDesktop,
		Facts: session.Facts{
			Audience:     session.AudienceSingle,
			Initiation:   session.InitiationDirect,
			Presentation: session.PresentationChat,
			Evaluation:   true,
		},
	}
	bootstrap, err := first.Conversations.Transcript.OpenOrCreateEndpointConversationContext(
		t.Context(), "character-foundation-conversation", binding,
		"abababababababababababababababababababababababababababababababab",
	)
	if err != nil {
		t.Fatalf("open endpoint conversation: %v", err)
	}
	turn, err := first.Conversations.Transcript.BeginCorrelatedTurnContext(
		t.Context(), bootstrap.Conversation.ID, "记住这段跨 Store 对话。", "foundation-message-1",
	)
	if err != nil {
		t.Fatalf("begin correlated turn: %v", err)
	}
	assistant, err := first.Conversations.Transcript.CompleteExpressionTurnContext(
		t.Context(), bootstrap.Conversation.ID, turn.ID, "我会记住。",
		[]historyexpr.Part{{Kind: historyexpr.Utterance, Text: "我会记住。", VisualState: "idle"}},
	)
	if err != nil {
		t.Fatalf("complete expression turn: %v", err)
	}
	completedState := "completed"
	if _, err := first.Conversations.Runtime.AppendTurnRuntimeEventContext(t.Context(), historyruntime.TurnRuntimeEventInput{
		ConversationID: bootstrap.Conversation.ID,
		TurnID:         turn.ID,
		EventType:      "foundation.integration",
		State:          &completedState,
		MetadataJSON:   `{"authority":"seekdb"}`,
	}); err != nil {
		t.Fatalf("append runtime event: %v", err)
	}
	plan, err := first.Conversations.Transcript.LoadConversationPromptContext(t.Context(), bootstrap.Conversation.ID)
	if err != nil {
		t.Fatalf("load prompt plan: %v", err)
	}
	continuation := historyruntime.LaneContinuationRecord{
		ConversationID:     bootstrap.Conversation.ID,
		Lane:               string(model.PromptLaneCompact),
		PreviousResponseID: "foundation-response-1",
		RequestShapeHash:   strings.Repeat("a", 64),
		InputPrefixHash:    strings.Repeat("b", 64),
		ResponseItemHash:   strings.Repeat("c", 64),
		WindowRevision:     plan.PromptWindow.Revision,
	}
	if _, err := first.Conversations.Runtime.SaveLaneContinuationContext(t.Context(), continuation); err != nil {
		t.Fatalf("save continuation: %v", err)
	}
	window := historyruntime.ContextWindowRecord{
		ConversationID:       bootstrap.Conversation.ID,
		Lane:                 string(model.PromptLaneCompact),
		WindowNumber:         1,
		FirstWindowID:        "foundation-window-1",
		WindowID:             "foundation-window-1",
		LastTrigger:          "foundation-integration",
		PromptWindowRevision: plan.PromptWindow.Revision + 1,
	}
	if _, err := first.Conversations.Compaction.CommitCompactionContext(
		t.Context(), bootstrap.Conversation.ID, plan.PromptWindow.Revision, plan.TranscriptBoundary,
		"首轮对话已压缩。", window, string(model.PromptLaneCompact),
	); err != nil {
		t.Fatalf("commit prompt projection: %v", err)
	}
	if _, found, err := first.Conversations.Runtime.LoadLaneContinuationContext(t.Context(), bootstrap.Conversation.ID, string(model.PromptLaneCompact)); err != nil || found {
		t.Fatalf("continuation after compaction = (found %v, %v), want cleared", found, err)
	}
	stalePlan, err := first.Conversations.Transcript.LoadConversationPromptContext(t.Context(), bootstrap.Conversation.ID)
	if err != nil {
		t.Fatalf("load stale compaction plan: %v", err)
	}
	newTurn, err := first.Conversations.Transcript.BeginTurnContext(t.Context(), bootstrap.Conversation.ID, "这是压缩计划后的新消息。")
	if err != nil {
		t.Fatalf("begin turn after compaction plan: %v", err)
	}
	if _, err := first.Conversations.Compaction.CommitPromptWindowContext(
		t.Context(), bootstrap.Conversation.ID, stalePlan.PromptWindow.Revision, stalePlan.TranscriptBoundary, "过期摘要",
	); !errors.Is(err, historycompaction.ErrPromptWindowRevisionChanged) {
		t.Fatalf("stale compaction error = %v", err)
	}
	if loaded, err := first.Conversations.Transcript.LoadConversationContext(t.Context(), bootstrap.Conversation.ID); err != nil || len(loaded.Messages) != 3 {
		t.Fatalf("full transcript before restart = (%#v, %v)", loaded.Messages, err)
	}
	if active, err := first.Conversations.Transcript.LoadConversationPromptContext(t.Context(), bootstrap.Conversation.ID); err != nil || len(active.Messages) != 1 || active.Messages[0].TurnID != newTurn.ID {
		t.Fatalf("active prompt before restart = (%#v, %v)", active.Messages, err)
	}

	closeFoundationIntegration(t, first)
	second := openFoundationIntegration(t, environment, characterRoot)
	defer closeFoundationIntegration(t, second)
	loaded, err := second.Conversations.Transcript.LoadConversationContext(t.Context(), bootstrap.Conversation.ID)
	if err != nil || len(loaded.Messages) != 3 || loaded.Messages[1].ID != assistant.ID || loaded.Messages[2].TurnID != newTurn.ID {
		t.Fatalf("restarted transcript = (%#v, %v)", loaded.Messages, err)
	}
	active, err := second.Conversations.Transcript.LoadConversationPromptContext(t.Context(), bootstrap.Conversation.ID)
	if err != nil || len(active.Messages) != 1 || active.Messages[0].TurnID != newTurn.ID {
		t.Fatalf("restarted active prompt = (%#v, %v)", active.Messages, err)
	}
	events, err := second.Conversations.Runtime.ListTurnRuntimeEventsContext(t.Context(), bootstrap.Conversation.ID, turn.ID)
	if err != nil || len(events) != 1 || events[0].EventType != "foundation.integration" {
		t.Fatalf("restarted runtime events = (%#v, %v)", events, err)
	}
	restoredWindow, found, err := second.Conversations.Runtime.LoadContextWindowContext(t.Context(), bootstrap.Conversation.ID, string(model.PromptLaneCompact))
	if err != nil || !found || restoredWindow.WindowID != window.WindowID {
		t.Fatalf("restarted context window = (%#v, found %v, %v)", restoredWindow, found, err)
	}
	environment.assertNoLegacyReads(t)
}

func TestRealSeekDBFoundationRuntimeChainPersistsConversationToolsAndObservabilityAcrossRestart(t *testing.T) {
	environment := newFoundationIntegrationEnvironment(t)
	characterRoot := filepath.Join(t.TempDir(), "characters")
	first := openFoundationIntegration(t, environment, characterRoot)
	defer closeFoundationIntegration(t, first)
	if first.Observability.Ledger == nil || first.Observability.History == nil {
		t.Fatal("foundation observability stores were not composed")
	}

	binding := session.Binding{
		Endpoint: session.EndpointDesktop,
		Facts: session.Facts{
			Audience:     session.AudienceSingle,
			Initiation:   session.InitiationDirect,
			Presentation: session.PresentationChat,
			Evaluation:   true,
		},
	}
	bootstrap, err := first.Conversations.Transcript.OpenOrCreateEndpointConversationContext(
		t.Context(), "character-foundation-runtime-chain", binding,
		"abababababababababababababababababababababababababababababababab",
	)
	if err != nil {
		t.Fatalf("open endpoint conversation: %v", err)
	}
	turn, err := first.Conversations.Transcript.BeginCorrelatedTurnContext(
		t.Context(), bootstrap.Conversation.ID, "请观察桌面。", "foundation-runtime-message-1",
	)
	if err != nil {
		t.Fatalf("begin correlated turn: %v", err)
	}
	if _, err := first.Conversations.Transcript.CompleteExpressionTurnContext(
		t.Context(), bootstrap.Conversation.ID, turn.ID, "我会看一眼。",
		[]historyexpr.Part{{Kind: historyexpr.Utterance, Text: "我会看一眼。", VisualState: "idle"}},
	); err != nil {
		t.Fatalf("complete expression turn: %v", err)
	}

	toolTurn, err := first.Conversations.Transcript.BeginTurnContext(t.Context(), bootstrap.Conversation.ID, "继续观察。")
	if err != nil {
		t.Fatalf("begin tool turn: %v", err)
	}
	created, err := first.Observability.Ledger.CreateToolExecution(t.Context(), ledger.CreateToolExecutionInput{
		ConversationID: bootstrap.Conversation.ID, TurnID: toolTurn.ID, CallID: "call-foundation-observe",
		ToolName: ledger.ToolNameDesktopObserve, DeadlineAtUnixMS: time.Now().UnixMilli() + 60_000,
	})
	if err != nil {
		t.Fatalf("create tool execution: %v", err)
	}
	completed, changed, err := first.Observability.Ledger.CompleteToolExecution(t.Context(), ledger.CompleteToolExecutionInput{
		ID: created.ID, ConversationID: created.ConversationID, TurnID: created.TurnID, CallID: created.CallID,
		ResultMediaType: "image/png", ResultWidth: 1280, ResultHeight: 720,
		ResultByteCount: 4096, ResultSHA256: strings.Repeat("a", 64),
	})
	if err != nil || !changed || completed.Status != ledger.ToolExecutionCompleted {
		t.Fatalf("complete tool execution = (%#v, %v, %v)", completed, changed, err)
	}

	logs := observability.NewLogStore(observability.DefaultLogCapacity)
	logs.SetHistorySink(first.Observability.History.EnqueueLog)
	logs.Append(observability.EntryInput{
		Time: time.Now(), Level: "info", Logger: "core", Message: "foundation-runtime-chain-safe",
		Fields: []observability.FieldInput{{Key: "status", Value: "ok"}},
	})

	metrics := observability.NewMessageMetrics()
	metrics.SetTerminalSink(first.Observability.History.EnqueueTrace)
	traceID := metrics.BeginCorrelated("direct", bootstrap.Conversation.ID, "foundation-runtime-message-1")
	metrics.Participation([]string{traceID}, traceID, "reply")
	metrics.TurnStarted(traceID, bootstrap.Conversation.ID, turn.ID)
	modelSpan := metrics.StartSpan(traceID, "", "模型调用", "model", map[string]string{
		"attempt": "1", "model": "demo", "query": "must-not-survive",
	})
	metrics.FinishSpan(modelSpan, "completed", map[string]string{"status": "ok", "prompt": "must-not-survive"})
	metrics.End(traceID, "completed")
	metrics.Close()
	liveSnapshot := metrics.Snapshot()

	firstStarted := time.Now().UnixMilli() - 5_000
	if !first.Observability.History.EnqueueMetric(observability.MetricHistoryPoint{
		TimestampUnixMS: firstStarted + 1_000, ProcessStartedUnixMS: firstStarted,
		HTTPScope: "conversation", Goroutines: 11, HTTPInFlight: 2, MessagesActive: 1,
	}) {
		t.Fatal("enqueue first-process metric")
	}

	conversationID := bootstrap.Conversation.ID
	closeFoundationIntegration(t, first)

	second := openFoundationIntegration(t, environment, characterRoot)
	defer closeFoundationIntegration(t, second)
	loaded, err := second.Conversations.Transcript.LoadConversationContext(t.Context(), conversationID)
	if err != nil || len(loaded.Messages) < 3 {
		t.Fatalf("restarted transcript = (%#v, %v)", loaded, err)
	}
	restoredTool, found, err := second.Observability.Ledger.LoadToolExecution(t.Context(), created.ID)
	if err != nil || !found || restoredTool.Status != ledger.ToolExecutionCompleted || restoredTool.ResultSHA256 == nil || *restoredTool.ResultSHA256 != strings.Repeat("a", 64) {
		t.Fatalf("restarted tool execution = (%#v, found %v, %v)", restoredTool, found, err)
	}

	restoredLogs, err := second.Observability.History.RecentLogs(t.Context(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if !containsLogMessage(restoredLogs, "foundation-runtime-chain-safe") {
		t.Fatalf("restarted logs = %#v", restoredLogs)
	}
	for _, entry := range restoredLogs {
		payload, _ := json.Marshal(entry)
		assertNoSensitiveObservabilityPayload(t, payload)
	}

	restoredTrace, found, err := second.Observability.History.Trace(t.Context(), traceID)
	if err != nil || !found || restoredTrace.Status != "completed" || restoredTrace.ConversationID != conversationID || restoredTrace.TurnID != turn.ID {
		t.Fatalf("restarted trace = (%#v, found %v, %v)", restoredTrace, found, err)
	}
	if restoredTrace.EndedAtUnixMS < restoredTrace.StartedAtUnixMS || restoredTrace.DurationMS != uint64(restoredTrace.EndedAtUnixMS-restoredTrace.StartedAtUnixMS) {
		t.Fatalf("restarted trace timing = %#v", restoredTrace)
	}
	var model observability.TraceSpan
	for _, span := range restoredTrace.Spans {
		if span.SpanID == modelSpan {
			model = span
			break
		}
	}
	if model.SpanID == "" || model.ParentSpanID == "" || model.Category != "model" || model.Attributes["status"] != "ok" {
		t.Fatalf("restarted model span = %#v from %#v", model, restoredTrace.Spans)
	}
	tracePayload, _ := json.Marshal(restoredTrace)
	assertNoSensitiveObservabilityPayload(t, tracePayload)

	restoredMetrics, err := second.Observability.History.RecentMetrics(t.Context(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if !containsMetricProcess(restoredMetrics, firstStarted, 11) {
		t.Fatalf("restarted metrics missing first process sample: %#v", restoredMetrics)
	}

	fresh := observability.NewMessageMetrics()
	t.Cleanup(fresh.Close)
	if snapshot := fresh.Snapshot(); snapshot.Received != 0 || snapshot.Active != 0 || len(snapshot.Recent) != 0 {
		t.Fatalf("new process inherited live message metrics: %#v", snapshot)
	}
	if liveSnapshot.Received == 0 {
		t.Fatal("first process produced no live message metrics")
	}

	secondStarted := time.Now().UnixMilli()
	if !second.Observability.History.EnqueueMetric(observability.MetricHistoryPoint{
		TimestampUnixMS: secondStarted, ProcessStartedUnixMS: secondStarted,
		HTTPScope: "conversation", Goroutines: 3, HTTPInFlight: 0, MessagesActive: 0,
	}) {
		t.Fatal("enqueue second-process metric")
	}
	closeFoundationIntegration(t, second)
	third := openFoundationIntegration(t, environment, characterRoot)
	defer closeFoundationIntegration(t, third)
	trend, err := third.Observability.History.RecentMetrics(t.Context(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if !containsMetricProcess(trend, firstStarted, 11) || !containsMetricProcess(trend, secondStarted, 3) {
		t.Fatalf("metric trend after second restart = %#v", trend)
	}

	environment.assertNoLegacyReads(t)
}

func containsLogMessage(entries []observability.LogEntry, message string) bool {
	for _, entry := range entries {
		if entry.Message == message {
			return true
		}
	}
	return false
}

func containsMetricProcess(points []observability.MetricHistoryPoint, processStarted int64, goroutines uint64) bool {
	for _, point := range points {
		if point.ProcessStartedUnixMS == processStarted && point.Goroutines == goroutines {
			return true
		}
	}
	return false
}

func assertNoSensitiveObservabilityPayload(t *testing.T, payload []byte) {
	t.Helper()
	for _, forbidden := range [][]byte{[]byte("prompt"), []byte("credential"), []byte("principal"), []byte("must-not-survive")} {
		if bytes.Contains(bytes.ToLower(payload), forbidden) {
			t.Fatalf("observability payload contains %q: %s", forbidden, payload)
		}
	}
}

func TestRealSeekDBFoundationStatusFailsClosedAfterSchemaJournalCorruption(t *testing.T) {
	environment := newFoundationIntegrationEnvironment(t)
	characterRoot := filepath.Join(t.TempDir(), "characters")
	opened := openFoundationIntegration(t, environment, characterRoot)
	defer closeFoundationIntegration(t, opened)
	assertFoundationIntegrationCurrent(t, opened)

	database, err := opened.SQL()
	if err != nil {
		t.Fatal(err)
	}
	revision := seekdb.CurrentSchemaRevision()
	corruptChecksum := bytes.Repeat([]byte{0x7f}, len(revision.Checksum))
	if bytes.Equal(corruptChecksum, revision.Checksum[:]) {
		t.Fatal("test checksum unexpectedly matches the current migration")
	}
	if _, err := database.ExecContext(t.Context(), `
UPDATE schema_revisions
SET checksum = ?
WHERE revision = ?`, corruptChecksum, revision.Number); err != nil {
		t.Fatalf("corrupt schema journal: %v", err)
	}
	before := readFoundationIntegrationJournal(t, database, revision.Number)

	status, err := opened.Status(t.Context())
	if status != (Status{}) || !errors.Is(err, ErrSchemaNotReady) || !errors.Is(err, seekdb.ErrSchemaChecksumMismatch) {
		t.Fatalf("Status() after checksum corruption = (%#v, %v), want fail closed", status, err)
	}
	after := readFoundationIntegrationJournal(t, database, revision.Number)
	if !bytes.Equal(before.checksum, after.checksum) || before.state != after.state || before.attempts != after.attempts {
		t.Fatalf("readiness repaired or mutated the schema journal: before=%#v after=%#v", before, after)
	}

	closeFoundationIntegration(t, opened)
	restarted, err := Open(t.Context(), Options{CharacterRoot: characterRoot, Getenv: environment.getenv})
	if restarted != nil || !errors.Is(err, seekdb.ErrSchemaChecksumMismatch) {
		t.Fatalf("Open() with corrupt journal = (%#v, %v), want checksum failure", restarted, err)
	}
	environment.assertNoLegacyReads(t)
}

func TestRealSeekDBFoundationRejectsUnsafeMasterKeyPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows permission semantics are covered by platform unit tests")
	}
	environment := newFoundationIntegrationEnvironment(t)
	characterRoot := filepath.Join(t.TempDir(), "characters")
	opened := openFoundationIntegration(t, environment, characterRoot)
	closeFoundationIntegration(t, opened)

	keyPath := filepath.Join(environment.values[seekdb.EnvDataDir], "secrets", "master.key")
	if err := os.Chmod(keyPath, 0o644); err != nil {
		t.Fatalf("make master key unsafe: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(keyPath, 0o600) })

	restarted, err := Open(t.Context(), Options{CharacterRoot: characterRoot, Getenv: environment.getenv})
	if restarted != nil || !errors.Is(err, config.ErrMasterKeyPermissions) {
		t.Fatalf("Open() with unsafe master key = (%#v, %v), want permission failure", restarted, err)
	}
	environment.assertNoLegacyReads(t)
}

type foundationIntegrationEnvironment struct {
	values    map[string]string
	requested []string
}

func newFoundationIntegrationEnvironment(t *testing.T) *foundationIntegrationEnvironment {
	t.Helper()
	binary := os.Getenv(seekdb.EnvBinaryPath)
	if binary == "" {
		t.Skip(seekdb.EnvBinaryPath + " is not set")
	}
	return &foundationIntegrationEnvironment{values: map[string]string{
		seekdb.EnvBinaryPath:     binary,
		seekdb.EnvLibraryPath:    os.Getenv(seekdb.EnvLibraryPath),
		seekdb.EnvDataDir:        filepath.Join(t.TempDir(), "seekdb-data"),
		seekdb.EnvAddress:        reserveFoundationIntegrationAddress(t),
		seekdb.EnvDatabase:       seekdb.DefaultDatabase,
		seekdb.EnvUser:           seekdb.DefaultUser,
		seekdb.EnvConnectLimit:   "5s",
		seekdb.EnvStartLimit:     "90s",
		seekdb.EnvQueryLimit:     "15s",
		seekdb.EnvShutdownLimit:  "20s",
		seekdb.EnvMaxOpenConns:   "8",
		seekdb.EnvMaxIdleConns:   "4",
		"FAIRY_DATABASE_URL":     "postgres://invalid-legacy-sentinel",
		"FAIRY_DB_QUERY_TIMEOUT": "invalid-legacy-sentinel",
		"FAIRY_PGVECTOR_URL":     "http://invalid-legacy-sentinel",
		"PGVECTOR_URL":           "http://invalid-legacy-sentinel",
		"FAIRY_QDRANT_URL":       "http://invalid-legacy-sentinel",
		"QDRANT_URL":             "http://invalid-legacy-sentinel",
	}}
}

func (e *foundationIntegrationEnvironment) getenv(name string) string {
	e.requested = append(e.requested, name)
	return e.values[name]
}

func (e *foundationIntegrationEnvironment) assertNoLegacyReads(t *testing.T) {
	t.Helper()
	for _, name := range e.requested {
		upper := strings.ToUpper(name)
		if strings.Contains(upper, "POSTGRES") || strings.Contains(upper, "PGVECTOR") || strings.Contains(upper, "QDRANT") || name == "FAIRY_DATABASE_URL" || strings.HasPrefix(upper, "FAIRY_DB_") {
			t.Fatalf("foundation requested forbidden legacy environment variable %q", name)
		}
	}
}

func openFoundationIntegration(t *testing.T, environment *foundationIntegrationEnvironment, characterRoot string) *Foundation {
	t.Helper()
	opened, err := Open(t.Context(), Options{CharacterRoot: characterRoot, Getenv: environment.getenv})
	if err != nil {
		t.Fatalf("open real SeekDB foundation: %v", err)
	}
	t.Cleanup(func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), opened.ShutdownLimit())
		defer cancel()
		if err := opened.Close(closeCtx); err != nil {
			t.Errorf("cleanup real SeekDB foundation: %v", err)
		}
	})
	return opened
}

func closeFoundationIntegration(t *testing.T, opened *Foundation) {
	t.Helper()
	closeCtx, cancel := context.WithTimeout(context.Background(), opened.ShutdownLimit())
	defer cancel()
	if err := opened.Close(closeCtx); err != nil {
		t.Fatalf("close real SeekDB foundation: %v", err)
	}
}

func assertFoundationIntegrationCurrent(t *testing.T, opened *Foundation) {
	t.Helper()
	status, err := opened.Status(t.Context())
	if err != nil {
		t.Fatalf("read foundation status: %v", err)
	}
	if status.Storage != "seekdb" || status.Schema.State != seekdb.SchemaCurrent || status.Schema.Observed == nil || status.Schema.Observed.Revision != seekdb.CurrentSchemaRevision() || !status.SecretsReady {
		t.Fatalf("foundation status = %#v", status)
	}
}

type foundationIntegrationJournal struct {
	checksum []byte
	state    string
	attempts int64
}

func readFoundationIntegrationJournal(t *testing.T, database interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, revision int64) foundationIntegrationJournal {
	t.Helper()
	var row foundationIntegrationJournal
	if err := database.QueryRowContext(t.Context(), `
SELECT checksum, status, attempt_count
FROM schema_revisions
WHERE revision = ?`, revision).Scan(&row.checksum, &row.state, &row.attempts); err != nil {
		t.Fatal(err)
	}
	return row
}

func reserveFoundationIntegrationAddress(t *testing.T) string {
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

func writeFoundationIntegrationVisual(t *testing.T, root, packID string) {
	t.Helper()
	writeFoundationIntegrationFile(
		t,
		filepath.Join(root, "visual-packs", packID, "manifest.json"),
		`{"schemaVersion":2,"packId":"`+packID+`","displayName":"Fairy","renderer":"state_images","frame":{"width":128,"height":128},"scale":1,"anchor":{"x":64,"y":127},"states":[{"id":"idle","description":"idle","imagePath":"fairy-character://localhost/`+packID+`/idle.png"}]}`,
	)
}

func writeFoundationIntegrationLegacyCharacter(t *testing.T, root, characterID string) {
	t.Helper()
	writeFoundationIntegrationFile(
		t,
		filepath.Join(root, "characters", characterID, "revisions", "1.json"),
		`{"schema_version":1,"data":{"schema_version":1,"compiler_version":"fairy-character-v1","character_id":"`+characterID+`","revision":1,"identity":{"name":"旧文件角色","description":"不应被读取。"},"worldview":"not_specified","attention_biases":["user_explicit_content"],"relationship_stance":"warm_respectful_non_possessive","response_drives":["understand_before_assuming"],"emotional_tendencies":["calm_attunement"],"speech_style":{"character_description_guidance":"不应被读取。","fallback":"natural_concise"},"hard_boundaries":["preserve_facts"],"fingerprint":"legacy-fixture"}}`,
	)
	writeFoundationIntegrationFile(
		t,
		filepath.Join(root, "character-appearances", characterID+".json"),
		`{"schema_version":1,"data":{"character_id":"`+characterID+`","revision":1,"visual_pack_id":"fairy.atri"}}`,
	)
	writeFoundationIntegrationFile(
		t,
		filepath.Join(root, "active-character.json"),
		`{"schema_version":1,"data":{"character_id":"`+characterID+`","revision":1}}`,
	)
}

func writeFoundationIntegrationFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
