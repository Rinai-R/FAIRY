package edge

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"

	"fairy/agent/sticker"
	"fairy/app/core"
	"fairy/context/character"
	"fairy/context/knowledge"
	"fairy/context/memory/personal"
	"fairy/plugin"
	"fairy/plugin/qqonebot"
	"fairy/runtime/config"
	"fairy/runtime/observability"
	"fairy/runtime/seekdb"
	"fairy/transport/session"
	api "fairy/transport/web"
)

var (
	ErrManagementUnavailable    = errors.New("management host is unavailable")
	ErrObservabilityUnavailable = errors.New("observability history is unavailable")
	ErrTraceNotFound            = errors.New("trace not found")
	ErrConversationIDRequired   = errors.New("conversationId is required")
	ErrTurnIDRequired           = errors.New("turnId is required")
	ErrBackupDataDirRequired    = errors.New("SeekDB data directory is required for backup")
)

type (
	CharacterCatalog = character.Catalog
	CharacterRecord  = character.Record
	ProfileSnapshot  = config.ProfileSnapshot
	ProfileUpdate    = config.ProfileUpdate
	ModelStatus      = config.ModelConnectionStatus
	SemanticStatus   = config.SemanticEmbeddingStatus
	WebSearchStatus  = config.WebSearchStatus
	MemoryCatalog    = personal.Catalog
	MemoryRecord     = personal.Record
	MemoryScope      = personal.Scope
	KnowledgeCatalog = knowledge.Catalog
	StickerPage      = sticker.Page
	LogSnapshot      = observability.LogSnapshot
	LogEntry         = observability.LogEntry
	LogFilter        = observability.LogFilter
	TraceDetail      = observability.MessageTraceDetail
	MessagePage      = session.MessagePage
)

type QQSettings struct {
	SchemaVersion  uint32   `json:"schemaVersion"`
	GroupAllowlist []string `json:"groupAllowlist"`
	InstanceID     string   `json:"instanceId,omitempty"`
	Ready          bool     `json:"ready"`
	APIBaseURL     string   `json:"apiBaseURL,omitempty"`
}

type Management struct {
	runtime *Runtime
}

func (r *Runtime) Management() *Management {
	if r == nil || r.core == nil {
		return nil
	}
	return &Management{runtime: r}
}

func (m *Management) coreRuntime() *core.Runtime {
	if m == nil || m.runtime == nil {
		return nil
	}
	return m.runtime.core
}

type Overview struct {
	Bootstrap  core.BootstrapStatus `json:"bootstrap"`
	ConfigRoot string               `json:"configRoot"`
	Storage    api.StorageStatus    `json:"storage"`
	SecretKey  SecretKeyStatus      `json:"secretKey"`
	Model      ModelStatus          `json:"model"`
	Semantic   SemanticStatus       `json:"semanticEmbedding"`
	WebSearch  WebSearchStatus      `json:"webSearch"`
}

type SecretKeyStatus struct {
	Ready bool   `json:"ready"`
	Mode  string `json:"mode"`
}

type IntelligenceSnapshot struct {
	Summary              personal.Summary `json:"summary"`
	CandidateKnowledge   int64            `json:"candidateKnowledge"`
	VerifiedKnowledge    int64            `json:"verifiedKnowledge"`
	WebSearch            WebSearchStatus  `json:"webSearch"`
	SemanticEmbedding    SemanticStatus   `json:"semanticEmbedding"`
	ActiveBackgroundJobs int64            `json:"activeBackgroundJobs"`
}

type PluginStatus struct {
	Ready     bool                                `json:"ready"`
	Reason    string                              `json:"reason,omitempty"`
	Metrics   observability.PluginMetricsSnapshot `json:"metrics"`
	Instances []PluginInstanceStatus              `json:"instances"`
	Upgrades  []PluginUpgradeStatus               `json:"upgrades"`
}

