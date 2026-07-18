package character

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"fairy/visual"
)

const pngSignature = "\x89PNG\r\n\x1a\n"

type packageManifest struct {
	SchemaVersion int           `json:"schemaVersion"`
	PackageID     string        `json:"packageId"`
	Character     Brief         `json:"character"`
	Visual        packageVisual `json:"visual"`
}

type packageVisual struct {
	DisplayName string         `json:"displayName"`
	Renderer    string         `json:"renderer"`
	Frame       visual.Frame   `json:"frame"`
	Scale       float64        `json:"scale"`
	Anchor      visual.Point   `json:"anchor"`
	States      []packageState `json:"states"`
}

type packageState struct {
	ID          string `json:"id"`
	Description string `json:"description"`
	File        string `json:"file"`
}

func (s *Store) ImportPackage(packagePath string) (Record, error) {
	if packagePath == "" {
		return Record{}, errors.New("character package path is required")
	}
	info, err := os.Stat(packagePath)
	if err != nil {
		return Record{}, fmt.Errorf("reading character package: %w", err)
	}
	var manifest packageManifest
	var files map[string][]byte
	if info.IsDir() {
		manifest, files, err = readDirectoryPackage(packagePath)
	} else {
		manifest, files, err = readArchivePackage(packagePath)
	}
	if err != nil {
		return Record{}, err
	}
	if err := validatePackageManifest(manifest); err != nil {
		return Record{}, err
	}
	if err := s.installVisualPackage(manifest, files); err != nil {
		return Record{}, err
	}
	return s.Create(manifest.Character, manifest.PackageID)
}

func (s *Store) ExportPackage(characterID string, outputPath string) error {
	if !validID(characterID) {
		return errors.New("character_id is invalid")
	}
	if filepath.Ext(outputPath) != ".pack" {
		return errors.New("character package export path must end with .pack")
	}
	record, ok, _, err := s.latestValid(characterID)
	if err != nil {
		return err
	}
	if !ok || record.Appearance.Status != "assigned" || record.Appearance.Visual == nil {
		return errors.New("character must have an assigned visual pack before export")
	}
	pack := record.Appearance.Visual
	states := make([]packageState, 0, len(pack.States))
	files := make(map[string]string)
	for _, state := range pack.States {
		relative, err := visualRelativePath(pack.PackID, state.ImagePath)
		if err != nil {
			return err
		}
		states = append(states, packageState{ID: state.ID, Description: state.Description, File: relative})
		files[relative] = filepath.Join(s.root, "visual-packs", pack.PackID, filepath.FromSlash(relative))
	}
	manifest := packageManifest{
		SchemaVersion: 1,
		PackageID:     pack.PackID,
		Character: Brief{
			Name:             record.Name,
			Description:      record.Description,
			DialogueStyle:    record.DialogueStyle,
			TextLanguage:     record.TextLanguage,
			SpeakingLanguage: record.SpeakingLanguage,
		},
		Visual: packageVisual{
			DisplayName: pack.DisplayName,
			Renderer:    pack.Renderer,
			Frame:       pack.Frame,
			Scale:       pack.Scale,
			Anchor:      pack.Anchor,
			States:      states,
		},
	}
	return writeArchive(outputPath, manifest, files)
}

func readDirectoryPackage(dir string) (packageManifest, map[string][]byte, error) {
	data, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return packageManifest{}, nil, fmt.Errorf("reading package manifest: %w", err)
	}
	manifest, err := parsePackageManifest(data)
	if err != nil {
		return packageManifest{}, nil, err
	}
	files := make(map[string][]byte, len(manifest.Visual.States))
	for _, state := range manifest.Visual.States {
		relative, err := validatePackageFile(state.File)
		if err != nil {
			return packageManifest{}, nil, err
		}
		bytes, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(relative)))
		if err != nil {
			return packageManifest{}, nil, fmt.Errorf("reading package image: %w", err)
		}
		if err := validatePNG(bytes); err != nil {
			return packageManifest{}, nil, err
		}
		files[relative] = bytes
	}
	return manifest, files, nil
}

func readArchivePackage(path string) (packageManifest, map[string][]byte, error) {
	reader, err := zip.OpenReader(path)
	if err != nil {
		return packageManifest{}, nil, fmt.Errorf("reading package archive: %w", err)
	}
	defer reader.Close()
	root := archiveRoot(reader.File)
	manifestBytes, err := archiveFileBytes(reader.File, root+"manifest.json")
	if err != nil {
		return packageManifest{}, nil, err
	}
	manifest, err := parsePackageManifest(manifestBytes)
	if err != nil {
		return packageManifest{}, nil, err
	}
	files := make(map[string][]byte, len(manifest.Visual.States))
	for _, state := range manifest.Visual.States {
		relative, err := validatePackageFile(state.File)
		if err != nil {
			return packageManifest{}, nil, err
		}
		bytes, err := archiveFileBytes(reader.File, root+relative)
		if err != nil {
			return packageManifest{}, nil, err
		}
		if err := validatePNG(bytes); err != nil {
			return packageManifest{}, nil, err
		}
		files[relative] = bytes
	}
	return manifest, files, nil
}

func parsePackageManifest(data []byte) (packageManifest, error) {
	var manifest packageManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return packageManifest{}, fmt.Errorf("parsing package manifest: %w", err)
	}
	return manifest, nil
}

