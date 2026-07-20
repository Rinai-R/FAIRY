package importer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	pgstore "fairy/postgres"
	"fairy/secret"
	"fairy/vectorindex"

	"github.com/jackc/pgx/v5"
)

type RunHooks struct {
	AfterPreflight    func() error
	AfterRelational   func() error
	AfterVectorSecret func() error
}

type RunOptions struct {
	IntelligencePath string
	SecretPath       string
	Getenv           func(string) string
	Hooks            RunHooks
}

type RunReport struct {
	RunID        string                    `json:"runId"`
	Status       string                    `json:"status"`
	NoOp         bool                      `json:"noOp"`
	Relational   *RelationalImportResult   `json:"relational,omitempty"`
	VectorSecret *VectorSecretImportResult `json:"vectorSecret,omitempty"`
	NextCommands []string                  `json:"nextCommands"`
}

func Run(ctx context.Context, options RunOptions) (RunReport, error) {
	getenv := options.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	preflight, err := Preflight(ctx, PreflightOptions{
		IntelligencePath: options.IntelligencePath, SecretPath: options.SecretPath, Getenv: getenv,
	})
	if err != nil {
		return RunReport{}, err
	}
	if options.Hooks.AfterPreflight != nil {
		if err := options.Hooks.AfterPreflight(); err != nil {
			return RunReport{}, err
		}
	}
	databaseConfig, err := pgstore.ConfigFromEnv(getenv)
	if err != nil {
		return RunReport{}, err
	}
	pool, err := pgstore.Open(ctx, databaseConfig)
	if err != nil {
		return RunReport{}, err
	}
	defer pool.Close()
	vectorConfig, err := vectorindex.ConfigFromEnv(getenv)
	if err != nil {
		return RunReport{}, err
	}
	index, err := vectorindex.Open(ctx, vectorConfig)
	if err != nil {
		return RunReport{}, err
	}
	defer index.Close()
	var cipher *secret.Cipher
	if preflight.SecretRows > 0 {
		cipher, err = secret.CipherFromEnv(getenv)
		if err != nil {
			return RunReport{}, err
		}
	}

	report := RunReport{
		RunID: preflight.ExistingRunID, Status: preflight.ExistingStatus,
		NextCommands: []string{"fairy db status", "fairy doctor", "fairy serve"},
	}
	phase := preflight.ExistingPhase
	if report.RunID == "" {
		relational, err := ImportRelational(ctx, options.IntelligencePath, pool)
		if err != nil {
			return RunReport{}, err
		}
		report.RunID = relational.RunID
		report.Status = "running"
		report.Relational = &relational
		phase = "relational_complete"
		if options.Hooks.AfterRelational != nil {
			if err := options.Hooks.AfterRelational(); err != nil {
				return report, err
			}
		}
	}
	if report.Status == "verified" {
		if err := verifyImportedState(ctx, options, pool, index, cipher, report.RunID); err != nil {
			return report, err
		}
		report.NoOp = true
		return report, nil
	}
	if phase == "relational_complete" {
		vectorSecret, err := ImportVectorsAndSecrets(ctx, report.RunID, options.IntelligencePath, options.SecretPath, pool, index, cipher)
		if err != nil {
			return report, err
		}
		report.VectorSecret = &vectorSecret
		phase = "vector_secret_complete"
		if options.Hooks.AfterVectorSecret != nil {
			if err := options.Hooks.AfterVectorSecret(); err != nil {
				return report, err
			}
		}
	}
	if phase != "vector_secret_complete" {
		return report, fmt.Errorf("import run phase %q cannot be resumed", phase)
	}
	if err := verifyImportedState(ctx, options, pool, index, cipher, report.RunID); err != nil {
		return report, err
	}
	report.Status = "verified"
	report.NoOp = false
	if err := markRunVerified(ctx, pool, report); err != nil {
		return report, err
	}
	return report, nil
}

