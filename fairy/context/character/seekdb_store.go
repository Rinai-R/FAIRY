package character

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

const (
	activeCharacterNamespace = "character"
	activeCharacterKey       = "active"
)

var (
	ErrCharacterSeekDBRequired    = errors.New("character SeekDB connection is required")
	ErrCharacterQueryLimitInvalid = errors.New("character query limit must be greater than zero")
	ErrCharacterVisualRootInvalid = errors.New("character visual root must be an absolute non-root path")
	ErrCharacterRevisionConflict  = errors.New("character revision conflict")
)

type seekDBCharacterStore struct {
	database     *sql.DB
	queryLimit   time.Duration
	now          func() time.Time
	mutationHook func(string) error
}

type seekDBAppearanceRef struct {
	SchemaVersion   uint32 `json:"schema_version"`
	VisualPackID    string `json:"visual_pack_id"`
	BindingRevision uint64 `json:"binding_revision"`
}

type storedCharacter struct {
	characterID   string
	revision      uint64
	name          string
	snapshot      []byte
	appearanceRef sql.NullString
}

// NewSeekDBStore returns a character Store whose authoritative snapshots live
// only in SeekDB. root is used solely to resolve immutable visual-pack assets;
// this constructor never reads legacy file-backed character revisions.
func NewSeekDBStore(database *sql.DB, root string, queryLimit time.Duration) (*Store, error) {
	if database == nil {
		return nil, ErrCharacterSeekDBRequired
	}
	if queryLimit <= 0 {
		return nil, ErrCharacterQueryLimitInvalid
	}
	root = filepath.Clean(strings.TrimSpace(root))
	if !filepath.IsAbs(root) || root == string(filepath.Separator) || root == "." {
		return nil, ErrCharacterVisualRootInvalid
	}
	return &Store{
		root: root,
		seekDB: &seekDBCharacterStore{
			database:   database,
			queryLimit: queryLimit,
			now:        time.Now,
		},
	}, nil
}

func (s *seekDBCharacterStore) list(parent context.Context, root string) (Catalog, error) {
	queryCtx, cancel := s.queryContext(parent)
	defer cancel()
	tx, err := s.database.BeginTx(queryCtx, nil)
	if err != nil {
		return Catalog{}, fmt.Errorf("starting character catalog read: %w", err)
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(queryCtx, `
SELECT character_id, revision, name, snapshot, appearance_ref
FROM characters
ORDER BY character_id ASC`)
	if err != nil {
		return Catalog{}, fmt.Errorf("listing SeekDB characters: %w", err)
	}
	characters := make([]Record, 0)
	diagnostics := make([]Diagnostic, 0)
	for rows.Next() {
		row, scanErr := scanStoredCharacter(rows)
		if scanErr != nil {
			rows.Close()
			return Catalog{}, fmt.Errorf("scanning SeekDB character: %w", scanErr)
		}
		record, diagnostic := decodeStoredCharacter(root, row)
		if diagnostic != nil {
			diagnostics = append(diagnostics, *diagnostic)
			continue
		}
		characters = append(characters, record)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return Catalog{}, fmt.Errorf("reading SeekDB characters: %w", err)
	}
	if err := rows.Close(); err != nil {
		return Catalog{}, fmt.Errorf("closing SeekDB character rows: %w", err)
	}

	selection, selected, err := loadActiveSelection(queryCtx, tx, false)
	if err != nil {
		return Catalog{}, err
	}
	if err := tx.Commit(); err != nil {
		return Catalog{}, fmt.Errorf("committing character catalog read: %w", err)
	}
	var active *Record
	if selected {
		for index := range characters {
			if characters[index].CharacterID == selection.CharacterID && characters[index].Revision == selection.Revision {
				copy := characters[index]
				active = &copy
				break
			}
		}
	}
	return Catalog{Characters: characters, Active: active, Diagnostics: diagnostics}, nil
}

func (s *seekDBCharacterStore) lookup(parent context.Context, root, characterID string) (Record, bool, error) {
	if !validID(characterID) {
		return Record{}, false, ErrInvalidCharacterID
	}
	queryCtx, cancel := s.queryContext(parent)
	defer cancel()
	row, err := queryStoredCharacter(queryCtx, s.database, characterID, false)
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, false, nil
	}
	if err != nil {
		return Record{}, false, fmt.Errorf("loading SeekDB character: %w", err)
	}
	record, diagnostic := decodeStoredCharacter(root, row)
	if diagnostic != nil {
		return Record{}, false, nil
	}
	return record, true, nil
}