type PluginInstanceStatus struct {
	ID               string `json:"id"`
	PluginID         string `json:"pluginId,omitempty"`
	Version          string `json:"version,omitempty"`
	Enabled          bool   `json:"enabled"`
	Lifecycle        string `json:"lifecycle,omitempty"`
	Calls            uint64 `json:"calls"`
	HostCalls        uint64 `json:"hostCalls"`
	BudgetExceeded   uint64 `json:"budgetExceeded"`
	CapabilityDenied uint64 `json:"capabilityDenied"`
	QueueDepth       uint64 `json:"queueDepth"`
	Traps            uint64 `json:"traps"`
	Restarts         uint64 `json:"restarts"`
	Cancelled        uint64 `json:"cancelled"`
	LastErrorCode    string `json:"lastErrorCode,omitempty"`
	LastTraceID      string `json:"lastTraceId,omitempty"`
}

type PluginUpgradeStatus struct {
	JournalID        string `json:"journalId"`
	InstanceID       string `json:"instanceId"`
	FromVersion      string `json:"fromVersion"`
	ToVersion        string `json:"toVersion"`
	Status           string `json:"status"`
	ErrorCode        string `json:"errorCode,omitempty"`
	StartedAtUnixMS  int64  `json:"startedAtUnixMs"`
	FinishedAtUnixMS int64  `json:"finishedAtUnixMs,omitempty"`
}

type ModelWrite struct {
	config.ModelConnectionInput
	APIKey string `json:"apiKey"`
}

type SemanticWrite struct {
	Provider string `json:"provider"`
	Enabled  bool   `json:"enabled"`
	Endpoint string `json:"endpoint"`
	Model    string `json:"model"`
	APIKey   string `json:"apiKey"`
}

type MemoryWrite struct {
	Kind                  string      `json:"kind"`
	Scope                 MemoryScope `json:"scope"`
	Content               string      `json:"content"`
	ConfidenceBasisPoints uint16      `json:"confidenceBasisPoints"`
}

type TurnRuntimeView struct {
	ConversationID string             `json:"conversationId"`
	TurnID         string             `json:"turnId"`
	Events         []TurnRuntimeEvent `json:"events"`
}

type TurnRuntimeEvent struct {
	Sequence        uint64  `json:"sequence"`
	EventType       string  `json:"eventType"`
	State           *string `json:"state,omitempty"`
	Code            *string `json:"code,omitempty"`
	CreatedAtUnixMS int64   `json:"createdAtUnixMs"`
}

type MetricsSnapshot struct {
	GeneratedAtUnixMS    int64                                `json:"generatedAtUnixMs"`
	Process              observability.ProcessMetrics         `json:"process"`
	Logs                 observability.LogStats               `json:"logs"`
	Messages             observability.MessageMetricsSnapshot `json:"messages"`
	Plugins              observability.PluginMetricsSnapshot  `json:"plugins"`
	History              []observability.MetricHistoryPoint   `json:"history"`
	HistoryPersistence   observability.HistoryStats           `json:"historyPersistence"`
	ActiveBackgroundJobs int64                                `json:"activeBackgroundJobs"`
}

type TraceSearch struct {
	MessageID string                       `json:"messageId"`
	Traces    []observability.MessageTrace `json:"traces"`
}

type BackupSource struct {
	ConfigRoot string
	DataDir    string
}

func (m *Management) Overview(ctx context.Context) (Overview, error) {
	rt := m.coreRuntime()
	if rt == nil || rt.Config == nil || rt.Bootstrap == nil {
		return Overview{}, ErrManagementUnavailable
	}
	bootstrap, err := rt.Bootstrap.Status()
	if err != nil {
		return Overview{}, err
	}
	storage, err := rt.StorageStatus(ctx)
	if err != nil {
		return Overview{}, err
	}
	model, err := rt.Config.ModelStatus()
	if err != nil {
		return Overview{}, err
	}
	semantic, err := rt.Config.SemanticEmbeddingStatus()
	if err != nil {
		return Overview{}, err
	}
	web, err := rt.Config.WebSearchStatus()
	if err != nil {
		return Overview{}, err
	}
	return Overview{
		Bootstrap:  bootstrap,
		ConfigRoot: rt.ConfigRoot,
		Storage:    storage,
		SecretKey:  SecretKeyStatus{Ready: rt.Secret != nil && rt.Secret.Encrypted(), Mode: "production"},
		Model:      model,
		Semantic:   semantic,
		WebSearch:  web,
	}, nil
}

