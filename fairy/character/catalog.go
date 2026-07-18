package character

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"fairy/visual"
)

type Catalog struct {
	Characters  []Record     `json:"characters"`
	Active      *Record      `json:"active"`
	Diagnostics []Diagnostic `json:"diagnostics"`
}

type Record struct {
	CharacterID      string     `json:"characterId"`
	Revision         uint64     `json:"revision"`
	Name             string     `json:"name"`
	Description      string     `json:"description"`
	DialogueStyle    *string    `json:"dialogueStyle"`
	TextLanguage     string     `json:"textLanguage"`
	SpeakingLanguage string     `json:"speakingLanguage"`
	Appearance       Appearance `json:"appearance"`
}

type Appearance struct {
	Status          string           `json:"status"`
	BindingRevision uint64           `json:"bindingRevision,omitempty"`
	Visual          *visual.Manifest `json:"visual,omitempty"`
}

type Diagnostic struct {
	CharacterID *string `json:"characterId"`
	Revision    *uint64 `json:"revision"`
	Code        string  `json:"code"`
	Message     string  `json:"message"`
}

type Store struct {
	root string
}

type Brief struct {
	Name             string  `json:"name"`
	Description      string  `json:"description"`
	DialogueStyle    *string `json:"dialogueStyle,omitempty"`
	TextLanguage     string  `json:"textLanguage"`
	SpeakingLanguage string  `json:"speakingLanguage"`
}

const DefaultSpeakingLanguage = "ja"
const DefaultTextLanguage = "zh"

func NewStore(root string) *Store {
	return &Store{root: root}
}

func (s *Store) List() (Catalog, error) {
	if s == nil || s.root == "" {
		return Catalog{}, errors.New("config root is required")
	}
	charactersDir := filepath.Join(s.root, "characters")
	entries, err := os.ReadDir(charactersDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Catalog{Characters: []Record{}, Diagnostics: []Diagnostic{}}, nil
		}
		return Catalog{}, fmt.Errorf("reading character directory: %w", err)
	}
	characters := make([]Record, 0, len(entries))
	diagnostics := make([]Diagnostic, 0)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		characterID := entry.Name()
		if !validID(characterID) {
			diagnostics = append(diagnostics, Diagnostic{Code: "STORAGE_CORRUPTED", Message: "角色目录名称无效"})
			continue
		}
		record, ok, recordDiagnostics, err := s.latestValid(characterID)
		if err != nil {
			return Catalog{}, err
		}
		diagnostics = append(diagnostics, recordDiagnostics...)
		if ok {
			characters = append(characters, record)
		}
	}
	sort.Slice(characters, func(i, j int) bool { return characters[i].CharacterID < characters[j].CharacterID })
	active, err := s.active(characters)
	if err != nil {
		return Catalog{}, err
	}
	return Catalog{Characters: characters, Active: active, Diagnostics: diagnostics}, nil
}

func (s *Store) Create(brief Brief, visualPackID string) (Record, error) {
	if err := validateVisualPackID(visualPackID); err != nil {
		return Record{}, err
	}
	characterID := newID()
	if err := s.writeAppearance(characterID, visualPackID); err != nil {
		return Record{}, err
	}
	snapshot, err := compileSnapshot(characterID, 1, brief)
	if err != nil {
		return Record{}, err
	}
	if err := s.writeSnapshot(snapshot); err != nil {
		_ = os.Remove(filepath.Join(s.root, "character-appearances", characterID+".json"))
		return Record{}, err
	}
	record, _, _, err := s.latestValid(characterID)
	return record, err
}

func (s *Store) Update(characterID string, brief Brief) (Record, error) {
	if !validID(characterID) {
		return Record{}, errors.New("character_id is invalid")
	}
	latest, err := s.latestRevision(characterID)
	if err != nil {
		return Record{}, err
	}
	snapshot, err := compileSnapshot(characterID, latest+1, brief)
	if err != nil {
		return Record{}, err
	}
	if err := s.writeSnapshot(snapshot); err != nil {
		return Record{}, err
	}
	record, _, _, err := s.latestValid(characterID)
	return record, err
}

