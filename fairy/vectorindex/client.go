package vectorindex

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/qdrant/go-client/qdrant"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type Client struct {
	config  Config
	client  *qdrant.Client
	metrics *clientMetrics
}

type Health struct {
	Ready      bool
	Version    string
	Collection string
}

func (c *Client) Collection() string {
	if c == nil {
		return ""
	}
	return c.config.collectionName()
}

func (c *Client) Descriptor() (Descriptor, error) {
	if c == nil || c.client == nil {
		return Descriptor{}, fmt.Errorf("qdrant client is not open")
	}
	return c.config.Descriptor()
}

func Open(ctx context.Context, config Config) (*Client, error) {
	endpoint, err := parseEndpoint(config.URL)
	if err != nil {
		return nil, err
	}
	if err := exactValue(config.APIKey); err != nil {
		return nil, err
	}
	if config.Timeout <= 0 {
		config.Timeout = DefaultTimeout
	}
	if config.CollectionName == "" {
		config.CollectionName = CollectionName
	}
	qdrantClient, err := qdrant.NewClient(&qdrant.Config{
		Host:                endpoint.host,
		Port:                endpoint.port,
		APIKey:              config.APIKey,
		UseTLS:              endpoint.useTLS,
		PoolSize:            1,
		VersionCheckTimeout: config.Timeout,
	})
	if err != nil {
		return nil, sanitizeError("open qdrant client", config, err)
	}
	wrapped := &Client{config: config, client: qdrantClient, metrics: newClientMetrics()}
	if _, err := wrapped.Health(ctx); err != nil {
		_ = qdrantClient.Close()
		return nil, err
	}
	return wrapped, nil
}

func (c *Client) Close() error {
	if c == nil || c.client == nil {
		return nil
	}
	return c.client.Close()
}

func (c *Client) Health(ctx context.Context) (health Health, err error) {
	started := time.Now()
	defer func() { c.observe("health", started, err) }()
	if c == nil || c.client == nil {
		return Health{}, fmt.Errorf("qdrant client is not open")
	}
	queryCtx, cancel := c.queryContext(ctx)
	defer cancel()
	reply, err := c.client.HealthCheck(queryCtx)
	if err != nil {
		return Health{}, sanitizeError("qdrant health check", c.config, err)
	}
	return Health{Ready: true, Version: reply.GetVersion(), Collection: c.config.collectionName()}, nil
}

func (c *Client) queryContext(parent context.Context) (context.Context, context.CancelFunc) {
	if c == nil || c.config.Timeout <= 0 {
		return context.WithCancel(parent)
	}
	return context.WithTimeout(parent, c.config.Timeout)
}

func sanitizeError(action string, config Config, err error) error {
	if err == nil {
		return nil
	}
	message := err.Error()
	for _, secret := range []string{config.APIKey, config.URL} {
		if secret != "" {
			message = strings.ReplaceAll(message, secret, "[REDACTED]")
		}
	}
	if config.URL != "" {
		message = strings.ReplaceAll(message, config.RedactedURL(), "[REDACTED_URL]")
	}
	if grpcStatus, ok := status.FromError(err); ok {
		return fmt.Errorf("%s failed: code=%s message=%s", action, grpcStatus.Code(), message)
	}
	return fmt.Errorf("%s failed: %s", action, message)
}

func isNotFound(err error) bool {
	grpcStatus, ok := status.FromError(err)
	return ok && grpcStatus.Code() == codes.NotFound
}
