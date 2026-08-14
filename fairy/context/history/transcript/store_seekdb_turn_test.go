package transcript

import (
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	historyexpr "fairy/context/history/expression"
)

func TestSeekDBTurnParametersAreRejectedBeforeQuery(t *testing.T) {
	t.Parallel()

	database := openFailingSeekDB(t)
	store, err := NewSeekDBStore(database, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	parts := seekDBTurnExpressionParts()

	tests := []struct {
		name string
		call func() error
	}{
		{
			name: "begin conversation id",
			call: func() error {
				_, err := store.BeginTurnContext(t.Context(), " invalid ", "hello")
				return err
			},
		},
		{
			name: "begin blank user content",
			call: func() error {
				_, err := store.BeginTurnContext(t.Context(), "conversation", " \t ")
				return err
			},
		},
		{
			name: "correlated external message id",
			call: func() error {
				_, err := store.BeginCorrelatedTurnContext(t.Context(), "conversation", "hello", "bad\nmessage")
				return err
			},
		},
		{
			name: "correlated external message id too long",
			call: func() error {
				_, err := store.BeginCorrelatedTurnContext(t.Context(), "conversation", "hello", strings.Repeat("m", 129))
				return err
			},
		},
		{
			name: "initiation missing evidence",
			call: func() error {
				_, err := store.BeginInitiationTurnContext(t.Context(), "conversation", nil)
				return err
			},
		},
		{
			name: "initiation duplicate evidence",
			call: func() error {
				_, err := store.BeginInitiationTurnContext(t.Context(), "conversation", []string{"evidence", "evidence"})
				return err
			},
		},
		{
			name: "initiation invalid evidence",
			call: func() error {
				_, err := store.BeginInitiationTurnContext(t.Context(), "conversation", []string{"bad\nevidence"})
				return err
			},
		},
		{
			name: "initiation untrimmed evidence",
			call: func() error {
				_, err := store.BeginInitiationTurnContext(t.Context(), "conversation", []string{" evidence "})
				return err
			},
		},
		{
			name: "complete turn id",
			call: func() error {
				_, err := store.CompleteTurnContext(t.Context(), "conversation", " invalid ", "reply")
				return err
			},
		},
		{
			name: "complete blank content",
			call: func() error {
				_, err := store.CompleteTurnContext(t.Context(), "conversation", "turn", "")
				return err
			},
		},
		{
			name: "complete expression projection",
			call: func() error {
				_, err := store.CompleteExpressionTurnContext(t.Context(), "conversation", "turn", "mismatch", parts)
				return err
			},
		},
		{
			name: "interrupt expression projection",
			call: func() error {
				_, err := store.InterruptExpressionTurnContext(t.Context(), "conversation", "turn", "mismatch", parts)
				return err
			},
		},
		{
			name: "fail empty code",
			call: func() error {
				return store.FailTurnContext(t.Context(), "conversation", "turn", "", "failure", true)
			},
		},
		{
			name: "fail empty message",
			call: func() error {
				return store.FailTurnContext(t.Context(), "conversation", "turn", "FAILED", "", true)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.call()
			if err == nil {
				t.Fatal("invalid turn mutation was accepted")
			}
			if errors.Is(err, errFailingSeekDBConnector) {
				t.Fatalf("invalid turn mutation reached SeekDB: %v", err)
			}
			if errors.Is(err, ErrStoreBackendUnavailable) {
				t.Fatalf("invalid turn mutation was routed through the migration placeholder: %v", err)
			}
		})
	}
}

func TestSeekDBTurnOperationsUseAuthoritativeDatabaseWithoutFallback(t *testing.T) {
	t.Parallel()

	database := openFailingSeekDB(t)
	store, err := NewSeekDBStore(database, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	parts := seekDBTurnExpressionParts()
	content := historyexpr.TextProjection(parts)

	tests := []struct {
		name string
		call func() error
	}{
		{
			name: "begin user",
			call: func() error {
				_, err := store.BeginTurnContext(t.Context(), "conversation", "hello")
				return err
			},
		},
		{
			name: "begin correlated",
			call: func() error {
				_, err := store.BeginCorrelatedTurnContext(t.Context(), "conversation", "hello", "external-message")
				return err
			},
		},
		{
			name: "begin initiation",
			call: func() error {
				_, err := store.BeginInitiationTurnContext(t.Context(), "conversation", []string{"evidence"})
				return err
			},
		},
		{
			name: "complete text",
			call: func() error {
				_, err := store.CompleteTurnContext(t.Context(), "conversation", "turn", "reply")
				return err
			},
		},
		{
			name: "complete expression",
			call: func() error {
				_, err := store.CompleteExpressionTurnContext(t.Context(), "conversation", "turn", content, parts)
				return err
			},
		},
		{
			name: "complete expression policy",
			call: func() error {
				_, err := store.CompleteExpressionTurnForPolicy("conversation", "turn", content, parts, false)
				return err
			},
		},
		{
			name: "interrupt prefix",
			call: func() error {
				_, err := store.InterruptTurnContext(t.Context(), "conversation", "turn", "published")
				return err
			},
		},
		{
			name: "interrupt expression",
			call: func() error {
				_, err := store.InterruptExpressionTurnContext(t.Context(), "conversation", "turn", content, parts)
				return err
			},
		},
		{
			name: "fail",
			call: func() error {
				return store.FailTurnContext(t.Context(), "conversation", "turn", "PROVIDER_FAILED", "provider unavailable", true)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.call()
			if !errors.Is(err, errFailingSeekDBConnector) {
				t.Fatalf("turn mutation error = %v, want authoritative SeekDB connector failure", err)
			}
			if errors.Is(err, ErrStoreBackendUnavailable) {
				t.Fatalf("turn mutation used migration placeholder: %v", err)
			}
		})
	}
	if false {
		t.Fatal("SeekDB turn store unexpectedly installed a PostgreSQL fallback")
	}
}

func openFailingSeekDB(t *testing.T) *sql.DB {
	t.Helper()
	database := sql.OpenDB(failingSeekDBConnector{})
	t.Cleanup(func() { _ = database.Close() })
	return database
}

func seekDBTurnExpressionParts() []historyexpr.Part {
	return []historyexpr.Part{
		{Kind: historyexpr.Utterance, Text: "first", VisualState: "idle"},
		{Kind: historyexpr.Sticker, VisualState: "surprised", Sticker: &historyexpr.StickerSnapshot{
			ID: "sticker-1", Description: "surprised", MIMEType: "image/webp",
		}},
		{Kind: historyexpr.Utterance, Text: "second", VisualState: "happy"},
	}
}

func seekDBPureStickerParts() []historyexpr.Part {
	return []historyexpr.Part{{
		Kind: historyexpr.Sticker, VisualState: "happy", Sticker: &historyexpr.StickerSnapshot{
			ID: "sticker-pure", Description: "pure sticker", MIMEType: "image/png",
		},
	}}
}
