package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fairy/app/edge"
	"fairy/runtime/config"
)

type scriptedManagement struct {
	overview      func(context.Context) (edge.Overview, error)
	characters    func() (edge.CharacterCatalog, error)
	activate      func(string, uint64) (edge.CharacterRecord, error)
	profile       func() (edge.ProfileSnapshot, error)
	saveProfile   func(*string) (edge.ProfileUpdate, error)
	clearProfile  func() (edge.ProfileUpdate, error)
	model         func() (edge.ModelStatus, error)
	saveModel     func(edge.ModelWrite) (edge.ModelStatus, error)
	clearModel    func() (edge.ModelStatus, error)
	semantic      func() (edge.SemanticStatus, error)
	saveSemantic  func(edge.SemanticWrite) (edge.SemanticStatus, error)
	clearSemantic func() (edge.SemanticStatus, error)
	intelligence  func(context.Context) (edge.IntelligenceSnapshot, error)
	memories      func(string) (edge.MemoryCatalog, error)
	createMemory  func(edge.MemoryWrite) (edge.MemoryRecord, error)
	tombstoneMem  func(string) error
	knowledge     func(context.Context) (edge.KnowledgeCatalog, error)
	tombstoneKnow func(context.Context, string) error
	plugins       func() (edge.PluginStatus, error)
	stickers      func(context.Context) (edge.StickerPage, error)
	qq            func() (edge.QQSettings, error)
	saveQQ        func(edge.QQSettings) (edge.QQSettings, error)
	conversation  func(context.Context, string, uint64, int) (edge.MessagePage, error)
	turnRuntime   func(context.Context, string, string) (edge.TurnRuntimeView, error)
	metrics       func(context.Context) (edge.MetricsSnapshot, error)
	traces        func(context.Context, string) (edge.TraceSearch, error)
	trace         func(context.Context, string) (edge.TraceDetail, error)
	logs          func(edge.LogFilter) (edge.LogSnapshot, error)
	subscribeLogs func(edge.LogFilter) ([]edge.LogEntry, <-chan edge.LogEntry, func(), error)
	backupSource  func() (edge.BackupSource, error)
}

