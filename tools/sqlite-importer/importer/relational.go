package importer

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	pgstore "fairy/postgres"
	"fairy/vectorindex"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type TableParity struct {
	Rows       int    `json:"rows"`
	SourceHash string `json:"sourceHash"`
	TargetHash string `json:"targetHash"`
}

type RelationalImportResult struct {
	RunID  string                 `json:"runId"`
	Tables map[string]TableParity `json:"tables"`
}

type relationalTable struct {
	name         string
	columns      []string
	orderBy      string
	boolColumns  map[int]bool
	jsonColumns  map[int]bool
	deferSelfRef int
	customRows   func(context.Context, *sql.DB) ([][]any, error)
}

var relationalTables = []relationalTable{
	table("conversations", "id,character_id,created_at_ms,updated_at_ms", "id"),
	table("conversation_turns", "id,conversation_id,sequence,status,error_code,error_message,error_retryable,extraction_state,created_at_ms,updated_at_ms", "conversation_id,sequence,id").withBool(6),
	table("conversation_messages", "id,conversation_id,turn_id,sequence,role,content,created_at_ms", "conversation_id,sequence,id"),
	table("prompt_windows", "conversation_id,revision,summary,cutoff_message_sequence,updated_at_ms", "conversation_id"),
	table("turn_runtime_events", "id,conversation_id,turn_id,sequence,event_type,state,code,metadata_json,created_at_ms", "conversation_id,turn_id,sequence,id").withJSON(7),
	table("lane_continuations", "conversation_id,lane,previous_response_id,request_shape_hash,input_prefix_hash,response_item_hash,window_revision,updated_at_ms", "conversation_id,lane"),
	table("context_windows", "conversation_id,lane,window_number,first_window_id,previous_window_id,window_id,observed_prefill_tokens,estimated_prefill_tokens,last_trigger,failure_count,prompt_window_revision,updated_at_ms", "conversation_id,lane"),
	table("personal_memories", "id,kind,scope_kind,character_id,review_status,content,status,confidence_basis_points,source_conversation_id,source_turn_id,supersedes_id,created_at_ms,updated_at_ms", "id").withDeferredSelfRef(10),
	table("knowledge_entries", "id,topic,statement,status,verification_basis,confidence_basis_points,source_conversation_id,source_turn_id,supersedes_id,created_at_ms,updated_at_ms", "id").withDeferredSelfRef(8),
	table("knowledge_sources", "knowledge_id,source_id,title,url,snippet,rank,fetched_at_ms", "knowledge_id,source_id"),
	table("extraction_batches", "id,conversation_id,character_id,status,first_turn_sequence,last_turn_sequence,error_code,error_message,error_retryable,created_at_ms,updated_at_ms", "id").withBool(8),
	table("extraction_batch_turns", "batch_id,turn_id,turn_sequence", "batch_id,turn_sequence,turn_id"),
	table("knowledge_ingest_jobs", "id,conversation_id,turn_id,query,title,url,snippet,rank,fetched_at_ms,status,error_message,created_at_ms,updated_at_ms", "id"),
	{name: "memory_embedding_items", columns: strings.Split("id,item_kind,item_id,model_id,dimensions,point_id,content_hash,status,error_code,error_message,embedded_at_ms,legacy_vector_rowid,created_at_ms,updated_at_ms", ","), orderBy: "legacy_vector_rowid", deferSelfRef: -1, customRows: embeddingItemRows},
	{name: "memory_embedding_jobs", columns: strings.Split("id,item_kind,item_id,model_id,dimensions,point_id,content_hash,status,error_code,error_message,retryable,created_at_ms,updated_at_ms", ","), orderBy: "id", boolColumns: map[int]bool{10: true}, deferSelfRef: -1, customRows: embeddingJobRows},
}

func table(name, columns, orderBy string) relationalTable {
	return relationalTable{name: name, columns: strings.Split(columns, ","), orderBy: orderBy, deferSelfRef: -1}
}

func (t relationalTable) withBool(indexes ...int) relationalTable {
	t.boolColumns = make(map[int]bool, len(indexes))
	for _, index := range indexes {
		t.boolColumns[index] = true
	}
	return t
}

