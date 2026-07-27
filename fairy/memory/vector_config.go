package memory

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	VectorEnvURL     = "FAIRY_QDRANT_URL"
	VectorEnvAPIKey  = "FAIRY_QDRANT_API_KEY"
	VectorEnvTimeout = "FAIRY_QDRANT_TIMEOUT"

	VectorCollectionName = "fairy_memory_v1"
	VectorDimensions     = 512
	VectorDistance       = "Cosine"

	VectorDefaultTimeout = 5 * time.Second
)

var (
	ErrVectorURLRequired      = errors.New("FAIRY_QDRANT_URL is required")
	ErrVectorConfigWhitespace = errors.New("qdrant configuration values must not contain leading or trailing whitespace")
)

type VectorConfig struct {
	URL            string
	APIKey         string
	Timeout        time.Duration
	CollectionName string
}

type VectorDescriptor struct {
	Scheme     string `json:"scheme"`
	Host       string `json:"host"`
	Collection string `json:"collection"`
}

func VectorConfigFromEnv(getenv func(string) string) (VectorConfig, error) {
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	config := VectorConfig{
		URL:            getenv(VectorEnvURL),
		APIKey:         getenv(VectorEnvAPIKey),
		Timeout:        VectorDefaultTimeout,
		CollectionName: VectorCollectionName,
	}
	if err := exactValue(config.URL); err != nil {
		return VectorConfig{}, err
	}
	if config.URL == "" {
		return VectorConfig{}, ErrVectorURLRequired
	}
	if _, err := parseEndpoint(config.URL); err != nil {
		return VectorConfig{}, err
	}
	if err := exactValue(config.APIKey); err != nil {
		return VectorConfig{}, err
	}
	if raw := getenv(VectorEnvTimeout); raw != "" {
		if err := exactValue(raw); err != nil {
			return VectorConfig{}, err
		}
		timeout, err := time.ParseDuration(raw)
		if err != nil {
			return VectorConfig{}, fmt.Errorf("%s must be a duration", VectorEnvTimeout)
		}
		if timeout <= 0 {
			return VectorConfig{}, fmt.Errorf("%s must be greater than zero", VectorEnvTimeout)
		}
		config.Timeout = timeout
	}
	return config, nil
}

func (c VectorConfig) Descriptor() (VectorDescriptor, error) {
	endpoint, err := parseEndpoint(c.URL)
	if err != nil {
		return VectorDescriptor{}, err
	}
	return VectorDescriptor{Scheme: endpoint.scheme, Host: endpoint.hostPort, Collection: c.collectionName()}, nil
}

func (c VectorConfig) RedactedURL() string {
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

func (c VectorConfig) collectionName() string {
	if c.CollectionName != "" {
		return c.CollectionName
	}
	return VectorCollectionName
}

func exactValue(value string) error {
	if strings.TrimSpace(value) != value {
		return ErrVectorConfigWhitespace
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
