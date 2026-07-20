package importer

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	pgstore "fairy/postgres"
	"fairy/secret"
	"fairy/vectorindex"

	_ "modernc.org/sqlite"
	_ "modernc.org/sqlite/vec"
)

const supportedIntelligenceSchema = 7

var requiredIntelligenceTables = []string{
	"schema_meta", "conversations", "conversation_turns", "conversation_messages",
	"prompt_windows", "turn_runtime_events", "lane_continuations", "context_windows",
	"personal_memories", "knowledge_entries", "knowledge_sources", "extraction_batches",
	"extraction_batch_turns", "knowledge_ingest_jobs", "memory_embedding_items",
	"memory_embedding_jobs", "memory_embedding_vec",
}

var targetBusinessTables = []string{
	"conversations", "conversation_turns", "conversation_messages", "prompt_windows",
	"turn_runtime_events", "lane_continuations", "context_windows", "personal_memories",
	"knowledge_entries", "knowledge_sources", "extraction_batches", "extraction_batch_turns",
	"knowledge_ingest_jobs", "memory_embedding_items", "memory_embedding_jobs", "secret_values",
	"sqlite_import_runs", "vector_rebuild_runs", "vector_reconciliation_runs",
}

type PreflightOptions struct {
	IntelligencePath string
	SecretPath       string
	Getenv           func(string) string
}

type FileFingerprint struct {
	Path          string `json:"path"`
	Size          int64  `json:"size"`
	ModifiedUnixN int64  `json:"modifiedUnixNanos"`
	SHA256        string `json:"sha256"`
}

type PreflightReport struct {
	Intelligence     FileFingerprint              `json:"intelligence"`
	Secrets          *FileFingerprint             `json:"secrets,omitempty"`
	SchemaVersion    int                          `json:"schemaVersion"`
	VectorRows       int64                        `json:"vectorRows"`
	SecretRows       int64                        `json:"secretRows"`
	Database         pgstore.Descriptor           `json:"database"`
	DatabaseSchema   pgstore.SchemaStatus         `json:"databaseSchema"`
	Qdrant           vectorindex.Descriptor       `json:"qdrant"`
	QdrantCollection vectorindex.CollectionStatus `json:"qdrantCollection"`
	Ready            bool                         `json:"ready"`
	ExistingRunID    string                       `json:"existingRunId,omitempty"`
	ExistingStatus   string                       `json:"existingStatus,omitempty"`
	ExistingPhase    string                       `json:"existingPhase,omitempty"`
}

func Preflight(ctx context.Context, options PreflightOptions) (PreflightReport, error) {
	if ctx == nil {
		return PreflightReport{}, errors.New("preflight context is required")
	}
	getenv := options.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	intelligenceBefore, err := fingerprint(options.IntelligencePath)
	if err != nil {
		return PreflightReport{}, fmt.Errorf("intelligence source: %w", err)
	}
	intelligence, err := openImmutableSQLite(options.IntelligencePath)
	if err != nil {
		return PreflightReport{}, fmt.Errorf("intelligence source: %w", err)
	}
	defer intelligence.Close()
	schemaVersion, vectorRows, err := validateIntelligence(ctx, intelligence)
	if err != nil {
		return PreflightReport{}, err
	}

	var secretFingerprint *FileFingerprint
	var secretRows int64
	if options.SecretPath != "" {
		value, err := fingerprint(options.SecretPath)
		if err != nil {
			return PreflightReport{}, fmt.Errorf("secret source: %w", err)
		}
		secretFingerprint = &value
		secretDB, err := openImmutableSQLite(options.SecretPath)
		if err != nil {
			return PreflightReport{}, fmt.Errorf("secret source: %w", err)
		}
		secretRows, err = validateSecrets(ctx, secretDB)
		secretDB.Close()
		if err != nil {
			return PreflightReport{}, err
		}
		if secretRows > 0 {
			if _, err := secret.CipherFromEnv(getenv); err != nil {
				return PreflightReport{}, fmt.Errorf("secret target key: %w", err)
			}
		}
	}

	databaseConfig, err := pgstore.ConfigFromEnv(getenv)
	if err != nil {
		return PreflightReport{}, fmt.Errorf("database target configuration: %w", err)
	}
	database, err := pgstore.Open(ctx, databaseConfig)
	if err != nil {
		return PreflightReport{}, err
	}
	defer database.Close()
	databaseSchema, err := pgstore.VerifySchema(ctx, database, pgstore.CurrentSchemaVersion)
	if err != nil {
		return PreflightReport{}, fmt.Errorf("database target schema: %w", err)
	}
	existingRunID, existingStatus, existingPhase, err := inspectPostgresTarget(ctx, database, intelligenceBefore)
	if err != nil {
		return PreflightReport{}, err
	}
	databaseDescriptor, err := databaseConfig.Descriptor()
	if err != nil {
		return PreflightReport{}, err
	}

	qdrantConfig, err := vectorindex.ConfigFromEnv(getenv)
	if err != nil {
		return PreflightReport{}, fmt.Errorf("qdrant target configuration: %w", err)
	}
	qdrant, err := vectorindex.Open(ctx, qdrantConfig)
	if err != nil {
		return PreflightReport{}, err
	}
	defer qdrant.Close()
	collection, err := qdrant.VerifyCollection(ctx)
	if err != nil {
		return PreflightReport{}, fmt.Errorf("qdrant target collection: %w", err)
	}
	if collection.PointsCount != 0 && existingRunID == "" {
		return PreflightReport{}, fmt.Errorf("qdrant target is not empty: points=%d", collection.PointsCount)
	}
	qdrantDescriptor, err := qdrant.Descriptor()
	if err != nil {
		return PreflightReport{}, err
	}

	if err := assertFingerprintUnchanged(options.IntelligencePath, intelligenceBefore); err != nil {
		return PreflightReport{}, err
	}
	if secretFingerprint != nil {
		if err := assertFingerprintUnchanged(options.SecretPath, *secretFingerprint); err != nil {
			return PreflightReport{}, err
		}
	}
	return PreflightReport{
		Intelligence: intelligenceBefore, Secrets: secretFingerprint,
		SchemaVersion: schemaVersion, VectorRows: vectorRows, SecretRows: secretRows,
		Database: databaseDescriptor, DatabaseSchema: databaseSchema,
		Qdrant: qdrantDescriptor, QdrantCollection: collection, Ready: true,
		ExistingRunID: existingRunID, ExistingStatus: existingStatus, ExistingPhase: existingPhase,
	}, nil
}

