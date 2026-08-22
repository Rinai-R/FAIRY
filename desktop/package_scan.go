package main

import (
	"bytes"
	"debug/macho"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

func verifyPackageRuntimeClosure(contentsDir, executable, library string) error {
	if err := verifyBundleFileClosure(contentsDir, executable, library); err != nil {
		return err
	}
	if err := verifyInfoPlistRuntimeBoundary(filepath.Join(contentsDir, "Info.plist")); err != nil {
		return err
	}
	if err := verifyPluginHostDefaults(filepath.Join(contentsDir, "Resources", "plugin-host.defaults.json")); err != nil {
		return err
	}
	if err := verifyEndpointExecutableBoundary(executable); err != nil {
		return err
	}
	if err := verifyMachORuntimeDependencies(executable, macho.TypeExec, true); err != nil {
		return fmt.Errorf("verify FAIRY Mach-O runtime closure: %w", err)
	}
	if err := verifyMachORuntimeDependencies(library, macho.TypeDylib, false); err != nil {
		return fmt.Errorf("verify SeekDB Mach-O runtime closure: %w", err)
	}
	return nil
}

func verifyEndpointExecutableBoundary(executable string) error {
	raw, err := readBoundedPackageFile(executable, 512<<20)
	if err != nil {
		return fmt.Errorf("read packaged FAIRY executable: %w", err)
	}
	markers := []struct {
		name    string
		encoded string
	}{
		{name: "OneBot package", encoded: "ZmFpcnkvcGx1Z2luL3Fxb25lYm90"},
		{name: "OneBot config authority", encoded: "ZmFpcnkvcnVudGltZS9jb25maWcuUmVhZFFRT25lQm90"},
		{name: "OneBot frontend route", encoded: "ZnJvbnRlbmQvcXEtb25lYm90"},
		{name: "OneBot Desktop binding", encoded: "TWFuYWdlbWVudFFR"},
		{name: "OneBot Desktop mutation", encoded: "U2F2ZU1hbmFnZW1lbnRRUQ=="},
		{name: "OneBot bridge", encoded: "UVFCcmlkZ2U="},
		{name: "Ollama runtime", encoded: "Z2l0aHViLmNvbS9vbGxhbWEvb2xsYW1h"},
		{name: "native Llama runtime", encoded: "bGxhbWEuY3Bw"},
		{name: "llama server", encoded: "bGxhbWEtc2VydmVy"},
		{name: "LocalAI runtime", encoded: "bG9jYWxhaQ=="},
		{name: "ONNX Runtime", encoded: "Z2l0aHViLmNvbS95YWx1ZS9vbm54cnVudGltZV9nbw=="},
		{name: "TensorFlow runtime", encoded: "Z2l0aHViLmNvbS90ZW5zb3JmbG93L3RlbnNvcmZsb3c="},
		{name: "TensorFlow Lite runtime", encoded: "Z2l0aHViLmNvbS9tYXR0bi9nby10ZmxpdGU="},
		{name: "Torch native inference runtime", encoded: "bGlidG9yY2g="},
		{name: "GGML runtime", encoded: "Z2l0aHViLmNvbS9nZ2VyZ2Fub3YvZ2dtbA=="},
		{name: "Python sentence embedding runtime", encoded: "c2VudGVuY2UtdHJhbnNmb3JtZXJz"},
		{name: "PostgreSQL pgx runtime", encoded: "Z2l0aHViLmNvbS9qYWNrYy9wZ3g="},
		{name: "PostgreSQL libpq runtime", encoded: "Z2l0aHViLmNvbS9saWIvcHE="},
		{name: "MySQL TCP driver", encoded: "Z2l0aHViLmNvbS9nby1zcWwtZHJpdmVyL215c3Fs"},
		{name: "SQLite CGO runtime", encoded: "Z2l0aHViLmNvbS9tYXR0bi9nby1zcWxpdGUz"},
		{name: "SQLite pure-Go runtime", encoded: "bW9kZXJuYy5vcmcvc3FsaXRl"},
		{name: "external vector database runtime", encoded: "Z2l0aHViLmNvbS9xZHJhbnQvZ28tY2xpZW50"},
		{name: "Docker runtime", encoded: "Z2l0aHViLmNvbS9kb2NrZXIvZG9ja2Vy"},
	}
	for _, marker := range markers {
		decoded, err := base64.StdEncoding.DecodeString(marker.encoded)
		if err != nil {
			return fmt.Errorf("decode packaged runtime marker %s: %w", marker.name, err)
		}
		if bytes.Contains(raw, decoded) {
			return fmt.Errorf("packaged FAIRY executable contains forbidden %s implementation", marker.name)
		}
	}
	return nil
}

func verifyBundleFileClosure(contentsDir, executable, library string) error {
	expectedFiles := map[string]string{
		"Info.plist":                                          "resource",
		"MacOS/FAIRY":                                         "executable",
		"Frameworks/libseekdb.dylib":                          "executable",
		"Resources/plugin-host.defaults.json":                 "resource",
		"Resources/plugin-abi/manifest.v1.json":               "resource",
		"Resources/plugin-abi/envelope.v1.json":               "resource",
		"Resources/licenses/SEEKDB-LICENSE":                   "resource",
		"Resources/licenses/SEEKDB-NOTICE":                    "resource",
		"Resources/plugin-release/inventory.json":             "resource",
		"Resources/plugin-release/installation-evidence.json": "resource",
	}
	if filepath.Clean(executable) != filepath.Join(contentsDir, "MacOS", "FAIRY") || filepath.Clean(library) != filepath.Join(contentsDir, "Frameworks", "libseekdb.dylib") {
		return errors.New("package runtime paths do not match the fixed App layout")
	}
	allowedDirs := map[string]struct{}{
		".": {}, "MacOS": {}, "Frameworks": {}, "Resources": {},
		"Resources/plugin-abi": {}, "Resources/licenses": {}, "Resources/plugin-release": {},
		"_CodeSignature": {},
	}
	signatureDir := false
	signatureFile := false
	signatureLink := false
	walkErr := filepath.WalkDir(contentsDir, func(filename string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(contentsDir, filename)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if relative == "Resources/plugin-release" || strings.HasPrefix(relative, "Resources/plugin-release/") {
			// VerifyInstalledReleaseInventory already proves the exact subtree,
			// including regular-file and symlink constraints.
			delete(expectedFiles, relative)
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if relative == "CodeResources" && info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(filename)
			if err != nil {
				return err
			}
			if filepath.ToSlash(target) != "_CodeSignature/CodeResources" {
				return errors.New("Contents/CodeResources must point to _CodeSignature/CodeResources")
			}
			signatureLink = true
			return nil
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("package contains forbidden symlink %s", relative)
		}
		if entry.IsDir() {
			if _, allowed := allowedDirs[relative]; !allowed {
				return fmt.Errorf("package contains undeclared directory %s", relative)
			}
			if relative == "_CodeSignature" {
				signatureDir = true
			}
			return nil
		}
		if relative == "_CodeSignature/CodeResources" {
			if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 != 0 {
				return errors.New("code signature resources must be a non-executable regular file")
			}
			signatureFile = true
			return nil
		}
		kind, expected := expectedFiles[relative]
		if !expected {
			return fmt.Errorf("package contains undeclared file %s", relative)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("package file %s is not regular", relative)
		}
		if kind == "resource" && info.Mode().Perm()&0o111 != 0 {
			return fmt.Errorf("package resource %s is unexpectedly executable", relative)
		}
		if kind == "executable" && info.Mode().Perm()&0o111 == 0 {
			return fmt.Errorf("package code %s is not executable", relative)
		}
		delete(expectedFiles, relative)
		return nil
	})
	if walkErr != nil {
		return walkErr
	}
	if len(expectedFiles) != 0 {
		return fmt.Errorf("package is missing %d declared runtime files", len(expectedFiles))
	}
	if signatureDir != signatureFile {
		return errors.New("package code signature directory and CodeResources must appear together")
	}
	if signatureLink && !signatureFile {
		return errors.New("Contents/CodeResources points to a missing code signature")
	}
	return nil
}

func verifyInfoPlistRuntimeBoundary(filename string) error {
	raw, err := readBoundedPackageFile(filename, 1<<20)
	if err != nil {
		return fmt.Errorf("read packaged Info.plist: %w", err)
	}
	decoder := xml.NewDecoder(bytes.NewReader(raw))
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("parse packaged Info.plist: %w", err)
		}
		start, ok := token.(xml.StartElement)
		if !ok || start.Name.Local != "key" {
			continue
		}
		var key string
		if err := decoder.DecodeElement(&key, &start); err != nil {
			return fmt.Errorf("parse packaged Info.plist key: %w", err)
		}
		upper := strings.ToUpper(strings.TrimSpace(key))
		if upper == "LSENVIRONMENT" || strings.HasPrefix(upper, "FAIRY_") || strings.HasPrefix(upper, "DYLD_") || strings.HasPrefix(upper, "PYTHON") || strings.HasPrefix(upper, "NODE_") {
			return fmt.Errorf("packaged Info.plist declares forbidden runtime environment key %q", key)
		}
	}
	return nil
}

