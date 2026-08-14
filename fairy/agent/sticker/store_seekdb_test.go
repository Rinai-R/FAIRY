package sticker

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestNewSeekDBStoreRejectsInvalidInputs(t *testing.T) {
	database := sql.OpenDB(stickerUnitConnector{})
	t.Cleanup(func() { _ = database.Close() })
	root := t.TempDir()
	tests := []struct {
		name       string
		database   *sql.DB
		root       string
		queryLimit time.Duration
		want       error
	}{
		{name: "missing database", root: root, queryLimit: time.Second, want: ErrSeekDBRequired},
		{name: "relative root", database: database, root: "stickers", queryLimit: time.Second, want: ErrContentRootInvalid},
		{name: "filesystem root", database: database, root: string(filepath.Separator), queryLimit: time.Second, want: ErrContentRootInvalid},
		{name: "invalid query limit", database: database, root: root, want: ErrQueryLimitInvalid},
		{name: "valid", database: database, root: root, queryLimit: time.Second},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, err := NewSeekDBStore(test.database, test.root, test.queryLimit)
			if !errors.Is(err, test.want) {
				t.Fatalf("NewSeekDBStore() error = %v, want %v", err, test.want)
			}
			if test.want == nil && (store == nil || !store.usesSeekDB() || store.contentRoot != filepath.Clean(test.root)) {
				t.Fatalf("NewSeekDBStore() = %#v", store)
			}
		})
	}
}

type stickerUnitConnector struct{}

func (stickerUnitConnector) Connect(context.Context) (driver.Conn, error) {
	return nil, errors.New("unexpected sticker unit database connection")
}

func (stickerUnitConnector) Driver() driver.Driver { return stickerUnitDriver{} }

type stickerUnitDriver struct{}

func (stickerUnitDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("unexpected sticker unit database open")
}
