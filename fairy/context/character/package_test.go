package character

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type packageArchiveEntry struct {
	name string
	data []byte
}

func writePackageArchive(t *testing.T, path string, entries []packageArchiveEntry) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("Create(%s) error = %v", path, err)
	}
	writer := zip.NewWriter(file)
	for _, entry := range entries {
		target, createErr := writer.Create(entry.name)
		if createErr != nil {
			t.Fatalf("Create(%s) error = %v", entry.name, createErr)
		}
		if _, writeErr := target.Write(entry.data); writeErr != nil {
			t.Fatalf("Write(%s) error = %v", entry.name, writeErr)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("zip Close() error = %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("file Close() error = %v", err)
	}
}

func packageManifestFixture(packID, states string) []byte {
	return []byte(`{"schemaVersion":1,"packageId":"` + packID + `","character":{"name":"亚托莉","description":"温柔、敏锐。","dialogueStyle":"短句。","speakingLanguage":"zh"},"visual":{"displayName":"亚托莉","renderer":"state_images","frame":{"width":16,"height":16},"scale":4,"anchor":{"x":8,"y":15},"states":[` + states + `]}}`)
}

func writePackageFixture(t *testing.T, dir string, packID string) {
	t.Helper()
	writeFile(t, filepath.Join(dir, "images", "idle.png"), pngSignature)
	writeFile(t, filepath.Join(dir, "manifest.json"), `{"schemaVersion":1,"packageId":"`+packID+`","character":{"name":"亚托莉","description":"温柔、敏锐。","dialogueStyle":"短句。","speakingLanguage":"zh"},"visual":{"displayName":"亚托莉","renderer":"state_images","frame":{"width":16,"height":16},"scale":4,"anchor":{"x":8,"y":15},"states":[{"id":"idle","description":"Quiet standing pose.","file":"images/idle.png"}]}}`)
}

func writeLegacyPackageFixture(t *testing.T, dir string, packID string) {
	t.Helper()
	writeFile(t, filepath.Join(dir, "images", "idle.png"), pngSignature)
	writeFile(t, filepath.Join(dir, "manifest.json"), `{"schemaVersion":1,"packageId":"`+packID+`","character":{"name":"亚托莉","description":"温柔、敏锐。","dialogueStyle":"短句。"},"visual":{"displayName":"亚托莉","renderer":"state_images","frame":{"width":16,"height":16},"scale":4,"anchor":{"x":8,"y":15},"states":[{"id":"idle","description":"Quiet standing pose.","file":"images/idle.png"}]}}`)
}

func TestImportDirectoryPackageCreatesCharacterAndVisualPack(t *testing.T) {
	root := t.TempDir()
	packageDir := t.TempDir()
	writePackageFixture(t, packageDir, "fairy.package")
	record, err := NewStore(root).ImportPackage(packageDir)
	if err != nil {
		t.Fatalf("ImportPackage() error = %v", err)
	}
	if record.Name != "亚托莉" || record.TextLanguage != DefaultTextLanguage || record.SpeakingLanguage != "zh" || record.Appearance.Status != "assigned" || record.Appearance.Visual == nil || record.Appearance.Visual.PackID != "fairy.package" {
		t.Fatalf("record = %#v", record)
	}
	if _, err := os.Stat(filepath.Join(root, "visual-packs", "fairy.package", "images", "idle.png")); err != nil {
		t.Fatalf("imported image missing: %v", err)
	}
}

func TestExportPackageRoundTrips(t *testing.T) {
	root := t.TempDir()
	packageDir := t.TempDir()
	writePackageFixture(t, packageDir, "fairy.package")
	record, err := NewStore(root).ImportPackage(packageDir)
	if err != nil {
		t.Fatalf("ImportPackage() error = %v", err)
	}
	exportPath := filepath.Join(t.TempDir(), "atri.pack")
	if err := NewStore(root).ExportPackage(record.CharacterID, exportPath); err != nil {
		t.Fatalf("ExportPackage() error = %v", err)
	}
	archive, err := zip.OpenReader(exportPath)
	if err != nil {
		t.Fatalf("OpenReader() error = %v", err)
	}
	defer archive.Close()
	manifestBytes, err := archiveFileBytes(archive.File, "manifest.json")
	if err != nil {
		t.Fatalf("manifest missing: %v", err)
	}
	if !strings.Contains(string(manifestBytes), `"speakingLanguage": "zh"`) {
		t.Fatalf("exported manifest missing speaking language: %s", manifestBytes)
	}
	if !strings.Contains(string(manifestBytes), `"textLanguage": "zh"`) {
		t.Fatalf("exported manifest missing text language: %s", manifestBytes)
	}
	if _, err := archiveFileBytes(archive.File, "images/idle.png"); err != nil {
		t.Fatalf("idle missing: %v", err)
	}
}

func TestPackageMotionRoundTripsAndRejectsUnsupportedValues(t *testing.T) {
	packageDir := t.TempDir()
	writeFile(t, filepath.Join(packageDir, "images", "idle.png"), pngSignature)
	writeFile(t, filepath.Join(packageDir, "images", "happy.png"), pngSignature)
	manifest := string(packageManifestFixture("fairy.motion", `{"id":"idle","description":"Idle.","file":"images/idle.png","motion":"pulse"},{"id":"happy","description":"Happy.","file":"images/happy.png","motion":"bounce"}`))
	writeFile(t, filepath.Join(packageDir, "manifest.json"), manifest)

	root := t.TempDir()
	record, err := NewStore(root).ImportPackage(packageDir)
	if err != nil {
		t.Fatalf("ImportPackage() error = %v", err)
	}
	if got := record.Appearance.Visual.States[0].Motion; got != MotionPulse {
		t.Fatalf("imported idle motion = %q", got)
	}
	exportPath := filepath.Join(t.TempDir(), "motion.pack")
	if err := NewStore(root).ExportPackage(record.CharacterID, exportPath); err != nil {
		t.Fatalf("ExportPackage() error = %v", err)
	}
	archive, err := zip.OpenReader(exportPath)
	if err != nil {
		t.Fatalf("OpenReader() error = %v", err)
	}
	defer archive.Close()
	exported, err := archiveFileBytes(archive.File, "manifest.json")
	if err != nil {
		t.Fatalf("manifest missing: %v", err)
	}
	for _, want := range []string{`"motion": "pulse"`, `"motion": "bounce"`} {
		if !strings.Contains(string(exported), want) {
			t.Fatalf("exported manifest missing %s: %s", want, exported)
		}
	}

	badDir := t.TempDir()
	writePackageFixture(t, badDir, "fairy.bad-motion")
	data, err := os.ReadFile(filepath.Join(badDir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	bad := strings.Replace(string(data), `"file":"images/idle.png"`, `"file":"images/idle.png","motion":"spin"`, 1)
	if err := os.WriteFile(filepath.Join(badDir, "manifest.json"), []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(t.TempDir()).ImportPackage(badDir); err == nil || !strings.Contains(err.Error(), "motion") {
		t.Fatalf("unsupported motion error = %v", err)
	}
}

func TestLegacyPackageWithoutSpeakingLanguageExportsJapanese(t *testing.T) {
	root := t.TempDir()
	packageDir := t.TempDir()
	writeLegacyPackageFixture(t, packageDir, "fairy.legacy")
	record, err := NewStore(root).ImportPackage(packageDir)
	if err != nil {
		t.Fatalf("ImportPackage() error = %v", err)
	}
	if record.SpeakingLanguage != DefaultSpeakingLanguage {
		t.Fatalf("record = %#v, want default speaking language", record)
	}
	if record.TextLanguage != DefaultTextLanguage {
		t.Fatalf("record = %#v, want default text language", record)
	}
	exportPath := filepath.Join(t.TempDir(), "legacy.pack")
	if err := NewStore(root).ExportPackage(record.CharacterID, exportPath); err != nil {
		t.Fatalf("ExportPackage() error = %v", err)
	}
	archive, err := zip.OpenReader(exportPath)
	if err != nil {
		t.Fatalf("OpenReader() error = %v", err)
	}
	defer archive.Close()
	manifestBytes, err := archiveFileBytes(archive.File, "manifest.json")
	if err != nil {
		t.Fatalf("manifest missing: %v", err)
	}
	if !strings.Contains(string(manifestBytes), `"speakingLanguage": "ja"`) {
		t.Fatalf("exported legacy manifest missing default speaking language: %s", manifestBytes)
	}
	if !strings.Contains(string(manifestBytes), `"textLanguage": "zh"`) {
		t.Fatalf("exported legacy manifest missing default text language: %s", manifestBytes)
	}
}

func TestImportPackageRejectsUnsupportedSpeakingLanguage(t *testing.T) {
	packageDir := t.TempDir()
	writePackageFixture(t, packageDir, "fairy.bad")
	data, err := os.ReadFile(filepath.Join(packageDir, "manifest.json"))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	bad := strings.ReplaceAll(string(data), `"speakingLanguage":"zh"`, `"speakingLanguage":"ko"`)
	if err := os.WriteFile(filepath.Join(packageDir, "manifest.json"), []byte(bad), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if _, err := NewStore(t.TempDir()).ImportPackage(packageDir); err == nil {
		t.Fatal("ImportPackage() error = nil, want unsupported speaking language error")
	}
}

func TestImportPackageRejectsPathTraversal(t *testing.T) {
	packageDir := t.TempDir()
	writePackageFixture(t, packageDir, "fairy.bad")
	data, err := os.ReadFile(filepath.Join(packageDir, "manifest.json"))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	bad := string(data)
	bad = strings.ReplaceAll(bad, "images/idle.png", "../idle.png")
	if err := os.WriteFile(filepath.Join(packageDir, "manifest.json"), []byte(bad), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if _, err := NewStore(t.TempDir()).ImportPackage(packageDir); err == nil {
		t.Fatal("ImportPackage() error = nil, want traversal error")
	}
}

func TestImportArchivePackageRejectsCompressedManifestExpansion(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "manifest-bomb.pack")
	manifest := append(packageManifestFixture(
		"fairy.manifest-bomb",
		`{"id":"idle","description":"Idle.","file":"images/idle.png"}`,
	), bytes.Repeat([]byte(" "), maxPackageManifestBytes)...)
	writePackageArchive(t, archivePath, []packageArchiveEntry{
		{name: "manifest.json", data: manifest},
		{name: "images/idle.png", data: []byte(pngSignature)},
	})
	info, err := os.Stat(archivePath)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if info.Size() >= int64(len(manifest))/10 {
		t.Fatalf("fixture is not highly compressed: archive=%d decoded=%d", info.Size(), len(manifest))
	}

	root := t.TempDir()
	_, err = NewStore(root).ImportPackage(archivePath)
	if !errors.Is(err, ErrPackageCapacity) || !strings.Contains(err.Error(), "manifest") {
		t.Fatalf("ImportPackage() error = %v, want manifest capacity", err)
	}
	assertPackageNotInstalled(t, root, "fairy.manifest-bomb")
}

func TestImportArchivePackageRejectsCompressedImageExpansion(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "image-bomb.pack")
	image := append([]byte(pngSignature), bytes.Repeat([]byte{0}, maxPackageImageBytes)...)
	writePackageArchive(t, archivePath, []packageArchiveEntry{
		{name: "manifest.json", data: packageManifestFixture(
			"fairy.image-bomb",
			`{"id":"idle","description":"Idle.","file":"images/idle.png"}`,
		)},
		{name: "images/idle.png", data: image},
	})
	info, err := os.Stat(archivePath)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if info.Size() >= int64(len(image))/10 {
		t.Fatalf("fixture is not highly compressed: archive=%d decoded=%d", info.Size(), len(image))
	}

	root := t.TempDir()
	_, err = NewStore(root).ImportPackage(archivePath)
	if !errors.Is(err, ErrPackageCapacity) || !strings.Contains(err.Error(), "image") {
		t.Fatalf("ImportPackage() error = %v, want image capacity", err)
	}
	assertPackageNotInstalled(t, root, "fairy.image-bomb")
}

func TestPackageReadersShareTotalImageBudget(t *testing.T) {
	states := `{"id":"idle","description":"Idle.","file":"images/idle.png"},` +
		`{"id":"talk","description":"Talk.","file":"images/talk.png"}`
	manifest := packageManifestFixture("fairy.total", states)
	image := append([]byte(pngSignature), []byte("abcd")...)
	limits := packageReadLimits{
		manifestBytes: 4 << 10,
		imageBytes:    int64(len(image)),
		imagesBytes:   int64(len(image)*2 - 1),
	}

	t.Run("archive", func(t *testing.T) {
		archivePath := filepath.Join(t.TempDir(), "total.pack")
		writePackageArchive(t, archivePath, []packageArchiveEntry{
			{name: "manifest.json", data: manifest},
			{name: "images/idle.png", data: image},
			{name: "images/talk.png", data: image},
		})
		_, _, err := readArchivePackageWithLimits(archivePath, limits)
		if !errors.Is(err, ErrPackageCapacity) || !strings.Contains(err.Error(), "total") {
			t.Fatalf("readArchivePackageWithLimits() error = %v, want total capacity", err)
		}
	})

	t.Run("directory", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, "images"), 0o700); err != nil {
			t.Fatalf("MkdirAll() error = %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "manifest.json"), manifest, 0o600); err != nil {
			t.Fatalf("WriteFile(manifest) error = %v", err)
		}
		for _, name := range []string{"idle.png", "talk.png"} {
			if err := os.WriteFile(filepath.Join(dir, "images", name), image, 0o600); err != nil {
				t.Fatalf("WriteFile(%s) error = %v", name, err)
			}
		}
		_, _, err := readDirectoryPackageWithLimits(dir, limits)
		if !errors.Is(err, ErrPackageCapacity) || !strings.Contains(err.Error(), "total") {
			t.Fatalf("readDirectoryPackageWithLimits() error = %v, want total capacity", err)
		}
	})
}

func TestPackageBytesWithinStopsAtLimitPlusOne(t *testing.T) {
	payload := bytes.Repeat([]byte("x"), 1024)
	reader := bytes.NewReader(payload)
	data, exceeded, err := bytesWithin(reader, 16)
	if err != nil {
		t.Fatalf("bytesWithin() error = %v", err)
	}
	if !exceeded || data != nil {
		t.Fatalf("bytesWithin() = %d bytes, exceeded=%t", len(data), exceeded)
	}
	if consumed := int64(len(payload) - reader.Len()); consumed != 17 {
		t.Fatalf("reader consumed %d bytes, want limit+1 = 17", consumed)
	}
}

func TestImportPackageRejectsStateCapacityBeforeReadingImages(t *testing.T) {
	var states strings.Builder
	for index := 0; index < maxPackageStates+1; index++ {
		if index > 0 {
			states.WriteByte(',')
		}
		id := fmt.Sprintf("state-%d", index)
		if index == 0 {
			id = "idle"
		}
		fmt.Fprintf(&states, `{"id":%q,"description":"State.","file":%q}`, id, fmt.Sprintf("images/missing-%d.png", index))
	}
	packageDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(packageDir, "manifest.json"), packageManifestFixture("fairy.states", states.String()), 0o600); err != nil {
		t.Fatalf("WriteFile(manifest) error = %v", err)
	}

	root := t.TempDir()
	_, err := NewStore(root).ImportPackage(packageDir)
	if !errors.Is(err, ErrPackageCapacity) || !strings.Contains(err.Error(), "states") {
		t.Fatalf("ImportPackage() error = %v, want state capacity before missing image", err)
	}
	assertPackageNotInstalled(t, root, "fairy.states")
}

func assertPackageNotInstalled(t *testing.T, root, packID string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(root, "visual-packs", packID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("visual pack side effect: %v", err)
	}
	if entries, err := os.ReadDir(filepath.Join(root, "characters")); err == nil && len(entries) != 0 {
		t.Fatalf("character side effects = %d", len(entries))
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ReadDir(characters) error = %v", err)
	}
}