func (t relationalTable) withJSON(indexes ...int) relationalTable {
	t.jsonColumns = make(map[int]bool, len(indexes))
	for _, index := range indexes {
		t.jsonColumns[index] = true
	}
	return t
}

func (t relationalTable) withDeferredSelfRef(index int) relationalTable {
	t.deferSelfRef = index
	return t
}

func ImportRelational(ctx context.Context, intelligencePath string, pool *pgstore.Pool) (RelationalImportResult, error) {
	if ctx == nil {
		return RelationalImportResult{}, errors.New("relational import context is required")
	}
	if pool == nil || pool.Raw() == nil {
		return RelationalImportResult{}, errors.New("PostgreSQL target is required")
	}
	fingerprint, err := fingerprint(intelligencePath)
	if err != nil {
		return RelationalImportResult{}, err
	}
	source, err := openImmutableSQLite(intelligencePath)
	if err != nil {
		return RelationalImportResult{}, err
	}
	defer source.Close()
	if _, _, err := validateIntelligence(ctx, source); err != nil {
		return RelationalImportResult{}, err
	}
	if err := requireEmptyPostgresTarget(ctx, pool); err != nil {
		return RelationalImportResult{}, err
	}

	loaded := make(map[string][][]any, len(relationalTables))
	for _, spec := range relationalTables {
		rows, err := loadSourceRows(ctx, source, spec)
		if err != nil {
			return RelationalImportResult{}, err
		}
		loaded[spec.name] = rows
	}
	queryCtx, cancel := pool.QueryContext(ctx)
	defer cancel()
	tx, err := pool.Raw().BeginTx(queryCtx, pgx.TxOptions{})
	if err != nil {
		return RelationalImportResult{}, fmt.Errorf("begin relational import: %w", err)
	}
	defer tx.Rollback(queryCtx)

	runID := uuid.NewString()
	now := time.Now().UnixMilli()
	fingerprintJSON, err := json.Marshal(fingerprint)
	if err != nil {
		return RelationalImportResult{}, err
	}
	if _, err := tx.Exec(queryCtx, `
INSERT INTO sqlite_import_runs(id, source_intelligence_path, source_fingerprint, status, phase, created_at_ms, updated_at_ms)
VALUES ($1, $2, $3::jsonb, 'running', 'relational', $4, $4)`, runID, fingerprint.Path, fingerprintJSON, now); err != nil {
		return RelationalImportResult{}, fmt.Errorf("create import run: %w", err)
	}

	result := RelationalImportResult{RunID: runID, Tables: make(map[string]TableParity, len(relationalTables))}
	for _, spec := range relationalTables {
		if err := insertRows(queryCtx, tx, spec, loaded[spec.name]); err != nil {
			return RelationalImportResult{}, err
		}
	}
	for _, spec := range relationalTables {
		if spec.deferSelfRef >= 0 {
			if err := restoreSelfReferences(queryCtx, tx, spec, loaded[spec.name]); err != nil {
				return RelationalImportResult{}, err
			}
		}
	}
	for _, spec := range relationalTables {
		sourceHash, err := normalizedRowsHash(loaded[spec.name], spec)
		if err != nil {
			return RelationalImportResult{}, err
		}
		targetRows, err := loadTargetRows(queryCtx, tx, spec)
		if err != nil {
			return RelationalImportResult{}, err
		}
		targetHash, err := normalizedRowsHash(targetRows, spec)
		if err != nil {
			return RelationalImportResult{}, err
		}
		parity := TableParity{Rows: len(loaded[spec.name]), SourceHash: sourceHash, TargetHash: targetHash}
		if len(targetRows) != parity.Rows || sourceHash != targetHash {
			return RelationalImportResult{}, fmt.Errorf("table %s parity mismatch: source_rows=%d target_rows=%d", spec.name, parity.Rows, len(targetRows))
		}
		result.Tables[spec.name] = parity
	}
	reportJSON, err := json.Marshal(result)
	if err != nil {
		return RelationalImportResult{}, err
	}
	if _, err := tx.Exec(queryCtx, `
UPDATE sqlite_import_runs SET phase = 'relational_complete', report_json = $2::jsonb, updated_at_ms = $3
WHERE id = $1 AND status = 'running'`, runID, reportJSON, time.Now().UnixMilli()); err != nil {
		return RelationalImportResult{}, fmt.Errorf("complete relational import phase: %w", err)
	}
	if _, err := tx.Exec(queryCtx, `
INSERT INTO sqlite_import_checkpoints(run_id, phase, checkpoint_json, updated_at_ms)
VALUES ($1, 'relational', $2::jsonb, $3)`, runID, reportJSON, time.Now().UnixMilli()); err != nil {
		return RelationalImportResult{}, fmt.Errorf("write relational checkpoint: %w", err)
	}
	if err := tx.Commit(queryCtx); err != nil {
		return RelationalImportResult{}, fmt.Errorf("commit relational import: %w", err)
	}
	if err := assertFingerprintUnchanged(intelligencePath, fingerprint); err != nil {
		return RelationalImportResult{}, err
	}
	return result, nil
}

