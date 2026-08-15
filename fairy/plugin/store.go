package plugin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
)

var (
	ErrStoreRequired         = errors.New("plugin SeekDB store is required")
	ErrQueryLimitInvalid     = errors.New("plugin store query limit must be greater than zero")
	ErrPackageInvalid        = errors.New("plugin package is invalid")
	ErrInstanceInvalid       = errors.New("plugin instance is invalid")
	ErrStateKeyInvalid       = errors.New("plugin state key is invalid")
	ErrConfigContainsSecret  = errors.New("plugin config document must not contain credential plaintext")
	ErrGuestSQLDenied        = errors.New("plugin guests cannot execute SQL")
	ErrConfigRefInvalid      = errors.New("plugin config secret reference is invalid")
	ErrUpgradeJournalInvalid = errors.New("plugin upgrade journal entry is invalid")
)

type PackageRecord struct {
	ID               string
	Version          string
	ABIVersion       uint32
	ArtifactSHA256   [sha256.Size]byte
	Manifest         Manifest
	VerifiedAtUnixMS int64
}

type InstanceRecord struct {
	ID               string
	PluginID         string
	PluginVersion    string
	Enabled          bool
	Lifecycle        string
	CapabilityGrants []string
	ConfigDocument   json.RawMessage
	CreatedAtUnixMS  int64
	UpdatedAtUnixMS  int64
}

type StatsRecord struct {
	InstanceID      string
	GuestCalls      uint64
	HostCalls       uint64
	LastErrorCode   string
	UpdatedAtUnixMS int64
}

type UpgradeRecord struct {
	JournalID        string
	InstanceID       string
	FromVersion      string
	ToVersion        string
	Status           string
	ErrorCode        string
	ErrorMessage     string
	StartedAtUnixMS  int64
	FinishedAtUnixMS int64
}

type ConfigRef struct {
	InstanceID      string
	Handle          string
	SecretNamespace string
	SecretName      string
}

type Store struct {
	db         *sql.DB
	queryLimit time.Duration
	now        func() time.Time
}

func NewStore(database *sql.DB, queryLimit time.Duration) (*Store, error) {
	if database == nil {
		return nil, ErrStoreRequired
	}
	if queryLimit <= 0 {
		return nil, ErrQueryLimitInvalid
	}
	return &Store{db: database, queryLimit: queryLimit, now: time.Now}, nil
}

func (s *Store) PutPackage(ctx context.Context, record PackageRecord) error {
	if err := validatePackage(record); err != nil {
		return err
	}
	manifest, err := EncodeManifest(record.Manifest)
	if err != nil {
		return err
	}
	qctx, cancel := s.queryContext(ctx)
	defer cancel()
	_, err = s.db.ExecContext(qctx, `
INSERT INTO plugin_packages(plugin_id, version, abi_version, artifact_sha256, publisher_digest, manifest, verified_at_ms)
VALUES (?, ?, ?, ?, NULL, ?, ?)
ON DUPLICATE KEY UPDATE abi_version = VALUES(abi_version), artifact_sha256 = VALUES(artifact_sha256),
  manifest = VALUES(manifest), verified_at_ms = VALUES(verified_at_ms)`,
		record.ID, record.Version, record.ABIVersion, record.ArtifactSHA256[:], manifest, record.VerifiedAtUnixMS)
	if err != nil {
		return fmt.Errorf("persist plugin package %s@%s: %w", record.ID, record.Version, err)
	}
	return nil
}

func (s *Store) PutInstance(ctx context.Context, record InstanceRecord) error {
	if err := validateInstance(record); err != nil {
		return err
	}
	if record.CapabilityGrants == nil {
		record.CapabilityGrants = []string{}
	}
	grants, err := json.Marshal(record.CapabilityGrants)
	if err != nil {
		return fmt.Errorf("encoding plugin capability grants: %w", err)
	}
	qctx, cancel := s.queryContext(ctx)
	defer cancel()
	now := s.currentUnixMS()
	if record.CreatedAtUnixMS == 0 {
		record.CreatedAtUnixMS = now
	}
	record.UpdatedAtUnixMS = now
	enabled := 0
	if record.Enabled {
		enabled = 1
	}
	_, err = s.db.ExecContext(qctx, `
INSERT INTO plugin_instances(instance_id, plugin_id, plugin_version, enabled, lifecycle_state, capability_grants, config_document, created_at_ms, updated_at_ms)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON DUPLICATE KEY UPDATE plugin_id = VALUES(plugin_id), plugin_version = VALUES(plugin_version),
  enabled = VALUES(enabled), lifecycle_state = VALUES(lifecycle_state),
  capability_grants = VALUES(capability_grants), config_document = VALUES(config_document),
  updated_at_ms = VALUES(updated_at_ms)`,
		record.ID, record.PluginID, record.PluginVersion, enabled, record.Lifecycle, grants, string(record.ConfigDocument),
		record.CreatedAtUnixMS, record.UpdatedAtUnixMS)
	if err != nil {
		return fmt.Errorf("persist plugin instance %s: %w", record.ID, err)
	}
	return nil
}

