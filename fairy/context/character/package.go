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
)

const pngSignature = "\x89PNG\r\n\x1a\n"

const (
	maxPackageManifestBytes = 256 << 10
	maxPackageImageBytes    = 8 << 20
	maxPackageImagesBytes   = 64 << 20
	maxPackageStates        = 16
)

var ErrPackageCapacity = errors.New("character package capacity exceeded")

type packageReadLimits struct {
	manifestBytes int64
	imageBytes    int64
	imagesBytes   int64
}

var defaultPackageReadLimits = packageReadLimits{
	manifestBytes: maxPackageManifestBytes,
	imageBytes:    maxPackageImageBytes,
	imagesBytes:   maxPackageImagesBytes,
}

type packageManifest struct {
	SchemaVersion int           `json:"schemaVersion"`
	PackageID     string        `json:"packageId"`
	Character     Brief         `json:"character"`
	Visual        packageVisual `json:"visual"`
}

type packageVisual struct {
	DisplayName string         `json:"displayName"`
	Renderer    string         `json:"renderer"`
	Frame       Frame          `json:"frame"`
	Scale       float64        `json:"scale"`
	Anchor      Point          `json:"anchor"`
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
	return readDirectoryPackageWithLimits(dir, defaultPackageReadLimits)
}

func readDirectoryPackageWithLimits(dir string, limits packageReadLimits) (packageManifest, map[string][]byte, error) {
	data, exceeded, err := fileBytesWithin(filepath.Join(dir, "manifest.json"), limits.manifestBytes)
	if err != nil {
		return packageManifest{}, nil, fmt.Errorf("reading package manifest: %w", err)
	}
	if exceeded {
		return packageManifest{}, nil, packageCapacityError("manifest", limits.manifestBytes)
	}
	manifest, err := parsePackageManifest(data)
	if err != nil {
		return packageManifest{}, nil, err
	}
	if err := validatePackageManifest(manifest); err != nil {
		return packageManifest{}, nil, err
	}
	files, err := readPackageImages(manifest, limits, func(relative string, limit int64) ([]byte, bool, error) {
		return fileBytesWithin(filepath.Join(dir, filepath.FromSlash(relative)), limit)
	})
	if err != nil {
		return packageManifest{}, nil, err
	}
	return manifest, files, nil
}

func readArchivePackage(path string) (packageManifest, map[string][]byte, error) {
	return readArchivePackageWithLimits(path, defaultPackageReadLimits)
}

func readArchivePackageWithLimits(path string, limits packageReadLimits) (packageManifest, map[string][]byte, error) {
	reader, err := zip.OpenReader(path)
	if err != nil {
		return packageManifest{}, nil, fmt.Errorf("reading package archive: %w", err)
	}
	defer reader.Close()
	root := archiveRoot(reader.File)
	manifestBytes, exceeded, err := archiveFileBytesWithin(reader.File, root+"manifest.json", limits.manifestBytes)
	if err != nil {
		return packageManifest{}, nil, err
	}
	if exceeded {
		return packageManifest{}, nil, packageCapacityError("manifest", limits.manifestBytes)
	}
	manifest, err := parsePackageManifest(manifestBytes)
	if err != nil {
		return packageManifest{}, nil, err
	}
	if err := validatePackageManifest(manifest); err != nil {
		return packageManifest{}, nil, err
	}
	files, err := readPackageImages(manifest, limits, func(relative string, limit int64) ([]byte, bool, error) {
		return archiveFileBytesWithin(reader.File, root+relative, limit)
	})
	if err != nil {
		return packageManifest{}, nil, err
	}
	return manifest, files, nil
}

type packageImageReader func(relative string, limit int64) ([]byte, bool, error)