func (s *seekDBCharacterStore) create(parent context.Context, root string, brief Brief, visualPackID string) (Record, error) {
	manifest, err := loadVisualManifest(root, visualPackID)
	if err != nil {
		return Record{}, err
	}
	characterID := newID()
	snapshot, err := compileSnapshot(characterID, 1, brief)
	if err != nil {
		return Record{}, err
	}
	snapshotJSON, err := json.Marshal(snapshot)
	if err != nil {
		return Record{}, fmt.Errorf("serializing character snapshot: %w", err)
	}
	appearanceRef, appearanceJSON, err := marshalAppearanceRef(visualPackID, 1)
	if err != nil {
		return Record{}, err
	}
	queryCtx, cancel := s.queryContext(parent)
	defer cancel()
	now := s.nowUnixMS()
	_, err = s.database.ExecContext(queryCtx, `
INSERT INTO characters(character_id, revision, name, snapshot, appearance_ref, created_at_ms, updated_at_ms)
VALUES (?, ?, ?, ?, ?, ?, ?)`, characterID, snapshot.Revision, snapshot.Identity.Name, string(snapshotJSON), appearanceJSON, now, now)
	if err != nil {
		return Record{}, fmt.Errorf("creating SeekDB character: %w", err)
	}
	return recordFromSnapshot(snapshot, Appearance{Status: "assigned", BindingRevision: appearanceRef.BindingRevision, Visual: &manifest}), nil
}

func (s *seekDBCharacterStore) update(parent context.Context, root, characterID string, brief Brief) (Record, error) {
	if !validID(characterID) {
		return Record{}, ErrInvalidCharacterID
	}
	queryCtx, cancel := s.queryContext(parent)
	defer cancel()
	tx, err := s.database.BeginTx(queryCtx, nil)
	if err != nil {
		return Record{}, fmt.Errorf("starting character update: %w", err)
	}
	defer tx.Rollback()

	row, err := queryStoredCharacter(queryCtx, tx, characterID, true)
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, ErrCharacterNotFound
	}
	if err != nil {
		return Record{}, fmt.Errorf("locking SeekDB character: %w", err)
	}
	snapshot, err := compileSnapshot(characterID, row.revision+1, brief)
	if err != nil {
		return Record{}, err
	}
	snapshotJSON, err := json.Marshal(snapshot)
	if err != nil {
		return Record{}, fmt.Errorf("serializing character snapshot: %w", err)
	}
	result, err := tx.ExecContext(queryCtx, `
UPDATE characters
SET revision = ?, name = ?, snapshot = ?, updated_at_ms = GREATEST(updated_at_ms, ?)
WHERE character_id = ? AND revision = ?`, snapshot.Revision, snapshot.Identity.Name, string(snapshotJSON), s.nowUnixMS(), characterID, row.revision)
	if err != nil {
		return Record{}, fmt.Errorf("updating SeekDB character: %w", err)
	}
	if err := requireOneCharacterMutation(result); err != nil {
		return Record{}, err
	}
	selection, selected, err := loadActiveSelection(queryCtx, tx, true)
	if err != nil {
		return Record{}, err
	}
	if selected && selection.CharacterID == characterID {
		documentRevision, exists, err := loadActiveDocumentRevision(queryCtx, tx, false)
		if err != nil {
			return Record{}, err
		}
		if !exists {
			return Record{}, errors.New("active character document disappeared during update")
		}
		selection.Revision = snapshot.Revision
		selectionJSON, err := json.Marshal(selection)
		if err != nil {
			return Record{}, fmt.Errorf("serializing active character selection: %w", err)
		}
		selectionResult, err := tx.ExecContext(queryCtx, `
UPDATE config_documents
SET revision = ?, document = ?, updated_at_ms = GREATEST(updated_at_ms, ?)
WHERE namespace = ? AND document_key = ? AND revision = ?`, documentRevision+1, string(selectionJSON), s.nowUnixMS(), activeCharacterNamespace, activeCharacterKey, documentRevision)
		if err != nil {
			return Record{}, fmt.Errorf("advancing active character revision: %w", err)
		}
		if err := requireOneCharacterMutation(selectionResult); err != nil {
			return Record{}, err
		}
	}
	if err := s.runMutationHook("update"); err != nil {
		return Record{}, err
	}
	if err := tx.Commit(); err != nil {
		return Record{}, fmt.Errorf("committing character update: %w", err)
	}
	appearance, _ := decodeAppearance(root, characterID, row.appearanceRef)
	return recordFromSnapshot(snapshot, appearance), nil
}