func (m *Management) Characters() (CharacterCatalog, error) {
	rt := m.coreRuntime()
	if rt == nil || rt.Character == nil {
		return CharacterCatalog{}, ErrCharacterCatalogUnavailable
	}
	return rt.Character.ListCharacters()
}

func (m *Management) ActivateCharacter(characterID string, revision uint64) (CharacterRecord, error) {
	rt := m.coreRuntime()
	if rt == nil || rt.Character == nil {
		return CharacterRecord{}, ErrCharacterCatalogUnavailable
	}
	return rt.Character.ActivateCharacter(characterID, revision)
}

func (m *Management) Profile() (ProfileSnapshot, error) {
	rt := m.coreRuntime()
	if rt == nil || rt.Profile == nil {
		return ProfileSnapshot{}, ErrManagementUnavailable
	}
	snap, err := rt.Profile.Current()
	if err != nil {
		return ProfileSnapshot{}, err
	}
	if snap == nil {
		return ProfileSnapshot{}, nil
	}
	return *snap, nil
}

func (m *Management) SaveProfile(preferredName *string) (ProfileUpdate, error) {
	rt := m.coreRuntime()
	if rt == nil || rt.Profile == nil {
		return ProfileUpdate{}, ErrManagementUnavailable
	}
	return rt.Profile.SetPreferredName(preferredName)
}

func (m *Management) ClearProfile() (ProfileUpdate, error) {
	rt := m.coreRuntime()
	if rt == nil || rt.Profile == nil {
		return ProfileUpdate{}, ErrManagementUnavailable
	}
	return rt.Profile.Clear()
}

func (m *Management) Model() (ModelStatus, error) {
	rt := m.coreRuntime()
	if rt == nil || rt.Config == nil {
		return ModelStatus{}, ErrManagementUnavailable
	}
	return rt.Config.ModelStatus()
}

func (m *Management) SaveModel(write ModelWrite) (ModelStatus, error) {
	rt := m.coreRuntime()
	if rt == nil || rt.Config == nil {
		return ModelStatus{}, ErrManagementUnavailable
	}
	var apiKey *string
	if write.APIKey != "" {
		key := write.APIKey
		apiKey = &key
	}
	return rt.Config.SaveModelConnection(write.ModelConnectionInput, apiKey)
}

func (m *Management) ClearModel() (ModelStatus, error) {
	rt := m.coreRuntime()
	if rt == nil || rt.Config == nil {
		return ModelStatus{}, ErrManagementUnavailable
	}
	return rt.Config.ClearModelConnection()
}

func (m *Management) Semantic() (SemanticStatus, error) {
	rt := m.coreRuntime()
	if rt == nil || rt.Config == nil {
		return SemanticStatus{}, ErrManagementUnavailable
	}
	return rt.Config.SemanticEmbeddingStatus()
}

func (m *Management) SaveSemantic(write SemanticWrite) (SemanticStatus, error) {
	rt := m.coreRuntime()
	if rt == nil || rt.Config == nil {
		return SemanticStatus{}, ErrManagementUnavailable
	}
	var apiKey *string
	if write.APIKey != "" {
		key := write.APIKey
		apiKey = &key
	}
	return rt.Config.SaveSemanticEmbeddingSettings(config.SemanticEmbeddingSettings{
		Provider: write.Provider,
		Enabled:  write.Enabled,
		Endpoint: write.Endpoint,
		Model:    write.Model,
	}, apiKey)
}

func (m *Management) ClearSemanticCredential() (SemanticStatus, error) {
	rt := m.coreRuntime()
	if rt == nil || rt.Config == nil {
		return SemanticStatus{}, ErrManagementUnavailable
	}
	return rt.Config.DeleteSemanticEmbeddingCredential()
}

