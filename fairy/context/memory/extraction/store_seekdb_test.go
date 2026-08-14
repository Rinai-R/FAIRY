package extraction

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"testing"
	"time"

	"fairy/context/memory/personal"
)

type seekDBExtractionCoordinator interface {
	PendingExtractionTurnCountContext(context.Context, string) (uint64, error)
	ClaimExtractionTurnsContext(context.Context, string, int) (*ClaimedBatch, error)
	FailExtractionBatchContext(context.Context, string, string, string, bool) error
	ExtractionBatchCatalogContext(context.Context, string) (Catalog, error)
	RetryExtractionBatchContext(context.Context, string) error
}

var _ seekDBExtractionCoordinator = (*Store)(nil)

func TestNewSeekDBExtractionStoreValidatesConfiguration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		database   *sql.DB
		queryLimit time.Duration
		workerID   string
		lease      time.Duration
	}{
		{name: "missing authority", queryLimit: time.Second, workerID: "worker", lease: time.Minute},
		{name: "zero query limit", database: sql.OpenDB(failingExtractionSeekDBConnector{}), workerID: "worker", lease: time.Minute},
		{name: "negative query limit", database: sql.OpenDB(failingExtractionSeekDBConnector{}), queryLimit: -time.Second, workerID: "worker", lease: time.Minute},
		{name: "empty worker", database: sql.OpenDB(failingExtractionSeekDBConnector{}), queryLimit: time.Second, lease: time.Minute},
		{name: "untrimmed worker", database: sql.OpenDB(failingExtractionSeekDBConnector{}), queryLimit: time.Second, workerID: " worker ", lease: time.Minute},
		{name: "zero lease", database: sql.OpenDB(failingExtractionSeekDBConnector{}), queryLimit: time.Second, workerID: "worker"},
		{name: "negative lease", database: sql.OpenDB(failingExtractionSeekDBConnector{}), queryLimit: time.Second, workerID: "worker", lease: -time.Minute},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if test.database != nil {
				t.Cleanup(func() { _ = test.database.Close() })
			}
			store, err := NewSeekDBStore(test.database, test.queryLimit, test.workerID, test.lease)
			if store != nil || err == nil {
				t.Fatalf("NewSeekDBStore() = (%#v, %v), want nil and validation error", store, err)
			}
		})
	}

	database := sql.OpenDB(failingExtractionSeekDBConnector{})
	t.Cleanup(func() { _ = database.Close() })
	store, err := NewSeekDBStore(database, 2*time.Second, "worker-valid", time.Minute)
	if err != nil || store == nil {
		t.Fatalf("NewSeekDBStore(valid) = (%#v, %v)", store, err)
	}
}