func validateIntelligence(ctx context.Context, db *sql.DB) (int, int64, error) {
	if err := validateSQLiteIntegrity(ctx, db, "intelligence"); err != nil {
		return 0, 0, err
	}
	for _, table := range requiredIntelligenceTables {
		if err := requireSQLiteTable(ctx, db, table); err != nil {
			return 0, 0, err
		}
	}
	var version int
	if err := db.QueryRowContext(ctx, "SELECT version FROM schema_meta WHERE singleton = 1").Scan(&version); err != nil {
		return 0, 0, fmt.Errorf("reading intelligence schema version: %w", err)
	}
	if version != supportedIntelligenceSchema {
		return 0, 0, fmt.Errorf("unsupported intelligence schema version %d, want %d", version, supportedIntelligenceSchema)
	}
	rows, err := db.QueryContext(ctx, "SELECT rowid, embedding FROM memory_embedding_vec ORDER BY rowid")
	if err != nil {
		return 0, 0, fmt.Errorf("reading legacy vectors: %w", err)
	}
	defer rows.Close()
	var count int64
	for rows.Next() {
		var rowID int64
		var vector []byte
		if err := rows.Scan(&rowID, &vector); err != nil {
			return 0, 0, fmt.Errorf("scanning legacy vector: %w", err)
		}
		if err := validateVectorBlob(vector); err != nil {
			return 0, 0, fmt.Errorf("legacy vector row %d: %w", rowID, err)
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return 0, 0, fmt.Errorf("iterating legacy vectors: %w", err)
	}
	return version, count, nil
}

func validateSecrets(ctx context.Context, db *sql.DB) (int64, error) {
	if err := validateSQLiteIntegrity(ctx, db, "secret"); err != nil {
		return 0, err
	}
	if err := requireSQLiteTable(ctx, db, "model_secrets"); err != nil {
		return 0, err
	}
	var count int64
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM model_secrets").Scan(&count); err != nil {
		return 0, fmt.Errorf("counting legacy secrets: %w", err)
	}
	return count, nil
}

func validateSQLiteIntegrity(ctx context.Context, db *sql.DB, name string) error {
	var result string
	if err := db.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&result); err != nil {
		return fmt.Errorf("%s SQLite quick_check: %w", name, err)
	}
	if result != "ok" {
		return fmt.Errorf("%s SQLite quick_check failed: %s", name, result)
	}
	rows, err := db.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return fmt.Errorf("%s SQLite foreign_key_check: %w", name, err)
	}
	defer rows.Close()
	if rows.Next() {
		return fmt.Errorf("%s SQLite foreign key check failed", name)
	}
	return rows.Err()
}