func (s *Store) Instance(ctx context.Context, instanceID string) (InstanceRecord, error) {
	if err := validatePluginID(instanceID); err != nil {
		return InstanceRecord{}, fmt.Errorf("%w: %v", ErrInstanceInvalid, err)
	}
	qctx, cancel := s.queryContext(ctx)
	defer cancel()
	record, err := scanInstance(s.db.QueryRowContext(qctx, `
SELECT instance_id, plugin_id, plugin_version, enabled, lifecycle_state, capability_grants, config_document, created_at_ms, updated_at_ms
FROM plugin_instances WHERE instance_id = ?`, instanceID))
	if errors.Is(err, sql.ErrNoRows) {
		return InstanceRecord{}, fmt.Errorf("%w: instance %s is not installed", ErrInstanceInvalid, instanceID)
	}
	if err != nil {
		return InstanceRecord{}, fmt.Errorf("read plugin instance %s: %w", instanceID, err)
	}
	return record, nil
}

const (
	maxInstanceList = 256
	maxUpgradeList  = 50
)

func (s *Store) Instances(ctx context.Context) ([]InstanceRecord, error) {
	qctx, cancel := s.queryContext(ctx)
	defer cancel()
	rows, err := s.db.QueryContext(qctx, `
SELECT instance_id, plugin_id, plugin_version, enabled, lifecycle_state, capability_grants, config_document, created_at_ms, updated_at_ms
FROM plugin_instances
ORDER BY instance_id
LIMIT ?`, maxInstanceList)
	if err != nil {
		return nil, fmt.Errorf("list plugin instances: %w", err)
	}
	defer rows.Close()
	records := make([]InstanceRecord, 0)
	for rows.Next() {
		record, err := scanInstance(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list plugin instances: %w", err)
	}
	return records, nil
}

func (s *Store) Upgrades(ctx context.Context, instanceID string) ([]UpgradeRecord, error) {
	if instanceID != "" {
		if err := validatePluginID(instanceID); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInstanceInvalid, err)
		}
	}
	qctx, cancel := s.queryContext(ctx)
	defer cancel()
	query := `
SELECT journal_id, instance_id, from_version, to_version, status, error_code, error_message, started_at_ms, finished_at_ms
FROM plugin_upgrade_journal`
	args := []any{}
	if instanceID != "" {
		query += ` WHERE instance_id = ?`
		args = append(args, instanceID)
	}
	query += ` ORDER BY started_at_ms DESC, journal_id DESC LIMIT ?`
	args = append(args, maxUpgradeList)
	rows, err := s.db.QueryContext(qctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list plugin upgrades: %w", err)
	}
	defer rows.Close()
	records := make([]UpgradeRecord, 0)
	for rows.Next() {
		var record UpgradeRecord
		var errCode, errMsg sql.NullString
		var finished sql.NullInt64
		if err := rows.Scan(
			&record.JournalID, &record.InstanceID, &record.FromVersion, &record.ToVersion, &record.Status,
			&errCode, &errMsg, &record.StartedAtUnixMS, &finished,
		); err != nil {
			return nil, fmt.Errorf("scan plugin upgrade: %w", err)
		}
		record.ErrorCode = errCode.String
		record.ErrorMessage = errMsg.String
		record.FinishedAtUnixMS = finished.Int64
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list plugin upgrades: %w", err)
	}
	return records, nil
}

func scanInstance(row interface {
	Scan(dest ...any) error
}) (InstanceRecord, error) {
	var record InstanceRecord
	var enabled int
	var grants json.RawMessage
	var config string
	if err := row.Scan(
		&record.ID, &record.PluginID, &record.PluginVersion, &enabled, &record.Lifecycle, &grants, &config,
		&record.CreatedAtUnixMS, &record.UpdatedAtUnixMS,
	); err != nil {
		return InstanceRecord{}, err
	}
	record.Enabled = enabled == 1
	record.ConfigDocument = json.RawMessage(config)
	if err := json.Unmarshal(grants, &record.CapabilityGrants); err != nil {
		return InstanceRecord{}, fmt.Errorf("decode plugin instance grants: %w", err)
	}
	if record.CapabilityGrants == nil {
		record.CapabilityGrants = []string{}
	}
	return record, nil
}