func loadSourceRows(ctx context.Context, db *sql.DB, spec relationalTable) ([][]any, error) {
	if spec.customRows != nil {
		return spec.customRows(ctx, db)
	}
	return querySQLRows(ctx, db, "SELECT "+strings.Join(spec.columns, ",")+" FROM "+spec.name+" ORDER BY "+spec.orderBy, len(spec.columns), spec)
}

func querySQLRows(ctx context.Context, db *sql.DB, query string, columns int, spec relationalTable) ([][]any, error) {
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("read source table %s: %w", spec.name, err)
	}
	defer rows.Close()
	result := make([][]any, 0)
	for rows.Next() {
		values := make([]any, columns)
		destinations := make([]any, columns)
		for index := range values {
			destinations[index] = &values[index]
		}
		if err := rows.Scan(destinations...); err != nil {
			return nil, fmt.Errorf("scan source table %s: %w", spec.name, err)
		}
		if err := transformRow(values, spec); err != nil {
			return nil, fmt.Errorf("transform source table %s: %w", spec.name, err)
		}
		result = append(result, values)
	}
	return result, rows.Err()
}

func transformRow(values []any, spec relationalTable) error {
	for index := range spec.boolColumns {
		if values[index] == nil {
			continue
		}
		value, ok := values[index].(int64)
		if !ok || value < 0 || value > 1 {
			return fmt.Errorf("boolean column %s has invalid value", spec.columns[index])
		}
		values[index] = value == 1
	}
	for index := range spec.jsonColumns {
		var decoded any
		raw, ok := values[index].(string)
		if !ok || json.Unmarshal([]byte(raw), &decoded) != nil {
			return fmt.Errorf("JSON column %s is invalid", spec.columns[index])
		}
		values[index] = decoded
	}
	return nil
}

func insertRows(ctx context.Context, tx pgx.Tx, spec relationalTable, rows [][]any) error {
	columns := append([]string(nil), spec.columns...)
	for _, sourceRow := range rows {
		row := append([]any(nil), sourceRow...)
		if spec.deferSelfRef >= 0 {
			row[spec.deferSelfRef] = nil
		}
		placeholders := make([]string, len(columns))
		for index := range placeholders {
			placeholders[index] = "$" + strconv.Itoa(index+1)
		}
		query := "INSERT INTO " + spec.name + "(" + strings.Join(columns, ",") + ") VALUES (" + strings.Join(placeholders, ",") + ")"
		if _, err := tx.Exec(ctx, query, row...); err != nil {
			return fmt.Errorf("insert target table %s: %w", spec.name, err)
		}
	}
	return nil
}

func restoreSelfReferences(ctx context.Context, tx pgx.Tx, spec relationalTable, rows [][]any) error {
	for _, row := range rows {
		if row[spec.deferSelfRef] == nil {
			continue
		}
		if _, err := tx.Exec(ctx, "UPDATE "+spec.name+" SET "+spec.columns[spec.deferSelfRef]+" = $2 WHERE id = $1", row[0], row[spec.deferSelfRef]); err != nil {
			return fmt.Errorf("restore target table %s self reference: %w", spec.name, err)
		}
	}
	return nil
}