func readPackageImages(manifest packageManifest, limits packageReadLimits, read packageImageReader) (map[string][]byte, error) {
	if limits.imageBytes < 1 || limits.imagesBytes < 1 {
		return nil, packageCapacityError("image", 0)
	}
	files := make(map[string][]byte, len(manifest.Visual.States))
	var total int64
	for _, state := range manifest.Visual.States {
		relative, err := validatePackageFile(state.File)
		if err != nil {
			return nil, err
		}
		remaining := limits.imagesBytes - total
		if remaining < 1 {
			return nil, packageCapacityError("total image content", limits.imagesBytes)
		}
		limit := limits.imageBytes
		resource := "image"
		reportedLimit := limits.imageBytes
		if remaining < limit {
			limit = remaining
			resource = "total image content"
			reportedLimit = limits.imagesBytes
		}
		data, exceeded, err := read(relative, limit)
		if err != nil {
			return nil, fmt.Errorf("reading package image %s: %w", relative, err)
		}
		if exceeded {
			return nil, packageCapacityError(resource, reportedLimit)
		}
		total += int64(len(data))
		if err := validatePNG(data); err != nil {
			return nil, err
		}
		files[relative] = data
	}
	return files, nil
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
	if len(manifest.Visual.States) > maxPackageStates {
		return fmt.Errorf("%w: character package declares more than %d states", ErrPackageCapacity, maxPackageStates)
	}
	return nil
}

func (s *Store) installVisualPackage(manifest packageManifest, files map[string][]byte) error {
	runtime := Manifest{
		SchemaVersion: 2,
		PackID:        manifest.PackageID,
		DisplayName:   manifest.Visual.DisplayName,
		Renderer:      "state_images",
		Frame:         manifest.Visual.Frame,
		Scale:         manifest.Visual.Scale,
		Anchor:        manifest.Visual.Anchor,
		States:        make([]State, 0, len(manifest.Visual.States)),
	}
	for _, state := range manifest.Visual.States {
		relative, err := validatePackageFile(state.File)
		if err != nil {
			return err
		}
		runtime.States = append(runtime.States, State{ID: state.ID, Description: state.Description, ImagePath: "fairy-character://localhost/" + manifest.PackageID + "/" + relative})
	}
	manifestBytes, err := json.MarshalIndent(runtime, "", "  ")
	if err != nil {
		return fmt.Errorf("serializing runtime visual manifest: %w", err)
	}
	if _, err := ParseManifest(manifestBytes); err != nil {
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
	var totalImageBytes int64
	for archiveName, sourcePath := range files {
		remaining := int64(maxPackageImagesBytes) - totalImageBytes
		if remaining < 1 {
			return packageCapacityError("total image content", maxPackageImagesBytes)
		}
		limit := int64(maxPackageImageBytes)
		resource := "image"
		reportedLimit := int64(maxPackageImageBytes)
		if remaining < limit {
			limit = remaining
			resource = "total image content"
			reportedLimit = maxPackageImagesBytes
		}
		data, exceeded, err := fileBytesWithin(sourcePath, limit)
		if err != nil {
			return err
		}
		if exceeded {
			return packageCapacityError(resource, reportedLimit)
		}
		totalImageBytes += int64(len(data))
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
	data, exceeded, err := archiveFileBytesWithin(files, name, maxPackageImagesBytes)
	if err != nil {
		return nil, err
	}
	if exceeded {
		return nil, packageCapacityError("archive entry", maxPackageImagesBytes)
	}
	return data, nil
}

func archiveFileBytesWithin(files []*zip.File, name string, limit int64) ([]byte, bool, error) {
	for _, file := range files {
		if file.Name != name || file.FileInfo().IsDir() {
			continue
		}
		if limit < 0 || file.UncompressedSize64 > uint64(limit) {
			return nil, true, nil
		}
		reader, err := file.Open()
		if err != nil {
			return nil, false, err
		}
		data, exceeded, readErr := bytesWithin(reader, limit)
		closeErr := reader.Close()
		if readErr != nil {
			return nil, false, readErr
		}
		if closeErr != nil {
			return nil, false, closeErr
		}
		return data, exceeded, nil
	}
	return nil, false, fmt.Errorf("archive entry %s not found", name)
}

func fileBytesWithin(path string, limit int64) ([]byte, bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	data, exceeded, readErr := bytesWithin(file, limit)
	closeErr := file.Close()
	if readErr != nil {
		return nil, false, readErr
	}
	if closeErr != nil {
		return nil, false, closeErr
	}
	return data, exceeded, nil
}

func bytesWithin(reader io.Reader, limit int64) ([]byte, bool, error) {
	if limit < 0 {
		return nil, true, nil
	}
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, false, err
	}
	if int64(len(data)) > limit {
		return nil, true, nil
	}
	return data, false, nil
}

func packageCapacityError(resource string, limit int64) error {
	return fmt.Errorf("%w: %s exceeds %d bytes", ErrPackageCapacity, resource, limit)
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
