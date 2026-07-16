package profile

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	currentPath = "user-profile/current.json"
)

type Snapshot struct {
	Revision      uint64  `json:"revision"`
	PreferredName *string `json:"preferredName"`
}

type Update struct {
	Profile             *Snapshot `json:"profile"`
	Changed             bool      `json:"changed"`
	RecoveredCorruption bool      `json:"recoveredCorruption"`
}

type Store struct {
	root string
}

func NewStore(root string) *Store {
	return &Store{root: root}
}

func (s *Store) Current() (*Snapshot, error) {
	if s == nil || s.root == "" {
		return nil, errors.New("config root is required")
	}
	revision, err := s.currentRevision()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	snapshot, err := s.readRevision(revision)
	if err != nil {
		return nil, err
	}
	return &snapshot, nil
}

func (s *Store) SetPreferredName(raw *string) (Update, error) {
	name, err := normalizePreferredName(raw)
	if err != nil {
		return Update{}, err
	}
	current, currentErr := s.Current()
	recovered := currentErr != nil
	if currentErr != nil {
		current = nil
	}
	if current != nil && equalOptionalString(current.PreferredName, name) {
		return Update{Profile: current, Changed: false, RecoveredCorruption: false}, nil
	}
	if current == nil && name == nil && !recovered {
		return Update{Profile: nil, Changed: false, RecoveredCorruption: false}, nil
	}
	next, err := s.nextRevision()
	if err != nil {
		return Update{}, err
	}
	snapshot := Snapshot{Revision: next, PreferredName: name}
	if err := s.writeSnapshot(snapshot); err != nil {
		return Update{}, err
	}
	return Update{Profile: &snapshot, Changed: true, RecoveredCorruption: recovered}, nil
}

func (s *Store) Clear() (Update, error) {
	return s.SetPreferredName(nil)
}

func (s *Store) currentRevision() (uint64, error) {
	var document struct {
		SchemaVersion uint32 `json:"schema_version"`
		Data          struct {
			Revision uint64 `json:"revision"`
		} `json:"data"`
	}
	data, err := os.ReadFile(filepath.Join(s.root, currentPath))
	if err != nil {
		return 0, err
	}
	if err := json.Unmarshal(data, &document); err != nil || document.SchemaVersion != 1 || document.Data.Revision == 0 {
		return 0, errors.New("user profile pointer is unavailable")
	}
	return document.Data.Revision, nil
}

func (s *Store) readRevision(revision uint64) (Snapshot, error) {
	var document struct {
		SchemaVersion uint32 `json:"schema_version"`
		Data          struct {
			SchemaVersion uint32  `json:"schema_version"`
			Revision      uint64  `json:"revision"`
			PreferredName *string `json:"preferred_name"`
		} `json:"data"`
	}
	data, err := os.ReadFile(filepath.Join(s.root, "user-profile", "revisions", fmt.Sprintf("%d.json", revision)))
	if err != nil {
		return Snapshot{}, err
	}
	if err := json.Unmarshal(data, &document); err != nil || document.SchemaVersion != 1 || document.Data.SchemaVersion != 1 || document.Data.Revision != revision {
		return Snapshot{}, errors.New("user profile revision is unavailable")
	}
	name, err := normalizePreferredName(document.Data.PreferredName)
	if err != nil {
		return Snapshot{}, err
	}
	return Snapshot{Revision: revision, PreferredName: name}, nil
}

func (s *Store) nextRevision() (uint64, error) {
	entries, err := os.ReadDir(filepath.Join(s.root, "user-profile", "revisions"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return 0, fmt.Errorf("reading user profile revisions: %w", err)
	}
	var maxRevision uint64
	for _, entry := range entries {
		var revision uint64
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		if _, err := fmt.Sscanf(strings.TrimSuffix(entry.Name(), ".json"), "%d", &revision); err == nil && revision > maxRevision {
			maxRevision = revision
		}
	}
	return maxRevision + 1, nil
}

func (s *Store) writeSnapshot(snapshot Snapshot) error {
	revisionDoc := struct {
		SchemaVersion uint32 `json:"schema_version"`
		Data          struct {
			SchemaVersion uint32  `json:"schema_version"`
			Revision      uint64  `json:"revision"`
			PreferredName *string `json:"preferred_name"`
		} `json:"data"`
	}{SchemaVersion: 1}
	revisionDoc.Data.SchemaVersion = 1
	revisionDoc.Data.Revision = snapshot.Revision
	revisionDoc.Data.PreferredName = snapshot.PreferredName
	if err := writeJSON(filepath.Join(s.root, "user-profile", "revisions", fmt.Sprintf("%d.json", snapshot.Revision)), revisionDoc); err != nil {
		return err
	}
	pointer := struct {
		SchemaVersion uint32 `json:"schema_version"`
		Data          struct {
			Revision uint64 `json:"revision"`
		} `json:"data"`
	}{SchemaVersion: 1}
	pointer.Data.Revision = snapshot.Revision
	return writeJSON(filepath.Join(s.root, currentPath), pointer)
}

func writeJSON(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("serializing user profile: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("creating user profile directory: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("writing user profile: %w", err)
	}
	return nil
}

func normalizePreferredName(raw *string) (*string, error) {
	if raw == nil {
		return nil, nil
	}
	value := strings.TrimSpace(*raw)
	if value == "" {
		return nil, nil
	}
	if len([]rune(value)) > 64 || strings.ContainsAny(value, "\r\n\x00") {
		return nil, errors.New("preferred name must be a single-line Unicode text up to 64 characters")
	}
	return &value, nil
}

func equalOptionalString(left *string, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}