func (s scriptedManagement) Overview(ctx context.Context) (edge.Overview, error) {
	if s.overview == nil {
		return edge.Overview{}, edge.ErrManagementUnavailable
	}
	return s.overview(ctx)
}
func (s scriptedManagement) Characters() (edge.CharacterCatalog, error) {
	if s.characters == nil {
		return edge.CharacterCatalog{}, edge.ErrManagementUnavailable
	}
	return s.characters()
}
func (s scriptedManagement) ActivateCharacter(id string, revision uint64) (edge.CharacterRecord, error) {
	if s.activate == nil {
		return edge.CharacterRecord{}, edge.ErrManagementUnavailable
	}
	return s.activate(id, revision)
}
func (s scriptedManagement) Profile() (edge.ProfileSnapshot, error) {
	if s.profile == nil {
		return edge.ProfileSnapshot{}, edge.ErrManagementUnavailable
	}
	return s.profile()
}
func (s scriptedManagement) SaveProfile(name *string) (edge.ProfileUpdate, error) {
	if s.saveProfile == nil {
		return edge.ProfileUpdate{}, edge.ErrManagementUnavailable
	}
	return s.saveProfile(name)
}
func (s scriptedManagement) ClearProfile() (edge.ProfileUpdate, error) {
	if s.clearProfile == nil {
		return edge.ProfileUpdate{}, edge.ErrManagementUnavailable
	}
	return s.clearProfile()
}
func (s scriptedManagement) Model() (edge.ModelStatus, error) {
	if s.model == nil {
		return edge.ModelStatus{}, edge.ErrManagementUnavailable
	}
	return s.model()
}
func (s scriptedManagement) SaveModel(write edge.ModelWrite) (edge.ModelStatus, error) {
	if s.saveModel == nil {
		return edge.ModelStatus{}, edge.ErrManagementUnavailable
	}
	return s.saveModel(write)
}
func (s scriptedManagement) ClearModel() (edge.ModelStatus, error) {
	if s.clearModel == nil {
		return edge.ModelStatus{}, edge.ErrManagementUnavailable
	}
	return s.clearModel()
}
func (s scriptedManagement) Semantic() (edge.SemanticStatus, error) {
	if s.semantic == nil {
		return edge.SemanticStatus{}, edge.ErrManagementUnavailable
	}
	return s.semantic()
}
func (s scriptedManagement) SaveSemantic(write edge.SemanticWrite) (edge.SemanticStatus, error) {
	if s.saveSemantic == nil {
		return edge.SemanticStatus{}, edge.ErrManagementUnavailable
	}
	return s.saveSemantic(write)
}
func (s scriptedManagement) ClearSemanticCredential() (edge.SemanticStatus, error) {
	if s.clearSemantic == nil {
		return edge.SemanticStatus{}, edge.ErrManagementUnavailable
	}
	return s.clearSemantic()
}
func (s scriptedManagement) Intelligence(ctx context.Context) (edge.IntelligenceSnapshot, error) {
	if s.intelligence == nil {
		return edge.IntelligenceSnapshot{}, edge.ErrManagementUnavailable
	}
	return s.intelligence(ctx)
}
func (s scriptedManagement) Memories(id string) (edge.MemoryCatalog, error) {
	if s.memories == nil {
		return edge.MemoryCatalog{}, edge.ErrManagementUnavailable
	}
	return s.memories(id)
}
func (s scriptedManagement) CreateMemory(write edge.MemoryWrite) (edge.MemoryRecord, error) {
	if s.createMemory == nil {
		return edge.MemoryRecord{}, edge.ErrManagementUnavailable
	}
	return s.createMemory(write)
}
func (s scriptedManagement) TombstoneMemory(id string) error {
	if s.tombstoneMem == nil {
		return edge.ErrManagementUnavailable
	}
	return s.tombstoneMem(id)
}
func (s scriptedManagement) Knowledge(ctx context.Context) (edge.KnowledgeCatalog, error) {
	if s.knowledge == nil {
		return edge.KnowledgeCatalog{}, edge.ErrManagementUnavailable
	}
	return s.knowledge(ctx)
}
func (s scriptedManagement) TombstoneKnowledge(ctx context.Context, id string) error {
	if s.tombstoneKnow == nil {
		return edge.ErrManagementUnavailable
	}
	return s.tombstoneKnow(ctx, id)
}
func (s scriptedManagement) Plugins() (edge.PluginStatus, error) {
	if s.plugins == nil {
		return edge.PluginStatus{}, edge.ErrPluginHostUnavailable
	}
	return s.plugins()
}
func (s scriptedManagement) Stickers(ctx context.Context) (edge.StickerPage, error) {
	if s.stickers == nil {
		return edge.StickerPage{}, edge.ErrManagementUnavailable
	}
	return s.stickers(ctx)
}
func (s scriptedManagement) QQ() (edge.QQSettings, error) {
	if s.qq == nil {
		return edge.QQSettings{}, edge.ErrManagementUnavailable
	}
	return s.qq()
}
func (s scriptedManagement) SaveQQ(settings edge.QQSettings) (edge.QQSettings, error) {
	if s.saveQQ == nil {
		return edge.QQSettings{}, edge.ErrManagementUnavailable
	}
	return s.saveQQ(settings)
}
func (s scriptedManagement) Conversation(ctx context.Context, id string, before uint64, limit int) (edge.MessagePage, error) {
	if s.conversation == nil {
		return edge.MessagePage{}, edge.ErrManagementUnavailable
	}
	return s.conversation(ctx, id, before, limit)
}
func (s scriptedManagement) TurnRuntime(ctx context.Context, conversationID, turnID string) (edge.TurnRuntimeView, error) {
	if s.turnRuntime == nil {
		return edge.TurnRuntimeView{}, edge.ErrManagementUnavailable
	}
	return s.turnRuntime(ctx, conversationID, turnID)
}
func (s scriptedManagement) Metrics(ctx context.Context) (edge.MetricsSnapshot, error) {
	if s.metrics == nil {
		return edge.MetricsSnapshot{}, edge.ErrObservabilityUnavailable
	}
	return s.metrics(ctx)
}
func (s scriptedManagement) Traces(ctx context.Context, messageID string) (edge.TraceSearch, error) {
	if s.traces == nil {
		return edge.TraceSearch{}, edge.ErrObservabilityUnavailable
	}
	return s.traces(ctx, messageID)
}
func (s scriptedManagement) Trace(ctx context.Context, traceID string) (edge.TraceDetail, error) {
	if s.trace == nil {
		return edge.TraceDetail{}, edge.ErrObservabilityUnavailable
	}
	return s.trace(ctx, traceID)
}
func (s scriptedManagement) Logs(filter edge.LogFilter) (edge.LogSnapshot, error) {
	if s.logs == nil {
		return edge.LogSnapshot{}, edge.ErrObservabilityUnavailable
	}
	return s.logs(filter)
}
func (s scriptedManagement) SubscribeLogs(filter edge.LogFilter) ([]edge.LogEntry, <-chan edge.LogEntry, func(), error) {
	if s.subscribeLogs == nil {
		return nil, nil, nil, edge.ErrObservabilityUnavailable
	}
	return s.subscribeLogs(filter)
}
func (s scriptedManagement) BackupSource() (edge.BackupSource, error) {
	if s.backupSource == nil {
		return edge.BackupSource{}, edge.ErrManagementUnavailable
	}
	return s.backupSource()
}