func verifyPluginHostDefaults(filename string) error {
	raw, err := readBoundedPackageFile(filename, 64<<10)
	if err != nil {
		return fmt.Errorf("read plugin host defaults: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var defaults struct {
		DefaultCapabilityGrants []string `json:"defaultCapabilityGrants"`
		Note                    string   `json:"note"`
	}
	if err := decoder.Decode(&defaults); err != nil {
		return fmt.Errorf("parse plugin host defaults: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return errors.New("plugin host defaults must contain one JSON value")
	}
	if defaults.DefaultCapabilityGrants == nil || len(defaults.DefaultCapabilityGrants) != 0 {
		return errors.New("strict endpoint plugin host defaults must explicitly deny all capabilities")
	}
	return nil
}

func verifyMachORuntimeDependencies(filename string, expectedType macho.Type, allowSeekDB bool) error {
	file, err := macho.Open(filename)
	if err != nil {
		return err
	}
	defer file.Close()
	if file.Cpu != macho.CpuArm64 {
		return fmt.Errorf("CPU = %s, want Arm64", file.Cpu)
	}
	if file.Type != expectedType {
		return fmt.Errorf("type = %s, want %s", file.Type, expectedType)
	}
	libraries, err := file.ImportedLibraries()
	if err != nil {
		return err
	}
	needsRPath := false
	for _, library := range libraries {
		if err := validateImportedMachOLibrary(library, allowSeekDB); err != nil {
			return err
		}
		if library == "@rpath/libseekdb.dylib" {
			needsRPath = true
		}
	}
	seenRPath := false
	for _, load := range file.Loads {
		rpath, ok := load.(*macho.Rpath)
		if !ok {
			continue
		}
		if seenRPath || rpath.Path != "@executable_path/../Frameworks" {
			return fmt.Errorf("undeclared Mach-O rpath %q", rpath.Path)
		}
		seenRPath = true
	}
	if needsRPath && !seenRPath {
		return errors.New("@rpath/libseekdb.dylib is declared without the packaged Frameworks rpath")
	}
	return nil
}

func validateImportedMachOLibrary(library string, allowSeekDB bool) error {
	if strings.HasPrefix(library, "/System/Library/") || strings.HasPrefix(library, "/usr/lib/") {
		return nil
	}
	if allowSeekDB && library == "@rpath/libseekdb.dylib" {
		return nil
	}
	return fmt.Errorf("undeclared non-system Mach-O dependency %q", library)
}

func readBoundedPackageFile(filename string, limit int64) ([]byte, error) {
	info, err := os.Lstat(filename)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > limit {
		return nil, errors.New("file must be a bounded regular non-symlink")
	}
	raw, err := os.ReadFile(filename)
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) != info.Size() {
		return nil, errors.New("file changed while being read")
	}
	return raw, nil
}
