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
	"math"
	"strings"
	"time"

	pgstore "fairy/postgres"
	"fairy/secret"
	"fairy/vectorindex"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type VectorSecretImportResult struct {
	RunID           string `json:"runId"`
	ImportedPoints  int    `json:"importedPoints"`
	PendingItems    int    `json:"pendingItems"`
	ImportedSecrets int    `json:"importedSecrets"`
}

type legacyVectorItem struct {
	RowID       int64
	ItemKind    string
	ItemID      string
	ModelID     string
	ContentHash string
	Vector      []float32
	HasVector   bool
	PointID     uuid.UUID
	ScopeType   string
	CharacterID string
}

type legacySecret struct {
	Name      string
	Plaintext []byte
	UpdatedAt int64
}

func ImportVectorsAndSecrets(ctx context.Context, runID, intelligencePath, secretPath string, pool *pgstore.Pool, index *vectorindex.Client, cipher *secret.Cipher) (VectorSecretImportResult, error) {
	if ctx == nil {
		return VectorSecretImportResult{}, errors.New("vector import context is required")
	}
	if runID == "" || strings.TrimSpace(runID) != runID {
		return VectorSecretImportResult{}, errors.New("import run ID is required")
	}
	if pool == nil || pool.Raw() == nil || index == nil {
		return VectorSecretImportResult{}, errors.New("PostgreSQL and Qdrant targets are required")
	}
	intelligenceBefore, err := fingerprint(intelligencePath)
	if err != nil {
		return VectorSecretImportResult{}, err
	}
	intelligence, err := openImmutableSQLite(intelligencePath)
	if err != nil {
		return VectorSecretImportResult{}, err
	}
	defer intelligence.Close()
	items, err := loadLegacyVectorItems(ctx, intelligence, pool)
	if err != nil {
		return VectorSecretImportResult{}, err
	}
	if err := rejectOrphanLegacyVectors(ctx, intelligence); err != nil {
		return VectorSecretImportResult{}, err
	}

	var secretBefore *FileFingerprint
	secrets := []legacySecret{}
	if secretPath != "" {
		fingerprint, err := fingerprint(secretPath)
		if err != nil {
			return VectorSecretImportResult{}, err
		}
		secretBefore = &fingerprint
		secretDB, err := openImmutableSQLite(secretPath)
		if err != nil {
			return VectorSecretImportResult{}, err
		}
		secrets, err = loadLegacySecrets(ctx, secretDB)
		secretDB.Close()
		if err != nil {
			return VectorSecretImportResult{}, err
		}
		if len(secrets) > 0 && cipher == nil {
			clearLegacySecrets(secrets)
			return VectorSecretImportResult{}, secret.ErrCipherRequired
		}
		defer clearLegacySecrets(secrets)
	}

	result := VectorSecretImportResult{RunID: runID, ImportedSecrets: len(secrets)}
	for _, item := range items {
		if !item.HasVector {
			result.PendingItems++
			continue
		}
		if err := index.Upsert(ctx, vectorindex.Point{ID: item.PointID, Vector: item.Vector, Payload: vectorindex.PointPayloadInput{
			ItemKind: item.ItemKind, ItemID: item.ItemID, ModelID: item.ModelID,
			ScopeType: item.ScopeType, CharacterID: item.CharacterID, ContentHash: item.ContentHash,
		}}); err != nil {
			return result, err
		}
		result.ImportedPoints++
	}

	queryCtx, cancel := pool.QueryContext(ctx)
	defer cancel()
	tx, err := pool.Raw().BeginTx(queryCtx, pgx.TxOptions{})
	if err != nil {
		return result, err
	}
	defer tx.Rollback(queryCtx)
	if err := verifyRunningImportRun(queryCtx, tx, runID, intelligenceBefore); err != nil {
		return result, err
	}
	now := time.Now().UnixMilli()
	for _, item := range items {
		if item.HasVector {
			changed, err := tx.Exec(queryCtx, `
UPDATE memory_embedding_items SET status = 'embedded', error_code = NULL, error_message = NULL,
  embedded_at_ms = $4, updated_at_ms = GREATEST(updated_at_ms, $4)
WHERE item_kind = $1 AND item_id = $2 AND model_id = $3 AND content_hash = $5`, item.ItemKind, item.ItemID, item.ModelID, now, item.ContentHash)
			if err != nil || changed.RowsAffected() != 1 {
				return result, fmt.Errorf("mark imported vector %s/%s embedded: affected=%d error=%w", item.ItemKind, item.ItemID, changed.RowsAffected(), err)
			}
			if _, err := tx.Exec(queryCtx, `
UPDATE memory_embedding_jobs SET status = 'succeeded', lease_owner = NULL, lease_expires_at_ms = NULL,
  error_code = NULL, error_message = NULL, retryable = false, updated_at_ms = GREATEST(updated_at_ms, $4)
WHERE item_kind = $1 AND item_id = $2 AND model_id = $3 AND content_hash = $5`, item.ItemKind, item.ItemID, item.ModelID, now, item.ContentHash); err != nil {
				return result, err
			}
			continue
		}
		if _, err := tx.Exec(queryCtx, `
UPDATE memory_embedding_items SET status = 'pending', error_code = NULL, error_message = NULL,
  embedded_at_ms = NULL, updated_at_ms = GREATEST(updated_at_ms, $4)
WHERE item_kind = $1 AND item_id = $2 AND model_id = $3 AND content_hash = $5`, item.ItemKind, item.ItemID, item.ModelID, now, item.ContentHash); err != nil {
			return result, err
		}
		if _, err := tx.Exec(queryCtx, `
INSERT INTO memory_embedding_jobs(id,item_kind,item_id,model_id,dimensions,point_id,content_hash,status,created_at_ms,updated_at_ms)
VALUES ($1,$2,$3,$4,512,$5,$6,'pending',$7,$7)
ON CONFLICT(item_kind,item_id,model_id,content_hash) DO UPDATE SET
  status='pending',lease_owner=NULL,lease_expires_at_ms=NULL,error_code=NULL,error_message=NULL,retryable=false,updated_at_ms=$7`,
			uuid.NewString(), item.ItemKind, item.ItemID, item.ModelID, item.PointID, item.ContentHash, now); err != nil {
			return result, err
		}
	}
	for _, value := range secrets {
		namespace := "model"
		if strings.HasPrefix(value.Name, "speech.") {
			namespace = "speech"
		}
		nonce, ciphertext, aad, err := cipher.Seal(namespace, value.Name, value.Plaintext)
		if err != nil {
			return result, err
		}
		_, err = tx.Exec(queryCtx, `
INSERT INTO secret_values(namespace,name,key_version,nonce,ciphertext,aad,created_at_ms,updated_at_ms)
VALUES ($1,$2,$3,$4,$5,$6,$7,$7)
ON CONFLICT(namespace,name) DO UPDATE SET key_version=excluded.key_version,nonce=excluded.nonce,ciphertext=excluded.ciphertext,aad=excluded.aad,updated_at_ms=excluded.updated_at_ms`,
			namespace, value.Name, secret.KeyVersion, nonce, ciphertext, aad, value.UpdatedAt)
		clear(ciphertext)
		if err != nil {
			return result, fmt.Errorf("write encrypted legacy secret: %w", err)
		}
	}
	reportJSON, err := json.Marshal(result)
	if err != nil {
		return result, err
	}
	if _, err := tx.Exec(queryCtx, `
UPDATE sqlite_import_runs SET phase='vector_secret_complete',report_json=report_json || $2::jsonb,updated_at_ms=$3
WHERE id=$1 AND status='running'`, runID, reportJSON, now); err != nil {
		return result, err
	}
	if _, err := tx.Exec(queryCtx, `
INSERT INTO sqlite_import_checkpoints(run_id,phase,checkpoint_json,updated_at_ms)
VALUES($1,'vector_secret',$2::jsonb,$3)`, runID, reportJSON, now); err != nil {
		return result, err
	}
	if err := tx.Commit(queryCtx); err != nil {
		return result, err
	}
	if err := assertFingerprintUnchanged(intelligencePath, intelligenceBefore); err != nil {
		return result, err
	}
	if secretBefore != nil {
		if err := assertFingerprintUnchanged(secretPath, *secretBefore); err != nil {
			return result, err
		}
	}
	return result, nil
}

