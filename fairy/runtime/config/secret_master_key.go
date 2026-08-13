package config

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
)

const (
	secretMasterKeyDirectory = "secrets"
	secretMasterKeyFilename  = "master.key"
)

var (
	ErrSecretDataDirectoryInvalid = errors.New("FAIRY secret data directory is invalid")
	ErrMasterKeyFileInvalid       = errors.New("FAIRY master key file is invalid")
	ErrMasterKeyPermissions       = errors.New("FAIRY master key permissions are invalid")

	errMasterKeyPublicationInProgress = fmt.Errorf("%w: publication has not settled", ErrMasterKeyFileInvalid)
)

// SecretCipherFromDataDir loads the local master key below dataDir, creating it
// on first use. The key is raw key material and must never be rendered, logged,
// or copied into SeekDB. Existing files and directories are validated rather
// than repaired so an unsafe local storage boundary fails closed.
func SecretCipherFromDataDir(dataDir string) (*SecretCipher, error) {
	key, err := loadOrCreateMasterKey(dataDir)
	if err != nil {
		return nil, err
	}
	defer clear(key)

	secretCipher, err := newSecretCipher(key, rand.Reader)
	if err != nil {
		return nil, errors.New("initializing FAIRY secret cipher from local master key failed")
	}
	return secretCipher, nil
}

func loadOrCreateMasterKey(dataDir string) ([]byte, error) {
	root, err := validateSecretDataDirectoryPath(dataDir)
	if err != nil {
		return nil, err
	}
	if err := ensurePrivateDirectory(root); err != nil {
		return nil, err
	}

	secretsDir := filepath.Join(root, secretMasterKeyDirectory)
	if err := ensurePrivateDirectory(secretsDir); err != nil {
		return nil, err
	}
	// The key may have been published by an earlier process. Synchronize the
	// directory before trusting its name so startup never accepts an entry that
	// this process has not first made durable.
	if err := syncPrivateDirectory(secretsDir); err != nil {
		return nil, fmt.Errorf("syncing FAIRY master key directory before reading: %w", err)
	}
	keyPath := filepath.Join(secretsDir, secretMasterKeyFilename)
	if key, found, err := readPublishedMasterKey(keyPath); err != nil || found {
		if errors.Is(err, errMasterKeyPublicationInProgress) {
			key, found, err = waitForPublishedMasterKey(secretsDir, keyPath)
		}
		if found && err == nil {
			if syncErr := syncPrivateDirectory(secretsDir); syncErr != nil {
				clear(key)
				return nil, fmt.Errorf("syncing FAIRY master key directory after reading: %w", syncErr)
			}
		}
		return key, err
	}
	return createAndPublishMasterKey(secretsDir, keyPath)
}

func validateSecretDataDirectoryPath(dataDir string) (string, error) {
	if dataDir == "" || dataDir != filepath.Clean(dataDir) || filepath.Dir(dataDir) == dataDir {
		return "", ErrSecretDataDirectoryInvalid
	}
	if !filepath.IsAbs(dataDir) {
		return "", ErrSecretDataDirectoryInvalid
	}
	return dataDir, nil
}

func ensurePrivateDirectory(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		if err := os.MkdirAll(path, 0o700); err != nil {
			return fmt.Errorf("creating FAIRY private directory: %w", err)
		}
		// MkdirAll is affected by the process umask. This path did not exist
		// before this call, so tightening its final component is safe.
		if err := os.Chmod(path, 0o700); err != nil {
			return fmt.Errorf("securing FAIRY private directory: %w", err)
		}
		info, err = os.Lstat(path)
	}
	if err != nil {
		return fmt.Errorf("inspecting FAIRY private directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return ErrSecretDataDirectoryInvalid
	}
	if err := validatePrivatePermissions(info.Mode(), 0o700); err != nil {
		return fmt.Errorf("%w: private directory", ErrMasterKeyPermissions)
	}
	return nil
}

func readPublishedMasterKey(path string) ([]byte, bool, error) {
	return readMasterKey(path, true)
}

func readPublishingMasterKey(path string) ([]byte, bool, error) {
	return readMasterKey(path, false)
}

