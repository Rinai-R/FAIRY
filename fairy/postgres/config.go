// Package postgres owns PostgreSQL runtime configuration, pooling,
// migrations, and health checks. Domain packages consume the pool through
// explicit dependencies; this package does not contain memory business logic.
package postgres

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	EnvDatabaseURL    = "FAIRY_DATABASE_URL"
	EnvMaxConns       = "FAIRY_DB_MAX_CONNS"
	EnvMinConns       = "FAIRY_DB_MIN_CONNS"
	EnvConnectTimeout = "FAIRY_DB_CONNECT_TIMEOUT"
	EnvQueryTimeout   = "FAIRY_DB_QUERY_TIMEOUT"

	DefaultMaxConns       int32         = 20
	DefaultMinConns       int32         = 2
	DefaultConnectTimeout time.Duration = 5 * time.Second
	DefaultQueryTimeout   time.Duration = 15 * time.Second

	maxPoolConns int32 = 200
)

var (
	ErrDatabaseURLRequired = errors.New("FAIRY_DATABASE_URL is required")
	ErrWhitespace          = errors.New("database configuration values must not contain leading or trailing whitespace")
)

type Config struct {
	URL            string
	MaxConns       int32
	MinConns       int32
	ConnectTimeout time.Duration
	QueryTimeout   time.Duration
}

type Descriptor struct {
	Scheme   string `json:"scheme"`
	Host     string `json:"host"`
	Database string `json:"database"`
}

func ConfigFromEnv(getenv func(string) string) (Config, error) {
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	config := Config{
		URL:            getenv(EnvDatabaseURL),
		MaxConns:       DefaultMaxConns,
		MinConns:       DefaultMinConns,
		ConnectTimeout: DefaultConnectTimeout,
		QueryTimeout:   DefaultQueryTimeout,
	}
	if err := exactValue(config.URL); err != nil {
		return Config{}, err
	}
	if config.URL == "" {
		return Config{}, ErrDatabaseURLRequired
	}
	var err error
	if raw := getenv(EnvMaxConns); raw != "" {
		config.MaxConns, err = parseConns(EnvMaxConns, raw)
		if err != nil {
			return Config{}, err
		}
	}
	if raw := getenv(EnvMinConns); raw != "" {
		config.MinConns, err = parseConns(EnvMinConns, raw)
		if err != nil {
			return Config{}, err
		}
	}
	if config.MaxConns < 1 || config.MaxConns > maxPoolConns {
		return Config{}, fmt.Errorf("%s must be between 1 and %d", EnvMaxConns, maxPoolConns)
	}
	if config.MinConns < 0 || config.MinConns > config.MaxConns {
		return Config{}, fmt.Errorf("%s must be between 0 and %s", EnvMinConns, EnvMaxConns)
	}
	if raw := getenv(EnvConnectTimeout); raw != "" {
		config.ConnectTimeout, err = parsePositiveDuration(EnvConnectTimeout, raw)
		if err != nil {
			return Config{}, err
		}
	}
	if raw := getenv(EnvQueryTimeout); raw != "" {
		config.QueryTimeout, err = parsePositiveDuration(EnvQueryTimeout, raw)
		if err != nil {
			return Config{}, err
		}
	}
	return config, nil
}

func (c Config) Descriptor() (Descriptor, error) {
	parsed, err := url.Parse(c.URL)
	if err != nil {
		return Descriptor{}, errors.New("database URL is invalid")
	}
	database := strings.TrimPrefix(parsed.Path, "/")
	return Descriptor{Scheme: parsed.Scheme, Host: parsed.Host, Database: database}, nil
}

func (c Config) RedactedURL() string {
	parsed, err := url.Parse(c.URL)
	if err != nil {
		return "[invalid database URL]"
	}
	if parsed.User != nil {
		parsed.User = url.User("redacted")
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}

func exactValue(value string) error {
	if value != strings.TrimSpace(value) {
		return ErrWhitespace
	}
	return nil
}

func parseConns(name, raw string) (int32, error) {
	if err := exactValue(raw); err != nil {
		return 0, err
	}
	value, err := strconv.ParseInt(raw, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer", name)
	}
	return int32(value), nil
}

func parsePositiveDuration(name, raw string) (time.Duration, error) {
	if err := exactValue(raw); err != nil {
		return 0, err
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