func (s *Store) PutState(ctx context.Context, instanceID, key, value string) error {
	if err := validateStateKey(key); err != nil {
		return err
	}
	if err := validatePluginID(instanceID); err != nil {
		return fmt.Errorf("%w: %v", ErrInstanceInvalid, err)
	}
	qctx, cancel := s.queryContext(ctx)
	defer cancel()
	_, err := s.db.ExecContext(qctx, `
INSERT INTO plugin_instance_state(instance_id, state_key, value, updated_at_ms)
VALUES (?, ?, ?, ?)
ON DUPLICATE KEY UPDATE value = VALUES(value), updated_at_ms = VALUES(updated_at_ms)`,
		instanceID, key, value, s.currentUnixMS())
	if err != nil {
		return fmt.Errorf("persist plugin state %s/%s: %w", instanceID, key, err)
	}
	return nil
}

func (s *Store) State(ctx context.Context, instanceID, key string) (string, bool, error) {
	if err := validateStateKey(key); err != nil {
		return "", false, err
	}
	qctx, cancel := s.queryContext(ctx)
	defer cancel()
	var value string
	err := s.db.QueryRowContext(qctx, `
SELECT value FROM plugin_instance_state WHERE instance_id = ? AND state_key = ?`, instanceID, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read plugin state %s/%s: %w", instanceID, key, err)
	}
	return value, true, nil
}

func (s *Store) RecordStats(ctx context.Context, stats StatsRecord) error {
	if err := validatePluginID(stats.InstanceID); err != nil {
		return fmt.Errorf("%w: %v", ErrInstanceInvalid, err)
	}
	qctx, cancel := s.queryContext(ctx)
	defer cancel()
	var last any
	if stats.LastErrorCode != "" {
		last = stats.LastErrorCode
	}
	_, err := s.db.ExecContext(qctx, `
INSERT INTO plugin_instance_stats(instance_id, guest_calls, host_calls, last_error_code, updated_at_ms)
VALUES (?, ?, ?, ?, ?)
ON DUPLICATE KEY UPDATE guest_calls = VALUES(guest_calls), host_calls = VALUES(host_calls),
  last_error_code = VALUES(last_error_code), updated_at_ms = VALUES(updated_at_ms)`,
		stats.InstanceID, stats.GuestCalls, stats.HostCalls, last, s.currentUnixMS())
	if err != nil {
		return fmt.Errorf("persist plugin stats %s: %w", stats.InstanceID, err)
	}
	return nil
}

