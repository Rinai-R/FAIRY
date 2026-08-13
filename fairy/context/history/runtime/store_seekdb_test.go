package runtime

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestNewSeekDBRuntimeStoreValidatesAuthorityAndQueryLimit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		database   *sql.DB
		queryLimit time.Duration
	}{
		{name: "missing authority", queryLimit: time.Second},
		{name: "zero query limit", database: sql.OpenDB(failingRuntimeSeekDBConnector{})},
		{name: "negative query limit", database: sql.OpenDB(failingRuntimeSeekDBConnector{}), queryLimit: -time.Second},
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

	database := sql.OpenDB(failingRuntimeSeekDBConnector{})
	t.Cleanup(func() { _ = database.Close() })
	store, err := NewSeekDBStore(database, 2*time.Second)
	if err != nil || store == nil {
		t.Fatalf("NewSeekDBStore(valid) = (%#v, %v)", store, err)
	}
}

func TestSeekDBRuntimeStoreNeverFallsBackWhenAuthorityFails(t *testing.T) {
	t.Parallel()

	database := sql.OpenDB(failingRuntimeSeekDBConnector{})
	t.Cleanup(func() { _ = database.Close() })
	store, err := NewSeekDBStore(database, time.Second)
	if err != nil {
		t.Fatal(err)
	}

	state := "planning"
	code := "MODEL_COMPLETED"
	hashA := strings.Repeat("a", 64)
	hashB := strings.Repeat("b", 64)
	hashC := strings.Repeat("c", 64)
	continuation := LaneContinuationRecord{
		ConversationID:     "conversation-no-fallback",
		Lane:               PromptLaneRespond,
		PreviousResponseID: "response-no-fallback",
		RequestShapeHash:   hashA,
		InputPrefixHash:    hashB,
		ResponseItemHash:   hashC,
		WindowRevision:     1,
	}
	window := ContextWindowRecord{
		ConversationID:       "conversation-no-fallback",
		Lane:                 PromptLaneRespond,
		WindowNumber:         1,
		FirstWindowID:        "window-first",
		WindowID:             "window-current",
		LastTrigger:          "created",
		PromptWindowRevision: 1,
	}
	event := TurnRuntimeEventInput{
		ConversationID: "conversation-no-fallback",
		TurnID:         "turn-no-fallback",
		EventType:      "model",
		State:          &state,
		Code:           &code,
		MetadataJSON:   `{"status":"complete"}`,
	}

	assertAuthorityFailure := func(name string, call func() error) {
		t.Helper()
		if err := call(); !errors.Is(err, errFailingRuntimeSeekDBAuthority) {
			t.Fatalf("%s error = %v, want failing SeekDB authority", name, err)
		}
	}
	assertAuthorityFailure("AppendTurnRuntimeEventContext", func() error {
		_, err := store.AppendTurnRuntimeEventContext(t.Context(), event)
		return err
	})
	assertAuthorityFailure("ListTurnRuntimeEventsContext", func() error {
		_, err := store.ListTurnRuntimeEventsContext(t.Context(), event.ConversationID, event.TurnID)
		return err
	})
	assertAuthorityFailure("SaveLaneContinuationContext", func() error {
		_, err := store.SaveLaneContinuationContext(t.Context(), continuation)
		return err
	})
	assertAuthorityFailure("LoadLaneContinuationContext", func() error {
		_, _, err := store.LoadLaneContinuationContext(t.Context(), continuation.ConversationID, continuation.Lane)
		return err
	})
	assertAuthorityFailure("ClearLaneContinuationContext", func() error {
		return store.ClearLaneContinuationContext(t.Context(), continuation.ConversationID, continuation.Lane)
	})
	assertAuthorityFailure("SaveContextWindowContext", func() error {
		_, err := store.SaveContextWindowContext(t.Context(), window)
		return err
	})
	assertAuthorityFailure("LoadContextWindowContext", func() error {
		_, _, err := store.LoadContextWindowContext(t.Context(), window.ConversationID, window.Lane)
		return err
	})
}

func TestSeekDBRuntimeParametersAreRejectedBeforeQuery(t *testing.T) {
	t.Parallel()

	database := sql.OpenDB(failingRuntimeSeekDBConnector{})
	t.Cleanup(func() { _ = database.Close() })
	store, err := NewSeekDBStore(database, time.Second)
	if err != nil {
		t.Fatal(err)
	}

	assertValidationFailure := func(name string, call func() error) {
		t.Helper()
		err := call()
		if err == nil || errors.Is(err, errFailingRuntimeSeekDBAuthority) {
			t.Fatalf("%s error = %v, want validation error before authority query", name, err)
		}
	}
	assertValidationFailure("runtime event metadata", func() error {
		_, err := store.AppendTurnRuntimeEventContext(t.Context(), TurnRuntimeEventInput{
			ConversationID: "conversation", TurnID: "turn", EventType: "model",
		})
		return err
	})
	assertValidationFailure("runtime event list id", func() error {
		_, err := store.ListTurnRuntimeEventsContext(t.Context(), " invalid ", "turn")
		return err
	})
	assertValidationFailure("lane continuation hash", func() error {
		_, err := store.SaveLaneContinuationContext(t.Context(), LaneContinuationRecord{
			ConversationID: "conversation", Lane: PromptLaneRespond,
			PreviousResponseID: "response", RequestShapeHash: "invalid",
			InputPrefixHash: strings.Repeat("b", 64), ResponseItemHash: strings.Repeat("c", 64),
			WindowRevision: 1,
		})
		return err
	})
	assertValidationFailure("lane load", func() error {
		_, _, err := store.LoadLaneContinuationContext(t.Context(), "conversation", "unknown")
		return err
	})
	assertValidationFailure("context window revision", func() error {
		_, err := store.SaveContextWindowContext(t.Context(), ContextWindowRecord{
			ConversationID: "conversation", Lane: PromptLaneRespond,
			FirstWindowID: "first", WindowID: "current", LastTrigger: "created",
		})
		return err
	})
}

var errFailingRuntimeSeekDBAuthority = errors.New("test runtime SeekDB authority unavailable")

type failingRuntimeSeekDBConnector struct{}

func (failingRuntimeSeekDBConnector) Connect(context.Context) (driver.Conn, error) {
	return nil, errFailingRuntimeSeekDBAuthority
}

func (failingRuntimeSeekDBConnector) Driver() driver.Driver { return failingRuntimeSeekDBDriver{} }

type failingRuntimeSeekDBDriver struct{}

func (failingRuntimeSeekDBDriver) Open(string) (driver.Conn, error) {
	return nil, errFailingRuntimeSeekDBAuthority
}