func (s *Store) SetAppearance(characterID string, visualPackID string) (Record, error) {
	if !validID(characterID) {
		return Record{}, errors.New("character_id is invalid")
	}
	if err := validateVisualPackID(visualPackID); err != nil {
		return Record{}, err
	}
	if _, err := visual.LoadManifestFromFile(filepath.Join(s.root, "visual-packs", visualPackID, "manifest.json")); err != nil {
		return Record{}, err
	}
	if _, ok, _, err := s.latestValid(characterID); err != nil || !ok {
		if err != nil {
			return Record{}, err
		}
		return Record{}, errors.New("character is not available")
	}
	if err := s.writeAppearance(characterID, visualPackID); err != nil {
		return Record{}, err
	}
	record, _, _, err := s.latestValid(characterID)
	return record, err
}

func (s *Store) Activate(characterID string, revision uint64) (Record, error) {
	if !validID(characterID) || revision == 0 {
		return Record{}, errors.New("character activation target is invalid")
	}
	record, ok, _, err := s.latestValid(characterID)
	if err != nil {
		return Record{}, err
	}
	if !ok || record.Revision != revision {
		return Record{}, errors.New("character revision is not available")
	}
	document := struct {
		SchemaVersion uint32 `json:"schema_version"`
		Data          struct {
			CharacterID string `json:"character_id"`
			Revision    uint64 `json:"revision"`
		} `json:"data"`
	}{SchemaVersion: 1}
	document.Data.CharacterID = characterID
	document.Data.Revision = revision
	if err := writeJSON(filepath.Join(s.root, "active-character.json"), document); err != nil {
		return Record{}, err
	}
	return record, nil
}

func (s *Store) latestValid(characterID string) (Record, bool, []Diagnostic, error) {
	revisionsDir := filepath.Join(s.root, "characters", characterID, "revisions")
	entries, err := os.ReadDir(revisionsDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Record{}, false, nil, nil
		}
		return Record{}, false, nil, fmt.Errorf("reading character revisions: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() > entries[j].Name() })
	diagnostics := make([]Diagnostic, 0)
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		revision, ok := parseRevisionFile(entry.Name())
		if !ok {
			diagnostics = append(diagnostics, Diagnostic{CharacterID: &characterID, Code: "STORAGE_CORRUPTED", Message: "角色 revision 文件名无效"})
			continue
		}
		snapshot, err := readCharacterSnapshot(filepath.Join(revisionsDir, entry.Name()))
		if err != nil || snapshot.CharacterID != characterID || snapshot.Revision != revision {
			rev := revision
			diagnostics = append(diagnostics, Diagnostic{CharacterID: &characterID, Revision: &rev, Code: "STORAGE_CORRUPTED", Message: "角色 revision 已损坏，已从列表结果中隔离"})
			continue
		}
		appearance, appearanceDiagnostic := s.appearance(characterID)
		if appearanceDiagnostic != nil {
			diagnostics = append(diagnostics, *appearanceDiagnostic)
		}
		return Record{
			CharacterID:      snapshot.CharacterID,
			Revision:         snapshot.Revision,
			Name:             snapshot.Identity.Name,
			Description:      snapshot.Identity.Description,
			DialogueStyle:    snapshot.Identity.DialogueStyle,
			TextLanguage:     textLanguageOrDefault(snapshot.Identity.TextLanguage),
			SpeakingLanguage: speakingLanguageOrDefault(snapshot.Identity.SpeakingLanguage),
			Appearance:       appearance,
		}, true, diagnostics, nil
	}
	return Record{}, false, diagnostics, nil
}

func (s *Store) active(characters []Record) (*Record, error) {
	var document struct {
		SchemaVersion uint32 `json:"schema_version"`
		Data          struct {
			CharacterID string `json:"character_id"`
			Revision    uint64 `json:"revision"`
		} `json:"data"`
	}
	path := filepath.Join(s.root, "active-character.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading active character: %w", err)
	}
	if err := json.Unmarshal(data, &document); err != nil {
		return nil, fmt.Errorf("parsing active character: %w", err)
	}
	for _, record := range characters {
		if record.CharacterID == document.Data.CharacterID && record.Revision == document.Data.Revision {
			active := record
			return &active, nil
		}
	}
	return nil, nil
}