func (s *seekDBCharacterStore) setAppearance(parent context.Context, root, characterID, visualPackID string) (Record, error) {
	if !validID(characterID) {
		return Record{}, ErrInvalidCharacterID
	}
	manifest, err := loadVisualManifest(root, visualPackID)
	if err != nil {
		return Record{}, err
	}
	queryCtx, cancel := s.queryContext(parent)
	defer cancel()
	tx, err := s.database.BeginTx(queryCtx, nil)
	if err != nil {
		return Record{}, fmt.Errorf("starting character appearance update: %w", err)
	}
	defer tx.Rollback()

	row, err := queryStoredCharacter(queryCtx, tx, characterID, true)
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, ErrCharacterNotFound
	}
	if err != nil {
		return Record{}, fmt.Errorf("locking SeekDB character appearance: %w", err)
	}
	bindingRevision := uint64(1)
	if current, ok := parseAppearanceRef(row.appearanceRef); ok {
		bindingRevision = current.BindingRevision + 1
	}
	appearanceRef, appearanceJSON, err := marshalAppearanceRef(visualPackID, bindingRevision)
	if err != nil {
		return Record{}, err
	}
	result, err := tx.ExecContext(queryCtx, `
UPDATE characters
SET appearance_ref = ?, updated_at_ms = GREATEST(updated_at_ms, ?)
WHERE character_id = ? AND revision = ?`, appearanceJSON, s.nowUnixMS(), characterID, row.revision)
	if err != nil {
		return Record{}, fmt.Errorf("updating SeekDB character appearance: %w", err)
	}
	if err := requireOneCharacterMutation(result); err != nil {
		return Record{}, err
	}
	if err := s.runMutationHook("set_appearance"); err != nil {
		return Record{}, err
	}
	if err := tx.Commit(); err != nil {
		return Record{}, fmt.Errorf("committing character appearance: %w", err)
	}
	record, diagnostic := decodeStoredCharacter(root, storedCharacter{
		characterID: characterID, revision: row.revision, name: row.name, snapshot: row.snapshot,
		appearanceRef: sql.NullString{String: appearanceJSON, Valid: true},
	})
	if diagnostic != nil {
		return Record{}, errors.New("stored character snapshot is invalid")
	}
	record.Appearance = Appearance{Status: "assigned", BindingRevision: appearanceRef.BindingRevision, Visual: &manifest}
	return record, nil
}

func (s *seekDBCharacterStore) activate(parent context.Context, root, characterID string, revision uint64) (Record, error) {
	if !validID(characterID) || revision == 0 {
		return Record{}, errors.New("character activation target is invalid")
	}
	queryCtx, cancel := s.queryContext(parent)
	defer cancel()
	tx, err := s.database.BeginTx(queryCtx, nil)
	if err != nil {
		return Record{}, fmt.Errorf("starting character activation: %w", err)
	}
	defer tx.Rollback()

	row, err := queryStoredCharacter(queryCtx, tx, characterID, true)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && row.revision != revision) {
		return Record{}, errors.New("character revision is not available")
	}
	if err != nil {
		return Record{}, fmt.Errorf("locking activation character: %w", err)
	}
	record, diagnostic := decodeStoredCharacter(root, row)
	if diagnostic != nil {
		return Record{}, errors.New("character revision is not available")
	}
	selectionJSON, err := json.Marshal(activeSelection{CharacterID: characterID, Revision: revision})
	if err != nil {
		return Record{}, fmt.Errorf("serializing active character selection: %w", err)
	}
	currentRevision, exists, err := loadActiveDocumentRevision(queryCtx, tx, true)
	if err != nil {
		return Record{}, err
	}
	now := s.nowUnixMS()
	if !exists {
		_, err = tx.ExecContext(queryCtx, `
INSERT INTO config_documents(namespace, document_key, schema_version, revision, document, created_at_ms, updated_at_ms)
VALUES (?, ?, ?, ?, ?, ?, ?)`, activeCharacterNamespace, activeCharacterKey, 1, 1, string(selectionJSON), now, now)
	} else {
		var result sql.Result
		result, err = tx.ExecContext(queryCtx, `
UPDATE config_documents
SET schema_version = ?, revision = ?, document = ?, updated_at_ms = GREATEST(updated_at_ms, ?)
WHERE namespace = ? AND document_key = ? AND revision = ?`, 1, currentRevision+1, string(selectionJSON), now, activeCharacterNamespace, activeCharacterKey, currentRevision)
		if err == nil {
			err = requireOneCharacterMutation(result)
		}
	}
	if err != nil {
		return Record{}, fmt.Errorf("saving active character selection: %w", err)
	}
	if err := s.runMutationHook("activate"); err != nil {
		return Record{}, err
	}
	if err := tx.Commit(); err != nil {
		return Record{}, fmt.Errorf("committing character activation: %w", err)
	}
	return record, nil
}