func TestNewSeekDBExtractionStoreWithPersonalRequiresOneAuthority(t *testing.T) {
	t.Parallel()

	database := sql.OpenDB(failingExtractionSeekDBConnector{})
	otherDatabase := sql.OpenDB(failingExtractionSeekDBConnector{})
	t.Cleanup(func() {
		_ = database.Close()
		_ = otherDatabase.Close()
	})
	personalStore, err := personal.NewSeekDBStore(database, time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	otherPersonalStore, err := personal.NewSeekDBStore(otherDatabase, time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}

	if store, err := NewSeekDBStoreWithPersonal(
		database, time.Second, "worker-personal", time.Minute, nil,
	); store != nil || !errors.Is(err, ErrPersonalStoreEmpty) {
		t.Fatalf("missing personal authority = (%#v, %v)", store, err)
	}
	if store, err := NewSeekDBStoreWithPersonal(
		database, time.Second, "worker-personal", time.Minute, otherPersonalStore,
	); store != nil || !errors.Is(err, ErrPersonalAuthorityMismatch) {
		t.Fatalf("mismatched personal authority = (%#v, %v)", store, err)
	}
	store, err := NewSeekDBStoreWithPersonal(
		database, time.Second, "worker-personal", time.Minute, personalStore,
	)
	if err != nil || store == nil || store.personal != personalStore {
		t.Fatalf("shared personal authority = (%#v, %v)", store, err)
	}
}

func TestSeekDBExtractionCoordinatorNeverFallsBack(t *testing.T) {
	t.Parallel()

	database := sql.OpenDB(failingExtractionSeekDBConnector{})
	t.Cleanup(func() { _ = database.Close() })
	store, err := NewSeekDBStore(database, time.Second, "worker-authority", time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	assertAuthorityFailure := func(name string, call func() error) {
		t.Helper()
		if err := call(); !errors.Is(err, errFailingExtractionSeekDBAuthority) {
			t.Fatalf("%s error = %v, want failing SeekDB authority", name, err)
		}
	}
	assertAuthorityFailure("PendingExtractionTurnCountContext", func() error {
		_, err := store.PendingExtractionTurnCountContext(t.Context(), "conversation-authority")
		return err
	})
	assertAuthorityFailure("ClaimExtractionTurnsContext", func() error {
		_, err := store.ClaimExtractionTurnsContext(t.Context(), "conversation-authority", 2)
		return err
	})
	assertAuthorityFailure("FailExtractionBatchContext", func() error {
		return store.FailExtractionBatchContext(
			t.Context(), "batch-authority", "model_failed", "model failed", true,
		)
	})
	assertAuthorityFailure("ExtractionBatchCatalogContext", func() error {
		_, err := store.ExtractionBatchCatalogContext(t.Context(), "character-authority")
		return err
	})
	assertAuthorityFailure("RetryExtractionBatchContext", func() error {
		return store.RetryExtractionBatchContext(t.Context(), "turn-authority")
	})
}

func TestSeekDBExtractionPersonalSettlementAPIsFailClosedBeforeQuery(t *testing.T) {
	t.Parallel()

	database := sql.OpenDB(failingExtractionSeekDBConnector{})
	t.Cleanup(func() { _ = database.Close() })
	store, err := NewSeekDBStore(database, time.Second, "worker-settlement", time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	assertSettlementPending := func(name string, call func() error) {
		t.Helper()
		err := call()
		if !errors.Is(err, ErrPersonalSettlementPending) {
			t.Fatalf("%s error = %v, want ErrPersonalSettlementPending", name, err)
		}
		if errors.Is(err, errFailingExtractionSeekDBAuthority) {
			t.Fatalf("%s queried SeekDB before failing closed: %v", name, err)
		}
	}
	assertSettlementPending("ClaimExtractionBatchContext", func() error {
		_, err := store.ClaimExtractionBatchContext(t.Context(), "conversation", 1)
		return err
	})
	assertSettlementPending("EnrichClaimedBatchContext", func() error {
		_, err := store.EnrichClaimedBatchContext(t.Context(), &ClaimedBatch{
			BatchID: "batch", ConversationID: "conversation", CharacterID: "character",
			Turns: []Turn{{TurnID: "turn"}},
		})
		return err
	})
	assertSettlementPending("CommitClaimedMemoryMutationsContext", func() error {
		_, err := store.CommitClaimedMemoryMutationsContext(t.Context(), &BatchInput{}, nil)
		return err
	})
	assertSettlementPending("CompleteExtractionBatchContext", func() error {
		return store.CompleteExtractionBatchContext(t.Context(), "batch")
	})
	assertAuthorityFailure := func(name string, call func() error) {
		t.Helper()
		if err := call(); !errors.Is(err, errFailingExtractionSeekDBAuthority) {
			t.Fatalf("%s error = %v, want failing SeekDB authority", name, err)
		}
	}
	assertAuthorityFailure("LoadCommittedMemoryCoverageContext", func() error {
		_, err := store.LoadCommittedMemoryCoverageContext(t.Context(), "conversation")
		return err
	})
}

func TestSeekDBExtractionParametersFailBeforeQuery(t *testing.T) {
	t.Parallel()

	database := sql.OpenDB(failingExtractionSeekDBConnector{})
	t.Cleanup(func() { _ = database.Close() })
	store, err := NewSeekDBStore(database, time.Second, "worker-validation", time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	assertValidationFailure := func(name string, call func() error) {
		t.Helper()
		err := call()
		if err == nil || errors.Is(err, errFailingExtractionSeekDBAuthority) {
			t.Fatalf("%s error = %v, want validation error before authority query", name, err)
		}
	}
	assertValidationFailure("pending conversation", func() error {
		_, err := store.PendingExtractionTurnCountContext(t.Context(), " invalid ")
		return err
	})
	assertValidationFailure("claim conversation", func() error {
		_, err := store.ClaimExtractionTurnsContext(t.Context(), " invalid ", 1)
		return err
	})
	for _, limit := range []int{0, -1, DefaultBatchLimit + 1} {
		limit := limit
		assertValidationFailure("claim limit", func() error {
			_, err := store.ClaimExtractionTurnsContext(t.Context(), "conversation", limit)
			return err
		})
	}
	assertValidationFailure("fail batch", func() error {
		return store.FailExtractionBatchContext(t.Context(), " invalid ", "code", "message", true)
	})
	assertValidationFailure("fail code", func() error {
		return store.FailExtractionBatchContext(t.Context(), "batch", "", "message", true)
	})
	assertValidationFailure("fail message", func() error {
		return store.FailExtractionBatchContext(t.Context(), "batch", "code", "", true)
	})
	assertValidationFailure("catalog character", func() error {
		_, err := store.ExtractionBatchCatalogContext(t.Context(), " invalid ")
		return err
	})
	assertValidationFailure("retry turn", func() error {
		return store.RetryExtractionBatchContext(t.Context(), " invalid ")
	})
}

var errFailingExtractionSeekDBAuthority = errors.New("test extraction SeekDB authority unavailable")

type failingExtractionSeekDBConnector struct{}

func (failingExtractionSeekDBConnector) Connect(context.Context) (driver.Conn, error) {
	return nil, errFailingExtractionSeekDBAuthority
}

func (failingExtractionSeekDBConnector) Driver() driver.Driver {
	return failingExtractionSeekDBDriver{}
}

type failingExtractionSeekDBDriver struct{}

func (failingExtractionSeekDBDriver) Open(string) (driver.Conn, error) {
	return nil, errFailingExtractionSeekDBAuthority
}
