package main

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const (
	healthcheckCoreToken   = "core-health-secret-fixture"
	healthcheckOneBotToken = "onebot-health-secret-fixture"
	healthcheckQQID        = int64(9876543210)
)

func TestRunReadinessCheck(t *testing.T) {
	core := newReadyCoreServer(t)
	defer core.Close()
	oneBot := newOneBotReadinessServer(t, http.StatusOK, fmt.Sprintf(`{"status":"ok","retcode":0,"data":{"user_id":%d,"nickname":"private-nickname"}}`, healthcheckQQID))
	defer oneBot.Close()
	listener := newReadinessListener(t)
	defer listener.Close()

	if err := runReadinessCheck(t.Context(), readinessConfig(core.URL, oneBot.URL, listener.Addr().String())); err != nil {
		t.Fatalf("runReadinessCheck: %v", err)
	}
}

func TestRunReadinessCheckClassifiesFailuresWithoutLeakingDependencyData(t *testing.T) {
	readyCore := newReadyCoreServer(t)
	defer readyCore.Close()
	readyOneBot := newOneBotReadinessServer(t, http.StatusOK, fmt.Sprintf(`{"status":"ok","retcode":0,"data":{"user_id":%d}}`, healthcheckQQID))
	defer readyOneBot.Close()
	listener := newReadinessListener(t)
	defer listener.Close()

	failedCore := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintf(w, `{"error":"Bearer %s qq=%d"}`, healthcheckCoreToken, healthcheckQQID)
	}))
	defer failedCore.Close()
	closedWebhook := reserveClosedReadinessAddress(t)

	tests := []struct {
		name        string
		coreURL     string
		oneBotURL   string
		webhookAddr string
		want        readinessError
	}{
		{name: "core", coreURL: failedCore.URL, oneBotURL: readyOneBot.URL, webhookAddr: listener.Addr().String(), want: errCoreUnavailable},
		{name: "onebot http", coreURL: readyCore.URL, oneBotURL: newOneBotFailureServer(t, http.StatusUnauthorized, `{"message":"Bearer onebot-health-secret-fixture"}`), webhookAddr: listener.Addr().String(), want: errOneBotUnavailable},
		{name: "onebot action", coreURL: readyCore.URL, oneBotURL: newOneBotFailureServer(t, http.StatusOK, `{"status":"failed","retcode":100,"data":{"user_id":9876543210}}`), webhookAddr: listener.Addr().String(), want: errOneBotUnavailable},
		{name: "onebot account", coreURL: readyCore.URL, oneBotURL: newOneBotFailureServer(t, http.StatusOK, `{"status":"ok","retcode":0,"data":{"user_id":0}}`), webhookAddr: listener.Addr().String(), want: errOneBotUnavailable},
		{name: "onebot oversized", coreURL: readyCore.URL, oneBotURL: newOneBotFailureServer(t, http.StatusOK, strings.Repeat("x", maxOneBotReadinessResponse+1)), webhookAddr: listener.Addr().String(), want: errOneBotUnavailable},
		{name: "webhook", coreURL: readyCore.URL, oneBotURL: readyOneBot.URL, webhookAddr: closedWebhook, want: errWebhookUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := runReadinessCheck(t.Context(), readinessConfig(test.coreURL, test.oneBotURL, test.webhookAddr))
			if err == nil || err.Error() != test.want.Error() {
				t.Fatalf("runReadinessCheck = %v, want %v", err, test.want)
			}
			for _, forbidden := range []string{healthcheckCoreToken, healthcheckOneBotToken, fmt.Sprint(healthcheckQQID), "private-nickname", "Bearer"} {
				if strings.Contains(err.Error(), forbidden) {
					t.Fatalf("readiness error leaked %q: %v", forbidden, err)
				}
			}
		})
	}
}

func TestRunReadinessCheckRecoversWhenWebhookStarts(t *testing.T) {
	core := newReadyCoreServer(t)
	defer core.Close()
	oneBot := newOneBotReadinessServer(t, http.StatusOK, fmt.Sprintf(`{"status":"ok","retcode":0,"data":{"user_id":%d}}`, healthcheckQQID))
	defer oneBot.Close()
	address := reserveClosedReadinessAddress(t)
	cfg := readinessConfig(core.URL, oneBot.URL, address)
	if err := runReadinessCheck(t.Context(), cfg); err == nil || err.Error() != errWebhookUnavailable.Error() {
		t.Fatalf("readiness before listener = %v", err)
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatalf("start recovered listener: %v", err)
	}
	defer listener.Close()
	if err := runReadinessCheck(t.Context(), cfg); err != nil {
		t.Fatalf("readiness after listener recovery: %v", err)
	}
}

func readinessConfig(coreURL, oneBotURL, webhookAddress string) Config {
	return Config{
		CoreEndpoint:          coreURL,
		CoreToken:             healthcheckCoreToken,
		OneBotWebhookEndpoint: "http://" + webhookAddress,
		OneBotAPIEndpoint:     oneBotURL,
		OneBotToken:           healthcheckOneBotToken,
	}
}

func newReadyCoreServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/status" || request.Header.Get("Authorization") != "Bearer "+healthcheckCoreToken {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"bootstrap":{},"configRoot":"/data","webSearch":{},"semanticEmbedding":{},"activeBackgroundJobs":0,"database":{"ready":true,"mode":"production"},"secretKey":{"ready":true,"mode":"production"}}`)
	}))
}

func newOneBotReadinessServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/get_login_info" || request.Method != http.MethodPost ||
			request.Header.Get("Authorization") != "Bearer "+healthcheckOneBotToken || request.Header.Get("Content-Type") != "application/json" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}))
}

func newOneBotFailureServer(t *testing.T, status int, body string) string {
	t.Helper()
	server := newOneBotReadinessServer(t, status, body)
	t.Cleanup(server.Close)
	return server.URL
}

func newReadinessListener(t *testing.T) net.Listener {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return listener
}

func reserveClosedReadinessAddress(t *testing.T) string {
	t.Helper()
	listener := newReadinessListener(t)
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return address
}
