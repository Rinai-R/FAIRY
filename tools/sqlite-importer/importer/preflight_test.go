package importer

import (
	"context"
	"database/sql"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIntelligencePreflightIsImmutableAndAcceptsSchemaV7(t *testing.T) {
	path := createIntelligenceFixture(t)
	before, err := fingerprint(path)
	if err != nil {
		t.Fatal(err)
	}
	db, err := openImmutableSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	version, vectors, err := validateIntelligence(context.Background(), db)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	if version != supportedIntelligenceSchema || vectors != 0 {
		t.Fatalf("version=%d vectors=%d", version, vectors)
	}
	if err := assertFingerprintUnchanged(path, before); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"-wal", "-journal"} {
		if _, err := os.Stat(path + suffix); !os.IsNotExist(err) {
			t.Fatalf("immutable preflight created %s: %v", suffix, err)
		}
	}
}

func TestIntelligencePreflightRejectsUnsupportedAndIncompleteSchema(t *testing.T) {
	path := createIntelligenceFixture(t)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE schema_meta SET version = 6 WHERE singleton = 1"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	readOnly, err := openImmutableSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = validateIntelligence(context.Background(), readOnly)
	readOnly.Close()
	if err == nil || !strings.Contains(err.Error(), "unsupported intelligence schema version 6") {
		t.Fatalf("unsupported schema error = %v", err)
	}

	incomplete := filepath.Join(t.TempDir(), "incomplete.sqlite3")
	db, err = sql.Open("sqlite", incomplete)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE schema_meta(singleton INTEGER PRIMARY KEY, version INTEGER NOT NULL); INSERT INTO schema_meta VALUES(1, 7)"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	readOnly, err = openImmutableSQLite(incomplete)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = validateIntelligence(context.Background(), readOnly)
	readOnly.Close()
	if err == nil || !strings.Contains(err.Error(), "required SQLite table") {
		t.Fatalf("incomplete schema error = %v", err)
	}
}

func TestFingerprintRejectsNonRegularAndWhitespacePaths(t *testing.T) {
	if _, err := fingerprint(" path "); err == nil || !strings.Contains(err.Error(), "whitespace") {
		t.Fatalf("whitespace path error = %v", err)
	}
	if _, err := fingerprint(t.TempDir()); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("directory path error = %v", err)
	}
}

func TestValidateVectorBlobRejectsWrongSizeAndNonFinite(t *testing.T) {
	if err := validateVectorBlob(make([]byte, 4)); err == nil || !strings.Contains(err.Error(), "byte length") {
		t.Fatalf("wrong size error = %v", err)
	}
	blob := make([]byte, 512*4)
	bits := math.Float32bits(float32(math.NaN()))
	blob[0] = byte(bits)
	blob[1] = byte(bits >> 8)
	blob[2] = byte(bits >> 16)
	blob[3] = byte(bits >> 24)
	if err := validateVectorBlob(blob); err == nil || !strings.Contains(err.Error(), "non-finite") {
		t.Fatalf("non-finite error = %v", err)
	}
}
