package model

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"fairy/runtime/config"
)

var (
	ErrProviderOriginInvalid  = errors.New("model provider origin is invalid")
	ErrProviderRedirectDenied = errors.New("model provider redirect denied")
	ErrProviderAddressDenied  = errors.New("model provider address policy rejected destination")
)

type providerLookupFunc func(context.Context, string) ([]net.IPAddr, error)
type providerDialFunc func(context.Context, string, string) (net.Conn, error)
type endpointProviderClientFactory func(string, time.Duration) (*http.Client, error)

// endpointProviderClient creates an HTTP client bound to one saved provider
// host. It deliberately ignores HTTP(S)_PROXY, rejects redirects, and pins the
// first resolved address set so a response cannot expand the configured
// authority through proxy, redirect, or DNS rebinding behavior.
func endpointProviderClient(rawEndpoint string, timeout time.Duration) (*http.Client, error) {
	resolver := net.DefaultResolver
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	return newEndpointProviderClient(rawEndpoint, timeout, resolver.LookupIPAddr, dialer.DialContext)
}

func newEndpointProviderClient(rawEndpoint string, timeout time.Duration, lookup providerLookupFunc, dial providerDialFunc) (*http.Client, error) {
	host, port, err := providerEndpointAddress(rawEndpoint)
	if err != nil {
		return nil, err
	}
	if timeout <= 0 {
		return nil, fmt.Errorf("%w: request timeout must be positive", ErrProviderOriginInvalid)
	}
	if lookup == nil || dial == nil {
		return nil, fmt.Errorf("%w: resolver and dialer are required", ErrProviderOriginInvalid)
	}
	pinned := &providerPinnedDialer{host: host, port: port, lookup: lookup, dial: dial}
	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           pinned.DialContext,
		ForceAttemptHTTP2:     true,
		DisableKeepAlives:     true,
		MaxConnsPerHost:       4,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
	}
	return &http.Client{
		Transport: transport,
		Timeout:   timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return ErrProviderRedirectDenied
		},
	}, nil
}

func providerEndpointAddress(rawEndpoint string) (string, string, error) {
	if err := config.ValidateEndpointStrictProviderURL(rawEndpoint); err != nil {
		return "", "", fmt.Errorf("%w: %v", ErrProviderOriginInvalid, err)
	}
	parsed, err := url.ParseRequestURI(strings.TrimSpace(rawEndpoint))
	if err != nil || parsed == nil {
		return "", "", fmt.Errorf("%w: malformed URL", ErrProviderOriginInvalid)
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", "", fmt.Errorf("%w: scheme must be http or https", ErrProviderOriginInvalid)
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", "", fmt.Errorf("%w: endpoint must not contain credentials, query, or fragment", ErrProviderOriginInvalid)
	}
	host := strings.ToLower(strings.TrimSpace(parsed.Hostname()))
	if host == "" || strings.IndexFunc(host, func(r rune) bool { return r > 127 || r <= 32 }) >= 0 {
		return "", "", fmt.Errorf("%w: host must be non-empty ASCII", ErrProviderOriginInvalid)
	}
	port := parsed.Port()
	if port == "" {
		if parsed.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	} else if value, err := strconv.Atoi(port); err != nil || value <= 0 || value > 65535 {
		return "", "", fmt.Errorf("%w: port is invalid", ErrProviderOriginInvalid)
	}
	return host, port, nil
}

type providerPinnedDialer struct {
	host   string
	port   string
	lookup providerLookupFunc
	dial   providerDialFunc

	mu     sync.Mutex
	pinned map[string]struct{}
}

func (d *providerPinnedDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if ctx == nil {
		return nil, fmt.Errorf("%w: dial context is required", ErrProviderAddressDenied)
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("%w: malformed dial address", ErrProviderAddressDenied)
	}
	if !strings.EqualFold(strings.Trim(host, "[]"), d.host) || port != d.port {
		return nil, fmt.Errorf("%w: dial target differs from saved provider", ErrProviderAddressDenied)
	}
	addresses, err := d.resolve(ctx)
	if err != nil {
		return nil, err
	}
	var joined error
	for _, ip := range addresses {
		connection, err := d.dial(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return connection, nil
		}
		joined = errors.Join(joined, err)
	}
	if joined == nil {
		joined = ErrProviderAddressDenied
	}
	return nil, joined
}

func (d *providerPinnedDialer) resolve(ctx context.Context) ([]net.IP, error) {
	if literal := net.ParseIP(d.host); literal != nil {
		return d.pin([]net.IP{literal})
	}
	resolved, err := d.lookup(ctx, d.host)
	if err != nil {
		return nil, fmt.Errorf("%w: resolving saved provider", ErrProviderAddressDenied)
	}
	addresses := make([]net.IP, 0, len(resolved))
	for _, candidate := range resolved {
		if candidate.IP == nil || candidate.IP.IsUnspecified() || candidate.IP.IsLoopback() ||
			candidate.IP.IsLinkLocalUnicast() || candidate.IP.IsLinkLocalMulticast() || candidate.IP.IsMulticast() {
			continue
		}
		addresses = append(addresses, append(net.IP(nil), candidate.IP...))
	}
	return d.pin(addresses)
}

func (d *providerPinnedDialer) pin(addresses []net.IP) ([]net.IP, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(addresses) == 0 {
		return nil, fmt.Errorf("%w: provider resolved to no usable address", ErrProviderAddressDenied)
	}
	if d.pinned == nil {
		d.pinned = make(map[string]struct{}, len(addresses))
		for _, ip := range addresses {
			d.pinned[ip.String()] = struct{}{}
		}
	}
	permitted := make([]net.IP, 0, len(addresses))
	for _, ip := range addresses {
		if _, ok := d.pinned[ip.String()]; ok {
			permitted = append(permitted, ip)
		}
	}
	if len(permitted) == 0 {
		return nil, fmt.Errorf("%w: provider address changed after pinning", ErrProviderAddressDenied)
	}
	return permitted, nil
}
