package wasm

import (
	"bytes"
	"context"
	"encoding/json"
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

func (i *Instance) httpRequest(ctx context.Context, grant Grant, payload json.RawMessage) ([]byte, error) {
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

	resp, err := i.host.http.Do(httpReq)
	if err != nil {
		return nil, coded(plugin.CodeModuleTrap, "http.request: "+i.scrub(err.Error()))
	}
	defer resp.Body.Close()
	limited := io.LimitReader(resp.Body, int64(grant.HTTPRequest.MaxResponseBytes)+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, coded(plugin.CodeModuleTrap, "http.request: "+i.scrub(err.Error()))
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