func TestManagementBindingsFailClosedWithoutEdge(t *testing.T) {
	service := NewCoreService()
	if _, err := service.ManagementOverview(); err == nil || !strings.Contains(err.Error(), "edge runtime is not started") {
		t.Fatalf("ManagementOverview() error = %v", err)
	}
	if _, err := service.ManagementPlugins(); err == nil || !strings.Contains(err.Error(), "edge runtime is not started") {
		t.Fatalf("ManagementPlugins() error = %v", err)
	}
	if _, err := service.CreateManagementBackup(); err == nil || !strings.Contains(err.Error(), "edge runtime is not started") {
		t.Fatalf("CreateManagementBackup() error = %v", err)
	}
}

func TestManagementModelWriteDoesNotEchoCredential(t *testing.T) {
	secret := "sk-desktop-management-secret"
	var stored string
	host := scriptedManagement{
		saveModel: func(write edge.ModelWrite) (edge.ModelStatus, error) {
			stored = write.APIKey
			return edge.ModelStatus{Configured: true, Protocol: write.Protocol, Model: write.Model, AuthMode: write.AuthMode}, nil
		},
		model: func() (edge.ModelStatus, error) {
			return edge.ModelStatus{Configured: true, Protocol: "openai_compatible_api", Model: "test", AuthMode: "bearer_key"}, nil
		},
		plugins: func() (edge.PluginStatus, error) {
			return edge.PluginStatus{}, edge.ErrPluginHostUnavailable
		},
	}
	service := NewCoreService()
	service.edge = &fakeOwnedRuntime{host: host}

	status, err := service.SaveManagementModel(edge.ModelWrite{
		ModelConnectionInput: config.ModelConnectionInput{Protocol: "openai_compatible_api", Model: "test", AuthMode: "bearer_key"},
		APIKey:               secret,
	})
	if err != nil {
		t.Fatal(err)
	}
	if stored != secret {
		t.Fatalf("host received %q, want credential", stored)
	}
	assertNoCredential(t, status, secret)
	read, err := service.ManagementModel()
	if err != nil {
		t.Fatal(err)
	}
	assertNoCredential(t, read, secret)
	if _, err := service.ManagementPlugins(); !errors.Is(err, edge.ErrPluginHostUnavailable) {
		t.Fatalf("ManagementPlugins() error = %v, want plugin host unavailable", err)
	}
}

func TestManagementLogsEventDoesNotCarryBearer(t *testing.T) {
	live := make(chan edge.LogEntry, 1)
	host := scriptedManagement{
		subscribeLogs: func(edge.LogFilter) ([]edge.LogEntry, <-chan edge.LogEntry, func(), error) {
			return []edge.LogEntry{{Sequence: 1, Message: "runtime ready", Level: "info"}}, live, func() { close(live) }, nil
		},
	}
	service := NewCoreService()
	service.edge = &fakeOwnedRuntime{host: host}
	var payloads []any
	service.attachEmitter(func(_ string, payload any) {
		payloads = append(payloads, payload)
	})
	if err := service.SubscribeManagementLogs(); err != nil {
		t.Fatal(err)
	}
	defer service.UnsubscribeManagementLogs()
	if len(payloads) != 1 {
		t.Fatalf("backlog events = %d, want 1", len(payloads))
	}
	assertNoCredential(t, payloads[0], "Bearer", "FAIRY_API_TOKEN", "127.0.0.1:8787")
}

func TestCreateManagementBackupCopiesDataDirWithoutEchoingContents(t *testing.T) {
	secret := "sk-backup-file-secret"
	dataDir := t.TempDir()
	configRoot := t.TempDir()
	if err := os.Chmod(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(configRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "store.dat"), []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	host := scriptedManagement{
		backupSource: func() (edge.BackupSource, error) {
			return edge.BackupSource{ConfigRoot: configRoot, DataDir: dataDir}, nil
		},
	}
	service := NewCoreService()
	service.edge = &fakeOwnedRuntime{host: host}
	backup, err := service.CreateManagementBackup()
	if err != nil {
		t.Fatal(err)
	}
	if backup.FileCount != 1 || backup.Path == "" {
		t.Fatalf("backup = %#v", backup)
	}
	assertNoCredential(t, backup, secret)
	copied, err := os.ReadFile(filepath.Join(backup.Path, "store.dat"))
	if err != nil {
		t.Fatal(err)
	}
	if string(copied) != secret {
		t.Fatalf("copied contents = %q", copied)
	}
}

func TestManagementMetricsFailClosedWhenHostRejects(t *testing.T) {
	service := NewCoreService()
	service.edge = &fakeOwnedRuntime{host: scriptedManagement{}}
	if _, err := service.ManagementMetrics(); !errors.Is(err, edge.ErrObservabilityUnavailable) {
		t.Fatalf("ManagementMetrics() error = %v, want observability unavailable", err)
	}
}

func assertNoCredential(t *testing.T, value any, secrets ...string) {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, secret := range secrets {
		if strings.Contains(text, secret) {
			t.Fatalf("payload contained %q: %s", secret, text)
		}
	}
	for _, forbidden := range []string{"apiKey", "FAIRY_API_TOKEN", "Authorization", "Bearer "} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("payload contained %q: %s", forbidden, text)
		}
	}
}
