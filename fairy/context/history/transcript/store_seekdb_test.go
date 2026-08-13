package transcript

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"
	"time"

	gomysql "github.com/go-sql-driver/mysql"
)

func TestNewSeekDBStoreValidatesAuthorityAndQueryLimit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		database   *sql.DB
		queryLimit time.Duration
		wantError  error
	}{
		{name: "missing authority", queryLimit: time.Second, wantError: ErrSeekDBConnectionEmpty},
		{name: "zero query limit", database: sql.OpenDB(failingSeekDBConnector{}), wantError: ErrSeekDBQueryLimitInvalid},
		{name: "negative query limit", database: sql.OpenDB(failingSeekDBConnector{}), queryLimit: -time.Second, wantError: ErrSeekDBQueryLimitInvalid},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if test.database != nil {
				t.Cleanup(func() { _ = test.database.Close() })
			}
			store, err := NewSeekDBStore(test.database, test.queryLimit)
			if store != nil || !errors.Is(err, test.wantError) {
				t.Fatalf("NewSeekDBStore() = (%#v, %v), want nil and %v", store, err, test.wantError)
			}
		})
	}

	database := sql.OpenDB(failingSeekDBConnector{})
	t.Cleanup(func() { _ = database.Close() })
	store, err := NewSeekDBStore(database, 2*time.Second)
	if err != nil {
		t.Fatalf("NewSeekDBStore(valid) error = %v", err)
	}
	if store.seekDB != database || store.pool != nil || store.queryLimit != 2*time.Second {
		t.Fatalf("NewSeekDBStore(valid) = %#v", store)
	}
}

func TestSeekDBStoreDoesNotFallbackWhenAuthorityFails(t *testing.T) {
	t.Parallel()

	database := sql.OpenDB(failingSeekDBConnector{})
	t.Cleanup(func() { _ = database.Close() })
	store, err := NewSeekDBStore(database, time.Second)
	if err != nil {
		t.Fatal(err)
	}

	_, err = store.LoadConversationContext(t.Context(), "conversation-no-fallback")
	if !errors.Is(err, errFailingSeekDBConnector) {
		t.Fatalf("LoadConversationContext() error = %v, want failing SeekDB authority", err)
	}
	if store.pool != nil {
		t.Fatal("SeekDB store unexpectedly installed a PostgreSQL fallback")
	}
}

func TestSeekDBConversationParametersAreRejectedBeforeQuery(t *testing.T) {
	t.Parallel()

	database := sql.OpenDB(failingSeekDBConnector{})
	t.Cleanup(func() { _ = database.Close() })
	store, err := NewSeekDBStore(database, time.Second)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := store.OpenOrCreateCharacterConversationContext(t.Context(), " invalid "); err == nil || errors.Is(err, errFailingSeekDBConnector) {
		t.Fatalf("OpenOrCreateCharacterConversationContext(invalid) error = %v", err)
	}
	for _, invalid := range []string{"角色", strings.Repeat("a", 129), "character\nnewline"} {
		if _, err := store.OpenOrCreateCharacterConversationContext(t.Context(), invalid); err == nil || errors.Is(err, errFailingSeekDBConnector) {
			t.Fatalf("OpenOrCreateCharacterConversationContext(%q) error = %v", invalid, err)
		}
	}
	if _, err := store.ListConversationMessagesBeforeContext(t.Context(), "conversation", 0, 0); err == nil || errors.Is(err, errFailingSeekDBConnector) {
		t.Fatalf("ListConversationMessagesBeforeContext(limit 0) error = %v", err)
	}
	if _, err := store.ListConversationMessagesBeforeContext(t.Context(), "conversation", 0, MaxMessagePageLimit+1); err == nil || errors.Is(err, errFailingSeekDBConnector) {
		t.Fatalf("ListConversationMessagesBeforeContext(too large) error = %v", err)
	}
}

func TestSeekDBDuplicateErrorClassificationIsTyped(t *testing.T) {
	t.Parallel()

	if !isDuplicateSeekDBError(&gomysql.MySQLError{Number: 1062, Message: "duplicate"}) {
		t.Fatal("MySQL duplicate error was not classified")
	}
	if isDuplicateSeekDBError(&gomysql.MySQLError{Number: 1064, Message: "Duplicate entry text is not enough"}) {
		t.Fatal("non-duplicate MySQL error was classified from message text")
	}
	if isDuplicateSeekDBError(errors.New("Error 1062: Duplicate entry")) {
		t.Fatal("untyped error was classified from message text")
	}
}

var errFailingSeekDBConnector = errors.New("test SeekDB authority unavailable")

type failingSeekDBConnector struct{}

func (failingSeekDBConnector) Connect(context.Context) (driver.Conn, error) {
	return nil, errFailingSeekDBConnector
}

func (failingSeekDBConnector) Driver() driver.Driver { return failingSeekDBDriver{} }

type failingSeekDBDriver struct{}

func (failingSeekDBDriver) Open(string) (driver.Conn, error) {
	return nil, errFailingSeekDBConnector
}
