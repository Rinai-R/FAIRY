//go:build integration

package transcript

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"fairy/runtime/seekdb"
	"fairy/runtime/seekdb/seekdbtest"
	"fairy/transport/session"
)

func TestRealSeekDBTranscriptQueriesAreIsolatedOrderedAndPersistent(t *testing.T) {
	instance, database, runtimeConfig := openTranscriptSeekDBRuntime(t)
	closed := false
	t.Cleanup(func() {
		if !closed {
			closeTranscriptSeekDBRuntime(t, instance, runtimeConfig.ShutdownLimit)
		}
	})
	if err := seekdb.MigrateSchema(t.Context(), database, seekdb.BuiltinMigrations()); err != nil {
		t.Fatalf("migrate SeekDB transcript schema: %v", err)
	}
	store, err := NewSeekDBStore(database, runtimeConfig.QueryLimit)
	if err != nil {
		t.Fatal(err)
	}
	store.now = func() time.Time { return time.UnixMilli(1_786_300_000_000) }

	defaultConversationID := assertConcurrentCharacterConversation(t, store, "character-atri", 8)
	assertTranscriptGraphCounts(t, database, transcriptGraphCounts{
		conversations: 1, promptWindows: 1, characterBindings: 1,
	})
	defaultBinding, found, err := store.LookupEndpointForConversationContext(t.Context(), defaultConversationID)
	if err != nil || found || defaultBinding != (session.Binding{}) {
		t.Fatalf("default conversation endpoint lookup = (%#v, %v, %v)", defaultBinding, found, err)
	}

	groupBinding := transcriptGroupBinding()
	groupDigest := transcriptDigest("onebot-group:100")
	groupConversationID := assertConcurrentEndpointConversation(t, store, "character-atri", groupBinding, groupDigest, 8)
	assertTranscriptGraphCounts(t, database, transcriptGraphCounts{
		conversations: 2, promptWindows: 2, characterBindings: 1, endpointBindings: 1,
	})
	secondGroup, err := store.OpenOrCreateEndpointConversationContext(
		t.Context(), "character-atri", groupBinding, transcriptDigest("onebot-group:200"),
	)
	if err != nil {
		t.Fatalf("open second group conversation: %v", err)
	}
	if secondGroup.Conversation.ID == groupConversationID || secondGroup.Conversation.ID == defaultConversationID {
		t.Fatalf("group isolation reused conversation %q", secondGroup.Conversation.ID)
	}

	privateBinding := transcriptPrivateBinding("qq.onebot", transcriptDigest("onebot-user:300"))
	privateConversation, err := store.OpenOrCreateEndpointConversationContext(
		t.Context(), "character-atri", privateBinding, transcriptDigest("onebot-private:300"),
	)
	if err != nil {
		t.Fatalf("open private conversation: %v", err)
	}
	if privateConversation.Conversation.ID == groupConversationID ||
		privateConversation.Conversation.ID == secondGroup.Conversation.ID ||
		privateConversation.Conversation.ID == defaultConversationID {
		t.Fatalf("private conversation was not isolated: %q", privateConversation.Conversation.ID)
	}
	storedPrivateBinding, found, err := store.LookupEndpointForConversationContext(t.Context(), privateConversation.Conversation.ID)
	if err != nil || !found || storedPrivateBinding != privateBinding {
		t.Fatalf("private endpoint lookup = (%#v, %v, %v), want %#v", storedPrivateBinding, found, err, privateBinding)
	}
	assertTranscriptGraphCounts(t, database, transcriptGraphCounts{
		conversations: 4, promptWindows: 4, characterBindings: 1, endpointBindings: 3,
	})

	mismatchedGroup := groupBinding
	mismatchedGroup.Facts.Initiation = session.InitiationDirect
	updatedBeforeMismatch := transcriptEndpointUpdatedAt(t, database, groupConversationID)
	if _, err := store.OpenOrCreateEndpointConversationContext(
		t.Context(), "character-atri", mismatchedGroup, groupDigest,
	); !errors.Is(err, ErrEndpointBindingMismatch) {
		t.Fatalf("binding mismatch error = %v, want %v", err, ErrEndpointBindingMismatch)
	}
	if updatedAfterMismatch := transcriptEndpointUpdatedAt(t, database, groupConversationID); updatedAfterMismatch != updatedBeforeMismatch {
		t.Fatalf("binding mismatch changed updated_at_ms from %d to %d", updatedBeforeMismatch, updatedAfterMismatch)
	}

	const repeatedExternalMessageID = "onebot-message-repeated"
	turnMessageIDs := []string{
		repeatedExternalMessageID,
		"onebot-message-2",
		strings.Repeat("界", 128),
		repeatedExternalMessageID,
		"onebot-message-5",
	}
	var pairedTurnID string
	for index := len(turnMessageIDs) - 1; index >= 0; index-- {
		sequence := uint64(index + 1)
		turnID := seedTranscriptTurnAndMessage(
			t, database, groupConversationID, sequence, turnMessageIDs[index],
			fmt.Sprintf("message-%d", sequence),
			1_786_300_001_000-int64(sequence*100),
		)
		if sequence == 5 {
			pairedTurnID = turnID
		}
	}
	seedTranscriptMessage(t, database, groupConversationID, pairedTurnID, 6, "assistant", "message-6", 1_786_300_001_600)
	messageIDs := append(append([]string(nil), turnMessageIDs...), turnMessageIDs[4])

	var repeatedAssociations int
	if err := database.QueryRowContext(t.Context(), `
SELECT COUNT(*) FROM conversation_turns
WHERE conversation_id = ? AND message_id = ?`, groupConversationID, repeatedExternalMessageID).Scan(&repeatedAssociations); err != nil {
		t.Fatalf("count repeated external message associations: %v", err)
	}
	if repeatedAssociations != 2 {
		t.Fatalf("repeated external message associations = %d, want 2", repeatedAssociations)
	}
	seedTranscriptTurnAndMessage(
		t, database, secondGroup.Conversation.ID, 1, repeatedExternalMessageID,
		"message-1", 1_786_300_002_000,
	)

	loaded, err := store.LoadConversationContext(t.Context(), groupConversationID)
	if err != nil {
		t.Fatalf("LoadConversationContext(): %v", err)
	}
	assertTranscriptMessages(t, loaded.Messages, []uint64{1, 2, 3, 4, 5, 6}, messageIDs)
	if loaded.Messages[4].TurnID != pairedTurnID || loaded.Messages[5].TurnID != pairedTurnID ||
		loaded.Messages[4].Role != "user" || loaded.Messages[5].Role != "assistant" ||
		loaded.Messages[4].MessageID != turnMessageIDs[4] || loaded.Messages[5].MessageID != turnMessageIDs[4] {
		t.Fatalf("same-turn external message association was not projected to both roles: %#v / %#v", loaded.Messages[4], loaded.Messages[5])
	}
	if loaded.Conversation.ID != groupConversationID || loaded.Conversation.CharacterID != "character-atri" {
		t.Fatalf("loaded conversation = %#v", loaded.Conversation)
	}
	if loaded.PromptWindow.ConversationID != groupConversationID || loaded.PromptWindow.Revision != 1 || loaded.PromptWindow.ProjectionRevision != 1 {
		t.Fatalf("loaded prompt window = %#v", loaded.PromptWindow)
	}
	for _, isolatedConversationID := range []string{defaultConversationID, privateConversation.Conversation.ID} {
		isolated, err := store.LoadConversationContext(t.Context(), isolatedConversationID)
		if err != nil {
			t.Fatalf("load isolated conversation %q: %v", isolatedConversationID, err)
		}
		if len(isolated.Messages) != 0 {
			t.Fatalf("conversation %q leaked %d group messages", isolatedConversationID, len(isolated.Messages))
		}
	}
	secondGroupLoaded, err := store.LoadConversationContext(t.Context(), secondGroup.Conversation.ID)
	if err != nil {
		t.Fatalf("load second group conversation: %v", err)
	}
	assertTranscriptMessages(t, secondGroupLoaded.Messages, []uint64{1}, []string{repeatedExternalMessageID})

	pageOne, err := store.ListConversationMessagesBeforeContext(t.Context(), groupConversationID, 0, 2)
	if err != nil {
		t.Fatalf("first message page: %v", err)
	}
	assertTranscriptPage(t, pageOne, []uint64{5, 6}, messageIDs[4:6], 5)
	const appendedSequence = uint64(100)
	const appendedExternalMessageID = "onebot-message-appended-after-cursor"
	seedTranscriptTurnAndMessage(
		t, database, groupConversationID, 6, appendedExternalMessageID,
		fmt.Sprintf("message-%d", appendedSequence), 1_786_300_003_000,
		appendedSequence,
	)
	pageTwo, err := store.ListConversationMessagesBeforeContext(t.Context(), groupConversationID, *pageOne.NextBeforeSequence, 2)
	if err != nil {
		t.Fatalf("second message page: %v", err)
	}
	assertTranscriptPage(t, pageTwo, []uint64{3, 4}, messageIDs[2:4], 3)
	pageThree, err := store.ListConversationMessagesBeforeContext(t.Context(), groupConversationID, *pageTwo.NextBeforeSequence, 2)
	if err != nil {
		t.Fatalf("third message page: %v", err)
	}
	assertTranscriptPage(t, pageThree, []uint64{1, 2}, messageIDs[:2], 0)
	latestAfterAppend, err := store.ListConversationMessagesBeforeContext(t.Context(), groupConversationID, 0, 2)
	if err != nil {
		t.Fatalf("latest message page after append: %v", err)
	}
	assertTranscriptPage(t, latestAfterAppend, []uint64{6, appendedSequence}, []string{messageIDs[5], appendedExternalMessageID}, 6)

	if _, err := store.LoadConversationContext(t.Context(), "missing-conversation"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("LoadConversationContext(missing) error = %v, want sql.ErrNoRows", err)
	}
	if _, err := store.ListConversationMessagesBeforeContext(t.Context(), "missing-conversation", 0, 2); err == nil || !strings.Contains(err.Error(), "conversation does not exist") {
		t.Fatalf("ListConversationMessagesBeforeContext(missing) error = %v", err)
	}

	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := store.LoadConversationContext(canceled, groupConversationID); !errors.Is(err, context.Canceled) {
		t.Fatalf("LoadConversationContext(canceled) error = %v, want context.Canceled", err)
	}
	if _, err := store.ListConversationMessagesBeforeContext(canceled, groupConversationID, 0, 2); !errors.Is(err, context.Canceled) {
		t.Fatalf("ListConversationMessagesBeforeContext(canceled) error = %v, want context.Canceled", err)
	}

	closeTranscriptSeekDBRuntime(t, instance, runtimeConfig.ShutdownLimit)
	closed = true
	restarted, err := seekdb.Open(t.Context(), runtimeConfig)
	if err != nil {
		t.Fatalf("restart SeekDB transcript runtime: %v", err)
	}
	instance, database, closed = restarted, restarted.SQL(), false
	restartedStore, err := NewSeekDBStore(database, runtimeConfig.QueryLimit)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := restartedStore.LoadConversationContext(t.Context(), groupConversationID)
	if err != nil {
		t.Fatalf("load conversation after restart: %v", err)
	}
	restartedMessageIDs := append(append([]string(nil), messageIDs...), appendedExternalMessageID)
	assertTranscriptMessages(t, restored.Messages, []uint64{1, 2, 3, 4, 5, 6, appendedSequence}, restartedMessageIDs)
	restoredBinding, found, err := restartedStore.LookupEndpointForConversationContext(t.Context(), groupConversationID)
	if err != nil || !found || restoredBinding != groupBinding {
		t.Fatalf("group binding after restart = (%#v, %v, %v), want %#v", restoredBinding, found, err, groupBinding)
	}
	reopenedDefault, err := restartedStore.OpenOrCreateCharacterConversationContext(t.Context(), "character-atri")
	if err != nil || reopenedDefault.Conversation.ID != defaultConversationID {
		t.Fatalf("default conversation after restart = (%#v, %v), want %q", reopenedDefault, err, defaultConversationID)
	}
}