func validatePackageManifest(manifest packageManifest) error {
	if manifest.SchemaVersion != 1 {
		return errors.New("unsupported package schema version")
	}
	if err := validateVisualPackID(manifest.PackageID); err != nil {
		return err
	}
	if manifest.Character.Name == "" || manifest.Character.Description == "" {
		return errors.New("package character brief is invalid")
	}
	if _, err := normalizeSpeakingLanguage(manifest.Character.SpeakingLanguage); err != nil {
		return err
	}
	if manifest.Visual.Renderer != "state_images" || len(manifest.Visual.States) == 0 {
		return errors.New("package visual manifest is invalid")
	}
	return nil
}

func (s *Store) installVisualPackage(manifest packageManifest, files map[string][]byte) error {
	runtime := visual.Manifest{
		SchemaVersion: 2,
		PackID:        manifest.PackageID,
		DisplayName:   manifest.Visual.DisplayName,
		Renderer:      "state_images",
		Frame:         manifest.Visual.Frame,
		Scale:         manifest.Visual.Scale,
		Anchor:        manifest.Visual.Anchor,
		States:        make([]visual.State, 0, len(manifest.Visual.States)),
	}
	for _, state := range manifest.Visual.States {
		relative, err := validatePackageFile(state.File)
		if err != nil {
			return err
		}
		runtime.States = append(runtime.States, visual.State{ID: state.ID, Description: state.Description, ImagePath: "fairy-character://localhost/" + manifest.PackageID + "/" + relative})
	}
	manifestBytes, err := json.MarshalIndent(runtime, "", "  ")
	if err != nil {
		return fmt.Errorf("serializing runtime visual manifest: %w", err)
	}
	if _, err := visual.ParseManifest(manifestBytes); err != nil {
		return err
	}
	staging := filepath.Join(s.root, "visual-packs", "."+manifest.PackageID+".importing."+fmt.Sprint(time.Now().UnixNano()))
	target := filepath.Join(s.root, "visual-packs", manifest.PackageID)
	if err := os.RemoveAll(staging); err != nil {
		return err
	}
	for relative, data := range files {
		path := filepath.Join(staging, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			return err
		}
	}
	if err := os.WriteFile(filepath.Join(staging, "manifest.json"), manifestBytes, 0o600); err != nil {
		return err
	}
	backup := target + ".backup"
	_ = os.RemoveAll(backup)
	if _, err := os.Stat(target); err == nil {
		if err := os.Rename(target, backup); err != nil {
			return err
		}
	}
	if err := os.Rename(staging, target); err != nil {
		_ = os.Rename(backup, target)
		return err
	}
	_ = os.RemoveAll(backup)
	return nil
}

func writeArchive(outputPath string, manifest packageManifest, files map[string]string) error {
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o700); err != nil && filepath.Dir(outputPath) != "." {
		return err
	}
	buffer := bytes.NewBuffer(nil)
	writer := zip.NewWriter(buffer)
	manifestBytes, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	manifestEntry, err := writer.Create("manifest.json")
	if err != nil {
		return err
	}
	if _, err := manifestEntry.Write(manifestBytes); err != nil {
		return err
	}
	for archiveName, sourcePath := range files {
		data, err := os.ReadFile(sourcePath)
		if err != nil {
			return err
		}
		if err := validatePNG(data); err != nil {
			return err
		}
		entry, err := writer.Create(archiveName)
		if err != nil {
			return err
		}
		if _, err := entry.Write(data); err != nil {
			return err
		}
	}
	if err := writer.Close(); err != nil {
		return err
	}
	return os.WriteFile(outputPath, buffer.Bytes(), 0o600)
}

func archiveRoot(files []*zip.File) string {
	for _, file := range files {
		if file.Name == "manifest.json" {
			return ""
		}
	}
	for _, file := range files {
		if strings.HasSuffix(file.Name, "/manifest.json") {
			parts := strings.Split(file.Name, "/")
			if len(parts) == 2 {
				return parts[0] + "/"
			}
		}
	}
	return ""
}

func archiveFileBytes(files []*zip.File, name string) ([]byte, error) {
	for _, file := range files {
		if file.Name != name || file.FileInfo().IsDir() {
			continue
		}
		reader, err := file.Open()
		if err != nil {
			return nil, err
		}
		defer reader.Close()
		return io.ReadAll(reader)
	}
	return nil, fmt.Errorf("archive entry %s not found", name)
}

func validatePackageFile(value string) (string, error) {
	if value == "" || !strings.HasSuffix(value, ".png") || strings.Contains(value, "://") || strings.ContainsAny(value, "\\?#") {
		return "", errors.New("package image path is invalid")
	}
	clean := filepath.Clean(value)
	if clean != value || strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
		return "", errors.New("package image path escapes package root")
	}
	return filepath.ToSlash(value), nil
}

func validatePNG(data []byte) error {
	if len(data) < len(pngSignature) || string(data[:len(pngSignature)]) != pngSignature {
		return errors.New("package image must be PNG")
	}
	return nil
}

func visualRelativePath(packID string, imagePath string) (string, error) {
	prefix := "fairy-character://localhost/" + packID + "/"
	relative, ok := strings.CutPrefix(imagePath, prefix)
	if !ok {
		return "", errors.New("visual asset is not local to pack")
	}
	return validatePackageFile(relative)
}
