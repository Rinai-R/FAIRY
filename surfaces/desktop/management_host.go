package main

import (
	"context"
	"errors"
	"strings"
	"time"

	"fairy/app/edge"
)

const managementQueryLimit = 15 * time.Second

type managementHost interface {
	Overview(context.Context) (edge.Overview, error)
	Characters() (edge.CharacterCatalog, error)
	ActivateCharacter(string, uint64) (edge.CharacterRecord, error)
	Profile() (edge.ProfileSnapshot, error)
	SaveProfile(*string) (edge.ProfileUpdate, error)
	ClearProfile() (edge.ProfileUpdate, error)
	Model() (edge.ModelStatus, error)
	SaveModel(edge.ModelWrite) (edge.ModelStatus, error)
	ClearModel() (edge.ModelStatus, error)
	Semantic() (edge.SemanticStatus, error)
	SaveSemantic(edge.SemanticWrite) (edge.SemanticStatus, error)
	ClearSemanticCredential() (edge.SemanticStatus, error)
	Intelligence(context.Context) (edge.IntelligenceSnapshot, error)
	Memories(string) (edge.MemoryCatalog, error)
	CreateMemory(edge.MemoryWrite) (edge.MemoryRecord, error)
	TombstoneMemory(string) error
	Knowledge(context.Context) (edge.KnowledgeCatalog, error)
	TombstoneKnowledge(context.Context, string) error
	Plugins() (edge.PluginStatus, error)
	Stickers(context.Context) (edge.StickerPage, error)
	QQ() (edge.QQSettings, error)
	SaveQQ(edge.QQSettings) (edge.QQSettings, error)
	Conversation(context.Context, string, uint64, int) (edge.MessagePage, error)
	TurnRuntime(context.Context, string, string) (edge.TurnRuntimeView, error)
	Metrics(context.Context) (edge.MetricsSnapshot, error)
	Traces(context.Context, string) (edge.TraceSearch, error)
	Trace(context.Context, string) (edge.TraceDetail, error)
	Logs(edge.LogFilter) (edge.LogSnapshot, error)
	SubscribeLogs(edge.LogFilter) ([]edge.LogEntry, <-chan edge.LogEntry, func(), error)
	BackupSource() (edge.BackupSource, error)
}

var _ managementHost = (*edge.Management)(nil)

func (s *CoreService) managementHost() (managementHost, error) {
	if s == nil {
		return nil, errors.New("desktop core service is unavailable")
	}
	s.mu.Lock()
	runtime := s.edge
	s.mu.Unlock()
	if runtime == nil {
		return nil, errors.New("edge runtime is not started")
	}
	host := runtime.Management()
	if host == nil {
		return nil, edge.ErrManagementUnavailable
	}
	return host, nil
}

func managementQueryContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), managementQueryLimit)
}

func (s *CoreService) ManagementOverview() (edge.Overview, error) {
	host, err := s.managementHost()
	if err != nil {
		return edge.Overview{}, err
	}
	ctx, cancel := managementQueryContext()
	defer cancel()
	return host.Overview(ctx)
}

func (s *CoreService) ManagementCharacters() (edge.CharacterCatalog, error) {
	host, err := s.managementHost()
	if err != nil {
		return edge.CharacterCatalog{}, err
	}
	return host.Characters()
}

func (s *CoreService) ActivateManagementCharacter(characterID string, revision uint64) (edge.CharacterRecord, error) {
	host, err := s.managementHost()
	if err != nil {
		return edge.CharacterRecord{}, err
	}
	record, err := host.ActivateCharacter(characterID, revision)
	if err != nil {
		return edge.CharacterRecord{}, err
	}
	s.mu.Lock()
	s.characterName = record.Name
	s.mu.Unlock()
	return record, nil
}

func (s *CoreService) ManagementProfile() (edge.ProfileSnapshot, error) {
	host, err := s.managementHost()
	if err != nil {
		return edge.ProfileSnapshot{}, err
	}
	return host.Profile()
}

