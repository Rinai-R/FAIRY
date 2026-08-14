package social

import (
	"context"
	"errors"
	"testing"
)

func TestNewSeekDBStoreRequiresConnection(t *testing.T) {
	if _, err := NewSeekDBStore(nil, 1); !errors.Is(err, ErrSeekDBConnectionEmpty) {
		t.Fatalf("NewSeekDBStore(nil) error = %v", err)
	}
}

func TestSeekDBSocialStoreDoesNotFallBackToPostgres(t *testing.T) {
	store := &Store{}
	if store.usesSeekDB() || store.usesPostgres() {
		t.Fatal("empty store reported a backend")
	}
	if _, err := store.StoreSocialMemoryEntries(context.Background(), SocialMemoryBatchInput{
		CharacterID: "character-1", ConversationID: "conversation-1",
		Entries: []SocialMemoryEntryInput{{
			Kind: SocialMemoryEpisode, Situation: "群里讨论找实习", Content: "项目经历要能经得住追问",
			RecallCue: "找实习", SourceStartUnixMS: 1, SourceEndUnixMS: 2,
		}},
	}); !errors.Is(err, ErrStoreBackendUnavailable) {
		t.Fatalf("StoreSocialMemoryEntries() error = %v, want %v", err, ErrStoreBackendUnavailable)
	}
	if _, err := store.RetrieveSocialMemoryContext(context.Background(), "character-1", "conversation-1", "找实习"); !errors.Is(err, ErrStoreBackendUnavailable) {
		t.Fatalf("RetrieveSocialMemoryContext() error = %v, want %v", err, ErrStoreBackendUnavailable)
	}
	if _, err := store.RecordSocialFeedbackBatch(context.Background(), SocialFeedbackBatchInput{
		CharacterID: "character-1", ConversationID: "conversation-1", TurnID: "turn-1",
		ObservedMessageCount: 1, EvaluatorRevision: "social-feedback-v1",
		Evaluations: []SocialFeedbackEvaluation{{
			EntryID: "entry-1", Adoption: SocialFeedbackAdopted, Outcome: SocialFeedbackPositive,
			Credit: SocialFeedbackCreditEntry, EvidenceMessageIDs: []string{"message-1"},
		}},
	}); !errors.Is(err, ErrStoreBackendUnavailable) {
		t.Fatalf("RecordSocialFeedbackBatch() error = %v, want %v", err, ErrStoreBackendUnavailable)
	}
	if _, err := store.UpsertSocialPersonNote(context.Background(), SocialPersonNoteInput{
		CharacterID: "character-1", ConversationID: "conversation-1", SenderID: "u1", Note: "常吐槽但会接话",
	}); !errors.Is(err, ErrStoreBackendUnavailable) {
		t.Fatalf("UpsertSocialPersonNote() error = %v, want %v", err, ErrStoreBackendUnavailable)
	}
}

func TestSocialMemoryContentDigestIsBinaryAndMatchesHexHash(t *testing.T) {
	entry := SocialMemoryEntryInput{
		Kind: SocialMemoryExpression, Situation: "群友用反讽方式夸张吐槽时",
		Content: "用一小句顺着反讽接话，不解释梗", RecallCue: "轻松群聊中的反讽和抽象梗",
	}
	digest := socialMemoryContentDigest(entry)
	if len(digest) != 32 {
		t.Fatalf("digest length = %d, want 32", len(digest))
	}
	hexHash := SocialMemoryContentHash(entry)
	if encoded := fmtHex(digest[:]); encoded != hexHash {
		t.Fatalf("digest hex = %s, SocialMemoryContentHash = %s", encoded, hexHash)
	}
	if SocialMemoryContentHash(entry) != hexHash {
		t.Fatal("social memory content hash is not stable")
	}
}

func TestSocialMemorySearchUsesLiteralOnlyForWildcardQueries(t *testing.T) {
	if socialMemorySearchUsesLiteralOnly("实习焦虑") {
		t.Fatal("natural query used literal-only search")
	}
	if !socialMemorySearchUsesLiteralOnly("qx%_z9abc") {
		t.Fatal("wildcard query did not use literal-only search")
	}
}

func TestSQLPlaceholdersAreBounded(t *testing.T) {
	if got := sqlPlaceholders(0); got != "" {
		t.Fatalf("sqlPlaceholders(0) = %q", got)
	}
	if got := sqlPlaceholders(1); got != "?" {
		t.Fatalf("sqlPlaceholders(1) = %q", got)
	}
	if got := sqlPlaceholders(3); got != "?,?,?" {
		t.Fatalf("sqlPlaceholders(3) = %q", got)
	}
}

func fmtHex(value []byte) string {
	const hexDigits = "0123456789abcdef"
	encoded := make([]byte, len(value)*2)
	for index, item := range value {
		encoded[index*2] = hexDigits[item>>4]
		encoded[index*2+1] = hexDigits[item&0x0f]
	}
	return string(encoded)
}