func (s *Store) appearance(characterID string) (Appearance, *Diagnostic) {
	path := filepath.Join(s.root, "character-appearances", characterID+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Appearance{Status: "unassigned"}, nil
		}
		return Appearance{Status: "unavailable"}, &Diagnostic{CharacterID: &characterID, Code: "CHARACTER_APPEARANCE_UNAVAILABLE", Message: "角色外观绑定已损坏或不可用"}
	}
	var document struct {
		SchemaVersion uint32 `json:"schema_version"`
		Data          struct {
			CharacterID  string `json:"character_id"`
			Revision     uint64 `json:"revision"`
			VisualPackID string `json:"visual_pack_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &document); err != nil || document.Data.CharacterID != characterID || document.Data.VisualPackID == "" {
		return Appearance{Status: "unavailable"}, &Diagnostic{CharacterID: &characterID, Code: "CHARACTER_APPEARANCE_UNAVAILABLE", Message: "角色外观绑定已损坏或不可用"}
	}
	manifest, err := visual.LoadManifestFromFile(filepath.Join(s.root, "visual-packs", document.Data.VisualPackID, "manifest.json"))
	if err != nil {
		return Appearance{Status: "unavailable"}, &Diagnostic{CharacterID: &characterID, Code: "CHARACTER_APPEARANCE_UNAVAILABLE", Message: "角色外观资源不可用"}
	}
	return Appearance{Status: "assigned", BindingRevision: document.Data.Revision, Visual: &manifest}, nil
}

type characterSnapshot struct {
	SchemaVersion       uint32            `json:"schema_version"`
	CompilerVersion     string            `json:"compiler_version"`
	CharacterID         string            `json:"character_id"`
	Revision            uint64            `json:"revision"`
	Identity            characterIdentity `json:"identity"`
	Worldview           string            `json:"worldview"`
	AttentionBiases     []string          `json:"attention_biases"`
	RelationshipStance  string            `json:"relationship_stance"`
	ResponseDrives      []string          `json:"response_drives"`
	EmotionalTendencies []string          `json:"emotional_tendencies"`
	SpeechStyle         speechStyle       `json:"speech_style"`
	HardBoundaries      []string          `json:"hard_boundaries"`
	Fingerprint         string            `json:"fingerprint"`
}

type characterIdentity struct {
	Name             string  `json:"name"`
	Description      string  `json:"description"`
	DialogueStyle    *string `json:"dialogueStyle,omitempty"`
	TextLanguage     string  `json:"textLanguage,omitempty"`
	SpeakingLanguage string  `json:"speakingLanguage,omitempty"`
}

type speechStyle struct {
	CharacterDescriptionGuidance string `json:"character_description_guidance"`
	Fallback                     string `json:"fallback"`
}

type canonicalCharacterSnapshot struct {
	SchemaVersion       uint32            `json:"schema_version"`
	CompilerVersion     string            `json:"compiler_version"`
	CharacterID         string            `json:"character_id"`
	Revision            uint64            `json:"revision"`
	Identity            characterIdentity `json:"identity"`
	Worldview           string            `json:"worldview"`
	AttentionBiases     []string          `json:"attention_biases"`
	RelationshipStance  string            `json:"relationship_stance"`
	ResponseDrives      []string          `json:"response_drives"`
	EmotionalTendencies []string          `json:"emotional_tendencies"`
	SpeechStyle         speechStyle       `json:"speech_style"`
	HardBoundaries      []string          `json:"hard_boundaries"`
}

func readCharacterSnapshot(path string) (characterSnapshot, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return characterSnapshot{}, err
	}
	var document struct {
		SchemaVersion uint32            `json:"schema_version"`
		Data          characterSnapshot `json:"data"`
	}
	if err := json.Unmarshal(data, &document); err != nil {
		return characterSnapshot{}, err
	}
	if document.SchemaVersion != 1 || document.Data.CharacterID == "" || document.Data.Revision == 0 || document.Data.Identity.Name == "" || document.Data.Identity.Description == "" {
		return characterSnapshot{}, errors.New("invalid character snapshot")
	}
	return document.Data, nil
}

func parseRevisionFile(name string) (uint64, bool) {
	stem := strings.TrimSuffix(name, filepath.Ext(name))
	var revision uint64
	if _, err := fmt.Sscanf(stem, "%d", &revision); err != nil || revision == 0 {
		return 0, false
	}
	return revision, true
}

func validID(value string) bool {
	return value != "" && strings.TrimSpace(value) == value && !strings.ContainsAny(value, "/\\\x00")
}

func (s *Store) latestRevision(characterID string) (uint64, error) {
	revisionsDir := filepath.Join(s.root, "characters", characterID, "revisions")
	entries, err := os.ReadDir(revisionsDir)
	if err != nil {
		return 0, fmt.Errorf("reading character revisions: %w", err)
	}
	var latest uint64
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		revision, ok := parseRevisionFile(entry.Name())
		if ok && revision > latest {
			latest = revision
		}
	}
	if latest == 0 {
		return 0, errors.New("character revision is not available")
	}
	return latest, nil
}

func compileSnapshot(characterID string, revision uint64, brief Brief) (characterSnapshot, error) {
	name, err := normalizeRequiredText(brief.Name, 48, "character name")
	if err != nil {
		return characterSnapshot{}, err
	}
	description, err := normalizeDescription(brief.Description)
	if err != nil {
		return characterSnapshot{}, err
	}
	dialogueStyle, err := normalizeOptionalText(brief.DialogueStyle, 1200, "character dialogue style")
	if err != nil {
		return characterSnapshot{}, err
	}
	speakingLanguage, err := normalizeSpeakingLanguage(brief.SpeakingLanguage)
	if err != nil {
		return characterSnapshot{}, err
	}
	textLanguage, err := normalizeTextLanguage(brief.TextLanguage)
	if err != nil {
		return characterSnapshot{}, err
	}
	canonical := canonicalCharacterSnapshot{
		SchemaVersion:       1,
		CompilerVersion:     "fairy-character-v1",
		CharacterID:         characterID,
		Revision:            revision,
		Identity:            characterIdentity{Name: name, Description: description, DialogueStyle: dialogueStyle, TextLanguage: textLanguage, SpeakingLanguage: speakingLanguage},
		Worldview:           "not_specified",
		AttentionBiases:     []string{"user_explicit_content", "interaction_goal_signals", "evidence_before_inference"},
		RelationshipStance:  "warm_respectful_non_possessive",
		ResponseDrives:      []string{"understand_before_assuming", "support_explicit_goal"},
		EmotionalTendencies: []string{"calm_attunement"},
		SpeechStyle:         speechStyle{CharacterDescriptionGuidance: description, Fallback: "natural_concise"},
		HardBoundaries:      []string{"preserve_facts", "preserve_safety", "preserve_privacy", "preserve_relationship_boundaries"},
	}
	fingerprint, err := fingerprint(canonical)
	if err != nil {
		return characterSnapshot{}, err
	}
	return characterSnapshot{
		SchemaVersion:       canonical.SchemaVersion,
		CompilerVersion:     canonical.CompilerVersion,
		CharacterID:         canonical.CharacterID,
		Revision:            canonical.Revision,
		Identity:            canonical.Identity,
		Worldview:           canonical.Worldview,
		AttentionBiases:     canonical.AttentionBiases,
		RelationshipStance:  canonical.RelationshipStance,
		ResponseDrives:      canonical.ResponseDrives,
		EmotionalTendencies: canonical.EmotionalTendencies,
		SpeechStyle:         canonical.SpeechStyle,
		HardBoundaries:      canonical.HardBoundaries,
		Fingerprint:         fingerprint,
	}, nil
}

func (s *Store) writeSnapshot(snapshot characterSnapshot) error {
	document := struct {
		SchemaVersion uint32            `json:"schema_version"`
		Data          characterSnapshot `json:"data"`
	}{SchemaVersion: 1, Data: snapshot}
	return writeJSON(filepath.Join(s.root, "characters", snapshot.CharacterID, "revisions", fmt.Sprintf("%d.json", snapshot.Revision)), document)
}

func (s *Store) writeAppearance(characterID string, visualPackID string) error {
	if _, err := visual.LoadManifestFromFile(filepath.Join(s.root, "visual-packs", visualPackID, "manifest.json")); err != nil {
		return err
	}
	currentRevision := uint64(0)
	if appearance, diagnostic := s.appearance(characterID); diagnostic == nil && appearance.Status == "assigned" {
		currentRevision = appearance.BindingRevision
	}
	document := struct {
		SchemaVersion uint32 `json:"schema_version"`
		Data          struct {
			CharacterID  string `json:"character_id"`
			Revision     uint64 `json:"revision"`
			VisualPackID string `json:"visual_pack_id"`
		} `json:"data"`
	}{SchemaVersion: 1}
	document.Data.CharacterID = characterID
	document.Data.Revision = currentRevision + 1
	document.Data.VisualPackID = visualPackID
	return writeJSON(filepath.Join(s.root, "character-appearances", characterID+".json"), document)
}

func writeJSON(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("serializing character document: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("creating character directory: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("writing character document: %w", err)
	}
	return nil
}

func normalizeRequiredText(raw string, max int, label string) (string, error) {
	value := strings.TrimSpace(strings.ReplaceAll(raw, "\r\n", "\n"))
	if value == "" || len([]rune(value)) > max || strings.Contains(value, "\x00") {
		return "", fmt.Errorf("%s is invalid", label)
	}
	return value, nil
}

func normalizeDescription(raw string) (string, error) {
	value := strings.TrimSpace(strings.ReplaceAll(raw, "\r\n", "\n"))
	if value == "" || len([]rune(value)) > 2000 || strings.Contains(value, "\x00") {
		return "", errors.New("character description is invalid")
	}
	return value, nil
}

func normalizeOptionalText(raw *string, max int, label string) (*string, error) {
	if raw == nil {
		return nil, nil
	}
	value := strings.TrimSpace(strings.ReplaceAll(*raw, "\r\n", "\n"))
	if value == "" {
		return nil, nil
	}
	if len([]rune(value)) > max || strings.Contains(value, "\x00") {
		return nil, fmt.Errorf("%s is invalid", label)
	}
	return &value, nil
}

func normalizeSpeakingLanguage(raw string) (string, error) {
	value := speakingLanguageOrDefault(strings.TrimSpace(raw))
	switch value {
	case "ja", "zh", "en":
		return value, nil
	default:
		return "", errors.New("character speaking language is invalid")
	}
}

func speakingLanguageOrDefault(value string) string {
	if value == "" {
		return DefaultSpeakingLanguage
	}
	return value
}

func normalizeTextLanguage(raw string) (string, error) {
	value := textLanguageOrDefault(strings.TrimSpace(raw))
	switch value {
	case "ja", "zh", "en":
		return value, nil
	default:
		return "", errors.New("character text language is invalid")
	}
}

func textLanguageOrDefault(value string) string {
	if value == "" {
		return DefaultTextLanguage
	}
	return value
}

func validateVisualPackID(value string) error {
	if value == "" || strings.TrimSpace(value) != value || strings.ContainsAny(value, "/\\\x00") {
		return errors.New("visual_pack_id is invalid")
	}
	return nil
}

func fingerprint(value canonicalCharacterSnapshot) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("serializing canonical character snapshot: %w", err)
	}
	digest := sha256.Sum256(data)
	return fmt.Sprintf("%x", digest[:]), nil
}

func newID() string {
	var data [16]byte
	if _, err := rand.Read(data[:]); err != nil {
		panic(err)
	}
	data[6] = (data[6] & 0x0f) | 0x40
	data[8] = (data[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", data[0:4], data[4:6], data[6:8], data[8:10], data[10:16])
}
