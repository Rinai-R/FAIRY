package visual

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path"
	"strings"
)

const (
	schemaVersion = 2
	renderer      = "state_images"
)

type Manifest struct {
	SchemaVersion int     `json:"schemaVersion"`
	PackID        string  `json:"packId"`
	DisplayName   string  `json:"displayName"`
	Renderer      string  `json:"renderer"`
	Frame         Frame   `json:"frame"`
	Scale         float64 `json:"scale"`
	Anchor        Point   `json:"anchor"`
	States        []State `json:"states"`
}

type Frame struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

type Point struct {
	X int `json:"x"`
	Y int `json:"y"`
}

type State struct {
	ID          string `json:"id"`
	Description string `json:"description"`
	ImagePath   string `json:"imagePath"`
}

func LoadManifestFromFile(filename string) (Manifest, error) {
	if filename == "" {
		return Manifest{}, errors.New("manifest path is required")
	}
	data, err := os.ReadFile(filename)
	if err != nil {
		return Manifest{}, fmt.Errorf("reading visual manifest %s: %w", filename, err)
	}
	return ParseManifest(data)
}

func ParseManifest(data []byte) (Manifest, error) {
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return Manifest{}, fmt.Errorf("parsing visual manifest: %w", err)
	}
	if err := validateManifest(manifest); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func validateManifest(manifest Manifest) error {
	if manifest.SchemaVersion != schemaVersion {
		return fmt.Errorf("visual manifest schemaVersion = %d, want %d", manifest.SchemaVersion, schemaVersion)
	}
	if manifest.PackID == "" {
		return errors.New("visual manifest packId is required")
	}
	if manifest.Renderer != renderer {
		return fmt.Errorf("visual manifest renderer = %q, want %q", manifest.Renderer, renderer)
	}
	if manifest.Frame.Width <= 0 || manifest.Frame.Height <= 0 {
		return errors.New("visual manifest frame width and height must be positive")
	}
	if manifest.Scale <= 0 {
		return errors.New("visual manifest scale must be positive")
	}
	if len(manifest.States) == 0 {
		return errors.New("visual manifest must declare at least idle state")
	}
	if len(manifest.States) > 16 {
		return errors.New("visual manifest declares more than 16 states")
	}

	stateIDs := make(map[string]struct{}, len(manifest.States))
	imagePaths := make(map[string]struct{}, len(manifest.States))
	hasIdle := false
	for _, state := range manifest.States {
		if state.ID == "" {
			return errors.New("visual state id is required")
		}
		if _, exists := stateIDs[state.ID]; exists {
			return fmt.Errorf("visual state %q is duplicated", state.ID)
		}
		stateIDs[state.ID] = struct{}{}
		if state.ID == "idle" {
			hasIdle = true
		}
		if err := ValidateImageURI(manifest.PackID, state.ImagePath); err != nil {
			return fmt.Errorf("visual state %q image path invalid: %w", state.ID, err)
		}
		if _, exists := imagePaths[state.ImagePath]; exists {
			return fmt.Errorf("visual image path %q is duplicated", state.ImagePath)
		}
		imagePaths[state.ImagePath] = struct{}{}
	}
	if !hasIdle {
		return errors.New("visual manifest must declare idle state")
	}
	return nil
}

func ValidateImageURI(packID string, raw string) error {
	if raw == "" {
		return errors.New("imagePath is required")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("parse URI: %w", err)
	}
	if parsed.Scheme != "fairy-character" {
		return fmt.Errorf("scheme = %q, want fairy-character", parsed.Scheme)
	}
	if parsed.Host != "localhost" {
		return fmt.Errorf("host = %q, want localhost", parsed.Host)
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("query and fragment are not allowed")
	}
	clean := path.Clean(parsed.Path)
	if clean != parsed.Path {
		return errors.New("path must already be clean")
	}
	if strings.Contains(clean, "..") {
		return errors.New("path traversal is not allowed")
	}
	prefix := "/" + packID + "/"
	if !strings.HasPrefix(clean, prefix) {
		return fmt.Errorf("path must start with %s", prefix)
	}
	if !strings.HasSuffix(clean, ".png") {
		return errors.New("image path must point to a PNG")
	}
	return nil
}

func (m Manifest) State(id string) (State, bool) {
	for _, state := range m.States {
		if state.ID == id {
			return state, true
		}
	}
	return State{}, false
}