type transcriptGraphCounts struct {
	conversations     int
	promptWindows     int
	characterBindings int
	endpointBindings  int
}

func assertTranscriptGraphCounts(t *testing.T, database *sql.DB, want transcriptGraphCounts) {
	t.Helper()
	got := transcriptGraphCounts{}
	queries := []struct {
		query string
		out   *int
	}{
		{"SELECT COUNT(*) FROM conversations", &got.conversations},
		{"SELECT COUNT(*) FROM prompt_windows", &got.promptWindows},
		{"SELECT COUNT(*) FROM character_conversations", &got.characterBindings},
		{"SELECT COUNT(*) FROM endpoint_conversations", &got.endpointBindings},
	}
	for _, query := range queries {
		if err := database.QueryRowContext(t.Context(), query.query).Scan(query.out); err != nil {
			t.Fatalf("count transcript graph rows with %q: %v", query.query, err)
		}
	}
	if got != want {
		t.Fatalf("transcript graph counts = %#v, want %#v", got, want)
	}
	var orphanConversations int
	if err := database.QueryRowContext(t.Context(), `
SELECT COUNT(*)
FROM conversations c
LEFT JOIN character_conversations cc ON cc.conversation_id = c.id
LEFT JOIN endpoint_conversations ec ON ec.conversation_id = c.id
WHERE cc.conversation_id IS NULL AND ec.conversation_id IS NULL`).Scan(&orphanConversations); err != nil {
		t.Fatalf("count orphan transcript conversations: %v", err)
	}
	if orphanConversations != 0 {
		t.Fatalf("orphan transcript conversations = %d, want 0", orphanConversations)
	}
}

