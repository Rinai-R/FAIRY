// Package seekdb owns FAIRY's in-process SeekDB engine and SQL connection.
package seekdb

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	EnvLibrary       = "FAIRY_SEEKDB_LIBRARY"
	EnvDataDir       = "FAIRY_SEEKDB_DATA_DIR"
	EnvDatabase      = "FAIRY_SEEKDB_DATABASE"
	EnvConnectLimit  = "FAIRY_SEEKDB_CONNECT_TIMEOUT"
	EnvStartLimit    = "FAIRY_SEEKDB_START_TIMEOUT"
	EnvQueryLimit    = "FAIRY_SEEKDB_QUERY_TIMEOUT"
	EnvShutdownLimit = "FAIRY_SEEKDB_SHUTDOWN_TIMEOUT"
	EnvMaxOpenConns  = "FAIRY_SEEKDB_MAX_OPEN_CONNS"
	EnvMaxIdleConns  = "FAIRY_SEEKDB_MAX_IDLE_CONNS"

	DefaultDatabase      = "fairy"
	DefaultConnectLimit  = 5 * time.Second
	DefaultStartLimit    = 30 * time.Second
	DefaultQueryLimit    = 15 * time.Second
	DefaultShutdownLimit = 10 * time.Second
	DefaultMaxOpenConns  = 8
	DefaultMaxIdleConns  = 4
	maximumOpenConns     = 64

	// Deprecated environment names kept only so leftover developer env fails closed
	// instead of being silently honored as a subprocess path.
	EnvBinaryPath  = "FAIRY_SEEKDB_BINARY"
	EnvLibraryPath = "FAIRY_SEEKDB_LIBRARY_PATH"
	EnvAddress     = "FAIRY_SEEKDB_ADDRESS"
	EnvUser        = "FAIRY_SEEKDB_USER"
)

