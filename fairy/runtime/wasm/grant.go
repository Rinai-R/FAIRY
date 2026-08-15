package wasm

import (
	"errors"
	"fmt"
	"strings"

	"fairy/plugin"
)

var ErrInvalidGrant = errors.New("plugin capability grant is invalid")

// Grant is the per-instance authorization set. Zero value denies every capability.
// Manifest capability declarations never become grants.
type Grant struct {
	HTTPRequest *HTTPRequestGrant
	HTTPIngress *HTTPIngressGrant
	State       bool
	Timer       bool
	Event       bool
	Action      bool
	Tool        bool
}

type HTTPRequestGrant struct {
	Scheme           string
	Host             string
	Port             uint16
	Methods          []string
	MaxResponseBytes uint32
	credentials      map[string]string
}

type HTTPIngressGrant struct {
	MaxBodyBytes uint32
}

func (g *HTTPRequestGrant) SetCredential(handle, secret string) error {
	if g == nil {
		return ErrInvalidGrant
	}
	if handle == "" || secret == "" {
		return fmt.Errorf("%w: credential handle and secret are required", ErrInvalidGrant)
	}
	if strings.TrimSpace(handle) != handle || strings.TrimSpace(secret) != secret {
		return fmt.Errorf("%w: credential handle and secret must not have surrounding whitespace", ErrInvalidGrant)
	}
	if strings.ContainsAny(handle, " \t\r\n") {
		return fmt.Errorf("%w: credential handle must not contain whitespace", ErrInvalidGrant)
	}
	if g.credentials == nil {
		g.credentials = make(map[string]string)
	}
	g.credentials[handle] = secret
	return nil
}

func (g Grant) validate() error {
	if g.HTTPRequest != nil {
		if err := g.HTTPRequest.validate(); err != nil {
			return err
		}
	}
	if g.HTTPIngress != nil && g.HTTPIngress.MaxBodyBytes == 0 {
		return fmt.Errorf("%w: http.ingress requires MaxBodyBytes", ErrInvalidGrant)
	}
	return nil
}

func (g *HTTPRequestGrant) validate() error {
	if g.Scheme != "http" && g.Scheme != "https" {
		return fmt.Errorf("%w: http.request scheme must be http or https", ErrInvalidGrant)
	}
	if g.Host == "" {
		return fmt.Errorf("%w: http.request host is required", ErrInvalidGrant)
	}
	if len(g.Methods) == 0 {
		return fmt.Errorf("%w: http.request methods are required", ErrInvalidGrant)
	}
	if g.MaxResponseBytes == 0 {
		return fmt.Errorf("%w: http.request MaxResponseBytes is required", ErrInvalidGrant)
	}
	return nil
}

func (g Grant) secrets() []string {
	if g.HTTPRequest == nil {
		return nil
	}
	out := make([]string, 0, len(g.HTTPRequest.credentials))
	for _, secret := range g.HTTPRequest.credentials {
		out = append(out, secret)
	}
	return out
}

func (g Grant) credential(handle string) (string, bool) {
	if g.HTTPRequest == nil || handle == "" {
		return "", false
	}
	secret, ok := g.HTTPRequest.credentials[handle]
	return secret, ok
}

func capabilityDenied(name, reason string) error {
	return coded(plugin.CodeCapabilityDenied, name+": "+reason)
}
