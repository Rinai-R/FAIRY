package seekdb

import "strings"

// rewriteEmbedCharsets maps foundation identifier charset clauses onto the
// charsets the embedded engine actually loads. Migration SQL stays ascii_bin
// so checksums do not change; this rewrite runs on the query template before
// argument interpolation so quoted payloads are not mutated.
func rewriteEmbedCharsets(query string) string {
	if !strings.Contains(strings.ToLower(query), "ascii") {
		return query
	}
	return embedCharsetReplacer.Replace(query)
}

var embedCharsetReplacer = strings.NewReplacer(
	"CHARACTER SET ascii COLLATE ascii_bin", "CHARACTER SET utf8mb4 COLLATE utf8mb4_bin",
	"character set ascii collate ascii_bin", "CHARACTER SET utf8mb4 COLLATE utf8mb4_bin",
	"CHARACTER SET ascii", "CHARACTER SET utf8mb4",
	"character set ascii", "CHARACTER SET utf8mb4",
	"COLLATE ascii_bin", "COLLATE utf8mb4_bin",
	"collate ascii_bin", "COLLATE utf8mb4_bin",
)

func schemaCollationsEqual(expected, actual string) bool {
	if expected == actual {
		return true
	}
	return expected == "ascii_bin" && actual == "utf8mb4_bin"
}

func schemaColumnsEqual(expected, actual schemaColumn) bool {
	return expected.name == actual.name &&
		expected.columnType == actual.columnType &&
		expected.nullable == actual.nullable &&
		expected.defaultValue == actual.defaultValue &&
		expected.extra == actual.extra &&
		expected.generationExpression == actual.generationExpression &&
		schemaCollationsEqual(expected.collation, actual.collation)
}
