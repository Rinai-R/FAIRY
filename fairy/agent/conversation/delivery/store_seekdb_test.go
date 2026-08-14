package delivery

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"testing"
	"time"

	"fairy/transport/session"
)

func TestNewSeekDBStoreRejectsInvalidInputs(t *testing.T) {
	database := sql.OpenDB(deliveryUnitConnector{})
	t.Cleanup(func() { _ = database.Close() })
	tests := []struct {
		name       string
		database   *sql.DB
		queryLimit time.Duration
		want       error
	}{
		{name: "missing database", queryLimit: time.Second, want: ErrSeekDBRequired},
		{name: "invalid query limit", database: database, want: ErrQueryLimitInvalid},
		{name: "valid", database: database, queryLimit: time.Second},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, err := NewSeekDBStore(test.database, test.queryLimit)
			if !errors.Is(err, test.want) {
				t.Fatalf("NewSeekDBStore() error = %v, want %v", err, test.want)
			}
			if test.want == nil && (store == nil || store.database != test.database) {
				t.Fatalf("NewSeekDBStore() = %#v", store)
			}
		})
	}
}

func TestSeekDBStoreRecordRejectsInvalidResultWithoutQuerying(t *testing.T) {
	database := sql.OpenDB(deliveryUnitConnector{})
	t.Cleanup(func() { _ = database.Close() })
	store, err := NewSeekDBStore(database, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	err = store.Record(t.Context(), session.ExpressionDeliveryResult{
		ConversationID: "conversation-1", TurnID: "turn-1", BeatID: "beat-1",
		Status: session.ExpressionDeliveryFailed,
	})
	if err == nil {
		t.Fatal("Record(invalid) succeeded")
	}
	if errors.Is(err, errUnexpectedDeliveryConnection) {
		t.Fatalf("Record queried SeekDB before Validate(): %v", err)
	}
}

type deliveryUnitConnector struct{}

func (deliveryUnitConnector) Connect(context.Context) (driver.Conn, error) {
	return nil, errUnexpectedDeliveryConnection
}

func (deliveryUnitConnector) Driver() driver.Driver { return deliveryUnitDriver{} }

type deliveryUnitDriver struct{}

func (deliveryUnitDriver) Open(string) (driver.Conn, error) {
	return nil, errUnexpectedDeliveryConnection
}

var errUnexpectedDeliveryConnection = errors.New("unexpected expression delivery database connection")
