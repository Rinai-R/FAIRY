package seekdb

import (
	"crypto/sha256"
	"debug/macho"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

const (
	machOLoadIDDylib       = 0x0d
	machOLoadCodeSignature = 0x1d
	machOLoadRPath         = 0x8000001c
	machOLoadBuildVersion  = 0x32
	machOPlatformMacOS     = 1
	machONListExternal     = 0x01
	machONListTypeMask     = 0x0e
	machONListUndefined    = 0x00
	machONListDebugMask    = 0xe0
)

func (a RuntimeArtifact) verifyNativeFile(goos, goarch, filename string) error {
	if a.MachO == nil {
		return nil
	}
	if goos != "darwin" || goarch != "arm64" {
		return fmt.Errorf("%w: Mach-O contract cannot verify %s/%s", ErrArtifactIntegrity, goos, goarch)
	}
	if err := verifyMachOFile(filename, a.MinimumOSVersion, *a.MachO); err != nil {
		return fmt.Errorf("%w: %v", ErrArtifactIntegrity, err)
	}
	return nil
}

func verifyMachOFile(filename, minimumOSVersion string, contract MachOContract) error {
	return verifyMachOFileWithSignaturePolicy(filename, minimumOSVersion, contract, false)
}

// verifyPackagedMachOFile verifies the immutable native contract after the
// release pipeline has signed the dylib. Signing appends an LC_CODE_SIGNATURE
// and changes the whole-file hash, but it must not change the architecture,
// deployment target, SDK, install name, dependencies, or exported ABI.
func verifyPackagedMachOFile(filename, minimumOSVersion string, contract MachOContract) error {
	return verifyMachOFileWithSignaturePolicy(filename, minimumOSVersion, contract, true)
}

func verifyMachOFileWithSignaturePolicy(filename, minimumOSVersion string, contract MachOContract, allowCodeSignature bool) error {
	file, err := macho.Open(filename)
	if err != nil {
		return fmt.Errorf("open native library as thin Mach-O: %w", err)
	}
	defer file.Close()

	if file.Cpu != macho.CpuArm64 {
		return fmt.Errorf("Mach-O architecture is %s, expected arm64", file.Cpu)
	}
	if file.Type != macho.TypeDylib {
		return fmt.Errorf("Mach-O type is %s, expected dylib", file.Type)
	}

	minimumVersion, err := parseMachOVersion(minimumOSVersion)
	if err != nil {
		return err
	}
	sdkVersion, err := parseMachOVersion(contract.SDKVersion)
	if err != nil {
		return err
	}

	buildVersionCount := 0
	installNameCount := 0
	codeSignatureCount := 0
	for _, load := range file.Loads {
		raw := load.Raw()
		if len(raw) < 8 {
			return errors.New("Mach-O contains a truncated load command")
		}
		command := file.ByteOrder.Uint32(raw[:4])
		switch command {
		case machOLoadBuildVersion:
			if len(raw) < 24 {
				return errors.New("Mach-O LC_BUILD_VERSION is truncated")
			}
			buildVersionCount++
			platform := file.ByteOrder.Uint32(raw[8:12])
			minimum := file.ByteOrder.Uint32(raw[12:16])
			sdk := file.ByteOrder.Uint32(raw[16:20])
			if platform != machOPlatformMacOS {
				return fmt.Errorf("Mach-O build platform is %d, expected macOS", platform)
			}
			if minimum != minimumVersion {
				return fmt.Errorf("Mach-O minimum OS is %s, expected %s", formatMachOVersion(minimum), minimumOSVersion)
			}
			if sdk != sdkVersion {
				return fmt.Errorf("Mach-O SDK is %s, expected %s", formatMachOVersion(sdk), contract.SDKVersion)
			}
		case machOLoadIDDylib:
			name, err := machODylibName(file, raw)
			if err != nil {
				return err
			}
			installNameCount++
			if name != contract.InstallName {
				return fmt.Errorf("Mach-O install name is %q, expected %q", name, contract.InstallName)
			}
		case machOLoadCodeSignature:
			if !allowCodeSignature {
				return errors.New("Mach-O build input must be unsigned before nested release signing")
			}
			codeSignatureCount++
		case machOLoadRPath:
			return errors.New("SeekDB Mach-O must not contain LC_RPATH; the App executable owns the fixed Frameworks lookup path")
		}
	}
	if buildVersionCount != 1 {
		return fmt.Errorf("Mach-O has %d LC_BUILD_VERSION commands, expected 1", buildVersionCount)
	}
	if installNameCount != 1 {
		return fmt.Errorf("Mach-O has %d LC_ID_DYLIB commands, expected 1", installNameCount)
	}
	if allowCodeSignature && codeSignatureCount != 1 {
		return fmt.Errorf("packaged Mach-O has %d LC_CODE_SIGNATURE commands, expected 1", codeSignatureCount)
	}

	dependencies, err := file.ImportedLibraries()
	if err != nil {
		return fmt.Errorf("read Mach-O dynamic dependencies: %w", err)
	}
	sort.Strings(dependencies)
	if !equalStrings(dependencies, contract.DynamicDependencies) {
		return fmt.Errorf("Mach-O dynamic dependencies are %q, expected %q", dependencies, contract.DynamicDependencies)
	}

	symbols, err := exportedMachOSymbols(file)
	if err != nil {
		return err
	}
	if len(symbols) != contract.ExportedSymbolCount {
		return fmt.Errorf("Mach-O exports %d symbols, expected %d", len(symbols), contract.ExportedSymbolCount)
	}
	digest := sha256.Sum256([]byte(strings.Join(symbols, "\n") + "\n"))
	if actual := hex.EncodeToString(digest[:]); actual != contract.ExportedSymbolsSHA256 {
		return fmt.Errorf("Mach-O exported-symbol SHA-256 is %s, expected %s", actual, contract.ExportedSymbolsSHA256)
	}
	return nil
}

func machOHasCodeSignature(filename string) (bool, error) {
	file, err := macho.Open(filename)
	if err != nil {
		return false, fmt.Errorf("open native library as thin Mach-O: %w", err)
	}
	defer file.Close()
	for _, load := range file.Loads {
		raw := load.Raw()
		if len(raw) < 8 {
			return false, errors.New("Mach-O contains a truncated load command")
		}
		if file.ByteOrder.Uint32(raw[:4]) == machOLoadCodeSignature {
			return true, nil
		}
	}
	return false, nil
}

func machODylibName(file *macho.File, raw []byte) (string, error) {
	if len(raw) < 24 {
		return "", errors.New("Mach-O dylib load command is truncated")
	}
	offset := int(file.ByteOrder.Uint32(raw[8:12]))
	if offset < 24 || offset >= len(raw) {
		return "", errors.New("Mach-O dylib load command has an invalid name offset")
	}
	nameBytes := raw[offset:]
	if terminator := strings.IndexByte(string(nameBytes), 0); terminator >= 0 {
		nameBytes = nameBytes[:terminator]
	}
	if len(nameBytes) == 0 {
		return "", errors.New("Mach-O dylib load command has an empty name")
	}
	return string(nameBytes), nil
}

func exportedMachOSymbols(file *macho.File) ([]string, error) {
	if file.Symtab == nil {
		return nil, errors.New("Mach-O has no symbol table")
	}
	seen := make(map[string]struct{})
	for _, symbol := range file.Symtab.Syms {
		if symbol.Type&machONListDebugMask != 0 || symbol.Type&machONListExternal == 0 || symbol.Type&machONListTypeMask == machONListUndefined {
			continue
		}
		if symbol.Name == "" || strings.ContainsRune(symbol.Name, 0) {
			return nil, errors.New("Mach-O contains an invalid exported symbol")
		}
		seen[symbol.Name] = struct{}{}
	}
	symbols := make([]string, 0, len(seen))
	for symbol := range seen {
		symbols = append(symbols, symbol)
	}
	sort.Strings(symbols)
	return symbols, nil
}

func parseMachOVersion(version string) (uint32, error) {
	parts := strings.Split(version, ".")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, fmt.Errorf("Mach-O version %q must contain two or three components", version)
	}
	values := [3]uint64{}
	for index, part := range parts {
		value, err := strconv.ParseUint(part, 10, 16)
		if err != nil {
			return 0, fmt.Errorf("Mach-O version %q is invalid", version)
		}
		values[index] = value
	}
	if values[0] > 0xffff || values[1] > 0xff || values[2] > 0xff {
		return 0, fmt.Errorf("Mach-O version %q exceeds load-command bounds", version)
	}
	return uint32(values[0]<<16 | values[1]<<8 | values[2]), nil
}

func formatMachOVersion(version uint32) string {
	major := version >> 16
	minor := version >> 8 & 0xff
	patch := version & 0xff
	if patch == 0 {
		return fmt.Sprintf("%d.%d", major, minor)
	}
	return fmt.Sprintf("%d.%d.%d", major, minor, patch)
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
