package seekdb

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVerifyMachOFileChecksNativeReleaseContract(t *testing.T) {
	contract := MachOContract{
		InstallName:           "@rpath/libseekdb.dylib",
		SDKVersion:            "26.5",
		DynamicDependencies:   []string{"/usr/lib/libSystem.B.dylib"},
		ExportedSymbolCount:   2,
		ExportedSymbolsSHA256: fixtureDigest("_seekdb_close\n_seekdb_open\n"),
	}
	library := filepath.Join(t.TempDir(), "libseekdb.dylib")
	writeFixtureMachO(t, library, "15.0", contract.SDKVersion, contract.InstallName, contract.DynamicDependencies, []string{"_seekdb_open", "_seekdb_close"})
	if err := verifyMachOFile(library, "15.0", contract); err != nil {
		t.Fatal(err)
	}
	rpathLibrary := filepath.Join(t.TempDir(), "libseekdb-rpath.dylib")
	writeFixtureMachOWithRPath(t, rpathLibrary, "15.0", contract.SDKVersion, contract.InstallName, contract.DynamicDependencies, []string{"_seekdb_open", "_seekdb_close"}, "/tmp/build/deps/lib")
	if err := verifyMachOFile(rpathLibrary, "15.0", contract); err == nil || !strings.Contains(err.Error(), "LC_RPATH") {
		t.Fatalf("verifyMachOFile(rpath) error = %v, want LC_RPATH rejection", err)
	}

	tests := []struct {
		name   string
		mutate func(*MachOContract) string
		want   string
	}{
		{
			name: "minimum OS drift",
			mutate: func(_ *MachOContract) string {
				return "13.0"
			},
			want: "minimum OS",
		},
		{
			name: "dependency drift",
			mutate: func(contract *MachOContract) string {
				contract.DynamicDependencies = []string{"/usr/lib/libc++.1.dylib"}
				return "15.0"
			},
			want: "dynamic dependencies",
		},
		{
			name: "ABI drift",
			mutate: func(contract *MachOContract) string {
				contract.ExportedSymbolsSHA256 = fixtureDigest("different\n")
				return "15.0"
			},
			want: "exported-symbol",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			changed := contract
			minimumOS := test.mutate(&changed)
			err := verifyMachOFile(library, minimumOS, changed)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("verifyMachOFile() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestParseMachOVersionRejectsUnencodableVersion(t *testing.T) {
	for _, version := range []string{"12", "12.x", "12.256", "65536.0", "12.0.0.1"} {
		if _, err := parseMachOVersion(version); err == nil {
			t.Fatalf("parseMachOVersion(%q) succeeded", version)
		}
	}
}

// This opt-in test replays the native release contract against an external
// copy of the verified artifact.
func TestExternalSeekDBMachOReleaseContract(t *testing.T) {
	library := os.Getenv("FAIRY_VERIFY_SEEKDB_MACHO")
	if library == "" {
		t.Skip("FAIRY_VERIFY_SEEKDB_MACHO is not set")
	}
	catalog, err := BuiltinArtifactCatalog()
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := catalog.Verified("darwin", "arm64")
	if err != nil {
		t.Fatal(err)
	}
	if artifact.MachO == nil {
		t.Fatal("darwin/arm64 artifact has no Mach-O contract")
	}
	if err := verifyMachOFile(library, artifact.MinimumOSVersion, *artifact.MachO); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyPackagedBundleAcceptsSignedVerifiedMachOButNotCandidateOrABIDrift(t *testing.T) {
	directory := t.TempDir()
	unsignedLibrary := filepath.Join(directory, "unsigned.dylib")
	signedLibrary := filepath.Join(directory, "signed.dylib")
	contract := MachOContract{
		InstallName:           "@rpath/libseekdb.dylib",
		SDKVersion:            "26.5",
		DynamicDependencies:   []string{"/usr/lib/libSystem.B.dylib"},
		ExportedSymbolCount:   1,
		ExportedSymbolsSHA256: fixtureDigest("_seekdb_open\n"),
	}
	writeFixtureMachO(t, unsignedLibrary, "15.0", contract.SDKVersion, contract.InstallName, contract.DynamicDependencies, []string{"_seekdb_open"})
	writeFixtureMachOWithSignature(t, signedLibrary, "15.0", contract.SDKVersion, contract.InstallName, contract.DynamicDependencies, []string{"_seekdb_open"}, true)
	unsigned, err := os.ReadFile(unsignedLibrary)
	if err != nil {
		t.Fatal(err)
	}
	artifact := fixtureRuntimeArtifact(string(unsigned))
	artifact.MachO = &contract
	bundle := ArtifactBundle{
		LibraryPath:      signedLibrary,
		LicensePath:      filepath.Join(directory, "LICENSE"),
		NoticePath:       filepath.Join(directory, "NOTICE"),
		AppInfoPlistPath: filepath.Join(directory, "Info.plist"),
	}
	for filename, content := range map[string]string{
		bundle.LicensePath:      "license",
		bundle.NoticePath:       "notice",
		bundle.AppInfoPlistPath: `<?xml version="1.0"?><plist><dict><key>LSMinimumSystemVersion</key><string>15.0.0</string></dict></plist>`,
	} {
		if err := os.WriteFile(filename, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	catalog := ArtifactCatalog{
		SchemaVersion: artifactCatalogSchemaVersion,
		Product:       "seekdb",
		ReleaseTag:    "v1.0.0",
		ReleaseURL:    "https://github.com/oceanbase/seekdb/releases/tag/v1.0.0",
		Targets: []ArtifactTarget{{
			GOOS: "darwin", GOARCH: "arm64", Status: ArtifactStatusVerified,
			Reason: "fixture", Artifact: &artifact,
		}},
	}
	if err := catalog.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := catalog.VerifyBundle("darwin", "arm64", bundle); !errors.Is(err, ErrArtifactIntegrity) {
		t.Fatalf("VerifyBundle(signed) error = %v, want unsigned hash rejection", err)
	}
	if err := catalog.VerifyPackagedBundle("darwin", "arm64", bundle); err != nil {
		t.Fatalf("VerifyPackagedBundle(signed) error = %v", err)
	}

	catalog.Targets[0].Status = ArtifactStatusCandidate
	if err := catalog.VerifyPackagedBundle("darwin", "arm64", bundle); !errors.Is(err, ErrArtifactCandidate) {
		t.Fatalf("VerifyPackagedBundle(candidate) error = %v", err)
	}
	catalog.Targets[0].Status = ArtifactStatusVerified
	catalog.Targets[0].Artifact.MachO.ExportedSymbolsSHA256 = fixtureDigest("_different\n")
	if err := catalog.VerifyPackagedBundle("darwin", "arm64", bundle); !errors.Is(err, ErrArtifactIntegrity) || !strings.Contains(err.Error(), "exported-symbol") {
		t.Fatalf("VerifyPackagedBundle(ABI drift) error = %v", err)
	}
}

func writeFixtureMachO(t *testing.T, filename, minimumOS, sdk, installName string, dependencies, symbols []string) {
	writeFixtureMachOWithSignatureAndRPath(t, filename, minimumOS, sdk, installName, dependencies, symbols, false, "")
}

func writeFixtureMachOWithSignature(t *testing.T, filename, minimumOS, sdk, installName string, dependencies, symbols []string, signed bool) {
	writeFixtureMachOWithSignatureAndRPath(t, filename, minimumOS, sdk, installName, dependencies, symbols, signed, "")
}

func writeFixtureMachOWithRPath(t *testing.T, filename, minimumOS, sdk, installName string, dependencies, symbols []string, rpath string) {
	writeFixtureMachOWithSignatureAndRPath(t, filename, minimumOS, sdk, installName, dependencies, symbols, false, rpath)
}

func writeFixtureMachOWithSignatureAndRPath(t *testing.T, filename, minimumOS, sdk, installName string, dependencies, symbols []string, signed bool, rpath string) {
	t.Helper()
	minimumVersion, err := parseMachOVersion(minimumOS)
	if err != nil {
		t.Fatal(err)
	}
	sdkVersion, err := parseMachOVersion(sdk)
	if err != nil {
		t.Fatal(err)
	}

	commands := [][]byte{
		fixtureDylibCommand(machOLoadIDDylib, installName),
	}
	for _, dependency := range dependencies {
		commands = append(commands, fixtureDylibCommand(0x0c, dependency))
	}
	if rpath != "" {
		commands = append(commands, fixtureRPathCommand(rpath))
	}
	buildVersion := new(bytes.Buffer)
	writeFixtureBinary(t, buildVersion,
		uint32(machOLoadBuildVersion), uint32(24), uint32(machOPlatformMacOS), minimumVersion, sdkVersion, uint32(0),
	)
	commands = append(commands, buildVersion.Bytes())

	stringTable := []byte{0}
	stringIndexes := make([]uint32, len(symbols))
	for index, symbol := range symbols {
		stringIndexes[index] = uint32(len(stringTable))
		stringTable = append(stringTable, symbol...)
		stringTable = append(stringTable, 0)
	}

	commandBytes := 0
	for _, command := range commands {
		commandBytes += len(command)
	}
	commandBytes += 24 // LC_SYMTAB
	if signed {
		commandBytes += 16 // LC_CODE_SIGNATURE
	}
	symbolOffset := uint32(32 + commandBytes)
	stringOffset := symbolOffset + uint32(len(symbols)*16)
	symtab := new(bytes.Buffer)
	writeFixtureBinary(t, symtab,
		uint32(0x02), uint32(24), symbolOffset, uint32(len(symbols)), stringOffset, uint32(len(stringTable)),
	)
	commands = append(commands, symtab.Bytes())
	if signed {
		signature := new(bytes.Buffer)
		writeFixtureBinary(t, signature,
			uint32(machOLoadCodeSignature), uint32(16), stringOffset+uint32(len(stringTable)), uint32(16),
		)
		commands = append(commands, signature.Bytes())
	}

	buffer := new(bytes.Buffer)
	writeFixtureBinary(t, buffer,
		uint32(0xfeedfacf), uint32(0x0100000c), uint32(0), uint32(6),
		uint32(len(commands)), uint32(commandBytes), uint32(0), uint32(0),
	)
	for _, command := range commands {
		buffer.Write(command)
	}
	for index := range symbols {
		writeFixtureBinary(t, buffer,
			stringIndexes[index], uint8(0x0f), uint8(1), uint16(0), uint64(index+1),
		)
	}
	buffer.Write(stringTable)
	if signed {
		buffer.Write(bytes.Repeat([]byte{0xa5}, 16))
	}
	if err := os.WriteFile(filename, buffer.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

func fixtureDylibCommand(command uint32, name string) []byte {
	size := 24 + len(name) + 1
	if remainder := size % 8; remainder != 0 {
		size += 8 - remainder
	}
	buffer := make([]byte, size)
	binary.LittleEndian.PutUint32(buffer[0:4], command)
	binary.LittleEndian.PutUint32(buffer[4:8], uint32(size))
	binary.LittleEndian.PutUint32(buffer[8:12], uint32(24))
	copy(buffer[24:], name)
	return buffer
}

func fixtureRPathCommand(path string) []byte {
	size := 12 + len(path) + 1
	if remainder := size % 8; remainder != 0 {
		size += 8 - remainder
	}
	buffer := make([]byte, size)
	binary.LittleEndian.PutUint32(buffer[0:4], machOLoadRPath)
	binary.LittleEndian.PutUint32(buffer[4:8], uint32(size))
	binary.LittleEndian.PutUint32(buffer[8:12], uint32(12))
	copy(buffer[12:], path)
	return buffer
}

func writeFixtureBinary(t *testing.T, buffer *bytes.Buffer, values ...any) {
	t.Helper()
	for _, value := range values {
		if err := binary.Write(buffer, binary.LittleEndian, value); err != nil {
			t.Fatal(err)
		}
	}
}
