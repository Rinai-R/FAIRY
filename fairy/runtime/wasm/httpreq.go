package wasm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"fairy/plugin"
)

type httpRequestPayload struct {
	Method     string `json:"method"`
	URL        string `json:"url"`
	Body       string `json:"body"`
	Credential string `json:"credential"`
}

type httpResponseBody struct {
	Status      int    `json:"status"`
	ContentType string `json:"contentType"`
	Body        string `json:"body"`
}

func (h *Host) HTTPRequest(ctx context.Context, grant Grant, payload json.RawMessage) ([]byte, error) {
	if h == nil {
		return nil, ErrHostClosed
	}
	h.mu.Lock()
	closed := h.closed
	client := h.http
	h.mu.Unlock()
	if closed || client == nil {
		return nil, ErrHostClosed
	}
	return doHTTPRequest(ctx, client, grant, payload, func(message string) string { return message })
}

func HTTPRequestGrantFromURL(raw string, max uint32) (*HTTPRequestGrant, error) {
	return HTTPRequestGrantFromURLMethods(raw, max, http.MethodGet)
}

func HTTPRequestGrantFromURLMethods(raw string, max uint32, methods ...string) (*HTTPRequestGrant, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return nil, fmt.Errorf("%w: http.request url is invalid", ErrInvalidGrant)
	}
	port := parsed.Port()
	if port == "" {
		if parsed.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	n, err := strconv.Atoi(port)
	if err != nil || n <= 0 || n > 65535 {
		return nil, fmt.Errorf("%w: http.request port is invalid", ErrInvalidGrant)
	}
	if len(methods) == 0 {
		methods = []string{http.MethodGet}
	}
	grant := &HTTPRequestGrant{
		Scheme:           parsed.Scheme,
		Host:             parsed.Hostname(),
		Port:             uint16(n),
		Methods:          append([]string(nil), methods...),
		MaxResponseBytes: max,
	}
	if err := grant.validate(); err != nil {
		return nil, err
	}
	return grant, nil
}

func (i *Instance) httpRequest(ctx context.Context, grant Grant, payload json.RawMessage) ([]byte, error) {
	if i == nil || i.host == nil {
		return nil, ErrHostClosed
	}
	return doHTTPRequest(ctx, i.host.http, grant, payload, i.scrub)
}

func doHTTPRequest(ctx context.Context, client *http.Client, grant Grant, payload json.RawMessage, scrub func(string) string) ([]byte, error) {
	if client == nil {
		return nil, ErrHostClosed
	}
	if scrub == nil {
		scrub = func(message string) string { return message }
	}
	if grant.HTTPRequest == nil {
		return nil, capabilityDenied("http.request", "not granted")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var req httpRequestPayload
	if err := decoder.Decode(&req); err != nil {
		return nil, capabilityDenied("http.request", "payload is invalid")
	}
	if req.Method == "" || req.URL == "" {
		return nil, capabilityDenied("http.request", "method and url are required")
	}
	parsed, err := url.Parse(req.URL)
	if err != nil {
		return nil, capabilityDenied("http.request", "url is invalid")
	}
	if !grant.HTTPRequest.allows(parsed, req.Method) {
		return nil, capabilityDenied("http.request", "target is not authorized")
	}

	httpReq, err := http.NewRequestWithContext(ctx, req.Method, parsed.String(), strings.NewReader(req.Body))
	if err != nil {
		return nil, capabilityDenied("http.request", "request could not be constructed")
	}
	if req.Credential != "" {
		secret, ok := grant.credential(req.Credential)
		if !ok {
			return nil, capabilityDenied("http.request", "credential handle is not authorized")
		}
		httpReq.Header.Set("Authorization", "Bearer "+secret)
	}

	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, coded(plugin.CodeModuleTrap, "http.request: "+scrub(err.Error()))
	}
	defer resp.Body.Close()
	limited := io.LimitReader(resp.Body, int64(grant.HTTPRequest.MaxResponseBytes)+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, coded(plugin.CodeModuleTrap, "http.request: "+scrub(err.Error()))
	}
	if uint32(len(body)) > grant.HTTPRequest.MaxResponseBytes {
		return nil, coded(plugin.CodeBudgetExceeded, "http.request response exceeds budget")
	}
	encoded, err := json.Marshal(httpResponseBody{
		Status:      resp.StatusCode,
		ContentType: resp.Header.Get("Content-Type"),
		Body:        string(body),
	})
	if err != nil {
		return nil, coded(plugin.CodeModuleTrap, "http.request result could not be encoded")
	}
	return marshalHostResult(hostResult{OK: true, Body: encoded})
}

func (g *HTTPRequestGrant) allows(target *url.URL, method string) bool {
	if g == nil || target == nil {
		return false
	}
	if target.Scheme != g.Scheme {
		return false
	}
	host := target.Hostname()
	port := target.Port()
	if port == "" {
		if target.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	wantPort := strconv.Itoa(int(g.Port))
	if g.Port == 0 {
		if g.Scheme == "https" {
			wantPort = "443"
		} else {
			wantPort = "80"
		}
	}
	if host == "" || !strings.EqualFold(host, g.Host) || port != wantPort {
		return false
	}
	for _, allowed := range g.Methods {
		if strings.EqualFold(allowed, method) {
			return true
		}
	}
	return false
}