func verifyImportedState(ctx context.Context, options RunOptions, pool *pgstore.Pool, index *vectorindex.Client, cipher *secret.Cipher, runID string) error {
	source, err := openImmutableSQLite(options.IntelligencePath)
	if err != nil {
		return err
	}
	defer source.Close()
	queryCtx, cancel := pool.QueryContext(ctx)
	defer cancel()
	tx, err := pool.Raw().BeginTx(queryCtx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return err
	}
	defer tx.Rollback(queryCtx)
	for _, spec := range relationalTables {
		if spec.name == "memory_embedding_items" || spec.name == "memory_embedding_jobs" {
			continue
		}
		sourceRows, err := loadSourceRows(ctx, source, spec)
		if err != nil {
			return err
		}
		targetRows, err := loadTargetRows(queryCtx, tx, spec)
		if err != nil {
			return err
		}
		sourceHash, err := normalizedRowsHash(sourceRows, spec)
		if err != nil {
			return err
		}
		targetHash, err := normalizedRowsHash(targetRows, spec)
		if err != nil {
			return err
		}
		if len(sourceRows) != len(targetRows) || sourceHash != targetHash {
			return fmt.Errorf("verified rerun table %s parity mismatch", spec.name)
		}
	}
	items, err := loadLegacyVectorItems(ctx, source, pool)
	if err != nil {
		return err
	}
	pointCount := 0
	for _, item := range items {
		found, err := index.HasPoint(ctx, item.PointID)
		if err != nil {
			return err
		}
		if item.HasVector {
			pointCount++
			if !found {
				return fmt.Errorf("verified vector point is missing for %s/%s", item.ItemKind, item.ItemID)
			}
		} else if found {
			return fmt.Errorf("missing legacy vector has unexpected point for %s/%s", item.ItemKind, item.ItemID)
		}
	}
	metrics, err := index.Metrics(ctx)
	if err != nil {
		return err
	}
	if metrics.PointCount != uint64(pointCount) {
		return fmt.Errorf("verified vector point count = %d, want %d", metrics.PointCount, pointCount)
	}
	if options.SecretPath != "" {
		secretDB, err := openImmutableSQLite(options.SecretPath)
		if err != nil {
			return err
		}
		values, err := loadLegacySecrets(ctx, secretDB)
		secretDB.Close()
		if err != nil {
			return err
		}
		defer clearLegacySecrets(values)
		if len(values) > 0 && cipher == nil {
			return secret.ErrCipherRequired
		}
		store, err := secret.NewPostgresStore(pool, cipher)
		if err != nil {
			return err
		}
		for _, value := range values {
			loaded, ok, err := store.Load(value.Name)
			if err != nil || !ok || loaded.Expose() != string(value.Plaintext) {
				return errors.New("verified secret round trip mismatch")
			}
		}
	}
	var storedStatus string
	if err := tx.QueryRow(queryCtx, "SELECT status FROM sqlite_import_runs WHERE id=$1", runID).Scan(&storedStatus); err != nil {
		return err
	}
	if storedStatus != "running" && storedStatus != "verified" {
		return fmt.Errorf("import run status %q cannot be verified", storedStatus)
	}
	return nil
}

func markRunVerified(ctx context.Context, pool *pgstore.Pool, report RunReport) error {
	raw, err := json.Marshal(report)
	if err != nil {
		return err
	}
	queryCtx, cancel := pool.QueryContext(ctx)
	defer cancel()
	changed, err := pool.Raw().Exec(queryCtx, `
UPDATE sqlite_import_runs SET status='verified',phase='verified',report_json=$2::jsonb,updated_at_ms=$3
WHERE id=$1 AND status='running' AND phase='vector_secret_complete'`, report.RunID, raw, time.Now().UnixMilli())
	if err != nil {
		return err
	}
	if changed.RowsAffected() != 1 {
		return errors.New("import run was not eligible for verified transition")
	}
	return nil
}