func (m *Management) Intelligence(ctx context.Context) (IntelligenceSnapshot, error) {
	rt := m.coreRuntime()
	if rt == nil || rt.Memory == nil || rt.KnowledgeStore == nil || rt.Config == nil {
		return IntelligenceSnapshot{}, ErrManagementUnavailable
	}
	summary, err := rt.Memory.SummaryContext(ctx)
	if err != nil {
		return IntelligenceSnapshot{}, err
	}
	stats, err := rt.KnowledgeStore.StatsContext(ctx)
	if err != nil {
		return IntelligenceSnapshot{}, err
	}
	web, err := rt.Config.WebSearchStatus()
	if err != nil {
		return IntelligenceSnapshot{}, err
	}
	semantic, err := rt.Config.SemanticEmbeddingStatus()
	if err != nil {
		return IntelligenceSnapshot{}, err
	}
	jobs := int64(0)
	if rt.Turn != nil {
		jobs = rt.Turn.ActiveBackgroundJobs()
	}
	return IntelligenceSnapshot{
		Summary:              summary,
		CandidateKnowledge:   stats.Candidates,
		VerifiedKnowledge:    stats.Verified,
		WebSearch:            web,
		SemanticEmbedding:    semantic,
		ActiveBackgroundJobs: jobs,
	}, nil
}

func (m *Management) Memories(characterID string) (MemoryCatalog, error) {
	rt := m.coreRuntime()
	if rt == nil || rt.Memory == nil || rt.Character == nil {
		return MemoryCatalog{}, ErrManagementUnavailable
	}
	id := strings.TrimSpace(characterID)
	if id == "" {
		catalog, err := rt.Character.ListCharacters()
		if err != nil {
			return MemoryCatalog{}, err
		}
		if catalog.Active == nil {
			return MemoryCatalog{}, errors.New("characterId is required")
		}
		id = catalog.Active.CharacterID
	}
	return rt.Memory.PersonalMemoryCatalog(id)
}

func (m *Management) CreateMemory(write MemoryWrite) (MemoryRecord, error) {
	rt := m.coreRuntime()
	if rt == nil || rt.Memory == nil {
		return MemoryRecord{}, ErrManagementUnavailable
	}
	return rt.Memory.CreatePersonalMemory(write.Kind, write.Scope, write.Content, write.ConfidenceBasisPoints)
}

func (m *Management) TombstoneMemory(id string) error {
	rt := m.coreRuntime()
	if rt == nil || rt.Memory == nil {
		return ErrManagementUnavailable
	}
	return rt.Memory.TombstonePersonalMemory(id)
}

func (m *Management) Knowledge(ctx context.Context) (KnowledgeCatalog, error) {
	rt := m.coreRuntime()
	if rt == nil || rt.KnowledgeStore == nil {
		return KnowledgeCatalog{}, ErrManagementUnavailable
	}
	return rt.KnowledgeStore.CatalogContext(ctx)
}

func (m *Management) TombstoneKnowledge(ctx context.Context, id string) error {
	rt := m.coreRuntime()
	if rt == nil || rt.KnowledgeStore == nil {
		return ErrManagementUnavailable
	}
	return rt.KnowledgeStore.TombstoneContext(ctx, id)
}