func (s *seekDBCharacterStore) delete(parent context.Context, characterID string) error {
	if !validID(characterID) {
		return ErrInvalidCharacterID
	}
	queryCtx, cancel := s.queryContext(parent)
	defer cancel()
	tx, err := s.database.BeginTx(queryCtx, nil)
	if err != nil {
		return fmt.Errorf("starting character deletion: %w", err)
	}
	defer tx.Rollback()

	var revision uint64
	if err := tx.QueryRowContext(queryCtx, "SELECT revision FROM characters WHERE character_id = ? FOR UPDATE", characterID).Scan(&revision); errors.Is(err, sql.ErrNoRows) {
		return ErrCharacterNotFound
	} else if err != nil {
		return fmt.Errorf("locking character for deletion: %w", err)
	}
	selection, selected, err := loadActiveSelection(queryCtx, tx, true)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(queryCtx, "DELETE FROM characters WHERE character_id = ? AND revision = ?", characterID, revision)
	if err != nil {
		return fmt.Errorf("deleting SeekDB character: %w", err)
	}
	if err := requireOneCharacterMutation(result); err != nil {
		return err
	}
	if selected && selection.CharacterID == characterID {
		if _, err := tx.ExecContext(queryCtx, "DELETE FROM config_documents WHERE namespace = ? AND document_key = ?", activeCharacterNamespace, activeCharacterKey); err != nil {
			return fmt.Errorf("clearing active character selection: %w", err)
		}
	}
	if err := s.runMutationHook("delete"); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing character deletion: %w", err)
	}
	return nil
}

func queryStoredCharacter(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, characterID string, forUpdate bool) (storedCharacter, error) {
	query := `
SELECT character_id, revision, name, snapshot, appearance_ref
FROM characters
WHERE character_id = ?`
	if forUpdate {
		query += " FOR UPDATE"
	}
	return scanStoredCharacter(queryer.QueryRowContext(ctx, query, characterID))
}

type rowScanner interface {
	Scan(...any) error
}

func scanStoredCharacter(scanner rowScanner) (storedCharacter, error) {
	var row storedCharacter
	err := scanner.Scan(&row.characterID, &row.revision, &row.name, &row.snapshot, &row.appearanceRef)
	return row, err
}

func decodeStoredCharacter(root string, row storedCharacter) (Record, *Diagnostic) {
	var snapshot characterSnapshot
	if err := json.Unmarshal(row.snapshot, &snapshot); err != nil ||
		!validID(row.characterID) || snapshot.CharacterID != row.characterID ||
		snapshot.Revision != row.revision || snapshot.Revision == 0 ||
		snapshot.Identity.Name == "" || snapshot.Identity.Name != row.name || snapshot.Identity.Description == "" {
		characterID := row.characterID
		revision := row.revision
		return Record{}, &Diagnostic{CharacterID: &characterID, Revision: &revision, Code: "STORAGE_CORRUPTED", Message: "角色 snapshot 已损坏，已从结果中隔离"}
	}
	appearance, diagnostic := decodeAppearance(root, row.characterID, row.appearanceRef)
	return recordFromSnapshot(snapshot, appearance), diagnostic
}

func recordFromSnapshot(snapshot characterSnapshot, appearance Appearance) Record {
	return Record{
		CharacterID: snapshot.CharacterID, Revision: snapshot.Revision,
		Name: snapshot.Identity.Name, Description: snapshot.Identity.Description,
		DialogueStyle:    snapshot.Identity.DialogueStyle,
		TextLanguage:     textLanguageOrDefault(snapshot.Identity.TextLanguage),
		SpeakingLanguage: speakingLanguageOrDefault(snapshot.Identity.SpeakingLanguage),
		Appearance:       appearance,
	}
}

