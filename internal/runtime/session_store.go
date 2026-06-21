package runtime

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Rinai-R/FAIRY/internal/app"
)

const maxSessionRuntimeEvents = 200

var ErrSessionNotFound = errors.New("session 不存在")

type SessionStore interface {
	BeginScene(req app.SceneGenerateRequest, resp app.SceneGenerateResponse) (app.SessionRecord, error)
	CreateGeneration(record app.SessionRecord) (app.SessionRecord, error)
	CompleteGeneration(sessionID string, resp app.SceneGenerateResponse) (app.SessionRecord, error)
	FailGeneration(sessionID string, message string) (app.SessionRecord, error)
	GenerationByFingerprint(fingerprint string) (app.SessionRecord, bool, error)
	ListGeneration(status string) ([]app.SessionRecord, error)
	AppendTurn(req app.TurnRequest, resp app.TurnResponse) (app.SessionRecord, error)
	AdvanceWorkflow(req app.WorkflowAdvanceRequest) (app.SessionRecord, error)
	AttachWorkflowAudio(sessionID string, nodeID string, audio app.AudioResult) (app.SessionRecord, error)
	UpdateWorkflowNode(sessionID string, node app.TeachingWorkflowNode) (app.SessionRecord, error)
	SaveWorkflow(sessionID string, workflow app.TeachingWorkflow) (app.SessionRecord, error)
	AppendEvent(sessionID string, event app.RuntimeEvent) (app.SessionRecord, error)
	SessionEvents(sessionID string) ([]app.RuntimeEvent, error)
	List() ([]app.SessionRecord, error)
	Get(id string) (app.SessionRecord, error)
	Delete(id string) error
}

type FileSessionStore struct {
	Path string
	mu   sync.Mutex
}

func NewFileSessionStore(path string) *FileSessionStore {
	if path == "" {
		path = "data/sessions.json"
	}
	return &FileSessionStore{Path: path}
}

func (s *FileSessionStore) BeginScene(req app.SceneGenerateRequest, resp app.SceneGenerateResponse) (app.SessionRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	state, err := s.load()
	if err != nil {
		return app.SessionRecord{}, err
	}

	record := sceneRecordFromResponse(req, resp, time.Now().UTC())
	state[record.Session.ID] = record
	return record, s.save(state)
}

