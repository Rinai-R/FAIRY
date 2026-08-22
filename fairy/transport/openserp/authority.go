// Package openserp owns the only Web outbound network capability available to
// the endpoint-strict runtime. Chat and semantic embedding use separately
// declared provider transports and never receive this general Web capability.
package openserp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	MaxSearchHits           = 5
	MaxSearchQueryRunes     = 512
	MaxHealthResponseBytes  = 64 << 10
	MaxSearchResponseBytes  = 2 << 20
	MaxExtractResponseBytes = 1 << 20
	MaxTargetURLBytes       = 2048
	requestTimeout          = 30 * time.Second
	dialTimeout             = 10 * time.Second
)

var (
	ErrOriginInvalid    = errors.New("openserp origin is invalid")
	ErrCapabilityDenied = errors.New("openserp capability denied")
	ErrRedirectDenied   = errors.New("openserp redirect denied")
	ErrAddressPolicy    = errors.New("openserp address policy rejected destination")
	ErrResponseTooLarge = errors.New("openserp response exceeds limit")
)

type Response struct {
	StatusCode  int
	ContentType string
	Body        []byte
}

type lookupIPAddrFunc func(context.Context, string) ([]net.IPAddr, error)
type dialContextFunc func(context.Context, string, string) (net.Conn, error)

// Authority exposes bounded OpenSERP operations without handing callers a
// general-purpose HTTP client. The first successful address resolution is
// pinned; later connections must resolve to the same address set.
type Authority struct {
	origin    url.URL
	host      string
	port      string
	client    *http.Client
	transport *http.Transport
	dialer    *pinnedDialer
}

type pinnedDialer struct {
	host   string
	port   string
	lookup lookupIPAddrFunc
	dial   dialContextFunc

	mu     sync.Mutex
	pinned map[string]struct{}
}

func NewAuthority(rawOrigin string) (*Authority, error) {
	resolver := net.DefaultResolver
	dialer := &net.Dialer{Timeout: dialTimeout, KeepAlive: 30 * time.Second}
	return newAuthority(rawOrigin, resolver.LookupIPAddr, dialer.DialContext)
}

func newAuthority(rawOrigin string, lookup lookupIPAddrFunc, dial dialContextFunc) (*Authority, error) {
	origin, host, port, err := parseOrigin(rawOrigin)
	if err != nil {
		return nil, err
	}
	if lookup == nil || dial == nil {
		return nil, fmt.Errorf("%w: network resolver and dialer are required", ErrOriginInvalid)
	}
	if port == "" {
		if origin.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	pinned := &pinnedDialer{host: host, port: port, lookup: lookup, dial: dial}
	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           pinned.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          4,
		MaxIdleConnsPerHost:   2,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 15 * time.Second,
	}
	client := &http.Client{
		Transport: transport,
		Timeout:   requestTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return ErrRedirectDenied
		},
	}
	return &Authority{origin: origin, host: host, port: port, client: client, transport: transport, dialer: pinned}, nil
}

func (a *Authority) Origin() string {
	if a == nil {
		return ""
	}
	return a.origin.String()
}

func (a *Authority) Close() {
	if a != nil && a.transport != nil {
		a.transport.CloseIdleConnections()
	}
}

func (a *Authority) Health(ctx context.Context) (Response, error) {
	return a.get(ctx, "/health", nil, MaxHealthResponseBytes)
}

func (a *Authority) Search(ctx context.Context, query string, limit int) (Response, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return Response{}, fmt.Errorf("%w: search query is empty", ErrCapabilityDenied)
	}
	if !utf8.ValidString(query) || utf8.RuneCountInString(query) > MaxSearchQueryRunes {
		return Response{}, fmt.Errorf("%w: search query exceeds %d runes", ErrCapabilityDenied, MaxSearchQueryRunes)
	}
	if limit <= 0 || limit > MaxSearchHits {
		return Response{}, fmt.Errorf("%w: search limit must be between 1 and %d", ErrCapabilityDenied, MaxSearchHits)
	}
	values := url.Values{}
	values.Set("text", query)
	values.Set("limit", strconv.Itoa(limit))
	return a.get(ctx, "/mega/search", values, MaxSearchResponseBytes)
}

// Extract asks OpenSERP to fetch and clean a public result URL. The target URL
// remains request data; FAIRY only connects to the pinned OpenSERP origin.
func (a *Authority) Extract(ctx context.Context, target string) (Response, error) {
	target = strings.TrimSpace(target)
	if target == "" || len(target) > MaxTargetURLBytes {
		return Response{}, fmt.Errorf("%w: extraction target is invalid", ErrCapabilityDenied)
	}
	parsed, err := url.ParseRequestURI(target)
	if err != nil || parsed == nil || parsed.User != nil || parsed.Hostname() == "" || parsed.Fragment != "" ||
		parsed.String() != target || parsed.Scheme != "http" && parsed.Scheme != "https" {
		return Response{}, fmt.Errorf("%w: extraction target is invalid", ErrCapabilityDenied)
	}
	values := url.Values{}
	values.Set("url", target)
	values.Set("mode", "auto")
	values.Set("clean", "true")
	values.Set("use_llms_txt", "false")
	values.Set("format", "text")
	return a.get(ctx, "/extract", values, MaxExtractResponseBytes)
}

