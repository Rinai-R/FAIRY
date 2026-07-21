package cmd

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"fairy-surfaces/qq-onebot/bridge"
)

func setValidEnv(t *testing.T) {
	t.Helper()
	t.Setenv(envCoreEndpoint, "http://127.0.0.1:8787")
	t.Setenv(envCoreToken, "exact-core-token")
	t.Setenv(envOneBotEndpoint, "ws://127.0.0.1:3001")
	t.Setenv(envOneBotToken, "exact-onebot-token")
	t.Setenv(envOneBotSelfID, "10001")
	t.Setenv(envOneBotGroups, "20001,20002")
}

func TestDoctorUsesTypedExactEnvironment(t *testing.T) {
	setValidEnv(t)
	var got bridge.Config
	var output bytes.Buffer
	err := Execute(context.Background(), []string{"doctor"}, Dependencies{Doctor: func(_ context.Context, cfg bridge.Config) error { got = cfg; return nil }}, &output)
	if err != nil || output.String() != "doctor: ok\n" {
		t.Fatalf("Execute = %v output=%q", err, output.String())
	}
	if got.CoreToken != "exact-core-token" || len(got.GroupAllowlist) != 2 {
		t.Fatalf("config = %#v", got)
	}
}

func TestConfigRejectsUnsafeRemoteAndMissingAllowlist(t *testing.T) {
	setValidEnv(t)
	t.Setenv(envCoreEndpoint, "http://core.example.com")
	if _, err := ConfigFromEnv(); err == nil || !strings.Contains(err.Error(), "requires https") {
		t.Fatalf("unsafe Core error = %v", err)
	}
	setValidEnv(t)
	t.Setenv(envOneBotGroups, "")
	if _, err := ConfigFromEnv(); err == nil || !strings.Contains(err.Error(), "allowlist") {
		t.Fatalf("allowlist error = %v", err)
	}
}

func TestTokenWhitespaceIsNotTrimmedAndSecretsHaveNoFlags(t *testing.T) {
	setValidEnv(t)
	t.Setenv(envCoreToken, " token ")
	cfg, err := ConfigFromEnv()
	if err != nil || cfg.CoreToken != " token " {
		t.Fatalf("exact token = %q error=%v", cfg.CoreToken, err)
	}
	root := NewRoot(Dependencies{}, &bytes.Buffer{})
	if root.Flags().Lookup("token") != nil || root.PersistentFlags().Lookup("token") != nil {
		t.Fatal("secret token flag exists")
	}
}