func (s *FileSessionStore) CreateGeneration(record app.SessionRecord) (app.SessionRecord, error) {
	if strings.TrimSpace(record.Session.ID) == "" {
		return app.SessionRecord{}, errors.New("session.id 不能为空")
	}
	if strings.TrimSpace(record.Generation.Fingerprint) == "" {
		return app.SessionRecord{}, errors.New("generation.fingerprint 不能为空")
	}
	if record.Generation.Status != app.SceneGenerationStatusGenerating {
		return app.SessionRecord{}, fmt.Errorf("generation.status 必须是 generating: %s", record.Generation.Status)
	}
	if len(record.Generation.Request.Characters) == 0 || strings.TrimSpace(record.Generation.Request.Characters[0].ID) == "" {
		return app.SessionRecord{}, errors.New("generation.request.characters[0].id 不能为空")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	state, err := s.load()
	if err != nil {
		return app.SessionRecord{}, err
	}
	if existing, ok := state[record.Session.ID]; ok && existing.Generation.Status == app.SceneGenerationStatusGenerating {
		return existing, nil
	}
	now := time.Now().UTC()
	if record.Generation.StartedAt.IsZero() {
		record.Generation.StartedAt = now
	}
	record.UpdatedAt = now
	state[record.Session.ID] = record
	return record, s.save(state)
}

func (s *FileSessionStore) CompleteGeneration(sessionID string, resp app.SceneGenerateResponse) (app.SessionRecord, error) {
	if strings.TrimSpace(sessionID) == "" {
		return app.SessionRecord{}, errors.New("session_id 不能为空")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	state, err := s.load()
	if err != nil {
		return app.SessionRecord{}, err
	}
	existing, ok := state[sessionID]
	if !ok {
		return app.SessionRecord{}, fmt.Errorf("%w: %s", ErrSessionNotFound, sessionID)
	}
	if existing.Generation.Status != app.SceneGenerationStatusGenerating {
		return app.SessionRecord{}, fmt.Errorf("generation.status 必须是 generating: %s", existing.Generation.Status)
	}

	resp.Session.ID = sessionID
	record := sceneRecordFromResponse(existing.Generation.Request, resp, time.Now().UTC())
	record.Generation = existing.Generation
	record.Generation.Error = ""
	record.Generation.CompletedAt = record.UpdatedAt
	record.Events = cloneRuntimeEvents(existing.Events)
	applyWorkflowGenerationStatus(&record)
	state[sessionID] = record
	return record, s.save(state)
}

func (s *FileSessionStore) FailGeneration(sessionID string, message string) (app.SessionRecord, error) {
	if strings.TrimSpace(sessionID) == "" {
		return app.SessionRecord{}, errors.New("session_id 不能为空")
	}
	if strings.TrimSpace(message) == "" {
		return app.SessionRecord{}, errors.New("generation error 不能为空")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	state, err := s.load()
	if err != nil {
		return app.SessionRecord{}, err
	}
	record, ok := state[sessionID]
	if !ok {
		return app.SessionRecord{}, fmt.Errorf("%w: %s", ErrSessionNotFound, sessionID)
	}
	now := time.Now().UTC()
	record.Generation.Status = app.SceneGenerationStatusFailed
	record.Generation.Error = message
	record.Generation.CompletedAt = now
	record.UpdatedAt = now
	state[sessionID] = record
	return record, s.save(state)
}

func (s *FileSessionStore) GenerationByFingerprint(fingerprint string) (app.SessionRecord, bool, error) {
	fingerprint = strings.TrimSpace(fingerprint)
	if fingerprint == "" {
		return app.SessionRecord{}, false, errors.New("generation.fingerprint 不能为空")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	state, err := s.load()
	if err != nil {
		return app.SessionRecord{}, false, err
	}
	for _, record := range state {
		if record.Generation.Fingerprint == fingerprint && generationStillInProgress(record.Generation.Status) {
			return record, true, nil
		}
	}
	return app.SessionRecord{}, false, nil
}

func (s *FileSessionStore) ListGeneration(status string) ([]app.SessionRecord, error) {
	status = strings.TrimSpace(status)
	if status == "" {
		return nil, errors.New("generation.status 不能为空")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	state, err := s.load()
	if err != nil {
		return nil, err
	}
	records := make([]app.SessionRecord, 0)
	for _, record := range state {
		if record.Generation.Status == status {
			records = append(records, record)
		}
	}
	sort.Slice(records, func(i, j int) bool {
		return records[i].UpdatedAt.After(records[j].UpdatedAt)
	})
	return records, nil
}

func (s *FileSessionStore) AdvanceWorkflow(req app.WorkflowAdvanceRequest) (app.SessionRecord, error) {
	if err := validateWorkflowAdvanceRequest(req); err != nil {
		return app.SessionRecord{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	state, err := s.load()
	if err != nil {
		return app.SessionRecord{}, err
	}
	record, ok := state[req.SessionID]
	if !ok {
		return app.SessionRecord{}, fmt.Errorf("%w: %s", ErrSessionNotFound, req.SessionID)
	}
	if len(record.Workflow.Nodes) == 0 {
		return app.SessionRecord{}, fmt.Errorf("session 没有教学工作流: %s", req.SessionID)
	}
	current := findWorkflowNode(record.Workflow.Nodes, record.Workflow.CurrentNodeID)
	if current.ID == "" {
		current = record.Workflow.Nodes[0]
	}
	if req.CurrentNodeID != "" && req.CurrentNodeID != current.ID {
		return app.SessionRecord{}, fmt.Errorf("workflow 当前节点不匹配: got %s want %s", req.CurrentNodeID, current.ID)
	}
	next := findWorkflowNode(record.Workflow.Nodes, req.NextNodeID)
	if next.ID == "" {
		return app.SessionRecord{}, fmt.Errorf("workflow 节点不存在: %s", req.NextNodeID)
	}
	var selectedChoice app.SceneChoice
	if req.ChoiceID != "" {
		choice, _, ok := workflowChoiceByID(current, req.ChoiceID)
		if !ok {
			return app.SessionRecord{}, fmt.Errorf("当前节点不包含选项: %s", req.ChoiceID)
		}
		selectedChoice = choice
	}
	replay := workflowAdvanceReplay(record.Workflow, current, req)
	if err := validateWorkflowAdvanceChoiceBoundary(current, req.ChoiceID, replay); err != nil {
		return app.SessionRecord{}, err
	}
	if replay && !workflowVisited(record.Workflow, req.NextNodeID) {
		return app.SessionRecord{}, fmt.Errorf("只能回溯到已访问节点: %s", req.NextNodeID)
	}
	choiceTarget := strings.TrimSpace(selectedChoice.TargetNodeID)
	choiceAdvance := req.ChoiceID != "" && choiceTarget != "" && req.NextNodeID == choiceTarget
	if !replay && current.NextNodeID != "" && req.NextNodeID != current.NextNodeID && !choiceAdvance {
		return app.SessionRecord{}, fmt.Errorf("workflow 不允许从 %s 跳转到 %s", current.ID, req.NextNodeID)
	}

	now := time.Now().UTC()
	record.Workflow.CurrentNodeID = next.ID
	record.Scene.Phase = next.Kind
	record.Scene.LastActiveAt = now
	record.UpdatedAt = now
	action := "advance"
	if replay {
		action = "replay"
	} else if req.ChoiceID != "" {
		action = "choice"
	}
	appendWorkflowHistory(&record.Workflow, next, req.ChoiceID, choiceLabel(current, req.ChoiceID), action, now)
	state[req.SessionID] = record
	return record, s.save(state)
}

func (s *FileSessionStore) AttachWorkflowAudio(sessionID string, nodeID string, audio app.AudioResult) (app.SessionRecord, error) {
	if strings.TrimSpace(sessionID) == "" {
		return app.SessionRecord{}, errors.New("session_id 不能为空")
	}
	if strings.TrimSpace(nodeID) == "" {
		return app.SessionRecord{}, errors.New("workflow_node_id 不能为空")
	}
	if strings.TrimSpace(audio.URL) == "" {
		return app.SessionRecord{}, errors.New("audio.url 不能为空")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	state, err := s.load()
	if err != nil {
		return app.SessionRecord{}, err
	}
	record, ok := state[sessionID]
	if !ok {
		return app.SessionRecord{}, fmt.Errorf("%w: %s", ErrSessionNotFound, sessionID)
	}
	found := false
	for index := range record.Workflow.History {
		if record.Workflow.History[index].NodeID == nodeID {
			record.Workflow.History[index].AudioURL = audio.URL
			record.Workflow.History[index].AudioFormat = audio.Format
			record.Workflow.History[index].AudioCached = audio.Cached
			found = true
		}
	}
	if !found {
		node := findWorkflowNode(record.Workflow.Nodes, nodeID)
		if node.ID == "" {
			return app.SessionRecord{}, fmt.Errorf("workflow 节点不存在: %s", nodeID)
		}
		appendWorkflowHistory(&record.Workflow, node, "", "", "audio", time.Now().UTC())
		last := len(record.Workflow.History) - 1
		record.Workflow.History[last].AudioURL = audio.URL
		record.Workflow.History[last].AudioFormat = audio.Format
		record.Workflow.History[last].AudioCached = audio.Cached
	}
	record.UpdatedAt = time.Now().UTC()
	state[sessionID] = record
	return record, s.save(state)
}

func (s *FileSessionStore) UpdateWorkflowNode(sessionID string, node app.TeachingWorkflowNode) (app.SessionRecord, error) {
	if strings.TrimSpace(sessionID) == "" {
		return app.SessionRecord{}, errors.New("session_id 不能为空")
	}
	if strings.TrimSpace(node.ID) == "" {
		return app.SessionRecord{}, errors.New("workflow node id 不能为空")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	state, err := s.load()
	if err != nil {
		return app.SessionRecord{}, err
	}
	record, ok := state[sessionID]
	if !ok {
		return app.SessionRecord{}, fmt.Errorf("%w: %s", ErrSessionNotFound, sessionID)
	}
	found := false
	for index := range record.Workflow.Nodes {
		if record.Workflow.Nodes[index].ID == node.ID {
			syncWorkflowNodeLegacyFields(&node)
			record.Workflow.Nodes[index] = node
			found = true
			break
		}
	}
	if !found {
		return app.SessionRecord{}, fmt.Errorf("workflow 节点不存在: %s", node.ID)
	}
	record.UpdatedAt = time.Now().UTC()
	state[sessionID] = record
	return record, s.save(state)
}

func (s *FileSessionStore) SaveWorkflow(sessionID string, workflow app.TeachingWorkflow) (app.SessionRecord, error) {
	if strings.TrimSpace(sessionID) == "" {
		return app.SessionRecord{}, errors.New("session_id 不能为空")
	}
	if len(workflow.Nodes) == 0 {
		return app.SessionRecord{}, errors.New("workflow.nodes 不能为空")
	}
	if strings.TrimSpace(workflow.CurrentNodeID) == "" {
		return app.SessionRecord{}, errors.New("workflow.current_node_id 不能为空")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	state, err := s.load()
	if err != nil {
		return app.SessionRecord{}, err
	}
	record, ok := state[sessionID]
	if !ok {
		return app.SessionRecord{}, fmt.Errorf("%w: %s", ErrSessionNotFound, sessionID)
	}
	for index := range workflow.Nodes {
		syncWorkflowNodeLegacyFields(&workflow.Nodes[index])
	}
	if node := findWorkflowNode(workflow.Nodes, workflow.CurrentNodeID); node.ID == "" {
		return app.SessionRecord{}, fmt.Errorf("workflow 当前节点不存在: %s", workflow.CurrentNodeID)
	}
	record.Workflow = workflow
	record.Scene.Phase = findWorkflowNode(workflow.Nodes, workflow.CurrentNodeID).Kind
	record.Scene.LastActiveAt = time.Now().UTC()
	record.UpdatedAt = time.Now().UTC()
	applyWorkflowGenerationStatus(&record)
	state[sessionID] = record
	return record, s.save(state)
}

func (s *FileSessionStore) AppendEvent(sessionID string, event app.RuntimeEvent) (app.SessionRecord, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return app.SessionRecord{}, errors.New("session_id 不能为空")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	state, err := s.load()
	if err != nil {
		return app.SessionRecord{}, err
	}
	record, ok := state[sessionID]
	if !ok {
		return app.SessionRecord{}, fmt.Errorf("%w: %s", ErrSessionNotFound, sessionID)
	}
	normalized, err := normalizeRuntimeEvent(sessionID, event, len(record.Events), time.Now().UTC())
	if err != nil {
		return app.SessionRecord{}, err
	}
	record.Events = append(record.Events, normalized)
	normalizeRuntimeEvents(&record)
	record.UpdatedAt = normalized.CreatedAt
	state[sessionID] = record
	return record, s.save(state)
}

func (s *FileSessionStore) SessionEvents(sessionID string) ([]app.RuntimeEvent, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, errors.New("session_id 不能为空")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	state, err := s.load()
	if err != nil {
		return nil, err
	}
	record, ok := state[sessionID]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrSessionNotFound, sessionID)
	}
	normalizeRuntimeEvents(&record)
	return cloneRuntimeEvents(record.Events), nil
}

func generationStillInProgress(status string) bool {
	return status == app.SceneGenerationStatusGenerating || status == app.SceneGenerationStatusPreparing
}

func applyWorkflowGenerationStatus(record *app.SessionRecord) {
	if record == nil || strings.TrimSpace(record.Generation.Fingerprint) == "" {
		return
	}
	switch record.Generation.Status {
	case app.SceneGenerationStatusGenerating, app.SceneGenerationStatusPreparing, app.SceneGenerationStatusReady, app.SceneGenerationStatusFailed:
	default:
		return
	}
	status, message := workflowGenerationStatus(record.Workflow)
	record.Generation.Status = status
	if status == app.SceneGenerationStatusFailed {
		record.Generation.Error = message
		if record.Generation.CompletedAt.IsZero() {
			record.Generation.CompletedAt = record.UpdatedAt
		}
		return
	}
	record.Generation.Error = ""
	if status == app.SceneGenerationStatusReady {
		record.Generation.CompletedAt = record.UpdatedAt
		return
	}
	record.Generation.CompletedAt = time.Time{}
}

func validateWorkflowAdvanceRequest(req app.WorkflowAdvanceRequest) error {
	if strings.TrimSpace(req.SessionID) == "" {
		return errors.New("session_id 不能为空")
	}
	if strings.TrimSpace(req.NextNodeID) == "" {
		return errors.New("next_node_id 不能为空")
	}
	return nil
}

func findWorkflowNode(nodes []app.TeachingWorkflowNode, id string) app.TeachingWorkflowNode {
	for _, node := range nodes {
		if node.ID == id {
			return node
		}
	}
	return app.TeachingWorkflowNode{}
}

func nodeHasChoice(node app.TeachingWorkflowNode, choiceID string) bool {
	for _, choice := range node.Choices {
		if choice.ID == choiceID {
			return true
		}
	}
	return false
}

func ensureWorkflowHistory(workflow *app.TeachingWorkflow, now time.Time) {
	if workflow == nil || len(workflow.History) > 0 || len(workflow.Nodes) == 0 {
		return
	}
	current := findWorkflowNode(workflow.Nodes, workflow.CurrentNodeID)
	if current.ID == "" {
		current = workflow.Nodes[0]
		workflow.CurrentNodeID = current.ID
	}
	appendWorkflowHistory(workflow, current, "", "", "enter", now)
}

func workflowVisited(workflow app.TeachingWorkflow, nodeID string) bool {
	for _, item := range workflow.History {
		if item.NodeID == nodeID && item.Action != "audio" {
			return true
		}
	}
	return false
}

func workflowAdvanceReplay(workflow app.TeachingWorkflow, current app.TeachingWorkflowNode, req app.WorkflowAdvanceRequest) bool {
	if req.Replay {
		return true
	}
	return current.NextNodeID != "" && req.NextNodeID != current.NextNodeID && workflowVisited(workflow, req.NextNodeID)
}

func validateWorkflowAdvanceChoiceBoundary(current app.TeachingWorkflowNode, choiceID string, replay bool) error {
	if replay || len(current.Choices) == 0 {
		return nil
	}
	if strings.TrimSpace(choiceID) == "" {
		return fmt.Errorf("当前节点包含选项，必须选择后才能推进: %s", current.ID)
	}
	return nil
}

func truncateWorkflowHistory(workflow *app.TeachingWorkflow, nodeID string) {
	if workflow == nil || nodeID == "" {
		return
	}
	for index, item := range workflow.History {
		if item.NodeID == nodeID && item.Action != "audio" {
			workflow.History = append([]app.WorkflowHistoryItem(nil), workflow.History[:index+1]...)
			return
		}
	}
}

func appendWorkflowHistory(workflow *app.TeachingWorkflow, node app.TeachingWorkflowNode, choiceID string, choiceLabel string, action string, occurredAt time.Time) {
	if workflow == nil || node.ID == "" {
		return
	}
	workflow.History = append(workflow.History, app.WorkflowHistoryItem{
		NodeID:      node.ID,
		NodeTitle:   node.Title,
		NodeKind:    node.Kind,
		ChoiceID:    choiceID,
		ChoiceLabel: choiceLabel,
		Action:      action,
		OccurredAt:  occurredAt,
	})
}

func choiceLabel(node app.TeachingWorkflowNode, choiceID string) string {
	if choiceID == "" {
		return ""
	}
	for _, choice := range node.Choices {
		if choice.ID == choiceID {
			return choice.Label
		}
	}
	return ""
}

func (s *FileSessionStore) AppendTurn(req app.TurnRequest, resp app.TurnResponse) (app.SessionRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	state, err := s.load()
	if err != nil {
		return app.SessionRecord{}, err
	}
	session := req.Session
	if session.ID == "" {
		session.ID = req.Scene.ID + ":" + req.User.UserID
	}
	if session.UserID == "" {
		session.UserID = req.User.UserID
	}
	if session.ActiveCharacterID == "" {
		session.ActiveCharacterID = req.Character.ID
	}

	record := state[session.ID]
	record.Session = session
	record.Scene = req.Scene
	record.Teaching = updateTeachingSnapshot(record.Teaching, req)
	record.Characters = req.Characters
	record.Relation = req.Relation
	if record.Workflow.CurrentNodeID == "" && len(record.Workflow.Nodes) > 0 {
		record.Workflow.CurrentNodeID = record.Workflow.Nodes[0].ID
	}
	record.UpdatedAt = time.Now().UTC()
	record.Messages = append(record.Messages,
		app.Message{
			ID:          messageID("user"),
			SessionID:   session.ID,
			Role:        "user",
			CharacterID: req.User.UserID,
			Text:        req.User.Text,
			DisplayText: req.User.Text,
			CreatedAt:   time.Now().UTC(),
		},
		app.Message{
			ID:               messageID("assistant"),
			SessionID:        session.ID,
			Role:             "assistant",
			CharacterID:      req.Character.ID,
			Text:             resp.DisplayText,
			DisplayText:      resp.DisplayText,
			SpeechText:       resp.SpeechText,
			Segments:         resp.Segments,
			Emotion:          resp.Emotion,
			Expression:       resp.Expression,
			Motion:           resp.Motion,
			AudioURL:         resp.Audio.URL,
			SceneImageURL:    resp.SceneImage.URL,
			SceneImagePrompt: resp.SceneImage.Prompt,
			SceneImageError:  resp.SceneImage.Error,
			CreatedAt:        time.Now().UTC(),
		},
	)
	state[session.ID] = record
	return record, s.save(state)
}

func teachingSnapshot(req app.SceneGenerateRequest, prompt app.PromptConfig) app.TeachingSnapshot {
	return app.TeachingSnapshot{
		Topic:           req.Topic,
		LearningGoal:    req.LearningGoal,
		Prompt:          firstPrompt(prompt, req.Prompt),
		Runtime:         req.Runtime,
		MaterialSource:  req.MaterialSource,
		MaterialContext: req.MaterialContext,
		Variables:       cloneStringMap(req.Variables),
	}
}

func sceneRecordFromResponse(req app.SceneGenerateRequest, resp app.SceneGenerateResponse, now time.Time) app.SessionRecord {
	session := resp.Session
	if session.ID == "" {
		session.ID = resp.Scene.ID + ":default"
	}
	if session.UserID == "" {
		session.UserID = "default"
	}
	if session.ActiveCharacterID == "" && len(req.Characters) > 0 {
		session.ActiveCharacterID = req.Characters[0].ID
	}
	if len(session.ParticipantIDs) == 0 {
		session.ParticipantIDs = characterIDs(req.Characters)
	}

	scene := resp.Scene
	if scene.LastActiveAt.IsZero() {
		scene.LastActiveAt = now
	}

	record := app.SessionRecord{
		Session:     session,
		Scene:       scene,
		Teaching:    teachingSnapshot(req, resp.Prompt),
		Characters:  req.Characters,
		Interaction: resp.Interaction,
		Workflow:    resp.Workflow,
		Relation:    resp.Relation,
		UpdatedAt:   now,
	}
	for index := range record.Workflow.Nodes {
		syncWorkflowNodeLegacyFields(&record.Workflow.Nodes[index])
	}
	ensureWorkflowHistory(&record.Workflow, now)
	if record.Relation.UserID == "" {
		record.Relation.UserID = session.UserID
	}
	if record.Relation.UpdatedAt.IsZero() {
		record.Relation.UpdatedAt = now
	}
	if resp.OpeningMessage != "" {
		record.Messages = append(record.Messages, app.Message{
			ID:          messageID("scene"),
			SessionID:   session.ID,
			Role:        "assistant",
			CharacterID: session.ActiveCharacterID,
			Text:        resp.OpeningMessage,
			DisplayText: resp.OpeningMessage,
			SpeechText:  resp.OpeningMessage,
			Emotion:     "welcoming",
			Expression:  "soft_smile",
			Motion:      "opening",
			CreatedAt:   now,
		})
	}
	return record
}

func updateTeachingSnapshot(current app.TeachingSnapshot, req app.TurnRequest) app.TeachingSnapshot {
	current.Prompt = req.Prompt
	current.Runtime = req.Runtime
	return current
}

func firstPrompt(values ...app.PromptConfig) app.PromptConfig {
	for _, value := range values {
		if value.System != "" || value.Developer != "" || value.SceneInstruction != "" || value.ResponseContract != "" || len(value.StyleRules) > 0 {
			return value
		}
	}
	return app.PromptConfig{}
}

func cloneStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]string, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

func (s *FileSessionStore) List() ([]app.SessionRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.load()
	if err != nil {
		return nil, err
	}
	records := make([]app.SessionRecord, 0, len(state))
	for _, record := range state {
		records = append(records, record)
	}
	sort.Slice(records, func(i, j int) bool {
		return records[i].UpdatedAt.After(records[j].UpdatedAt)
	})
	return records, nil
}

func (s *FileSessionStore) Get(id string) (app.SessionRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.load()
	if err != nil {
		return app.SessionRecord{}, err
	}
	record, ok := state[id]
	if !ok {
		return app.SessionRecord{}, ErrSessionNotFound
	}
	return record, nil
}

func (s *FileSessionStore) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.load()
	if err != nil {
		return err
	}
	if _, ok := state[id]; !ok {
		return ErrSessionNotFound
	}
	delete(state, id)
	return s.save(state)
}

func (s *FileSessionStore) load() (map[string]app.SessionRecord, error) {
	raw, err := os.ReadFile(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]app.SessionRecord{}, nil
	}
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return map[string]app.SessionRecord{}, nil
	}
	var state map[string]app.SessionRecord
	if err := json.Unmarshal(raw, &state); err != nil {
		return nil, err
	}
	if state == nil {
		state = map[string]app.SessionRecord{}
	}
	for id, record := range state {
		normalizeWorkflowReadyState(&record)
		normalizeRuntimeEvents(&record)
		state[id] = record
	}
	return state, nil
}

func normalizeRuntimeEvent(sessionID string, event app.RuntimeEvent, existingCount int, now time.Time) (app.RuntimeEvent, error) {
	if event.SessionID == "" {
		event.SessionID = sessionID
	}
	if event.SessionID != sessionID {
		return app.RuntimeEvent{}, fmt.Errorf("runtime event session_id 不匹配: %s != %s", event.SessionID, sessionID)
	}
	if strings.TrimSpace(event.Type) == "" {
		return app.RuntimeEvent{}, errors.New("runtime event type 不能为空")
	}
	if strings.TrimSpace(event.Level) == "" {
		return app.RuntimeEvent{}, errors.New("runtime event level 不能为空")
	}
	if strings.TrimSpace(event.Stage) == "" {
		return app.RuntimeEvent{}, errors.New("runtime event stage 不能为空")
	}
	if strings.TrimSpace(event.Message) == "" {
		return app.RuntimeEvent{}, errors.New("runtime event message 不能为空")
	}
	if event.CreatedAt.IsZero() {
		event.CreatedAt = now
	}
	if strings.TrimSpace(event.ID) == "" {
		event.ID = runtimeEventID(event.Type, event.CreatedAt, existingCount+1)
	}
	if event.DurationMS < 0 {
		event.DurationMS = 0
	}
	return event, nil
}

func normalizeRuntimeEvents(record *app.SessionRecord) {
	if record == nil || len(record.Events) == 0 {
		return
	}
	sort.SliceStable(record.Events, func(i, j int) bool {
		return record.Events[i].CreatedAt.Before(record.Events[j].CreatedAt)
	})
	if len(record.Events) > maxSessionRuntimeEvents {
		record.Events = append([]app.RuntimeEvent(nil), record.Events[len(record.Events)-maxSessionRuntimeEvents:]...)
	}
}

func runtimeEventID(eventType string, createdAt time.Time, sequence int) string {
	cleanType := strings.NewReplacer(".", "-", "_", "-").Replace(strings.TrimSpace(eventType))
	if cleanType == "" {
		cleanType = "event"
	}
	return fmt.Sprintf("%s-%d-%03d", cleanType, createdAt.UnixNano(), sequence)
}

func cloneRuntimeEvents(events []app.RuntimeEvent) []app.RuntimeEvent {
	if len(events) == 0 {
		return nil
	}
	out := make([]app.RuntimeEvent, len(events))
	copy(out, events)
	return out
}

func normalizeWorkflowReadyState(record *app.SessionRecord) {
	if record == nil || len(record.Workflow.Nodes) == 0 {
		return
	}
	audioByNode := map[string]app.AudioResult{}
	for _, item := range record.Workflow.History {
		if item.NodeID == "" || item.AudioURL == "" {
			continue
		}
		audioByNode[item.NodeID] = app.AudioResult{
			URL:    item.AudioURL,
			Format: item.AudioFormat,
			Cached: item.AudioCached,
		}
	}
	for index := range record.Workflow.Nodes {
		node := &record.Workflow.Nodes[index]
		syncWorkflowNodeLegacyFields(node)
		if node.Status != "" && node.VoiceStatus != "" {
			continue
		}
		if workflowNodeHasReadyAudio(*node) {
			node.Status = app.WorkflowNodeStatusReady
			node.VoiceStatus = app.WorkflowNodeStatusReady
			continue
		}
		if audio, ok := audioByNode[node.ID]; ok {
			attachLegacyAudioToNode(node, audio)
			node.Status = app.WorkflowNodeStatusReady
			node.VoiceStatus = app.WorkflowNodeStatusReady
			continue
		}
		if node.Status == "" {
			node.Status = app.WorkflowNodeStatusPending
		}
		if node.VoiceStatus == "" {
			node.VoiceStatus = app.WorkflowNodeStatusPending
		}
	}
}

func workflowNodeHasReadyAudio(node app.TeachingWorkflowNode) bool {
	if len(node.Lines) == 0 {
		return false
	}
	for _, line := range node.Lines {
		if text, _ := dialogueSpeechText(line); text == "" {
			return false
		}
		if line.AudioStatus != app.DialogueAudioStatusReady || line.Audio.URL == "" {
			return false
		}
	}
	return true
}

func attachLegacyAudioToNode(node *app.TeachingWorkflowNode, audio app.AudioResult) {
	if node == nil {
		return
	}
	if len(node.Lines) == 0 {
		node.Lines = workflowNodeLinesFromLegacy(*node)
	}
	for index := range node.Lines {
		if node.Lines[index].Audio.URL == "" {
			node.Lines[index].Audio = audio
		}
		node.Lines[index].AudioStatus = app.DialogueAudioStatusReady
	}
}

func workflowNodeLinesFromLegacy(node app.TeachingWorkflowNode) []app.DialogueLine {
	if strings.TrimSpace(node.Line) == "" && strings.TrimSpace(node.SpeechText) == "" {
		return nil
	}
	return []app.DialogueLine{{
		Speaker:    node.Speaker,
		Text:       node.Line,
		SpeechText: node.SpeechText,
		Expression: "soft_smile",
	}}
}

func (s *FileSessionStore) save(state map[string]app.SessionRecord) error {
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.Path, raw, 0o600)
}

func messageID(prefix string) string {
	return prefix + "-" + time.Now().UTC().Format("20060102150405.000000000")
}

func characterIDs(characters []app.Character) []string {
	ids := make([]string, 0, len(characters))
	for _, character := range characters {
		if character.ID == "" {
			continue
		}
		ids = append(ids, character.ID)
	}
	return ids
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