func (a *Authority) get(ctx context.Context, path string, query url.Values, maxResponseBytes int64) (Response, error) {
	if a == nil || a.client == nil {
		return Response{}, fmt.Errorf("%w: authority is unavailable", ErrCapabilityDenied)
	}
	if ctx == nil {
		return Response{}, fmt.Errorf("%w: request context is required", ErrCapabilityDenied)
	}
	if err := ctx.Err(); err != nil {
		return Response{}, err
	}
	if path != "/health" && path != "/mega/search" && path != "/extract" {
		return Response{}, fmt.Errorf("%w: path is not allowed", ErrCapabilityDenied)
	}
	requestURL := a.origin
	requestURL.Path = path
	requestURL.RawPath = ""
	requestURL.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL.String(), nil)
	if err != nil {
		return Response{}, err
	}
	response, err := a.client.Do(request)
	if err != nil {
		return Response{}, err
	}
	defer response.Body.Close()
	body, err := readBounded(response.Body, maxResponseBytes)
	if err != nil {
		return Response{}, err
	}
	return Response{
		StatusCode:  response.StatusCode,
		ContentType: response.Header.Get("Content-Type"),
		Body:        body,
	}, nil
}

func parseOrigin(raw string) (url.URL, string, string, error) {
	raw = strings.TrimSpace(raw)
	parsed, err := url.ParseRequestURI(raw)
	if err != nil || parsed == nil {
		return url.URL{}, "", "", fmt.Errorf("%w: malformed URL", ErrOriginInvalid)
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return url.URL{}, "", "", fmt.Errorf("%w: scheme must be http or https", ErrOriginInvalid)
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return url.URL{}, "", "", fmt.Errorf("%w: origin must not contain credentials, path, query, or fragment", ErrOriginInvalid)
	}
	host := strings.ToLower(strings.TrimSpace(parsed.Hostname()))
	if host == "" || strings.IndexFunc(host, func(r rune) bool { return r > 127 || r <= 32 }) >= 0 {
		return url.URL{}, "", "", fmt.Errorf("%w: host must be non-empty ASCII", ErrOriginInvalid)
	}
	port := parsed.Port()
	if port != "" {
		value, err := strconv.Atoi(port)
		if err != nil || value <= 0 || value > 65535 {
			return url.URL{}, "", "", fmt.Errorf("%w: port is invalid", ErrOriginInvalid)
		}
	}
	hostPort := host
	if strings.Contains(host, ":") {
		hostPort = "[" + host + "]"
	}
	if port != "" {
		hostPort = net.JoinHostPort(host, port)
	}
	return url.URL{Scheme: parsed.Scheme, Host: hostPort}, host, port, nil
}

func readBounded(reader io.Reader, limit int64) ([]byte, error) {
	if reader == nil || limit <= 0 {
		return nil, fmt.Errorf("%w: invalid response reader or limit", ErrCapabilityDenied)
	}
	body, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, ErrResponseTooLarge
	}
	return body, nil
}

func (d *pinnedDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if ctx == nil {
		return nil, fmt.Errorf("%w: dial context is required", ErrAddressPolicy)
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("%w: malformed dial address", ErrAddressPolicy)
	}
	if !strings.EqualFold(strings.Trim(host, "[]"), d.host) || port != d.effectivePort() {
		return nil, fmt.Errorf("%w: dial target differs from configured origin", ErrAddressPolicy)
	}
	addresses, err := d.resolve(ctx)
	if err != nil {
		return nil, err
	}
	var dialErr error
	for _, ip := range addresses {
		connection, err := d.dial(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return connection, nil
		}
		dialErr = errors.Join(dialErr, err)
	}
	if dialErr == nil {
		dialErr = ErrAddressPolicy
	}
	return nil, dialErr
}

func (d *pinnedDialer) effectivePort() string {
	return d.port
}

func (d *pinnedDialer) resolve(ctx context.Context) ([]net.IP, error) {
	if literal := net.ParseIP(d.host); literal != nil {
		return d.pin([]net.IP{literal})
	}
	resolved, err := d.lookup(ctx, d.host)
	if err != nil {
		return nil, fmt.Errorf("%w: resolving configured origin: %v", ErrAddressPolicy, err)
	}
	addresses := make([]net.IP, 0, len(resolved))
	for _, candidate := range resolved {
		ip := candidate.IP
		if ip == nil || ip.IsUnspecified() || ip.IsMulticast() {
			continue
		}
		addresses = append(addresses, append(net.IP(nil), ip...))
	}
	return d.pin(addresses)
}

func (d *pinnedDialer) pin(addresses []net.IP) ([]net.IP, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(addresses) == 0 {
		return nil, fmt.Errorf("%w: origin resolved to no usable address", ErrAddressPolicy)
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
		return nil, fmt.Errorf("%w: origin address changed after pinning", ErrAddressPolicy)
	}
	return permitted, nil
}
