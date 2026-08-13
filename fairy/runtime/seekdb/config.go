// Package seekdb owns FAIRY's local SeekDB process and SQL connection.
package seekdb

import (
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	EnvBinaryPath    = "FAIRY_SEEKDB_BINARY"
	EnvDataDir       = "FAIRY_SEEKDB_DATA_DIR"
	EnvAddress       = "FAIRY_SEEKDB_ADDRESS"
	EnvDatabase      = "FAIRY_SEEKDB_DATABASE"
	EnvUser          = "FAIRY_SEEKDB_USER"
	EnvConnectLimit  = "FAIRY_SEEKDB_CONNECT_TIMEOUT"
	EnvStartLimit    = "FAIRY_SEEKDB_START_TIMEOUT"
	EnvQueryLimit    = "FAIRY_SEEKDB_QUERY_TIMEOUT"
	EnvShutdownLimit = "FAIRY_SEEKDB_SHUTDOWN_TIMEOUT"
	EnvMaxOpenConns  = "FAIRY_SEEKDB_MAX_OPEN_CONNS"
	EnvMaxIdleConns  = "FAIRY_SEEKDB_MAX_IDLE_CONNS"

	DefaultAddress       = "127.0.0.1:2881"
	DefaultDatabase      = "fairy"
	DefaultUser          = "root"
	DefaultConnectLimit  = 5 * time.Second
	DefaultStartLimit    = 30 * time.Second
	DefaultQueryLimit    = 15 * time.Second
	DefaultShutdownLimit = 10 * time.Second
	DefaultMaxOpenConns  = 8
	DefaultMaxIdleConns  = 4
	maximumOpenConns     = 64
)

var (
	ErrBinaryPathRequired = errors.New("FAIRY_SEEKDB_BINARY is required")
	ErrDataDirRequired    = errors.New("FAIRY_SEEKDB_DATA_DIR is required")
	ErrUncleanValue       = errors.New("SeekDB configuration values must not contain surrounding whitespace")
	identifierPattern     = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,63}$`)
)

type Config struct {
	BinaryPath    string
	DataDir       string
	Address       string
	Database      string
	User          string
	Password      string
	ConnectLimit  time.Duration
	StartLimit    time.Duration
	QueryLimit    time.Duration
	ShutdownLimit time.Duration
	MaxOpenConns  int
	MaxIdleConns  int
}

type Descriptor struct {
	BinaryName string `json:"binaryName"`
	Address    string `json:"address"`
	Database   string `json:"database"`
}

func ConfigFromEnv(getenv func(string) string) (Config, error) {
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	config := Config{
		BinaryPath:    getenv(EnvBinaryPath),
		DataDir:       getenv(EnvDataDir),
		Address:       valueOrDefault(getenv(EnvAddress), DefaultAddress),
		Database:      valueOrDefault(getenv(EnvDatabase), DefaultDatabase),
		User:          valueOrDefault(getenv(EnvUser), DefaultUser),
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
		{EnvBinaryPath, c.BinaryPath},
		{EnvDataDir, c.DataDir},
		{EnvAddress, c.Address},
		{EnvDatabase, c.Database},
		{EnvUser, c.User},
	}
	for _, item := range values {
		if item.value != strings.TrimSpace(item.value) {
			return fmt.Errorf("%s: %w", item.name, ErrUncleanValue)
		}
		if strings.ContainsRune(item.value, 0) {
			return fmt.Errorf("%s must not contain NUL", item.name)
		}
	}
	if c.BinaryPath == "" {
		return ErrBinaryPathRequired
	}
	if err := validateAbsoluteCleanPath(EnvBinaryPath, c.BinaryPath, false); err != nil {
		return err
	}
	if c.DataDir == "" {
		return ErrDataDirRequired
	}
	if err := validateAbsoluteCleanPath(EnvDataDir, c.DataDir, true); err != nil {
		return err
	}
	if err := validateLoopbackAddress(c.Address); err != nil {
		return err
	}
	if !identifierPattern.MatchString(c.Database) {
		return fmt.Errorf("%s must be a portable SQL identifier", EnvDatabase)
	}
	if !identifierPattern.MatchString(c.User) {
		return fmt.Errorf("%s must be a portable SQL identifier", EnvUser)
	}
	if strings.ContainsRune(c.Password, 0) {
		return errors.New("SeekDB password must not contain NUL")
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

func (c Config) Descriptor() Descriptor {
	return Descriptor{
		BinaryName: filepath.Base(c.BinaryPath),
		Address:    c.Address,
		Database:   c.Database,
	}
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

func validateLoopbackAddress(address string) error {
	host, rawPort, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("%s must be host:port: %w", EnvAddress, err)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("%s must use a loopback IP literal", EnvAddress)
	}
	port, err := strconv.ParseUint(rawPort, 10, 16)
	if err != nil || port == 0 {
		return fmt.Errorf("%s port must be between 1 and 65535", EnvAddress)
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
