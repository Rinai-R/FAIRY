package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"fairy/api"
	fairyruntime "fairy/runtime"
	"go.uber.org/zap"
)

func TestStatusAndAuth(t *testing.T) {
	root := t.TempDir()
	rt, err := fairyruntime.Open(fairyruntime.Options{ConfigRoot: root, Logger: zap.NewNop()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rt.Close() })

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	srv, err := api.NewServer(rt, api.Options{Addr: addr, Token: "secret", Logger: zap.NewNop()})
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Run() }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})
	waitHTTP(t, "http://"+addr+"/v1/status")

	res, err := http.Get("http://" + addr + "/v1/status")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status without token = %d", res.StatusCode)
	}

	req, _ := http.NewRequest(http.MethodGet, "http://"+addr+"/v1/status", nil)
	req.Header.Set("Authorization", "Bearer secret")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("status = %d body=%s", res.StatusCode, body)
	}
	var payload map[string]any
	if err := json.NewDecoder(res.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload["configRoot"] != root {
		t.Fatalf("payload = %#v", payload)
	}
}

func TestOpenSessionRequiresActiveCharacter(t *testing.T) {
	root := t.TempDir()
	rt, err := fairyruntime.Open(fairyruntime.Options{ConfigRoot: root, Logger: zap.NewNop()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rt.Close() })

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	srv, err := api.NewServer(rt, api.Options{Addr: addr, Logger: zap.NewNop()})
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Run() }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})
	waitHTTP(t, "http://"+addr+"/v1/status")

	res, err := http.Post("http://"+addr+"/v1/sessions", "application/json", bytes.NewReader([]byte("{}")))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusConflict {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("sessions = %d body=%s", res.StatusCode, body)
	}
}

func waitHTTP(t *testing.T, url string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		res, err := http.Get(url)
		if err == nil {
			res.Body.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("server not ready for %s", url)
}