var (
	ErrLibraryRequired      = errors.New("FAIRY_SEEKDB_LIBRARY is required")
	ErrDataDirRequired      = errors.New("FAIRY_SEEKDB_DATA_DIR is required")
	ErrUncleanValue         = errors.New("SeekDB configuration values must not contain surrounding whitespace")
	ErrDeprecatedProcessEnv = errors.New("legacy SeekDB process environment is not supported")
	identifierPattern       = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,63}$`)
)

type Config struct {
	LibraryPath   string
	DataDir       string
	Database      string
	ConnectLimit  time.Duration
	StartLimit    time.Duration
	QueryLimit    time.Duration
	ShutdownLimit time.Duration
	MaxOpenConns  int
	MaxIdleConns  int
}

type Descriptor struct {
	Engine   string `json:"engine"`
	Database string `json:"database"`
}

func ProfileGetenv(configRoot string, getenv func(string) string) func(string) string {
	if getenv == nil {
		getenv = os.Getenv
	}
	return func(name string) string {
		if value := getenv(name); value != "" {
			return value
		}
		if name == EnvDataDir && configRoot != "" {
			return filepath.Join(filepath.Clean(configRoot), "seekdb")
		}
		return ""
	}
}

func ConfigFromEnv(getenv func(string) string) (Config, error) {
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	for _, name := range []string{EnvBinaryPath, EnvLibraryPath, EnvAddress, EnvUser} {
		if raw := getenv(name); raw != "" {
			return Config{}, fmt.Errorf("%s: %w", name, ErrDeprecatedProcessEnv)
		}
	}
	config := Config{
		LibraryPath:   getenv(EnvLibrary),
		DataDir:       getenv(EnvDataDir),
		Database:      valueOrDefault(getenv(EnvDatabase), DefaultDatabase),
		ConnectLimit:  DefaultConnectLimit,
		StartLimit:    DefaultStartLimit,
		QueryLimit:    DefaultQueryLimit,
		ShutdownLimit: DefaultShutdownLimit,
		MaxOpenConns:  DefaultMaxOpenConns,
		MaxIdleConns:  DefaultMaxIdleConns,
	}
	var err error
	if raw := getenv(EnvConnectLimit); raw != "" {
		config.ConnectLimit, err = parsePositiveDuration(EnvConnectLimit, raw)
		if err != nil {
			return Config{}, err
		}
	}
	if raw := getenv(EnvStartLimit); raw != "" {
		config.StartLimit, err = parsePositiveDuration(EnvStartLimit, raw)
		if err != nil {
			return Config{}, err
		}
	}
	if raw := getenv(EnvQueryLimit); raw != "" {
		config.QueryLimit, err = parsePositiveDuration(EnvQueryLimit, raw)
		if err != nil {
			return Config{}, err
		}
	}
	if raw := getenv(EnvShutdownLimit); raw != "" {
		config.ShutdownLimit, err = parsePositiveDuration(EnvShutdownLimit, raw)
		if err != nil {
			return Config{}, err
		}
	}
	if raw := getenv(EnvMaxOpenConns); raw != "" {
		config.MaxOpenConns, err = parseConnectionCount(EnvMaxOpenConns, raw)
		if err != nil {
			return Config{}, err
		}
	}
	if raw := getenv(EnvMaxIdleConns); raw != "" {
		config.MaxIdleConns, err = parseConnectionCount(EnvMaxIdleConns, raw)
		if err != nil {
			return Config{}, err
		}
	}
	if config.LibraryPath == "" {
		if located, locateErr := LocateLibrary(); locateErr == nil {
			config.LibraryPath = located
		}
	}
	if err := config.Validate(); err != nil {
		return Config{}, err
	}
	return config, nil
}

func (c Config) Validate() error {
	values := []struct {
		name  string
		value string
	}{
		{EnvLibrary, c.LibraryPath},
		{EnvDataDir, c.DataDir},
		{EnvDatabase, c.Database},
	}
	for _, item := range values {
		if item.value != strings.TrimSpace(item.value) {
			return fmt.Errorf("%s: %w", item.name, ErrUncleanValue)
		}
		if strings.ContainsRune(item.value, 0) {
			return fmt.Errorf("%s must not contain NUL", item.name)
		}
	}
	if c.LibraryPath != "" {
		if err := validateAbsoluteCleanPath(EnvLibrary, c.LibraryPath, false); err != nil {
			return err
		}
	}
	if c.DataDir == "" {
		return ErrDataDirRequired
	}
	if err := validateAbsoluteCleanPath(EnvDataDir, c.DataDir, true); err != nil {
		return err
	}
	if !identifierPattern.MatchString(c.Database) {
		return fmt.Errorf("%s must be a portable SQL identifier", EnvDatabase)
	}
	limits := []struct {
		name  string
		value time.Duration
	}{
		{EnvConnectLimit, c.ConnectLimit},
		{EnvStartLimit, c.StartLimit},
		{EnvQueryLimit, c.QueryLimit},
		{EnvShutdownLimit, c.ShutdownLimit},
	}
	for _, limit := range limits {
		if limit.value <= 0 {
			return fmt.Errorf("%s must be greater than zero", limit.name)
		}
	}
	if c.MaxOpenConns < 1 || c.MaxOpenConns > maximumOpenConns {
		return fmt.Errorf("%s must be between 1 and %d", EnvMaxOpenConns, maximumOpenConns)
	}
	if c.MaxIdleConns < 0 || c.MaxIdleConns > c.MaxOpenConns {
		return fmt.Errorf("%s must be between 0 and %s", EnvMaxIdleConns, EnvMaxOpenConns)
	}
	return nil
}

func (c Config) requireLibrary() error {
	if c.LibraryPath == "" {
		return ErrLibraryRequired
	}
	return validateAbsoluteCleanPath(EnvLibrary, c.LibraryPath, false)
}

func (c Config) Descriptor() Descriptor {
	engine := "seekdb"
	if c.LibraryPath != "" {
		engine = filepath.Base(c.LibraryPath)
	}
	return Descriptor{Engine: engine, Database: c.Database}
}

func validateAbsoluteCleanPath(name, value string, rejectRoot bool) error {
	if !filepath.IsAbs(value) {
		return fmt.Errorf("%s must be absolute", name)
	}
	if filepath.Clean(value) != value {
		return fmt.Errorf("%s must be clean", name)
	}
	if rejectRoot && filepath.Dir(value) == value {
		return fmt.Errorf("%s must not be a filesystem root", name)
	}
	return nil
}

func parsePositiveDuration(name, raw string) (time.Duration, error) {
	if raw != strings.TrimSpace(raw) {
		return 0, fmt.Errorf("%s: %w", name, ErrUncleanValue)
	}
	value, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be a duration", name)
	}
	if value <= 0 {
		return 0, fmt.Errorf("%s must be greater than zero", name)
	}
	return value, nil
}

func parseConnectionCount(name, raw string) (int, error) {
	if raw != strings.TrimSpace(raw) {
		return 0, fmt.Errorf("%s: %w", name, ErrUncleanValue)
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer", name)
	}
	return value, nil
}

func valueOrDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