func (s *Store) AppendUpgrade(ctx context.Context, record UpgradeRecord) error {
	if err := validateUpgrade(record); err != nil {
		return err
	}
	qctx, cancel := s.queryContext(ctx)
	defer cancel()
	var errCode, errMsg, finished any
	if record.ErrorCode != "" {
		errCode = record.ErrorCode
		errMsg = record.ErrorMessage
	}
	if record.FinishedAtUnixMS > 0 {
		finished = record.FinishedAtUnixMS
	}
	_, err := s.db.ExecContext(qctx, `
INSERT INTO plugin_upgrade_journal(journal_id, instance_id, from_version, to_version, status, error_code, error_message, started_at_ms, finished_at_ms)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		record.JournalID, record.InstanceID, record.FromVersion, record.ToVersion, record.Status,
		errCode, errMsg, record.StartedAtUnixMS, finished)
	if err != nil {
		return fmt.Errorf("persist plugin upgrade journal %s: %w", record.JournalID, err)
	}
	return nil
}

func (s *Store) PutConfigRef(ctx context.Context, ref ConfigRef) error {
	if err := validateConfigRef(ref); err != nil {
		return err
	}
	qctx, cancel := s.queryContext(ctx)
	defer cancel()
	_, err := s.db.ExecContext(qctx, `
INSERT INTO plugin_instance_config_refs(instance_id, handle, secret_namespace, secret_name, created_at_ms)
VALUES (?, ?, ?, ?, ?)
ON DUPLICATE KEY UPDATE secret_namespace = VALUES(secret_namespace), secret_name = VALUES(secret_name)`,
		ref.InstanceID, ref.Handle, ref.SecretNamespace, ref.SecretName, s.currentUnixMS())
	if err != nil {
		return fmt.Errorf("persist plugin config ref %s/%s: %w", ref.InstanceID, ref.Handle, err)
	}
	return nil
}

func (s *Store) queryContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(parent, s.queryLimit)
}

func (s *Store) currentUnixMS() int64 {
	now := time.Now
	if s != nil && s.now != nil {
		now = s.now
	}
	return max(now().UnixMilli(), int64(1))
}

func EncodeManifest(manifest Manifest) ([]byte, error) {
	if err := validateManifest(manifest); err != nil {
		return nil, err
	}
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(manifest); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buffer.Bytes(), []byte("\n")), nil
}

func validatePackage(record PackageRecord) error {
	if err := validatePluginID(record.ID); err != nil {
		return fmt.Errorf("%w: %v", ErrPackageInvalid, err)
	}
	if err := validateSemver(record.Version); err != nil {
		return fmt.Errorf("%w: %v", ErrPackageInvalid, err)
	}
	if record.ABIVersion < 1 || record.VerifiedAtUnixMS < 1 {
		return fmt.Errorf("%w: abi and verified_at are required", ErrPackageInvalid)
	}
	var zero [sha256.Size]byte
	if record.ArtifactSHA256 == zero {
		return fmt.Errorf("%w: artifact sha256 is required", ErrPackageInvalid)
	}
	return validateManifest(record.Manifest)
}

func validateInstance(record InstanceRecord) error {
	if err := validatePluginID(record.ID); err != nil {
		return fmt.Errorf("%w: %v", ErrInstanceInvalid, err)
	}
	if err := validatePluginID(record.PluginID); err != nil {
		return fmt.Errorf("%w: %v", ErrInstanceInvalid, err)
	}
	if err := validateSemver(record.PluginVersion); err != nil {
		return fmt.Errorf("%w: %v", ErrInstanceInvalid, err)
	}
	switch record.Lifecycle {
	case "disabled", "ready", "degraded", "failed":
	default:
		return fmt.Errorf("%w: lifecycle_state is invalid", ErrInstanceInvalid)
	}
	if record.Enabled && record.Lifecycle == "disabled" {
		return fmt.Errorf("%w: enabled instance cannot be disabled", ErrInstanceInvalid)
	}
	if !record.Enabled && record.Lifecycle != "disabled" {
		return fmt.Errorf("%w: disabled instance must use lifecycle disabled", ErrInstanceInvalid)
	}
	if err := validateCapabilities(record.CapabilityGrants); err != nil {
		return fmt.Errorf("%w: %v", ErrInstanceInvalid, err)
	}
	return rejectSecretfulConfig(record.ConfigDocument)
}

func validateStateKey(key string) error {
	if key == "" || len(key) > 128 || strings.TrimSpace(key) != key {
		return ErrStateKeyInvalid
	}
	if strings.ContainsAny(key, `/\\`) || strings.Contains(key, "..") {
		return ErrStateKeyInvalid
	}
	for _, r := range key {
		letter := r >= 'a' && r <= 'z'
		digit := r >= '0' && r <= '9'
		if !letter && !digit && r != '.' && r != '_' && r != '-' {
			return ErrStateKeyInvalid
		}
	}
	return nil
}

func rejectSecretfulConfig(document json.RawMessage) error {
	if len(document) == 0 {
		return fmt.Errorf("%w: config document is required", ErrInstanceInvalid)
	}
	if !json.Valid(document) {
		return fmt.Errorf("%w: config document is not valid JSON", ErrInstanceInvalid)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(document, &object); err != nil {
		return fmt.Errorf("%w: config document must be an object", ErrInstanceInvalid)
	}
	raw := strings.ToLower(string(document))
	for _, token := range []string{"bearer ", "sk-live", "password=", `"api_key"`, `"apikey"`, `"secret"`} {
		if strings.Contains(raw, token) {
			return ErrConfigContainsSecret
		}
	}
	return nil
}

func validateUpgrade(record UpgradeRecord) error {
	if record.JournalID == "" || record.InstanceID == "" || record.FromVersion == "" || record.ToVersion == "" || record.StartedAtUnixMS < 1 {
		return ErrUpgradeJournalInvalid
	}
	switch record.Status {
	case "started":
		if record.FinishedAtUnixMS != 0 || record.ErrorCode != "" || record.ErrorMessage != "" {
			return ErrUpgradeJournalInvalid
		}
	case "succeeded":
		if record.FinishedAtUnixMS < record.StartedAtUnixMS || record.ErrorCode != "" {
			return ErrUpgradeJournalInvalid
		}
	case "failed", "rolled_back":
		if record.FinishedAtUnixMS < record.StartedAtUnixMS || record.ErrorCode == "" || record.ErrorMessage == "" {
			return ErrUpgradeJournalInvalid
		}
	default:
		return ErrUpgradeJournalInvalid
	}
	return nil
}

func validateConfigRef(ref ConfigRef) error {
	if err := validatePluginID(ref.InstanceID); err != nil {
		return fmt.Errorf("%w: %v", ErrConfigRefInvalid, err)
	}
	if ref.Handle == "" || strings.TrimSpace(ref.Handle) != ref.Handle {
		return ErrConfigRefInvalid
	}
	for _, r := range ref.Handle {
		if unicode.IsSpace(r) {
			return ErrConfigRefInvalid
		}
	}
	if ref.SecretNamespace == "" || ref.SecretName == "" {
		return ErrConfigRefInvalid
	}
	return nil
}