func loadLegacyVectorItems(ctx context.Context, db *sql.DB, pool *pgstore.Pool) ([]legacyVectorItem, error) {
	rows, err := db.QueryContext(ctx, `
SELECT i.vector_rowid,i.item_kind,i.item_id,i.model_id,i.content_hash,v.embedding
FROM memory_embedding_items i LEFT JOIN memory_embedding_vec v ON v.rowid=i.vector_rowid
ORDER BY i.vector_rowid`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]legacyVectorItem, 0)
	for rows.Next() {
		var item legacyVectorItem
		var blob []byte
		if err := rows.Scan(&item.RowID, &item.ItemKind, &item.ItemID, &item.ModelID, &item.ContentHash, &blob); err != nil {
			return nil, err
		}
		pointID, err := vectorindex.PointID(item.ItemKind, item.ItemID, item.ModelID)
		if err != nil {
			return nil, err
		}
		item.PointID = pointID
		content, scope, characterID, err := authoritativeVectorPayload(ctx, pool, item.ItemKind, item.ItemID)
		if err != nil {
			return nil, err
		}
		if contentHash(content) != item.ContentHash {
			return nil, fmt.Errorf("legacy vector item %s/%s content hash does not match PostgreSQL", item.ItemKind, item.ItemID)
		}
		item.ScopeType, item.CharacterID = scope, characterID
		if blob != nil {
			if err := validateVectorBlob(blob); err != nil {
				return nil, fmt.Errorf("legacy vector item %s/%s: %w", item.ItemKind, item.ItemID, err)
			}
			item.Vector = decodeVector(blob)
			item.HasVector = true
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func authoritativeVectorPayload(ctx context.Context, pool *pgstore.Pool, itemKind, itemID string) (string, string, string, error) {
	queryCtx, cancel := pool.QueryContext(ctx)
	defer cancel()
	var content, scope, characterID string
	switch itemKind {
	case vectorindex.ItemKindPersonalMemory:
		err := pool.Raw().QueryRow(queryCtx, "SELECT content,scope_kind,COALESCE(character_id,'') FROM personal_memories WHERE id=$1", itemID).Scan(&content, &scope, &characterID)
		return content, scope, characterID, err
	case vectorindex.ItemKindKnowledge:
		err := pool.Raw().QueryRow(queryCtx, "SELECT topic || chr(10) || statement,'knowledge','' FROM knowledge_entries WHERE id=$1", itemID).Scan(&content, &scope, &characterID)
		return content, scope, characterID, err
	default:
		return "", "", "", errors.New("legacy vector item kind is unsupported")
	}
}

func rejectOrphanLegacyVectors(ctx context.Context, db *sql.DB) error {
	var count int64
	err := db.QueryRowContext(ctx, "SELECT count(*) FROM memory_embedding_vec v LEFT JOIN memory_embedding_items i ON i.vector_rowid=v.rowid WHERE i.vector_rowid IS NULL").Scan(&count)
	if err != nil {
		return err
	}
	if count != 0 {
		return fmt.Errorf("legacy vector table contains %d orphan rows", count)
	}
	return nil
}

func loadLegacySecrets(ctx context.Context, db *sql.DB) ([]legacySecret, error) {
	if _, err := validateSecrets(ctx, db); err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, "SELECT connection_id,secret,updated_at_ms FROM model_secrets ORDER BY connection_id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]legacySecret, 0)
	for rows.Next() {
		var name, raw string
		var updatedAt int64
		if err := rows.Scan(&name, &raw, &updatedAt); err != nil {
			clearLegacySecrets(result)
			return nil, err
		}
		if _, err := secret.NewValue(raw); err != nil {
			clearLegacySecrets(result)
			return nil, errors.New("legacy secret value is invalid")
		}
		result = append(result, legacySecret{Name: name, Plaintext: []byte(raw), UpdatedAt: updatedAt})
	}
	return result, rows.Err()
}

func verifyRunningImportRun(ctx context.Context, tx pgx.Tx, runID string, fingerprint FileFingerprint) error {
	var storedPath, phase string
	var storedFingerprint []byte
	err := tx.QueryRow(ctx, "SELECT source_intelligence_path,source_fingerprint,phase FROM sqlite_import_runs WHERE id=$1 AND status='running' FOR UPDATE", runID).Scan(&storedPath, &storedFingerprint, &phase)
	if err != nil {
		return fmt.Errorf("load import run: %w", err)
	}
	var stored FileFingerprint
	if json.Unmarshal(storedFingerprint, &stored) != nil || storedPath != fingerprint.Path || stored.SHA256 != fingerprint.SHA256 || phase != "relational_complete" {
		return errors.New("import run does not match relational source checkpoint")
	}
	return nil
}

func decodeVector(blob []byte) []float32 {
	vector := make([]float32, len(blob)/4)
	for index := range vector {
		vector[index] = math.Float32frombits(binary.LittleEndian.Uint32(blob[index*4 : index*4+4]))
	}
	return vector
}

func contentHash(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

func clearLegacySecrets(values []legacySecret) {
	for index := range values {
		clear(values[index].Plaintext)
	}
}
