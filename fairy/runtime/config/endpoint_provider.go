package config

import (
	"errors"
	"net"
	"net/url"
	"strings"
)

var ErrEndpointProviderLocal = errors.New("endpoint-strict model provider must not be local")

// ValidateEndpointStrictProviderURL rejects origins that could select a local
// chat or embedding daemon. Non-strict development profiles deliberately do
// not use this policy so their explicit test/local transports remain usable.
// Resolution is checked again by the endpoint-strict transport at dial time.
func ValidateEndpointStrictProviderURL(raw string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if parsed != nil {
		parsed.Scheme = strings.ToLower(parsed.Scheme)
	}
	if err != nil || parsed == nil || parsed.Scheme != "http" && parsed.Scheme != "https" ||
		parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("endpoint-strict model provider must be a valid HTTP(S) URL")
	}
	host := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(parsed.Hostname())), ".")
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return ErrEndpointProviderLocal
	}
	if ip := net.ParseIP(host); ip != nil && endpointStrictProviderIPDenied(ip) {
		return ErrEndpointProviderLocal
	}
	return nil
}

func endpointStrictProviderIPDenied(ip net.IP) bool {
	return ip == nil || ip.IsUnspecified() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast()
}
