//go:build integration

package core

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fairy/runtime/config"
	"fairy/runtime/model"
	"fairy/runtime/seekdb"
	"fairy/runtime/seekdb/seekdbtest"
)

func TestProductionRuntimeCompositionUsesSeekDBWithoutPostgres(t *testing.T) {
	environment := newCoreSeekDBEnvironment(t)
	root := t.TempDir()
	applyCoreSeekDBEnvironment(t, environment)

	rt, err := Open(RuntimeOptions{ConfigRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	if rt.Foundation == nil || rt.MemoryStore == nil || rt.Secret == nil || rt.History == nil || rt.ObservabilityStore == nil {
		t.Fatalf("runtime SeekDB composition incomplete: foundation:%v memory:%v secret:%v history:%v ledger:%v",
			rt.Foundation != nil, rt.MemoryStore != nil, rt.Secret != nil, rt.History != nil, rt.ObservabilityStore != nil)
	}
	if !rt.Secret.Encrypted() {
		t.Fatal("runtime secret store is not encrypted")
	}
	status, err := rt.Foundation.Status(t.Context())
	if err != nil || status.Storage != "seekdb" || status.Schema.State != seekdb.SchemaCurrent {
		t.Fatalf("foundation status = (%#v, %v)", status, err)
	}
	if _, err := rt.MemoryStore.SummaryContext(context.Background()); err != nil {
		t.Fatalf("memory store does not share SeekDB authority: %v", err)
	}
	if _, err := rt.KnowledgeStore.StatsContext(t.Context()); err != nil {
		t.Fatalf("knowledge store does not share SeekDB authority: %v", err)
	}
	for _, path := range []string{"intelligence/fairy.sqlite3", "model/secrets.sqlite3"} {
		if _, err := os.Stat(filepath.Join(root, path)); !os.IsNotExist(err) {
			t.Fatalf("runtime created legacy SQLite path %s: %v", path, err)
		}
	}
	if err := rt.Close(); err != nil {
		t.Fatal(err)
	}
	if err := rt.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	if _, err := rt.Foundation.SQL(); err == nil {
		t.Fatal("closed foundation still exposed SQL")
	}
}

func TestEndpointStrictRuntimeRejectsSavedLoopbackChatAndEmbedding(t *testing.T) {
	environment := newCoreSeekDBEnvironment(t)
	applyCoreSeekDBEnvironment(t, environment)
	t.Setenv(EnvRuntimeProfile, string(ProfileFull))
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")

	rt, err := Open(RuntimeOptions{ConfigRoot: t.TempDir(), Profile: ProfileEndpointStrict})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rt.Close() })
	if rt.RuntimeProfile != ProfileEndpointStrict {
		t.Fatalf("runtime profile = %q", rt.RuntimeProfile)
	}
	modelStatus, err := rt.Config.SaveModelConnection(config.ModelConnectionInput{
		Protocol:            "chat_completions",
		Endpoint:            "http://127.0.0.1:28889",
		Model:               "endpoint-chat",
		ContextWindowTokens: 8192,
		AuthMode:            "no_auth",
	}, nil)
	if err != nil || !modelStatus.Ready {
		t.Fatalf("SaveModelConnection(loopback) = (%#v, %v)", modelStatus, err)
	}
	events, err := rt.Model.ExecuteRequest(model.CompiledPromptRequest{
		Shape: model.ModelRequestShape{
			Lane:            model.PromptLaneRespond,
			Model:           "endpoint-chat",
			Instructions:    "respond",
			MaxOutputTokens: 32,
		},
		Input: []model.PromptItem{{Type: model.PromptItemUserMessage, Content: "hello"}},
	})
	if err == nil || len(events) != 0 || !strings.Contains(strings.ToLower(err.Error()), "local") {
		t.Fatalf("ExecuteRequest(loopback) = (%#v, %v), want local-provider rejection", events, err)
	}

	semanticKey := "sk-endpoint-semantic"
	semanticStatus, err := rt.Config.SaveSemanticEmbeddingSettings(config.SemanticEmbeddingSettings{
		Provider:   config.SemanticEmbeddingProviderOpenAICompatible,
		Enabled:    true,
		Endpoint:   "http://127.0.0.1:28890/v1",
		Model:      "endpoint-embedding",
		Dimensions: config.SemanticEmbeddingDimensions,
	}, &semanticKey)
	if err == nil || semanticStatus.Configured || !strings.Contains(strings.ToLower(err.Error()), "local") {
		t.Fatalf("SaveSemanticEmbeddingSettings(loopback) = (%#v, %v), want local-provider rejection", semanticStatus, err)
	}
}

func TestProductionRuntimeRejectsMissingSeekDBDependencies(t *testing.T) {
	environment := newCoreSeekDBEnvironment(t)
	tests := []struct {
		name   string
		mutate func(*coreSeekDBEnvironment)
		want   string
	}{
		{
			name: "library",
			mutate: func(environment *coreSeekDBEnvironment) {
				environment.values[seekdb.EnvLibrary] = ""
			},
			want: "library",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cloned := environment.clone()
			test.mutate(cloned)
			applyCoreSeekDBEnvironment(t, cloned)
			rt, err := Open(RuntimeOptions{ConfigRoot: t.TempDir()})
			if rt != nil || err == nil || !strings.Contains(strings.ToLower(err.Error()), test.want) {
				t.Fatalf("Open() = (%v, %v), want error containing %q", rt, err, test.want)
			}
		})
	}
}

type coreSeekDBEnvironment struct {
	values map[string]string
}

func (e *coreSeekDBEnvironment) clone() *coreSeekDBEnvironment {
	values := make(map[string]string, len(e.values))
	for key, value := range e.values {
		values[key] = value
	}
	return &coreSeekDBEnvironment{values: values}
}

func newCoreSeekDBEnvironment(t *testing.T) *coreSeekDBEnvironment {
	t.Helper()
	library := os.Getenv(seekdb.EnvLibrary)
	if library == "" {
		t.Skip(seekdb.EnvLibrary + " is not set")
	}
	return &coreSeekDBEnvironment{values: map[string]string{
		seekdb.EnvLibrary:       library,
		seekdb.EnvDataDir:       seekdbtest.DataDir(t),
		seekdb.EnvDatabase:      seekdb.DefaultDatabase,
		seekdb.EnvConnectLimit:  "5s",
		seekdb.EnvStartLimit:    "90s",
		seekdb.EnvQueryLimit:    "15s",
		seekdb.EnvShutdownLimit: "20s",
		seekdb.EnvMaxOpenConns:  "8",
		seekdb.EnvMaxIdleConns:  "4",
		"FAIRY_DATABASE_URL":    "postgres://invalid-legacy-sentinel",
		"FAIRY_PGVECTOR_URL":    "http://invalid-legacy-sentinel",
		"QDRANT_URL":            "http://invalid-legacy-sentinel",
	}}
}

func applyCoreSeekDBEnvironment(t *testing.T, environment *coreSeekDBEnvironment) {
	t.Helper()
	for key, value := range environment.values {
		t.Setenv(key, value)
	}
}

func reserveCoreSeekDBAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return address
}