func decodeAppearance(root, characterID string, raw sql.NullString) (Appearance, *Diagnostic) {
	if !raw.Valid {
		return Appearance{Status: "unassigned"}, nil
	}
	ref, ok := parseAppearanceRef(raw)
	if !ok {
		return unavailableAppearance(characterID)
	}
	manifest, err := LoadManifestFromFile(filepath.Join(root, "visual-packs", ref.VisualPackID, "manifest.json"))
	if err != nil {
		return unavailableAppearance(characterID)
	}
	return Appearance{Status: "assigned", BindingRevision: ref.BindingRevision, Visual: &manifest}, nil
}

func unavailableAppearance(characterID string) (Appearance, *Diagnostic) {
	return Appearance{Status: "unavailable"}, &Diagnostic{CharacterID: &characterID, Code: "CHARACTER_APPEARANCE_UNAVAILABLE", Message: "角色外观资源不可用"}
}

func loadVisualManifest(root, visualPackID string) (Manifest, error) {
	if err := validateVisualPackID(visualPackID); err != nil {
		return Manifest{}, err
	}
	return LoadManifestFromFile(filepath.Join(root, "visual-packs", visualPackID, "manifest.json"))
}

func marshalAppearanceRef(visualPackID string, bindingRevision uint64) (seekDBAppearanceRef, string, error) {
	ref := seekDBAppearanceRef{SchemaVersion: 1, VisualPackID: visualPackID, BindingRevision: bindingRevision}
	data, err := json.Marshal(ref)
	if err != nil {
		return seekDBAppearanceRef{}, "", fmt.Errorf("serializing character appearance reference: %w", err)
	}
	if len(data) > 255 {
		return seekDBAppearanceRef{}, "", errors.New("character appearance reference is too long")
	}
	return ref, string(data), nil
}

func parseAppearanceRef(raw sql.NullString) (seekDBAppearanceRef, bool) {
	if !raw.Valid || strings.TrimSpace(raw.String) != raw.String {
		return seekDBAppearanceRef{}, false
	}
	var ref seekDBAppearanceRef
	if err := json.Unmarshal([]byte(raw.String), &ref); err != nil || ref.SchemaVersion != 1 || ref.BindingRevision == 0 || validateVisualPackID(ref.VisualPackID) != nil {
		return seekDBAppearanceRef{}, false
	}
	return ref, true
}

func loadActiveSelection(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, forUpdate bool) (activeSelection, bool, error) {
	query := `
SELECT document
FROM config_documents
WHERE namespace = ? AND document_key = ?`
	if forUpdate {
		query += " FOR UPDATE"
	}
	var document []byte
	err := queryer.QueryRowContext(ctx, query, activeCharacterNamespace, activeCharacterKey).Scan(&document)
	if errors.Is(err, sql.ErrNoRows) {
		return activeSelection{}, false, nil
	}
	if err != nil {
		return activeSelection{}, false, fmt.Errorf("loading active character selection: %w", err)
	}
	var selection activeSelection
	if err := json.Unmarshal(document, &selection); err != nil || !validID(selection.CharacterID) || selection.Revision == 0 {
		return activeSelection{}, false, errors.New("active character selection is invalid")
	}
	return selection, true, nil
}

func loadActiveDocumentRevision(ctx context.Context, tx *sql.Tx, forUpdate bool) (uint64, bool, error) {
	query := `
SELECT revision
FROM config_documents
WHERE namespace = ? AND document_key = ?`
	if forUpdate {
		query += " FOR UPDATE"
	}
	var revision uint64
	err := tx.QueryRowContext(ctx, query, activeCharacterNamespace, activeCharacterKey).Scan(&revision)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("loading active character document revision: %w", err)
	}
	if revision == 0 {
		return 0, false, errors.New("active character document revision is invalid")
	}
	return revision, true, nil
}

func requireOneCharacterMutation(result sql.Result) error {
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("reading character mutation result: %w", err)
	}
	if rows != 1 {
		return ErrCharacterRevisionConflict
	}
	return nil
}

func (s *seekDBCharacterStore) queryContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(parent, s.queryLimit)
}

func (s *seekDBCharacterStore) nowUnixMS() int64 {
	now := s.now().UnixMilli()
	if now < 1 {
		return 1
	}
	return now
}

func (s *seekDBCharacterStore) runMutationHook(operation string) error {
	if s.mutationHook == nil {
		return nil
	}
	if err := s.mutationHook(operation); err != nil {
		return fmt.Errorf("character %s aborted before commit: %w", operation, err)
	}
	return nil
}
