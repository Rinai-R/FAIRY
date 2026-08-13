package compaction

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"testing"
	"time"

	historyprojection "fairy/context/history/projection"
	historyruntime "fairy/context/history/runtime"
	historytranscript "fairy/context/history/transcript"
)

func TestNewSeekDBCompactionStoreValidatesAuthorityAndQueryLimit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		database   *sql.DB
		queryLimit time.Duration
	}{
		{name: "missing authority", queryLimit: time.Second},
		{name: "zero query limit", database: sql.OpenDB(failingCompactionSeekDBConnector{})},
		{name: "negative query limit", database: sql.OpenDB(failingCompactionSeekDBConnector{}), queryLimit: -time.Second},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if test.database != nil {
				t.Cleanup(func() { _ = test.database.Close() })
			}
			store, err := NewSeekDBStore(test.database, test.queryLimit)
			if store != nil || err == nil {
				t.Fatalf("NewSeekDBStore() = (%#v, %v), want nil and an error", store, err)
			}
		})
	}

	database := sql.OpenDB(failingCompactionSeekDBConnector{})
	t.Cleanup(func() { _ = database.Close() })
	store, err := NewSeekDBStore(database, 2*time.Second)
	if err != nil || store == nil {
		t.Fatalf("NewSeekDBStore(valid) = (%#v, %v)", store, err)
	}
}

func TestSeekDBCompactionStoreNeverFallsBackWhenAuthorityFails(t *testing.T) {
	t.Parallel()

	database := sql.OpenDB(failingCompactionSeekDBConnector{})
	t.Cleanup(func() { _ = database.Close() })
	store, err := NewSeekDBStore(database, time.Second)
	if err != nil {
		t.Fatal(err)
	}

	const conversationID = "conversation-no-fallback"
	projection := historyprojection.Empty()
	window := validSeekDBCompactionWindow(conversationID, 2)
	assertAuthorityFailure := func(name string, call func() error) {
		t.Helper()
		if err := call(); !errors.Is(err, errFailingCompactionSeekDBAuthority) {
			t.Fatalf("%s error = %v, want failing SeekDB authority", name, err)
		}
	}
	assertAuthorityFailure("CommitPromptWindowContext", func() error {
		_, err := store.CommitPromptWindowContext(t.Context(), conversationID, 1, seekDBTestBoundary(), "summary")
		return err
	})
	assertAuthorityFailure("CommitCompactionContext", func() error {
		_, err := store.CommitCompactionContext(
			t.Context(), conversationID, 1, seekDBTestBoundary(), "summary", window, historyruntime.PromptLaneRespond,
		)
		return err
	})
	assertAuthorityFailure("CommitPromptProjectionContext", func() error {
		_, err := store.CommitPromptProjectionContext(
			t.Context(), conversationID, 1, 1, seekDBTestBoundary(),
			projection, window, historyruntime.PromptLaneRespond,
		)
		return err
	})
	assertAuthorityFailure("CommitTieredCompactionContext", func() error {
		_, err := store.CommitTieredCompactionContext(
			t.Context(), conversationID, 1, 1, seekDBTestBoundary(), "summary", 0,
			projection, window, historyruntime.PromptLaneRespond,
		)
		return err
	})
}

func TestSeekDBCompactionParametersAreRejectedBeforeQuery(t *testing.T) {
	t.Parallel()

	database := sql.OpenDB(failingCompactionSeekDBConnector{})
	t.Cleanup(func() { _ = database.Close() })
	store, err := NewSeekDBStore(database, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	assertValidationFailure := func(name string, call func() error) {
		t.Helper()
		err := call()
		if err == nil || errors.Is(err, errFailingCompactionSeekDBAuthority) {
			t.Fatalf("%s error = %v, want validation error before authority query", name, err)
		}
	}

	assertValidationFailure("missing prompt revision", func() error {
		_, err := store.CommitPromptWindowContext(t.Context(), "conversation", 0, seekDBTestBoundary(), "summary")
		return err
	})
	assertValidationFailure("invalid prompt summary", func() error {
		_, err := store.CommitPromptWindowContext(t.Context(), "conversation", 1, seekDBTestBoundary(), " \t ")
		return err
	})
	assertValidationFailure("prompt revision overflow", func() error {
		_, err := store.CommitPromptWindowContext(t.Context(), "conversation", uint64(1<<63-1), seekDBTestBoundary(), "summary")
		return err
	})

	wrongConversation := validSeekDBCompactionWindow("other-conversation", 2)
	assertValidationFailure("compaction window conversation", func() error {
		_, err := store.CommitCompactionContext(
			t.Context(), "conversation", 1, seekDBTestBoundary(), "summary", wrongConversation, historyruntime.PromptLaneRespond,
		)
		return err
	})
	wrongRevision := validSeekDBCompactionWindow("conversation", 3)
	assertValidationFailure("compaction window revision", func() error {
		_, err := store.CommitCompactionContext(
			t.Context(), "conversation", 1, seekDBTestBoundary(), "summary", wrongRevision, historyruntime.PromptLaneRespond,
		)
		return err
	})

	window := validSeekDBCompactionWindow("conversation", 2)
	assertValidationFailure("projection revisions", func() error {
		_, err := store.CommitPromptProjectionContext(
			t.Context(), "conversation", 0, 1, seekDBTestBoundary(),
			historyprojection.Empty(), window, historyruntime.PromptLaneRespond,
		)
		return err
	})
	assertValidationFailure("projection state", func() error {
		_, err := store.CommitPromptProjectionContext(
			t.Context(), "conversation", 1, 1, seekDBTestBoundary(),
			historyprojection.State{}, window, historyruntime.PromptLaneRespond,
		)
		return err
	})
	assertValidationFailure("projection revision overflow", func() error {
		_, err := store.CommitPromptProjectionContext(
			t.Context(), "conversation", 1, uint64(1<<63-1), seekDBTestBoundary(),
			historyprojection.Empty(), window, historyruntime.PromptLaneRespond,
		)
		return err
	})
	assertValidationFailure("tiered cutoff overflow", func() error {
		_, err := store.CommitTieredCompactionContext(
			t.Context(), "conversation", 1, 1, seekDBTestBoundary(), "summary", uint64(1<<63),
			historyprojection.Empty(), window, historyruntime.PromptLaneRespond,
		)
		return err
	})
}

func validSeekDBCompactionWindow(conversationID string, revision uint64) historyruntime.ContextWindowRecord {
	return historyruntime.ContextWindowRecord{
		ConversationID:       conversationID,
		Lane:                 historyruntime.PromptLaneRespond,
		WindowNumber:         revision,
		FirstWindowID:        "window-first",
		WindowID:             "window-current",
		LastTrigger:          "compaction_committed",
		PromptWindowRevision: revision,
	}
}

func seekDBTestBoundary() historytranscript.TranscriptBoundary {
	return historytranscript.TranscriptBoundary{TurnSequence: 2, MessageSequence: 4}
}

var errFailingCompactionSeekDBAuthority = errors.New("test compaction SeekDB authority unavailable")

type failingCompactionSeekDBConnector struct{}

func (failingCompactionSeekDBConnector) Connect(context.Context) (driver.Conn, error) {
	return nil, errFailingCompactionSeekDBAuthority
}

func (failingCompactionSeekDBConnector) Driver() driver.Driver {
	return failingCompactionSeekDBDriver{}
}

type failingCompactionSeekDBDriver struct{}

func (failingCompactionSeekDBDriver) Open(string) (driver.Conn, error) {
	return nil, errFailingCompactionSeekDBAuthority
}