func readMasterKey(path string, requireSettledLink bool) ([]byte, bool, error) {
	before, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("inspecting FAIRY master key file: %w", err)
	}
	if before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() {
		return nil, false, ErrMasterKeyFileInvalid
	}
	if err := validatePrivatePermissions(before.Mode(), 0o600); err != nil {
		return nil, false, ErrMasterKeyPermissions
	}

	file, err := openMasterKeyNoFollow(path)
	if err != nil {
		return nil, false, fmt.Errorf("opening FAIRY master key file: %w", err)
	}
	opened, statErr := file.Stat()
	if statErr != nil {
		_ = file.Close()
		return nil, false, fmt.Errorf("inspecting opened FAIRY master key file: %w", statErr)
	}
	if !opened.Mode().IsRegular() || !os.SameFile(before, opened) {
		_ = file.Close()
		return nil, false, ErrMasterKeyFileInvalid
	}
	if err := validatePrivatePermissions(opened.Mode(), 0o600); err != nil {
		_ = file.Close()
		return nil, false, ErrMasterKeyPermissions
	}
	if err := validateOpenedMasterKey(file, opened, requireSettledLink); err != nil {
		_ = file.Close()
		return nil, false, err
	}

	key, readErr := io.ReadAll(io.LimitReader(file, keyBytes+1))
	closeErr := file.Close()
	if readErr != nil {
		clear(key)
		return nil, false, fmt.Errorf("reading FAIRY master key file: %w", readErr)
	}
	if closeErr != nil {
		clear(key)
		return nil, false, fmt.Errorf("closing FAIRY master key file: %w", closeErr)
	}
	if len(key) != keyBytes {
		clear(key)
		return nil, false, ErrMasterKeyFileInvalid
	}
	return key, true, nil
}

func createAndPublishMasterKey(secretsDir, keyPath string) ([]byte, error) {
	generated := make([]byte, keyBytes)
	if _, err := io.ReadFull(rand.Reader, generated); err != nil {
		clear(generated)
		return nil, errors.New("generating FAIRY master key failed")
	}
	defer clear(generated)

	temporary, err := os.CreateTemp(secretsDir, ".master-key-*.tmp")
	if err != nil {
		return nil, fmt.Errorf("creating temporary FAIRY master key file: %w", err)
	}
	temporaryPath := temporary.Name()
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()

	closeTemporary := func() error {
		if temporary == nil {
			return nil
		}
		err := temporary.Close()
		temporary = nil
		return err
	}
	failTemporary := func(operation string, cause error) ([]byte, error) {
		_ = closeTemporary()
		return nil, fmt.Errorf("%s temporary FAIRY master key file: %w", operation, cause)
	}

	if err := temporary.Chmod(0o600); err != nil {
		return failTemporary("securing", err)
	}
	if err := writeAll(temporary, generated); err != nil {
		return failTemporary("writing", err)
	}
	if err := temporary.Sync(); err != nil {
		return failTemporary("syncing", err)
	}
	if err := closeTemporary(); err != nil {
		return nil, fmt.Errorf("closing temporary FAIRY master key file: %w", err)
	}

	// A hard-link publishes an already-complete same-filesystem file while
	// retaining O_EXCL-like no-overwrite semantics. Rename cannot provide both
	// properties portably and could replace a key published by another startup.
	if err := os.Link(temporaryPath, keyPath); err != nil {
		if errors.Is(err, fs.ErrExist) {
			// Remove our unpublished inode and synchronize the directory before
			// observing the winner. This sync also makes the winner's key name
			// durable even if it is still completing its own cleanup.
			removeErr := os.Remove(temporaryPath)
			if removeErr == nil || errors.Is(removeErr, fs.ErrNotExist) {
				removeTemporary = false
			}
			syncErr := syncPrivateDirectory(secretsDir)
			if cleanupErr := errors.Join(
				wrapMasterKeyFileError("removing unpublished temporary FAIRY master key file", removeErr),
				wrapMasterKeyFileError("syncing FAIRY master key directory after concurrent publication", syncErr),
			); cleanupErr != nil {
				return nil, cleanupErr
			}

			key, found, readErr := waitForPublishedMasterKey(secretsDir, keyPath)
			if readErr != nil {
				return nil, readErr
			}
			if !found {
				return nil, ErrMasterKeyFileInvalid
			}
			// The winning publisher may have removed its temporary hard link
			// after our first sync. Synchronize once more after observing nlink=1
			// so returning from the concurrent path implies a settled directory.
			if err := syncPrivateDirectory(secretsDir); err != nil {
				clear(key)
				return nil, fmt.Errorf("syncing FAIRY master key directory after publication settled: %w", err)
			}
			return key, nil
		}
		return nil, fmt.Errorf("publishing FAIRY master key file: %w", err)
	}

	// Persist the published name before removing the temporary name. Then
	// persist the cleanup separately. This preserves at least one durable name
	// for the complete inode across every crash boundary.
	publishSyncErr := syncPrivateDirectory(secretsDir)
	removeErr := os.Remove(temporaryPath)
	if removeErr == nil || errors.Is(removeErr, fs.ErrNotExist) {
		removeTemporary = false
	}
	cleanupSyncErr := syncPrivateDirectory(secretsDir)
	if settleErr := errors.Join(
		wrapMasterKeyFileError("syncing published FAIRY master key file", publishSyncErr),
		wrapMasterKeyFileError("removing temporary FAIRY master key file", removeErr),
		wrapMasterKeyFileError("syncing FAIRY master key directory after cleanup", cleanupSyncErr),
	); settleErr != nil {
		return nil, settleErr
	}

	key, found, err := readPublishedMasterKey(keyPath)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, ErrMasterKeyFileInvalid
	}
	return key, nil
}

