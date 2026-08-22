package config

import (
	"errors"
	"testing"
)

func TestValidateEndpointStrictProviderURLRejectsLocalModelEndpoints(t *testing.T) {
	for _, endpoint := range []string{
		"http://localhost:11434/v1",
		"http://model.localhost/v1",
		"http://127.0.0.1:8080/v1",
		"http://127.200.3.4/v1",
		"http://[::1]:8080/v1",
		"http://[0:0:0:0:0:0:0:1]:8080/v1",
		"http://[::ffff:127.0.0.1]:8080/v1",
		"http://[fe80::1]:8080/v1",
		"http://224.0.0.1:8080/v1",
		"http://[ff02::1]:8080/v1",
		"http://0.0.0.0:8080/v1",
	} {
		if err := ValidateEndpointStrictProviderURL(endpoint); !errors.Is(err, ErrEndpointProviderLocal) {
			t.Fatalf("ValidateEndpointStrictProviderURL(%q) error = %v, want %v", endpoint, err, ErrEndpointProviderLocal)
		}
	}
}

func TestValidateEndpointStrictProviderURLAcceptsExplicitThirdPartyBaseURL(t *testing.T) {
	for _, endpoint := range []string{
		"https://api.example.test/v1",
		"https://192.0.2.10:8443/v1",
	} {
		if err := ValidateEndpointStrictProviderURL(endpoint); err != nil {
			t.Fatalf("ValidateEndpointStrictProviderURL(%q) error = %v", endpoint, err)
		}
	}
}