func (s *CoreService) SaveManagementProfile(preferredName string) (edge.ProfileUpdate, error) {
	host, err := s.managementHost()
	if err != nil {
		return edge.ProfileUpdate{}, err
	}
	name := preferredName
	return host.SaveProfile(&name)
}

func (s *CoreService) ClearManagementProfile() (edge.ProfileUpdate, error) {
	host, err := s.managementHost()
	if err != nil {
		return edge.ProfileUpdate{}, err
	}
	return host.ClearProfile()
}

func (s *CoreService) ManagementModel() (edge.ModelStatus, error) {
	host, err := s.managementHost()
	if err != nil {
		return edge.ModelStatus{}, err
	}
	return host.Model()
}

func (s *CoreService) SaveManagementModel(write edge.ModelWrite) (edge.ModelStatus, error) {
	host, err := s.managementHost()
	if err != nil {
		return edge.ModelStatus{}, err
	}
	return host.SaveModel(write)
}

func (s *CoreService) ClearManagementModel() (edge.ModelStatus, error) {
	host, err := s.managementHost()
	if err != nil {
		return edge.ModelStatus{}, err
	}
	return host.ClearModel()
}

func (s *CoreService) ManagementSemantic() (edge.SemanticStatus, error) {
	host, err := s.managementHost()
	if err != nil {
		return edge.SemanticStatus{}, err
	}
	return host.Semantic()
}

func (s *CoreService) SaveManagementSemantic(write edge.SemanticWrite) (edge.SemanticStatus, error) {
	host, err := s.managementHost()
	if err != nil {
		return edge.SemanticStatus{}, err
	}
	return host.SaveSemantic(write)
}

func (s *CoreService) ClearManagementSemanticCredential() (edge.SemanticStatus, error) {
	host, err := s.managementHost()
	if err != nil {
		return edge.SemanticStatus{}, err
	}
	return host.ClearSemanticCredential()
}

func (s *CoreService) ManagementIntelligence() (edge.IntelligenceSnapshot, error) {
	host, err := s.managementHost()
	if err != nil {
		return edge.IntelligenceSnapshot{}, err
	}
	ctx, cancel := managementQueryContext()
	defer cancel()
	return host.Intelligence(ctx)
}

func (s *CoreService) ManagementMemories(characterID string) (edge.MemoryCatalog, error) {
	host, err := s.managementHost()
	if err != nil {
		return edge.MemoryCatalog{}, err
	}
	return host.Memories(characterID)
}

func (s *CoreService) CreateManagementMemory(write edge.MemoryWrite) (edge.MemoryRecord, error) {
	host, err := s.managementHost()
	if err != nil {
		return edge.MemoryRecord{}, err
	}
	return host.CreateMemory(write)
}

func (s *CoreService) TombstoneManagementMemory(id string) error {
	host, err := s.managementHost()
	if err != nil {
		return err
	}
	return host.TombstoneMemory(id)
}

func (s *CoreService) ManagementKnowledge() (edge.KnowledgeCatalog, error) {
	host, err := s.managementHost()
	if err != nil {
		return edge.KnowledgeCatalog{}, err
	}
	ctx, cancel := managementQueryContext()
	defer cancel()
	return host.Knowledge(ctx)
}

func (s *CoreService) TombstoneManagementKnowledge(id string) error {
	host, err := s.managementHost()
	if err != nil {
		return err
	}
	ctx, cancel := managementQueryContext()
	defer cancel()
	return host.TombstoneKnowledge(ctx, id)
}

func (s *CoreService) ManagementPlugins() (edge.PluginStatus, error) {
	host, err := s.managementHost()
	if err != nil {
		return edge.PluginStatus{}, err
	}
	return host.Plugins()
}

func (s *CoreService) ManagementStickers() (edge.StickerPage, error) {
	host, err := s.managementHost()
	if err != nil {
		return edge.StickerPage{}, err
	}
	ctx, cancel := managementQueryContext()
	defer cancel()
	return host.Stickers(ctx)
}