func (m *Management) Plugins() (PluginStatus, error) {
	if m == nil || m.runtime == nil {
		return PluginStatus{}, ErrManagementUnavailable
	}
	host, err := m.runtime.PluginHost()
	if err != nil {
		return PluginStatus{}, err
	}
	metrics := host.SnapshotMetrics()
	status := PluginStatus{
		Ready:     true,
		Metrics:   metrics,
		Instances: make([]PluginInstanceStatus, 0, len(metrics.Instances)),
		Upgrades:  []PluginUpgradeStatus{},
	}
	live := make(map[string]observability.PluginInstanceMetrics, len(metrics.Instances))
	for _, item := range metrics.Instances {
		live[item.InstanceID] = item
		status.Instances = append(status.Instances, instanceStatusFromMetrics(item))
	}
	if m.runtime.plugins == nil {
		return status, nil
	}
	records, err := m.runtime.plugins.Instances(context.Background())
	if err != nil {
		return PluginStatus{}, err
	}
	merged := make([]PluginInstanceStatus, 0, len(records)+len(status.Instances))
	seen := make(map[string]struct{}, len(records))
	for _, record := range records {
		seen[record.ID] = struct{}{}
		view := PluginInstanceStatus{
			ID: record.ID, PluginID: record.PluginID, Version: record.PluginVersion,
			Enabled: record.Enabled, Lifecycle: record.Lifecycle,
		}
		if item, ok := live[record.ID]; ok {
			view = overlayInstanceMetrics(view, item)
		}
		merged = append(merged, view)
	}
	for _, item := range status.Instances {
		if _, ok := seen[item.ID]; ok {
			continue
		}
		merged = append(merged, item)
	}
	status.Instances = merged
	upgrades, err := m.runtime.plugins.Upgrades(context.Background(), "")
	if err != nil {
		return PluginStatus{}, err
	}
	status.Upgrades = make([]PluginUpgradeStatus, 0, len(upgrades))
	for _, record := range upgrades {
		status.Upgrades = append(status.Upgrades, PluginUpgradeStatus{
			JournalID: record.JournalID, InstanceID: record.InstanceID,
			FromVersion: record.FromVersion, ToVersion: record.ToVersion,
			Status: record.Status, ErrorCode: record.ErrorCode,
			StartedAtUnixMS: record.StartedAtUnixMS, FinishedAtUnixMS: record.FinishedAtUnixMS,
		})
	}
	return status, nil
}

func instanceStatusFromMetrics(item observability.PluginInstanceMetrics) PluginInstanceStatus {
	return PluginInstanceStatus{
		ID: item.InstanceID, Calls: item.Calls, HostCalls: item.HostCalls,
		BudgetExceeded: item.BudgetExceeded, CapabilityDenied: item.CapabilityDenied,
		QueueDepth: item.QueueDepth, Traps: item.Traps, Restarts: item.Restarts,
		Cancelled: item.Cancelled, LastErrorCode: item.LastErrorCode, LastTraceID: item.LastTraceID,
	}
}

func overlayInstanceMetrics(view PluginInstanceStatus, item observability.PluginInstanceMetrics) PluginInstanceStatus {
	view.Calls = item.Calls
	view.HostCalls = item.HostCalls
	view.BudgetExceeded = item.BudgetExceeded
	view.CapabilityDenied = item.CapabilityDenied
	view.QueueDepth = item.QueueDepth
	view.Traps = item.Traps
	view.Restarts = item.Restarts
	view.Cancelled = item.Cancelled
	view.LastErrorCode = item.LastErrorCode
	view.LastTraceID = item.LastTraceID
	return view
}

func (m *Management) Stickers(ctx context.Context) (StickerPage, error) {
	rt := m.coreRuntime()
	if rt == nil || rt.Stickers == nil {
		return StickerPage{}, ErrStickerStoreUnavailable
	}
	return rt.Stickers.List(ctx, sticker.ListInput{Limit: sticker.DefaultPageLimit})
}

func (m *Management) QQ() (QQSettings, error) {
	if m == nil || m.runtime == nil {
		return QQSettings{}, ErrManagementUnavailable
	}
	if m.runtime.plugins == nil {
		return QQSettings{SchemaVersion: 1, GroupAllowlist: []string{}}, nil
	}
	records, err := m.runtime.plugins.Instances(context.Background())
	if err != nil {
		return QQSettings{}, err
	}
	record, ok := qqPluginInstance(records)
	if !ok {
		return QQSettings{SchemaVersion: 1, GroupAllowlist: []string{}}, nil
	}
	config, err := qqonebot.ParseInstanceConfig(record.ConfigDocument)
	if err != nil {
		return QQSettings{}, err
	}
	_, discovered := qqonebot.Discover([]plugin.InstanceRecord{record})
	return QQSettings{
		SchemaVersion:  config.SchemaVersion,
		GroupAllowlist: config.GroupAllowlist,
		InstanceID:     record.ID,
		Ready:          discovered,
		APIBaseURL:     config.APIBaseURL,
	}, nil
}