func waitForPublishedMasterKey(secretsDir, path string) ([]byte, bool, error) {
	// The winning process publishes with a hard link and then removes its
	// temporary name. Only wait when that other name is one of FAIRY's private
	// publication temporaries and points at the same inode; arbitrary extra hard
	// links are invalid rather than evidence of an in-progress publication.
	publishing, err := hasMasterKeyPublicationTemporary(secretsDir, path)
	if err != nil {
		return nil, false, err
	}
	if !publishing {
		// The temporary name can disappear after the earlier nlink stat but
		// before this scan. Re-read the published inode once; nlink=1 is the
		// settled winner, while nlink>1 without a FAIRY temporary is invalid.
		return readPublishedMasterKey(path)
	}
	for range 4096 {
		key, found, err := readPublishingMasterKey(path)
		if !errors.Is(err, errMasterKeyPublicationInProgress) {
			if err != nil || !found {
				return key, found, err
			}
			publishing, checkErr := hasMasterKeyPublicationTemporary(secretsDir, path)
			if checkErr != nil {
				clear(key)
				return nil, false, checkErr
			}
			if !publishing {
				return key, found, nil
			}
			clear(key)
		}
		runtime.Gosched()
	}
	return nil, false, errMasterKeyPublicationInProgress
}

func hasMasterKeyPublicationTemporary(secretsDir, keyPath string) (bool, error) {
	keyInfo, err := os.Lstat(keyPath)
	if err != nil {
		return false, fmt.Errorf("inspecting published FAIRY master key file: %w", err)
	}
	entries, err := os.ReadDir(secretsDir)
	if err != nil {
		return false, fmt.Errorf("listing FAIRY master key directory: %w", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if name == secretMasterKeyFilename || !isMasterKeyTemporaryName(name) {
			continue
		}
		info, err := os.Lstat(filepath.Join(secretsDir, name))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return false, fmt.Errorf("inspecting FAIRY master key publication temporary: %w", err)
		}
		if info.Mode().IsRegular() && os.SameFile(keyInfo, info) {
			return true, nil
		}
	}
	return false, nil
}

func isMasterKeyTemporaryName(name string) bool {
	const prefix = ".master-key-"
	const suffix = ".tmp"
	return len(name) > len(prefix)+len(suffix) &&
		name[:len(prefix)] == prefix && name[len(name)-len(suffix):] == suffix
}

func wrapMasterKeyFileError(operation string, err error) error {
	if err == nil || errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return fmt.Errorf("%s: %w", operation, err)
}

func writeAll(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		written, err := writer.Write(data)
		if err != nil {
			return err
		}
		if written <= 0 {
			return io.ErrShortWrite
		}
		data = data[written:]
	}
	return nil
}