func (s *CoreService) ManagementQQ() (edge.QQSettings, error) {
	host, err := s.managementHost()
	if err != nil {
		return edge.QQSettings{}, err
	}
	return host.QQ()
}

func (s *CoreService) SaveManagementQQ(settings edge.QQSettings) (edge.QQSettings, error) {
	host, err := s.managementHost()
	if err != nil {
		return edge.QQSettings{}, err
	}
	return host.SaveQQ(settings)
}

func (s *CoreService) ManagementConversation(conversationID string, beforeSequence uint64, limit int) (edge.MessagePage, error) {
	host, err := s.managementHost()
	if err != nil {
		return edge.MessagePage{}, err
	}
	id := strings.TrimSpace(conversationID)
	if id == "" {
		s.mu.Lock()
		id = s.conversation
		s.mu.Unlock()
	}
	ctx, cancel := managementQueryContext()
	defer cancel()
	return host.Conversation(ctx, id, beforeSequence, limit)
}

func (s *CoreService) ManagementTurnRuntime(conversationID, turnID string) (edge.TurnRuntimeView, error) {
	host, err := s.managementHost()
	if err != nil {
		return edge.TurnRuntimeView{}, err
	}
	ctx, cancel := managementQueryContext()
	defer cancel()
	return host.TurnRuntime(ctx, conversationID, turnID)
}

func (s *CoreService) ManagementMetrics() (edge.MetricsSnapshot, error) {
	host, err := s.managementHost()
	if err != nil {
		return edge.MetricsSnapshot{}, err
	}
	ctx, cancel := managementQueryContext()
	defer cancel()
	return host.Metrics(ctx)
}

func (s *CoreService) ManagementTraces(messageID string) (edge.TraceSearch, error) {
	host, err := s.managementHost()
	if err != nil {
		return edge.TraceSearch{}, err
	}
	ctx, cancel := managementQueryContext()
	defer cancel()
	return host.Traces(ctx, messageID)
}

func (s *CoreService) ManagementTrace(traceID string) (edge.TraceDetail, error) {
	host, err := s.managementHost()
	if err != nil {
		return edge.TraceDetail{}, err
	}
	ctx, cancel := managementQueryContext()
	defer cancel()
	return host.Trace(ctx, traceID)
}

func (s *CoreService) ManagementLogs() (edge.LogSnapshot, error) {
	host, err := s.managementHost()
	if err != nil {
		return edge.LogSnapshot{}, err
	}
	return host.Logs(edge.LogFilter{Limit: 200})
}

func (s *CoreService) SubscribeManagementLogs() error {
	host, err := s.managementHost()
	if err != nil {
		return err
	}
	backlog, live, unsubscribe, err := host.SubscribeLogs(edge.LogFilter{})
	if err != nil {
		return err
	}
	s.mu.Lock()
	previous := s.logUnsub
	s.logUnsub = unsubscribe
	emit := s.emit
	s.mu.Unlock()
	if previous != nil {
		previous()
	}
	if emit != nil {
		for _, entry := range backlog {
			emit("desktop:management-logs", entry)
		}
	}
	go s.forwardManagementLogs(live)
	return nil
}

func (s *CoreService) UnsubscribeManagementLogs() {
	s.mu.Lock()
	unsub := s.logUnsub
	s.logUnsub = nil
	s.mu.Unlock()
	if unsub != nil {
		unsub()
	}
}

func (s *CoreService) forwardManagementLogs(live <-chan edge.LogEntry) {
	for entry := range live {
		s.mu.Lock()
		emit := s.emit
		s.mu.Unlock()
		if emit != nil {
			emit("desktop:management-logs", entry)
		}
	}
}

func (s *CoreService) CreateManagementBackup() (ManagementBackup, error) {
	host, err := s.managementHost()
	if err != nil {
		return ManagementBackup{}, err
	}
	source, err := host.BackupSource()
	if err != nil {
		return ManagementBackup{}, err
	}
	return createSeekDBBackup(source)
}