func loadTargetRows(ctx context.Context, tx pgx.Tx, spec relationalTable) ([][]any, error) {
	rows, err := tx.Query(ctx, "SELECT "+strings.Join(spec.columns, ",")+" FROM "+spec.name+" ORDER BY "+spec.orderBy)
	if err != nil {
		return nil, fmt.Errorf("read target table %s: %w", spec.name, err)
	}
	defer rows.Close()
	result := make([][]any, 0)
	for rows.Next() {
		values, err := rows.Values()
		if err != nil {
			return nil, err
		}
		result = append(result, values)
	}
	return result, rows.Err()
}

func normalizedRowsHash(rows [][]any, spec relationalTable) (string, error) {
	normalized := make([][]any, len(rows))
	for rowIndex, row := range rows {
		normalized[rowIndex] = make([]any, len(row))
		for columnIndex, value := range row {
			normalizedValue, err := normalizeValue(value, spec.jsonColumns[columnIndex])
			if err != nil {
				return "", fmt.Errorf("normalize %s row %d column %s: %w", spec.name, rowIndex, spec.columns[columnIndex], err)
			}
			normalized[rowIndex][columnIndex] = normalizedValue
		}
	}
	raw, err := json.Marshal(normalized)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func normalizeValue(value any, isJSON bool) (any, error) {
	if value == nil {
		return nil, nil
	}
	if isJSON {
		switch typed := value.(type) {
		case []byte:
			var decoded any
			if err := json.Unmarshal(typed, &decoded); err != nil {
				return nil, err
			}
			return decoded, nil
		case string:
			var decoded any
			if err := json.Unmarshal([]byte(typed), &decoded); err != nil {
				return nil, err
			}
			return decoded, nil
		default:
			return typed, nil
		}
	}
	switch typed := value.(type) {
	case int32:
		return int64(typed), nil
	case int:
		return int64(typed), nil
	case uuid.UUID:
		return typed.String(), nil
	case [16]byte:
		return uuid.UUID(typed).String(), nil
	case []byte:
		return string(typed), nil
	default:
		return typed, nil
	}
}

func embeddingItemRows(ctx context.Context, db *sql.DB) ([][]any, error) {
	rows, err := db.QueryContext(ctx, `
SELECT vector_rowid,item_kind,item_id,model_id,dimensions,content_hash,status,error_code,error_message,created_at_ms,updated_at_ms
FROM memory_embedding_items ORDER BY vector_rowid`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([][]any, 0)
	for rows.Next() {
		var rowID, dimensions, createdAt, updatedAt int64
		var itemKind, itemID, modelID, contentHash, status string
		var errorCode, errorMessage sql.NullString
		if err := rows.Scan(&rowID, &itemKind, &itemID, &modelID, &dimensions, &contentHash, &status, &errorCode, &errorMessage, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		pointID, err := vectorindex.PointID(itemKind, itemID, modelID)
		if err != nil {
			return nil, err
		}
		var embeddedAt any
		if status == "embedded" {
			embeddedAt = updatedAt
		}
		result = append(result, []any{"legacy-item-" + strconv.FormatInt(rowID, 10), itemKind, itemID, modelID, dimensions, pointID, contentHash, status, nullableString(errorCode), nullableString(errorMessage), embeddedAt, rowID, createdAt, updatedAt})
	}
	return result, rows.Err()
}

func embeddingJobRows(ctx context.Context, db *sql.DB) ([][]any, error) {
	rows, err := db.QueryContext(ctx, `
SELECT id,item_kind,item_id,model_id,dimensions,content_hash,status,error_code,error_message,retryable,created_at_ms,updated_at_ms
FROM memory_embedding_jobs ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([][]any, 0)
	for rows.Next() {
		var id, itemKind, itemID, modelID, contentHash, status string
		var dimensions, retryable, createdAt, updatedAt int64
		var errorCode, errorMessage sql.NullString
		if err := rows.Scan(&id, &itemKind, &itemID, &modelID, &dimensions, &contentHash, &status, &errorCode, &errorMessage, &retryable, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		pointID, err := vectorindex.PointID(itemKind, itemID, modelID)
		if err != nil {
			return nil, err
		}
		result = append(result, []any{id, itemKind, itemID, modelID, dimensions, pointID, contentHash, status, nullableString(errorCode), nullableString(errorMessage), retryable == 1, createdAt, updatedAt})
	}
	return result, rows.Err()
}

func nullableString(value sql.NullString) any {
	if !value.Valid {
		return nil
	}
	return value.String
}