func (m *Management) SaveQQ(settings QQSettings) (QQSettings, error) {
	if m == nil || m.runtime == nil {
		return QQSettings{}, ErrManagementUnavailable
	}
	if m.runtime.plugins == nil {
		return QQSettings{}, ErrQQPluginNotInstalled
	}
	records, err := m.runtime.plugins.Instances(context.Background())
	if err != nil {
		return QQSettings{}, err
	}
	record, ok := qqPluginInstance(records)
	if !ok {
		return QQSettings{}, ErrQQPluginNotInstalled
	}
	current, err := qqonebot.ParseInstanceConfig(record.ConfigDocument)
	if err != nil {
		return QQSettings{}, err
	}
	allowlist, err := qqonebot.NormalizeAllowlist(settings.GroupAllowlist)
	if err != nil {
		return QQSettings{}, err
	}
	current.GroupAllowlist = allowlist
	if strings.TrimSpace(settings.APIBaseURL) != "" {
		current.APIBaseURL = strings.TrimRight(strings.TrimSpace(settings.APIBaseURL), "/")
	}
	current.SchemaVersion = 1
	raw, err := json.Marshal(current)
	if err != nil {
		return QQSettings{}, err
	}
	record.ConfigDocument = raw
	if err := m.runtime.plugins.PutInstance(context.Background(), record); err != nil {
		return QQSettings{}, err
	}
	return m.QQ()
}

func qqPluginInstance(records []plugin.InstanceRecord) (plugin.InstanceRecord, bool) {
	for _, record := range records {
		if record.PluginID == qqonebot.PluginID {
			return record, true
		}
	}
	return plugin.InstanceRecord{}, false
}

func (m *Management) Conversation(ctx context.Context, conversationID string, beforeSequence uint64, limit int) (MessagePage, error) {
	if m == nil || m.runtime == nil {
		return MessagePage{}, ErrManagementUnavailable
	}
	if strings.TrimSpace(conversationID) == "" {
		return MessagePage{}, ErrConversationIDRequired
	}
	return m.runtime.ListMessages(ctx, conversationID, beforeSequence, limit)
}

func (m *Management) TurnRuntime(ctx context.Context, conversationID, turnID string) (TurnRuntimeView, error) {
	rt := m.coreRuntime()
	if rt == nil || rt.RuntimeStore == nil {
		return TurnRuntimeView{}, ErrManagementUnavailable
	}
	if strings.TrimSpace(conversationID) == "" {
		return TurnRuntimeView{}, ErrConversationIDRequired
	}
	if strings.TrimSpace(turnID) == "" {
		return TurnRuntimeView{}, ErrTurnIDRequired
	}
	records, err := rt.RuntimeStore.ListTurnRuntimeEventsContext(ctx, conversationID, turnID)
	if err != nil {
		return TurnRuntimeView{}, err
	}
	events := make([]TurnRuntimeEvent, 0, len(records))
	for _, record := range records {
		events = append(events, TurnRuntimeEvent{
			Sequence:        record.Sequence,
			EventType:       record.EventType,
			State:           record.State,
			Code:            record.Code,
			CreatedAtUnixMS: record.CreatedAtUnixMS,
		})
	}
	return TurnRuntimeView{ConversationID: conversationID, TurnID: turnID, Events: events}, nil
}

func (m *Management) BackupSource() (BackupSource, error) {
	rt := m.coreRuntime()
	if rt == nil || strings.TrimSpace(rt.ConfigRoot) == "" {
		return BackupSource{}, ErrManagementUnavailable
	}
	cfg, err := seekdb.ConfigFromEnv(seekdb.ProfileGetenv(rt.ConfigRoot, os.Getenv))
	if err != nil {
		return BackupSource{}, err
	}
	if strings.TrimSpace(cfg.DataDir) == "" {
		return BackupSource{}, ErrBackupDataDirRequired
	}
	return BackupSource{ConfigRoot: rt.ConfigRoot, DataDir: cfg.DataDir}, nil
}
