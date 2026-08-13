package character

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestNewSeekDBStoreValidatesDependencies(t *testing.T) {
	database := new(sql.DB)
	absoluteRoot := filepath.Join(t.TempDir(), "characters")
	tests := []struct {
		name       string
		database   *sql.DB
		root       string
		queryLimit time.Duration
		want       error
	}{
		{name: "missing database", root: absoluteRoot, queryLimit: time.Second, want: ErrCharacterSeekDBRequired},
		{name: "relative visual root", database: database, root: "characters", queryLimit: time.Second, want: ErrCharacterVisualRootInvalid},
		{name: "filesystem root", database: database, root: string(filepath.Separator), queryLimit: time.Second, want: ErrCharacterVisualRootInvalid},
		{name: "invalid query limit", database: database, root: absoluteRoot, want: ErrCharacterQueryLimitInvalid},
		{name: "valid", database: database, root: absoluteRoot, queryLimit: time.Second},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, err := NewSeekDBStore(test.database, test.root, test.queryLimit)
			if !errors.Is(err, test.want) {
				t.Fatalf("NewSeekDBStore() error = %v, want %v", err, test.want)
			}
			if test.want == nil && (store == nil || store.seekDB == nil || store.root != filepath.Clean(test.root)) {
				t.Fatalf("NewSeekDBStore() = %#v", store)
			}
		})
	}
}

func TestNewCharacterServiceWithStoreRejectsNil(t *testing.T) {
	if service, err := NewCharacterServiceWithStore(nil); err == nil || service != nil {
		t.Fatalf("NewCharacterServiceWithStore(nil) = (%#v, %v)", service, err)
	}
}

func TestValidateVisualRootNormalizesWithoutFilesystemSideEffects(t *testing.T) {
	base := t.TempDir()
	raw := filepath.Join(base, "visuals") + string(filepath.Separator) + "."
	root, err := ValidateVisualRoot(raw)
	if err != nil || root != filepath.Join(base, "visuals") {
		t.Fatalf("ValidateVisualRoot(%q) = (%q, %v)", raw, root, err)
	}
	if _, err := ValidateVisualRoot("visuals"); !errors.Is(err, ErrCharacterVisualRootInvalid) {
		t.Fatalf("ValidateVisualRoot(relative) error = %v", err)
	}
	if _, err := ValidateVisualRoot(string(filepath.Separator)); !errors.Is(err, ErrCharacterVisualRootInvalid) {
		t.Fatalf("ValidateVisualRoot(root) error = %v", err)
	}
}

func TestDecodeStoredCharacterIsolatesMismatchedSnapshot(t *testing.T) {
	row := storedCharacter{
		characterID: "character-1",
		revision:    2,
		name:        "亚托莉",
		snapshot:    []byte(`{"character_id":"character-1","revision":1,"identity":{"name":"亚托莉","description":"陪伴"}}`),
	}
	if record, diagnostic := decodeStoredCharacter(t.TempDir(), row); diagnostic == nil || record != (Record{}) {
		t.Fatalf("decodeStoredCharacter() = (%#v, %#v), want isolated diagnostic", record, diagnostic)
	}
}
