package vectorindex

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	EnvURL     = "FAIRY_QDRANT_URL"
	EnvAPIKey  = "FAIRY_QDRANT_API_KEY"
	EnvTimeout = "FAIRY_QDRANT_TIMEOUT"

	CollectionName = "fairy_memory_v1"
	Dimensions     = 512
	Distance       = "Cosine"

	DefaultTimeout = 5 * time.Second
)

var (
	ErrURLRequired = errors.New("FAIRY_QDRANT_URL is required")
	ErrWhitespace  = errors.New("qdrant configuration values must not contain leading or trailing whitespace")
)

type Config struct {
	URL            string
	APIKey         string
	Timeout        time.Duration
	CollectionName string
}

type Descriptor struct {
	Scheme     string `json:"scheme"`
	Host       string `json:"host"`
	Collection string `json:"collection"`
}

func ConfigFromEnv(getenv func(string) string) (Config, error) {
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	config := Config{
		URL:            getenv(EnvURL),
		APIKey:         getenv(EnvAPIKey),
		Timeout:        DefaultTimeout,
		CollectionName: CollectionName,
	}
	if err := exactValue(config.URL); err != nil {
		return Config{}, err
	}
	if config.URL == "" {
		return Config{}, ErrURLRequired
	}
	if _, err := parseEndpoint(config.URL); err != nil {
		return Config{}, err
	}
	if err := exactValue(config.APIKey); err != nil {
		return Config{}, err
	}
	if raw := getenv(EnvTimeout); raw != "" {
		if err := exactValue(raw); err != nil {
			return Config{}, err
		}
		timeout, err := time.ParseDuration(raw)
		if err != nil {
			return Config{}, fmt.Errorf("%s must be a duration", EnvTimeout)
		}
		if timeout <= 0 {
			return Config{}, fmt.Errorf("%s must be greater than zero", EnvTimeout)
		}
		config.Timeout = timeout
	}
	return config, nil
}

func (c Config) Descriptor() (Descriptor, error) {
	endpoint, err := parseEndpoint(c.URL)
	if err != nil {
		return Descriptor{}, err
	}
	return Descriptor{Scheme: endpoint.scheme, Host: endpoint.hostPort, Collection: c.collectionName()}, nil
}

func (c Config) RedactedURL() string {
	parsed, err := url.Parse(c.URL)
	if err != nil {
		return "[invalid qdrant URL]"
	}
	if parsed.User != nil {
		parsed.User = url.User("redacted")
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}

func (c Config) collectionName() string {
	if c.CollectionName != "" {
		return c.CollectionName
	}
	return CollectionName
}

func exactValue(value string) error {
	if strings.TrimSpace(value) != value {
		return ErrWhitespace
	}
	return nil
}

type endpoint struct {
	scheme   string
	host     string
	hostPort string
	port     int
	useTLS   bool
}

func parseEndpoint(raw string) (endpoint, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return endpoint{}, errors.New("qdrant URL is invalid")
	}
	switch parsed.Scheme {
	case "http", "grpc":
	case "https", "grpcs":
	default:
		return endpoint{}, fmt.Errorf("qdrant URL scheme %q is not supported", parsed.Scheme)
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return endpoint{}, errors.New("qdrant URL must not include query or fragment")
	}
	port := 6334
	if rawPort := parsed.Port(); rawPort != "" {
		value, err := strconv.Atoi(rawPort)
		if err != nil || value < 1 || value > 65535 {
			return endpoint{}, errors.New("qdrant URL port is invalid")
		}
		port = value
	}
	host := parsed.Hostname()
	if host == "" || strings.TrimSpace(host) != host {
		return endpoint{}, errors.New("qdrant URL host is invalid")
	}
	return endpoint{scheme: parsed.Scheme, host: host, hostPort: parsed.Host, port: port, useTLS: parsed.Scheme == "https" || parsed.Scheme == "grpcs"}, nil
}
