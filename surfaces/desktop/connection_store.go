package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

const (
	defaultCoreEndpoint         = "http://127.0.0.1:8787"
	maxConnectionFileSize int64 = 64 << 10
)

var errConnectionNotFound = errors.New("Desktop Core connection is not configured")

type desktopConnection struct {
	Endpoint    string `json:"endpoint"`
	EndpointKey string `json:"endpointKey"`
	Token       string `json:"token"`
}

type connectionStore interface {
	Load() (desktopConnection, error)
	Save(desktopConnection) error
}

type fileConnectionStore struct {
	path string
}

type failedConnectionStore struct {
	err error
}

func newSystemConnectionStore() connectionStore {
	configRoot, err := os.UserConfigDir()
	if err != nil {
		return failedConnectionStore{err: fmt.Errorf("resolve Desktop connection directory: %w", err)}
	}
	return fileConnectionStore{
		path: filepath.Join(configRoot, "dev.rinai.fairy", "desktop", "v1", "connection.json"),
	}
}

func (s failedConnectionStore) Load() (desktopConnection, error) {
	return desktopConnection{}, s.err
}

func (s failedConnectionStore) Save(desktopConnection) error {
	return s.err
}

func (s fileConnectionStore) Load() (desktopConnection, error) {
	fileInfo, err := os.Lstat(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return desktopConnection{}, errConnectionNotFound
	}
	if err != nil {
		return desktopConnection{}, fmt.Errorf("inspect Desktop connection file: %w", err)
	}
	if err := validateConnectionMetadata(s.path, fileInfo, false); err != nil {
		return desktopConnection{}, err
	}

	directory := filepath.Dir(s.path)
	directoryInfo, err := os.Lstat(directory)
	if err != nil {
		return desktopConnection{}, fmt.Errorf("inspect Desktop connection directory: %w", err)
	}
	if err := validateConnectionMetadata(directory, directoryInfo, true); err != nil {
		return desktopConnection{}, err
	}
	if fileInfo.Size() > maxConnectionFileSize {
		return desktopConnection{}, errors.New("Desktop connection file exceeds the size limit")
	}

	file, err := os.Open(s.path)
	if err != nil {
		return desktopConnection{}, fmt.Errorf("open Desktop connection file: %w", err)
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil {
		return desktopConnection{}, fmt.Errorf("inspect opened Desktop connection file: %w", err)
	}
	if !os.SameFile(fileInfo, openedInfo) {
		return desktopConnection{}, errors.New("Desktop connection file changed while opening")
	}

	decoder := json.NewDecoder(io.LimitReader(file, maxConnectionFileSize+1))
	decoder.DisallowUnknownFields()
	var connection desktopConnection
	if err := decoder.Decode(&connection); err != nil {
		return desktopConnection{}, fmt.Errorf("decode Desktop connection file: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return desktopConnection{}, errors.New("decode Desktop connection file: trailing JSON value")
		}
		return desktopConnection{}, fmt.Errorf("decode Desktop connection file: %w", err)
	}
	return validateDesktopConnection(connection)
}

func (s fileConnectionStore) Save(connection desktopConnection) error {
	connection, err := validateDesktopConnection(connection)
	if err != nil {
		return err
	}

	directory := filepath.Dir(s.path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create Desktop connection directory: %w", err)
	}
	directoryInfo, err := os.Lstat(directory)
	if err != nil {
		return fmt.Errorf("inspect Desktop connection directory: %w", err)
	}
	if !directoryInfo.IsDir() {
		return errors.New("Desktop connection directory is not a directory")
	}
	if err := validateConnectionOwner(directory, directoryInfo); err != nil {
		return err
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return fmt.Errorf("restrict Desktop connection directory permissions: %w", err)
	}

	if targetInfo, err := os.Lstat(s.path); err == nil {
		if !targetInfo.Mode().IsRegular() {
			return errors.New("Desktop connection path is not a regular file")
		}
		if err := validateConnectionOwner(s.path, targetInfo); err != nil {
			return err
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("inspect Desktop connection file: %w", err)
	}

	temporary, err := os.CreateTemp(directory, ".connection-*.tmp")
	if err != nil {
		return fmt.Errorf("create Desktop connection temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	keepTemporary := true
	defer func() {
		_ = temporary.Close()
		if keepTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()

	if err := temporary.Chmod(0o600); err != nil {
		return fmt.Errorf("restrict Desktop connection temporary file permissions: %w", err)
	}
	encoder := json.NewEncoder(temporary)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(connection); err != nil {
		return fmt.Errorf("encode Desktop connection file: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync Desktop connection temporary file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close Desktop connection temporary file: %w", err)
	}
	if err := os.Rename(temporaryPath, s.path); err != nil {
		return fmt.Errorf("replace Desktop connection file: %w", err)
	}
	keepTemporary = false

	writtenInfo, err := os.Lstat(s.path)
	if err != nil {
		return fmt.Errorf("inspect saved Desktop connection file: %w", err)
	}
	if err := validateConnectionMetadata(s.path, writtenInfo, false); err != nil {
		return err
	}
	directoryHandle, err := os.Open(directory)
	if err != nil {
		return fmt.Errorf("open Desktop connection directory for sync: %w", err)
	}
	defer directoryHandle.Close()
	if err := directoryHandle.Sync(); err != nil {
		return fmt.Errorf("sync Desktop connection directory: %w", err)
	}
	return nil
}

func validateDesktopConnection(connection desktopConnection) (desktopConnection, error) {
	endpoint, err := validateEndpoint(connection.Endpoint)
	if err != nil {
		return desktopConnection{}, err
	}
	if !installationKeyPattern.MatchString(connection.EndpointKey) {
		return desktopConnection{}, errors.New("installation key is invalid")
	}
	if connection.Token == "" {
		return desktopConnection{}, errors.New("Core token must not be empty")
	}
	if connection.Token != strings.TrimSpace(connection.Token) {
		return desktopConnection{}, errors.New("Core token must contain no surrounding whitespace")
	}
	connection.Endpoint = endpoint
	return connection, nil
}

func validateConnectionMetadata(path string, info fs.FileInfo, directory bool) error {
	if directory {
		if !info.IsDir() {
			return errors.New("Desktop connection directory is not a directory")
		}
		if info.Mode().Perm() != 0o700 {
			return fmt.Errorf("Desktop connection directory %q must use mode 0700", path)
		}
	} else {
		if !info.Mode().IsRegular() {
			return errors.New("Desktop connection path is not a regular file")
		}
		if info.Mode().Perm() != 0o600 {
			return fmt.Errorf("Desktop connection file %q must use mode 0600", path)
		}
	}
	return validateConnectionOwner(path, info)
}

func validateConnectionOwner(path string, info fs.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("Desktop connection path %q has unsupported owner metadata", path)
	}
	if int(stat.Uid) != os.Getuid() {
		return fmt.Errorf("Desktop connection path %q must be owned by the current user", path)
	}
	return nil
}