func requireSQLiteTable(ctx context.Context, db *sql.DB, table string) error {
	var found string
	err := db.QueryRowContext(ctx, "SELECT name FROM sqlite_master WHERE (type = 'table' OR type = 'view') AND name = ?", table).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("required SQLite table %s is missing", table)
	}
	if err != nil {
		return fmt.Errorf("checking SQLite table %s: %w", table, err)
	}
	return nil
}

func requireEmptyPostgresTarget(ctx context.Context, pool *pgstore.Pool) error {
	queryCtx, cancel := pool.QueryContext(ctx)
	defer cancel()
	for _, table := range targetBusinessTables {
		var count int64
		if err := pool.Raw().QueryRow(queryCtx, "SELECT count(*) FROM "+table).Scan(&count); err != nil {
			return fmt.Errorf("checking target table %s: %w", table, err)
		}
		if count != 0 {
			return fmt.Errorf("PostgreSQL target table %s is not empty: rows=%d", table, count)
		}
	}
	return nil
}

func inspectPostgresTarget(ctx context.Context, pool *pgstore.Pool, source FileFingerprint) (string, string, string, error) {
	queryCtx, cancel := pool.QueryContext(ctx)
	defer cancel()
	rows, err := pool.Raw().Query(queryCtx, "SELECT id,status,phase,source_intelligence_path,source_fingerprint FROM sqlite_import_runs ORDER BY created_at_ms,id")
	if err != nil {
		return "", "", "", err
	}
	defer rows.Close()
	var matchingID, matchingStatus, matchingPhase string
	var runCount int
	for rows.Next() {
		runCount++
		var id, status, phase, path string
		var raw []byte
		if err := rows.Scan(&id, &status, &phase, &path, &raw); err != nil {
			return "", "", "", err
		}
		var stored FileFingerprint
		if json.Unmarshal(raw, &stored) == nil && source.SHA256 != "" && path == source.Path && stored.SHA256 == source.SHA256 && stored.Size == source.Size {
			matchingID, matchingStatus, matchingPhase = id, status, phase
		}
	}
	if err := rows.Err(); err != nil {
		return "", "", "", err
	}
	var businessRows int64
	for _, table := range targetBusinessTables {
		if table == "sqlite_import_runs" || table == "sqlite_import_checkpoints" {
			continue
		}
		var count int64
		if err := pool.Raw().QueryRow(queryCtx, "SELECT count(*) FROM "+table).Scan(&count); err != nil {
			return "", "", "", err
		}
		businessRows += count
	}
	if businessRows == 0 && runCount == 0 {
		return "", "", "", nil
	}
	if matchingID == "" || runCount != 1 {
		return "", "", "", errors.New("PostgreSQL target is non-empty and has no unique compatible import run")
	}
	return matchingID, matchingStatus, matchingPhase, nil
}

func openImmutableSQLite(path string) (*sql.DB, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	uri := (&url.URL{Scheme: "file", Path: absolute, RawQuery: "mode=ro&immutable=1"}).String()
	db, err := sql.Open("sqlite", uri)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func fingerprint(path string) (FileFingerprint, error) {
	if path == "" {
		return FileFingerprint{}, errors.New("path is required")
	}
	if path != strings.TrimSpace(path) {
		return FileFingerprint{}, errors.New("path must not contain leading or trailing whitespace")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return FileFingerprint{}, err
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return FileFingerprint{}, err
	}
	if !info.Mode().IsRegular() {
		return FileFingerprint{}, errors.New("path must reference a regular file")
	}
	file, err := os.Open(absolute)
	if err != nil {
		return FileFingerprint{}, err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return FileFingerprint{}, err
	}
	return FileFingerprint{
		Path: absolute, Size: info.Size(), ModifiedUnixN: info.ModTime().UnixNano(),
		SHA256: hex.EncodeToString(hash.Sum(nil)),
	}, nil
}

func assertFingerprintUnchanged(path string, before FileFingerprint) error {
	after, err := fingerprint(path)
	if err != nil {
		return err
	}
	if after.Size != before.Size || after.ModifiedUnixN != before.ModifiedUnixN || after.SHA256 != before.SHA256 {
		return fmt.Errorf("source changed during preflight: %s", before.Path)
	}
	return nil
}

func validateVectorBlob(blob []byte) error {
	if len(blob) != vectorindex.Dimensions*4 {
		return fmt.Errorf("vector byte length = %d, want %d", len(blob), vectorindex.Dimensions*4)
	}
	for index := range vectorindex.Dimensions {
		value := math.Float32frombits(binary.LittleEndian.Uint32(blob[index*4 : index*4+4]))
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return fmt.Errorf("vector contains non-finite value at index %d", index)
		}
	}
	return nil
}

func WriteJSON(writer io.Writer, value any) error {
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}
