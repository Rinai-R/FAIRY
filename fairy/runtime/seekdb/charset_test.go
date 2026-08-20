package seekdb

import (
	"database/sql/driver"
	"testing"
)

func TestRewriteEmbedCharsetsMapsFoundationIdentifierDDL(t *testing.T) {
	query := "CREATE TABLE config_documents (\n  id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL\n)"
	got := rewriteEmbedCharsets(query)
	want := "CREATE TABLE config_documents (\n  id VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL\n)"
	if got != want {
		t.Fatalf("rewriteEmbedCharsets() = %q, want %q", got, want)
	}
}

func TestRewriteEmbedCharsetsLeavesQueriesWithoutAscii(t *testing.T) {
	query := "SELECT payload FROM fairy_seekdb_runtime_probe WHERE id = ?"
	if got := rewriteEmbedCharsets(query); got != query {
		t.Fatalf("rewriteEmbedCharsets() mutated %q into %q", query, got)
	}
}

func TestInterpolateSQLKeepsRegexQuestionMarkInsideLiterals(t *testing.T) {
	query := "CHECK (source_url REGEXP '^https?://[^[:space:]]+$')"
	got, err := interpolateSQL(query, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != query {
		t.Fatalf("interpolated SQL = %q", got)
	}
}

func TestInterpolateSQLStillBindsUnquotedPlaceholders(t *testing.T) {
	got, err := interpolateSQL("SELECT payload FROM t WHERE id = ?", []driver.NamedValue{{
		Ordinal: 1,
		Value:   int64(7),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got != "SELECT payload FROM t WHERE id = 7" {
		t.Fatalf("interpolated SQL = %q", got)
	}
}

func TestInterpolateDoesNotRewriteQuotedAsciiPayload(t *testing.T) {
	query := "INSERT INTO notes(body) VALUES (?)"
	sqlText, err := interpolateSQL(rewriteEmbedCharsets(query), []driver.NamedValue{{
		Ordinal: 1,
		Value:   "CHARACTER SET ascii COLLATE ascii_bin",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if sqlText != `INSERT INTO notes(body) VALUES ('CHARACTER SET ascii COLLATE ascii_bin')` {
		t.Fatalf("interpolated SQL = %q", sqlText)
	}
}

func TestSchemaCollationsEqualAcceptsEmbedUtf8mb4BinForAsciiBin(t *testing.T) {
	if !schemaCollationsEqual("ascii_bin", "utf8mb4_bin") {
		t.Fatal("ascii_bin should match embed utf8mb4_bin")
	}
	if !schemaCollationsEqual("ascii_bin", "ascii_bin") {
		t.Fatal("ascii_bin should match ascii_bin")
	}
	if schemaCollationsEqual("ascii_bin", "utf8mb4_general_ci") {
		t.Fatal("ascii_bin must not match a case-insensitive collation")
	}
	if schemaCollationsEqual("utf8mb4_bin", "ascii_bin") {
		t.Fatal("utf8mb4_bin expected columns must not match ascii_bin")
	}
}