func transcriptEndpointUpdatedAt(t *testing.T, database *sql.DB, conversationID string) int64 {
	t.Helper()
	var updatedAt int64
	if err := database.QueryRowContext(t.Context(), `
SELECT updated_at_ms FROM endpoint_conversations WHERE conversation_id = ?`, conversationID).Scan(&updatedAt); err != nil {
		t.Fatalf("read endpoint updated_at_ms: %v", err)
	}
	return updatedAt
}

func assertConcurrentCharacterConversation(t *testing.T, store *Store, characterID string, count int) string {
	t.Helper()
	start := make(chan struct{})
	type result struct {
		conversationID string
		err            error
	}
	results := make(chan result, count)
	var wait sync.WaitGroup
	for range count {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			bootstrap, err := store.OpenOrCreateCharacterConversationContext(t.Context(), characterID)
			results <- result{conversationID: bootstrap.Conversation.ID, err: err}
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	var conversationID string
	for result := range results {
		if result.err != nil {
			t.Fatalf("concurrent character conversation: %v", result.err)
		}
		if conversationID == "" {
			conversationID = result.conversationID
		}
		if result.conversationID != conversationID {
			t.Fatalf("concurrent character conversation IDs differ: %q != %q", result.conversationID, conversationID)
		}
	}
	if conversationID == "" {
		t.Fatal("concurrent character conversation returned an empty ID")
	}
	return conversationID
}

func assertConcurrentEndpointConversation(t *testing.T, store *Store, characterID string, binding session.Binding, digest string, count int) string {
	t.Helper()
	start := make(chan struct{})
	type result struct {
		conversationID string
		err            error
	}
	results := make(chan result, count)
	var wait sync.WaitGroup
	for range count {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			bootstrap, err := store.OpenOrCreateEndpointConversationContext(t.Context(), characterID, binding, digest)
			results <- result{conversationID: bootstrap.Conversation.ID, err: err}
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	var conversationID string
	for result := range results {
		if result.err != nil {
			t.Fatalf("concurrent endpoint conversation: %v", result.err)
		}
		if conversationID == "" {
			conversationID = result.conversationID
		}
		if result.conversationID != conversationID {
			t.Fatalf("concurrent endpoint conversation IDs differ: %q != %q", result.conversationID, conversationID)
		}
	}
	if conversationID == "" {
		t.Fatal("concurrent endpoint conversation returned an empty ID")
	}
	return conversationID
}

func seedTranscriptTurnAndMessage(t *testing.T, database *sql.DB, conversationID string, turnSequence uint64, messageID, content string, createdAt int64, messageSequenceOverride ...uint64) string {
	t.Helper()
	turnID := fmt.Sprintf("turn-%s-%d", conversationID, turnSequence)
	if _, err := database.ExecContext(t.Context(), `
INSERT INTO conversation_turns(
    id, conversation_id, message_id, sequence, status, origin,
    error_code, error_message, error_retryable, extraction_state,
    extraction_claim_id, extraction_lease_owner, extraction_lease_expires_at_ms,
    extraction_attempt_count, extraction_next_attempt_at_ms,
    extraction_error_code, extraction_error_message, created_at_ms, updated_at_ms
) VALUES (?, ?, ?, ?, 'completed', 'user', NULL, NULL, 0, 'ineligible',
          NULL, NULL, NULL, 0, 0, NULL, NULL, ?, ?)`,
		turnID, conversationID, messageID, turnSequence, createdAt, createdAt,
	); err != nil {
		t.Fatalf("seed transcript turn %d: %v", turnSequence, err)
	}
	messageSequence := turnSequence
	if len(messageSequenceOverride) > 0 {
		messageSequence = messageSequenceOverride[0]
	}
	seedTranscriptMessage(t, database, conversationID, turnID, messageSequence, "user", content, createdAt)
	return turnID
}

func seedTranscriptMessage(t *testing.T, database *sql.DB, conversationID, turnID string, sequence uint64, role, content string, createdAt int64) {
	t.Helper()
	if _, err := database.ExecContext(t.Context(), `
INSERT INTO conversation_messages(
	    id, conversation_id, turn_id, sequence, role, content, expression_parts, created_at_ms
) VALUES (?, ?, ?, ?, ?, ?, '[]', ?)`,
		fmt.Sprintf("message-%s-%d-%s", conversationID, sequence, role), conversationID, turnID, sequence, role, content, createdAt,
	); err != nil {
		t.Fatalf("seed transcript message %d: %v", sequence, err)
	}
}

func assertTranscriptMessages(t *testing.T, messages []MessageRecord, sequences []uint64, messageIDs []string) {
	t.Helper()
	if len(messages) != len(sequences) || len(messages) != len(messageIDs) {
		t.Fatalf("message count = %d, want sequences %v and message IDs %v", len(messages), sequences, messageIDs)
	}
	for index, message := range messages {
		if message.Sequence != sequences[index] || message.MessageID != messageIDs[index] {
			t.Fatalf("message %d = sequence %d, external ID %q; want %d, %q", index, message.Sequence, message.MessageID, sequences[index], messageIDs[index])
		}
		if message.Content != fmt.Sprintf("message-%d", message.Sequence) {
			t.Fatalf("message %d content = %q", index, message.Content)
		}
	}
}

func assertTranscriptPage(t *testing.T, page MessagePage, sequences []uint64, messageIDs []string, next uint64) {
	t.Helper()
	assertTranscriptMessages(t, page.Messages, sequences, messageIDs)
	if next == 0 {
		if page.NextBeforeSequence != nil {
			t.Fatalf("next cursor = %d, want nil", *page.NextBeforeSequence)
		}
		return
	}
	if page.NextBeforeSequence == nil || *page.NextBeforeSequence != next {
		t.Fatalf("next cursor = %v, want %d", page.NextBeforeSequence, next)
	}
}

func transcriptGroupBinding() session.Binding {
	return session.Binding{Endpoint: session.EndpointIM, Facts: session.Facts{
		Audience: session.AudienceMulti, Initiation: session.InitiationAmbient,
		Presentation: session.PresentationChat,
	}}
}

func transcriptPrivateBinding(namespace, principalDigest string) session.Binding {
	return session.Binding{Endpoint: session.EndpointIM, Facts: session.Facts{
		Audience: session.AudienceSingle, Initiation: session.InitiationDirect,
		Presentation:       session.PresentationChat,
		PrincipalNamespace: namespace, PrincipalDigest: principalDigest,
	}}
}

func transcriptDigest(value string) string {
	digest := sha256.Sum256([]byte(value))
	return fmt.Sprintf("%x", digest[:])
}

func openTranscriptSeekDBRuntime(t *testing.T) (*seekdb.Runtime, *sql.DB, seekdb.Config) {
	t.Helper()
	library := os.Getenv(seekdb.EnvLibrary)
	if library == "" {
		t.Skip(seekdb.EnvLibrary + " is not set")
	}
	config := seekdb.Config{
		LibraryPath:   library,
		DataDir:       seekdbtest.DataDir(t),
		Database:      seekdb.DefaultDatabase,
		ConnectLimit:  5 * time.Second,
		StartLimit:    90 * time.Second,
		QueryLimit:    15 * time.Second,
		ShutdownLimit: 20 * time.Second,
		MaxOpenConns:  16,
		MaxIdleConns:  8,
	}
	instance, err := seekdb.Open(t.Context(), config)
	if err != nil {
		t.Fatalf("open real SeekDB transcript runtime: %v", err)
	}
	return instance, instance.SQL(), config
}

func reserveTranscriptLoopbackAddress(t *testing.T) string {
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

func closeTranscriptSeekDBRuntime(t *testing.T, instance *seekdb.Runtime, timeout time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := instance.Close(ctx); err != nil {
		t.Errorf("close real SeekDB transcript runtime: %v", err)
	}
}
