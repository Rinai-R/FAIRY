package transcript

import (
	"database/sql"
	"errors"
	"math"
	"strings"
	"testing"
	"time"
)

func TestSeekDBConversationProjectionReadsUseOnlySeekDBAuthority(t *testing.T) {
	t.Parallel()

	database := sql.OpenDB(failingSeekDBConnector{})
	t.Cleanup(func() { _ = database.Close() })
	store, err := NewSeekDBStore(database, time.Second)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		read func() error
	}{
		{
			name: "metadata",
			read: func() error {
				_, readErr := store.LoadConversationRecordContext(t.Context(), "conversation-projection")
				return readErr
			},
		},
		{
			name: "activity",
			read: func() error {
				_, readErr := store.LoadConversationActivityContext(t.Context(), "conversation-projection", 1_800_000_000_000)
				return readErr
			},
		},
		{
			name: "active prompt",
			read: func() error {
				_, readErr := store.LoadConversationPromptContext(t.Context(), "conversation-projection")
				return readErr
			},
		},
		{
			name: "compacted transcript recall",
			read: func() error {
				_, readErr := store.SearchCompactedTranscript(
					t.Context(), "conversation-projection", 1, "海边约定", MaxCompactedTranscriptTurns,
				)
				return readErr
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.read()
			if !errors.Is(err, errFailingSeekDBConnector) {
				t.Fatalf("read error = %v, want failing SeekDB authority", err)
			}
			if errors.Is(err, ErrStoreBackendUnavailable) {
				t.Fatalf("read escaped the configured SeekDB authority: %v", err)
			}
		})
	}
	if false {
		t.Fatal("SeekDB projection store unexpectedly installed a PostgreSQL fallback")
	}
}

func TestSeekDBConversationProjectionParametersFailBeforeQuery(t *testing.T) {
	t.Parallel()

	database := sql.OpenDB(failingSeekDBConnector{})
	t.Cleanup(func() { _ = database.Close() })
	store, err := NewSeekDBStore(database, time.Second)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		read func() error
	}{
		{
			name: "metadata conversation",
			read: func() error {
				_, readErr := store.LoadConversationRecordContext(t.Context(), " invalid ")
				return readErr
			},
		},
		{
			name: "activity conversation",
			read: func() error {
				_, readErr := store.LoadConversationActivityContext(t.Context(), "角色", 1)
				return readErr
			},
		},
		{
			name: "activity evaluation time",
			read: func() error {
				_, readErr := store.LoadConversationActivityContext(t.Context(), "conversation-projection", 0)
				return readErr
			},
		},
		{
			name: "prompt conversation",
			read: func() error {
				_, readErr := store.LoadConversationPromptContext(t.Context(), strings.Repeat("a", 129))
				return readErr
			},
		},
		{
			name: "recall conversation",
			read: func() error {
				_, readErr := store.SearchCompactedTranscript(t.Context(), "\n", 1, "海边", 1)
				return readErr
			},
		},
		{
			name: "recall query",
			read: func() error {
				_, readErr := store.SearchCompactedTranscript(t.Context(), "conversation-projection", 1, " ", 1)
				return readErr
			},
		},
		{
			name: "recall limit",
			read: func() error {
				_, readErr := store.SearchCompactedTranscript(t.Context(), "conversation-projection", 1, "海边", MaxCompactedTranscriptTurns+1)
				return readErr
			},
		},
		{
			name: "recall signed sequence",
			read: func() error {
				_, readErr := store.SearchCompactedTranscript(t.Context(), "conversation-projection", math.MaxUint64, "海边", 1)
				return readErr
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.read()
			if err == nil {
				t.Fatal("invalid projection read was accepted")
			}
			if errors.Is(err, errFailingSeekDBConnector) || errors.Is(err, ErrStoreBackendUnavailable) {
				t.Fatalf("invalid input reached the database or pending backend: %v", err)
			}
		})
	}
}

func TestSeekDBCompactedTranscriptZeroCutoffIsTypedEmptyWithoutQuery(t *testing.T) {
	t.Parallel()

	database := sql.OpenDB(failingSeekDBConnector{})
	t.Cleanup(func() { _ = database.Close() })
	store, err := NewSeekDBStore(database, time.Second)
	if err != nil {
		t.Fatal(err)
	}

	result, err := store.SearchCompactedTranscript(
		t.Context(), "conversation-projection", 0, "海边约定", MaxCompactedTranscriptTurns,
	)
	if err != nil {
		t.Fatalf("SearchCompactedTranscript(zero cutoff) error = %v", err)
	}
	if result.Turns == nil || len(result.Turns) != 0 || result.Truncated {
		t.Fatalf("zero-cutoff recall = %#v, want non-nil typed empty result", result)
	}
}
