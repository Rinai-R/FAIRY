package seekdb

import (
	"os"
	"path/filepath"
	"runtime"
)

var locateExecutable = os.Executable

func LocateLibrary() (string, error) {
	executable, err := locateExecutable()
	if err != nil {
		return "", err
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(executable)
	candidates := []string{
		filepath.Join(dir, libraryFileName()),
		filepath.Join(dir, "..", "Frameworks", libraryFileName()),
		filepath.Join(dir, "..", "Resources", libraryFileName()),
		filepath.Join(dir, "..", "Resources", "seekdb", libraryFileName()),
	}
	for _, candidate := range candidates {
		clean := filepath.Clean(candidate)
		info, err := os.Stat(clean)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		return clean, nil
	}
	return "", ErrLibraryRequired
}

func libraryFileName() string {
	if runtime.GOOS == "darwin" {
		return "libseekdb.dylib"
	}
	return "libseekdb.so"
}
