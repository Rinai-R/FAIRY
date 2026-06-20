package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/Rinai-R/FAIRY/internal/adapters/agent"
	"github.com/Rinai-R/FAIRY/internal/adapters/voice"
	"github.com/Rinai-R/FAIRY/internal/app"
)

type preparedDialogueLine struct {
	index int
	line  app.DialogueLine
}

type workflowStageTask struct {
	request       app.SceneGenerateRequest
	sessionID     string
	currentNodeID string
}

type workflowStageResult struct {
	sessionID     string
	currentNodeID string
	plannedNodeID string
	prepared      app.TeachingWorkflowNode
	decision      agent.ActDecision
	err           error
}

type workflowFollowupTarget struct {
	node   app.TeachingWorkflowNode
	choice app.SceneChoice
}

func initializeDynamicWorkflow(template app.TeachingWorkflow, sceneID string, topic string, goal string) app.TeachingWorkflow {
	workflowID := strings.TrimSpace(template.ID)
	if workflowID == "" {
		workflowID = sceneID + "-workflow"
	}
	title := strings.TrimSpace(template.Title)
	if title == "" {
		title = "教学剧情：" + topic
	}
	workflowGoal := strings.TrimSpace(template.Goal)
	if workflowGoal == "" {
		workflowGoal = goal
	}
	return app.TeachingWorkflow{
		ID:            workflowID,
		Title:         title,
		Goal:          workflowGoal,
		CurrentNodeID: "",
		Preparing:     false,
		PendingNodeID: "",
		Nodes:         nil,
		History:       nil,
	}
}

func (r *Runtime) prepareWorkflowNodeActAndVoice(
	ctx context.Context,
	request app.SceneGenerateRequest,
	session app.Session,
	workflow app.TeachingWorkflow,
	node app.TeachingWorkflowNode,
) (app.TeachingWorkflowNode, agent.ActDecision, error) {
	return r.prepareWorkflowNodeActAndVoiceWithChoice(ctx, request, session, workflow, node, app.SceneChoice{})
}

func (r *Runtime) prepareWorkflowNodeActAndVoiceWithChoice(
	ctx context.Context,
	request app.SceneGenerateRequest,
	session app.Session,
	workflow app.TeachingWorkflow,
	node app.TeachingWorkflowNode,
	choice app.SceneChoice,
) (app.TeachingWorkflowNode, agent.ActDecision, error) {
	preparedNode, decision, err := r.generateWorkflowNodeAct(ctx, request, session, workflow, node, choice)
	if err != nil {
		preparedNode.Status = app.WorkflowNodeStatusError
		preparedNode.VoiceStatus = app.WorkflowNodeStatusError
		preparedNode.PrepareError = err.Error()
		return preparedNode, decision, err
	}
	if nodeNeedsPreparedVoice(preparedNode) {
		voiceReady, voiceErr := r.prepareWorkflowNodeVoice(ctx, request, preparedNode)
		return voiceReady, decision, voiceErr
	}
	preparedNode.Status = app.WorkflowNodeStatusReady
	preparedNode.VoiceStatus = app.WorkflowNodeStatusReady
	preparedNode.ReadyAt = time.Now().UTC()
	return preparedNode, decision, nil
}

func (r *Runtime) generateWorkflowNodeAct(
	ctx context.Context,
	request app.SceneGenerateRequest,
	session app.Session,
	workflow app.TeachingWorkflow,
	node app.TeachingWorkflowNode,
	choice app.SceneChoice,
) (app.TeachingWorkflowNode, agent.ActDecision, error) {
	if !nodeNeedsGeneratedDialogue(node) {
		syncWorkflowNodeLegacyFields(&node)
		assignChoiceTargets(&node)
		if err := validateGeneratedActDialogueUnits(node, request.Runtime.Language); err != nil {
			return node, normalizeActDecision(node, agent.ActDecisionContinue), err
		}
		return node, normalizeActDecision(node, agent.ActDecisionContinue), nil
	}
	engine, err := r.agent(request.Runtime.AgentProvider)
	if err != nil {
		return node, "", err
	}
	index := workflowNodeActIndex(workflow.Nodes, node.ID)
	if index < 1 {
		index = countTeachingActs(workflow.Nodes) + 1
	}
	previous := previousTeachingNode(workflow.Nodes, node.ID)
	out, err := engine.GenerateAct(ctx, agent.ActInput{
		Request:       request,
		Session:       session,
		Workflow:      workflow,
		PlannedNode:   node,
		PreviousNode:  previous,
		CoveredPoints: coveredTeachingPoints(workflow.Nodes, node.ID),
		ActIndex:      index,
		Choice:        choice,
	})
	if err != nil {
		return node, "", err
	}
	decision := normalizeActDecision(node, out.Decision)
	merged := mergeGeneratedActNode(node, out.Node)
	assignChoiceTargets(&merged)
	merged.Decision = string(decision)
	if err := validateGeneratedActLanguage(merged, request.Runtime.Language); err != nil {
		return node, decision, err
	}
	if err := validateGeneratedActDialogueUnits(merged, request.Runtime.Language); err != nil {
		return node, decision, err
	}
	if err := (agent.ActOutput{
		Node:          merged,
		Decision:      decision,
		CoveredPoints: out.CoveredPoints,
		Summary:       out.Summary,
	}).Validate(); err != nil {
		return node, decision, err
	}
	return merged, decision, nil
}

func validateGeneratedActLanguage(node app.TeachingWorkflowNode, plan app.LanguagePlan) error {
	displayLanguage := normalizeLanguageCode(plan.DisplayLanguage)
	speechLanguage := normalizeLanguageCode(plan.SpeechLanguage)
	if speechLanguage == "" {
		speechLanguage = displayLanguage
	}
	for index, line := range workflowNodeDialogueLines(node, app.Character{}) {
		text := strings.TrimSpace(line.Text)
		speechText := strings.TrimSpace(line.SpeechText)
		if isChineseLanguage(displayLanguage) && containsJapaneseKana(text) {
			return fmt.Errorf("node.lines[%d].text 必须使用屏幕显示语言 %s，当前包含日文假名", index, plan.DisplayLanguage)
		}
		if isJapaneseLanguage(speechLanguage) && looksLikeChineseSentence(speechText) && !containsJapaneseKana(speechText) {
			return fmt.Errorf("node.lines[%d].speech_text 必须使用语音合成语言 %s，当前像中文文本", index, plan.SpeechLanguage)
		}
	}
	return nil
}

func validateGeneratedActDialogueUnits(node app.TeachingWorkflowNode, plan app.LanguagePlan) error {
	if !nodeNeedsPreparedVoice(node) {
		return nil
	}
	lines := workflowNodeDialogueLines(node, app.Character{})
	minLines := minimumGeneratedActLines(node.Kind)
	if len(lines) < minLines {
		return fmt.Errorf("node %q lines 至少需要 %d 条文本框台词，当前 %d 条", node.ID, minLines, len(lines))
	}
	for index, line := range lines {
		text := strings.TrimSpace(line.Text)
		if text == "" {
			return fmt.Errorf("node %q lines[%d].text 不能为空", node.ID, index)
		}
	}
	return nil
}

func minimumGeneratedActLines(kind string) int {
	switch strings.TrimSpace(kind) {
	case "opening", "lesson":
		return 4
	case "choice":
		return 2
	default:
		return 2
	}
}

func (r *Runtime) prepareWorkflowNodeVoice(ctx context.Context, request app.SceneGenerateRequest, node app.TeachingWorkflowNode) (app.TeachingWorkflowNode, error) {
	character, err := resolveSceneCharacter(request)
	if err != nil {
		node.Status = app.WorkflowNodeStatusError
		node.VoiceStatus = app.WorkflowNodeStatusError
		node.PrepareError = err.Error()
		return node, err
	}
	engine, err := r.voice(request.Runtime.VoiceProvider)
	if err != nil {
		node.Status = app.WorkflowNodeStatusError
		node.VoiceStatus = app.WorkflowNodeStatusError
		node.PrepareError = err.Error()
		return node, err
	}

	lines := workflowNodeDialogueLines(node, character)
	if len(lines) == 0 {
		err := errors.New("剧情幕没有可合成的台词")
		node.Status = app.WorkflowNodeStatusError
		node.VoiceStatus = app.WorkflowNodeStatusError
		node.PrepareError = err.Error()
		return node, err
	}

	node.Status = app.WorkflowNodeStatusSynthesizing
	node.VoiceStatus = app.WorkflowNodeStatusSynthesizing
	node.PrepareError = ""
	for index := range lines {
		lines[index].AudioStatus = app.DialogueAudioStatusPending
		lines[index].AudioError = ""
	}

	results := make([]app.DialogueLine, len(lines))
	errs := make([]error, len(lines))
	var wg sync.WaitGroup
	for index, line := range lines {
		index := index
		line := line
		wg.Add(1)
		if err := r.voicePool.Submit(func() {
			defer wg.Done()
			result, err := r.prepareDialogueLineVoice(ctx, request.Runtime.VoiceProvider, engine, request.Runtime.Voice, character, line)
			results[index] = result.line
			if err != nil {
				errs[index] = err
			}
		}); err != nil {
			wg.Done()
			line.AudioStatus = app.DialogueAudioStatusError
			line.AudioError = fmt.Errorf("提交语音合成任务失败: %w", err).Error()
			results[index] = line
			errs[index] = errors.New(line.AudioError)
		}
	}
	wg.Wait()

	var firstErr error
	for _, err := range errs {
		if err != nil {
			firstErr = err
			break
		}
	}
	node.Lines = results
	syncWorkflowNodeLegacyFields(&node)
	if firstErr != nil {
		node.Status = app.WorkflowNodeStatusError
		node.VoiceStatus = app.WorkflowNodeStatusError
		node.PrepareError = firstErr.Error()
		return node, firstErr
	}
	node.Status = app.WorkflowNodeStatusReady
	node.VoiceStatus = app.WorkflowNodeStatusReady
	node.ReadyAt = time.Now().UTC()
	return node, nil
}

func initializeWorkflowNodeStatuses(workflow *app.TeachingWorkflow) {
	if workflow == nil {
		return
	}
	for index := range workflow.Nodes {
		if workflow.Nodes[index].Status == "" {
			workflow.Nodes[index].Status = app.WorkflowNodeStatusPending
		}
		if workflow.Nodes[index].VoiceStatus == "" {
			workflow.Nodes[index].VoiceStatus = app.WorkflowNodeStatusPending
		}
	}
}

func replaceWorkflowNode(workflow *app.TeachingWorkflow, node app.TeachingWorkflowNode) bool {
	if workflow == nil || node.ID == "" {
		return false
	}
	for index := range workflow.Nodes {
		if workflow.Nodes[index].ID == node.ID {
			workflow.Nodes[index] = node
			return true
		}
	}
	return false
}

func workflowNodeIsReady(node app.TeachingWorkflowNode) bool {
	if node.Kind == "free_discussion" {
		return true
	}
	if node.Status == app.WorkflowNodeStatusReady && node.VoiceStatus == app.WorkflowNodeStatusReady {
		return true
	}
	return workflowNodeHasReadyAudio(node)
}

func workflowNodeHasError(node app.TeachingWorkflowNode) bool {
	if node.Status == app.WorkflowNodeStatusError || node.VoiceStatus == app.WorkflowNodeStatusError {
		return true
	}
	for _, line := range node.Lines {
		if line.AudioStatus == app.DialogueAudioStatusError || strings.TrimSpace(line.AudioError) != "" {
			return true
		}
	}
	return false
}

func workflowNodeErrorMessage(node app.TeachingWorkflowNode) string {
	if message := strings.TrimSpace(node.PrepareError); message != "" {
		return message
	}
	for index, line := range node.Lines {
		if message := strings.TrimSpace(line.AudioError); message != "" {
			return fmt.Sprintf("node %s lines[%d] audio failed: %s", node.ID, index, message)
		}
		if line.AudioStatus == app.DialogueAudioStatusError {
			return fmt.Sprintf("node %s lines[%d] audio failed", node.ID, index)
		}
	}
	if node.VoiceStatus == app.WorkflowNodeStatusError {
		return fmt.Sprintf("workflow node %s voice failed", node.ID)
	}
	if node.Status == app.WorkflowNodeStatusError {
		return fmt.Sprintf("workflow node %s failed", node.ID)
	}
	return ""
}

func workflowHasPendingMarker(workflow app.TeachingWorkflow) bool {
	return workflow.Preparing || strings.TrimSpace(workflow.PendingNodeID) != ""
}

func workflowNextNodePending(workflow app.TeachingWorkflow, current app.TeachingWorkflowNode, nextNodeID string) bool {
	nextNodeID = strings.TrimSpace(nextNodeID)
	if nextNodeID == "" {
		return false
	}
	if workflow.Preparing && strings.TrimSpace(workflow.PendingNodeID) == nextNodeID {
		return true
	}
	return workflow.Preparing && strings.TrimSpace(current.NextNodeID) == nextNodeID
}

func workflowNodeNeedsPreload(workflow app.TeachingWorkflow, nodeID string) bool {
	nodeID = strings.TrimSpace(nodeID)
	if nodeID == "" {
		return false
	}
	existing := findWorkflowNode(workflow.Nodes, nodeID)
	if existing.ID == "" {
		return true
	}
	return !workflowNodeIsReady(existing) && !workflowNodeHasError(existing)
}

func workflowFollowupsReady(workflow app.TeachingWorkflow, current app.TeachingWorkflowNode) bool {
	if current.ID == "" || current.FreeDiscussion || current.Kind == "free_discussion" {
		return true
	}
	_, targets, _ := workflowFollowupTargets(workflow, current)
	if len(targets) == 0 {
		return true
	}
	for _, target := range targets {
		node := findWorkflowNode(workflow.Nodes, target.node.ID)
		if node.ID == "" || !workflowNodeIsReady(node) {
			return false
		}
	}
	return true
}

func workflowGenerationStatus(workflow app.TeachingWorkflow) (string, string) {
	if len(workflow.Nodes) == 0 {
		return app.SceneGenerationStatusPreparing, ""
	}
	if workflowHasPendingMarker(workflow) {
		return app.SceneGenerationStatusPreparing, ""
	}
	for _, node := range workflow.Nodes {
		if node.ID == "" {
			continue
		}
		if workflowNodeHasError(node) {
			message := workflowNodeErrorMessage(node)
			if message == "" {
				message = fmt.Sprintf("workflow node %s failed", node.ID)
			}
			return app.SceneGenerationStatusFailed, message
		}
		if !workflowNodeIsReady(node) {
			return app.SceneGenerationStatusPreparing, ""
		}
	}
	for _, node := range workflow.Nodes {
		if node.ID == "" || !shouldQueueWorkflowFollowups(node) {
			continue
		}
		if !workflowFollowupsReady(workflow, node) {
			return app.SceneGenerationStatusPreparing, ""
		}
	}
	return app.SceneGenerationStatusReady, ""
}

func workflowFollowupTargets(workflow app.TeachingWorkflow, current app.TeachingWorkflowNode) (app.TeachingWorkflowNode, []workflowFollowupTarget, bool) {
	if current.ID == "" || current.FreeDiscussion || current.Kind == "free_discussion" {
		return current, nil, false
	}
	updated := current
	changed := assignChoiceTargets(&updated)
	targets := make([]workflowFollowupTarget, 0, 1+len(updated.Choices))
	if mainline, nextCurrent, ok := planWorkflowMainlineFollowup(workflow, updated); ok {
		if nextCurrent.NextNodeID != updated.NextNodeID {
			updated = nextCurrent
			changed = true
		}
		targets = append(targets, workflowFollowupTarget{node: mainline})
	}
	for _, choice := range updated.Choices {
		branch := plannedChoiceBranchNode(updated, choice)
		if existing := findWorkflowNode(workflow.Nodes, branch.ID); existing.ID != "" {
			branch = existing
		}
		targets = append(targets, workflowFollowupTarget{
			node:   branch,
			choice: choice,
		})
	}
	return updated, targets, changed
}

func planWorkflowMainlineFollowup(workflow app.TeachingWorkflow, current app.TeachingWorkflowNode) (app.TeachingWorkflowNode, app.TeachingWorkflowNode, bool) {
	decision := normalizeStoredDecision(current)
	planned, ok := plannedNodeForDecision(current, countLessonNodes(workflow.Nodes)+1, decision)
	if !ok {
		return app.TeachingWorkflowNode{}, current, false
	}
	if nextID := strings.TrimSpace(current.NextNodeID); nextID != "" {
		existing := findWorkflowNode(workflow.Nodes, nextID)
		if existing.ID != "" {
			return existing, current, true
		}
		planned.ID = nextID
		return planned, current, true
	}
	current.NextNodeID = planned.ID
	return planned, current, true
}

func (r *Runtime) preloadRemainingWorkflowNodes(request app.SceneGenerateRequest, sessionID string, currentNodeID string) {
	if r.sessions == nil || strings.TrimSpace(sessionID) == "" || r.stagePool == nil {
		return
	}
	key, ok := preloadJobKey(sessionID, currentNodeID)
	if !ok || !r.startPreloadJob(key) {
		return
	}
	go r.runWorkflowStageWaiter(context.Background(), key, workflowStageTask{
		request:       request,
		sessionID:     sessionID,
		currentNodeID: currentNodeID,
	})
}

func (r *Runtime) continueWorkflowFromNode(ctx context.Context, request app.SceneGenerateRequest, sessionID string, currentNodeID string) {
	r.runWorkflowStageWaiter(ctx, "", workflowStageTask{
		request:       request,
		sessionID:     sessionID,
		currentNodeID: currentNodeID,
	})
}

func (r *Runtime) runWorkflowStageWaiter(ctx context.Context, jobKey string, task workflowStageTask) {
	if jobKey != "" {
		defer r.finishPreloadJob(jobKey)
	}
	record, err := r.sessions.Get(task.sessionID)
	if err != nil {
		r.logger.Warn("读取剧情续写会话失败", "error", err, "session_id", task.sessionID)
		return
	}
	current := findWorkflowNode(record.Workflow.Nodes, task.currentNodeID)
	if current.ID == "" {
		r.logger.Warn("剧情续写起点不存在", "session_id", task.sessionID, "node_id", task.currentNodeID)
		return
	}
	current, targets, currentChanged := workflowFollowupTargets(record.Workflow, current)
	if currentChanged {
		if !replaceWorkflowNode(&record.Workflow, current) {
			r.logger.Warn("剧情续写起点保存失败", "session_id", task.sessionID, "node_id", current.ID)
			return
		}
	}
	if currentChanged {
		saved, err := r.sessions.SaveWorkflow(task.sessionID, record.Workflow)
		if err != nil {
			r.logger.Warn("写入剧情后续规划失败", "error", err, "session_id", task.sessionID, "node_id", current.ID)
			return
		}
		record = saved
	}
	preloaded := false
	for _, target := range targets {
		if !workflowNodeNeedsPreload(record.Workflow, target.node.ID) {
			continue
		}
		var err error
		record, err = r.prepareWorkflowFollowup(ctx, task, record, target.node, target.choice)
		if err != nil {
			r.logger.Warn("准备剧情后续失败", "error", err, "session_id", task.sessionID, "node_id", target.node.ID, "choice_id", target.choice.ID)
			return
		}
		preloaded = true
	}
	if !preloaded && workflowHasPendingMarker(record.Workflow) {
		record.Workflow.Preparing = false
		record.Workflow.PendingNodeID = ""
		if _, err := r.sessions.SaveWorkflow(task.sessionID, record.Workflow); err != nil {
			r.logger.Warn("清理剧情准备状态失败", "error", err, "session_id", task.sessionID, "node_id", current.ID)
			return
		}
	}
}

func (r *Runtime) prepareWorkflowFollowup(
	ctx context.Context,
	task workflowStageTask,
	record app.SessionRecord,
	planned app.TeachingWorkflowNode,
	choice app.SceneChoice,
) (app.SessionRecord, error) {
	if strings.TrimSpace(planned.ID) == "" {
		return record, errors.New("剧情幕 id 不能为空")
	}
	if !workflowNodeNeedsPreload(record.Workflow, planned.ID) {
		return record, nil
	}
	record.Workflow.PendingNodeID = planned.ID
	record.Workflow.Preparing = true
	saved, err := r.sessions.SaveWorkflow(task.sessionID, record.Workflow)
	if err != nil {
		return record, fmt.Errorf("写入剧情准备状态失败: %w", err)
	}
	record = saved
	resultCh := make(chan workflowStageResult, 1)
	workerTask := task
	if err := r.stagePool.Submit(func() {
		resultCh <- r.runWorkflowStageWorker(ctx, workerTask, record.Session, record.Workflow, planned, choice)
	}); err != nil {
		resultCh <- workflowStageResult{
			sessionID:     task.sessionID,
			currentNodeID: task.currentNodeID,
			plannedNodeID: planned.ID,
			prepared:      workflowStageErrorNode(planned, err),
			err:           fmt.Errorf("提交剧情幕 worker 失败: %w", err),
		}
	}
	result := <-resultCh
	r.applyWorkflowStageResult(result)
	latest, err := r.sessions.Get(task.sessionID)
	if err != nil {
		return record, fmt.Errorf("刷新剧情准备结果失败: %w", err)
	}
	if shouldPreloadWorkflowDescendants(latest) {
		prepared := findWorkflowNode(latest.Workflow.Nodes, planned.ID)
		if shouldQueueWorkflowFollowups(prepared) {
			r.preloadRemainingWorkflowNodes(task.request, task.sessionID, prepared.ID)
		}
	}
	return latest, nil
}

func shouldPreloadWorkflowDescendants(record app.SessionRecord) bool {
	return strings.TrimSpace(record.Generation.Fingerprint) != "" && record.Generation.Status != app.SceneGenerationStatusFailed
}

func (r *Runtime) runWorkflowStageWorker(
	ctx context.Context,
	task workflowStageTask,
	session app.Session,
	workflow app.TeachingWorkflow,
	planned app.TeachingWorkflowNode,
	choice app.SceneChoice,
) workflowStageResult {
	prepared, nextDecision, err := r.prepareWorkflowNodeActAndVoiceWithChoice(ctx, task.request, session, workflow, planned, choice)
	if err != nil {
		prepared = workflowStageErrorNode(prepared, err)
	}
	prepared.Decision = string(nextDecision)
	return workflowStageResult{
		sessionID:     task.sessionID,
		currentNodeID: task.currentNodeID,
		plannedNodeID: planned.ID,
		prepared:      prepared,
		decision:      nextDecision,
		err:           err,
	}
}

func (r *Runtime) applyWorkflowStageResult(result workflowStageResult) {
	record, err := r.sessions.Get(result.sessionID)
	if err != nil {
		r.logger.Warn("刷新剧情续写会话失败", "error", err, "session_id", result.sessionID, "node_id", result.plannedNodeID)
		return
	}
	if !replaceWorkflowNode(&record.Workflow, result.prepared) {
		record.Workflow.Nodes = append(record.Workflow.Nodes, result.prepared)
	}
	record.Workflow.PendingNodeID = ""
	record.Workflow.Preparing = false
	if _, saveErr := r.sessions.SaveWorkflow(result.sessionID, record.Workflow); saveErr != nil {
		r.logger.Warn("写入剧情续写结果失败", "error", saveErr, "session_id", result.sessionID, "node_id", result.plannedNodeID)
		return
	}
	if result.err != nil {
		r.logger.Warn("剧情续写失败", "error", result.err, "session_id", result.sessionID, "node_id", result.plannedNodeID)
		return
	}
}

func workflowStageErrorNode(node app.TeachingWorkflowNode, err error) app.TeachingWorkflowNode {
	if strings.TrimSpace(node.ID) == "" {
		node.ID = "unknown"
	}
	node.Status = app.WorkflowNodeStatusError
	node.VoiceStatus = app.WorkflowNodeStatusError
	if err != nil {
		node.PrepareError = err.Error()
	}
	return node
}

func preloadJobKey(sessionID string, currentNodeID string) (string, bool) {
	sessionID = strings.TrimSpace(sessionID)
	currentNodeID = strings.TrimSpace(currentNodeID)
	if sessionID == "" || currentNodeID == "" {
		return "", false
	}
	return sessionID + "\x00" + currentNodeID, true
}

func (r *Runtime) startPreloadJob(key string) bool {
	r.preloadMu.Lock()
	defer r.preloadMu.Unlock()
	if r.preloadJobs == nil {
		r.preloadJobs = map[string]struct{}{}
	}
	if _, exists := r.preloadJobs[key]; exists {
		return false
	}
	r.preloadJobs[key] = struct{}{}
	return true
}

func (r *Runtime) finishPreloadJob(key string) {
	r.preloadMu.Lock()
	defer r.preloadMu.Unlock()
	delete(r.preloadJobs, key)
}

func (r *Runtime) prepareDialogueLineVoice(
	ctx context.Context,
	provider string,
	engine voice.Engine,
	profile app.VoiceProfile,
	character app.Character,
	line app.DialogueLine,
) (preparedDialogueLine, error) {
	text, fallback := dialogueSpeechText(line)
	if strings.TrimSpace(text) == "" {
		err := errors.New("台词缺少 speech_text 和 text")
		line.AudioStatus = app.DialogueAudioStatusError
		line.AudioError = err.Error()
		return preparedDialogueLine{line: line}, err
	}
	if line.SpeechText == "" {
		line.SpeechText = text
	}
	plan := app.VoicePlan{
		VoiceID: voiceIDForSynthesis(profile, character),
		Style:   strings.TrimSpace(line.Expression),
		Speed:   1,
		Pitch:   1,
	}
	if plan.Style == "" {
		plan.Style = "neutral"
	}
	audio, err := r.synthesizeWithCache(ctx, provider, engine, voice.Input{
		Text:      text,
		Plan:      plan,
		Emotion:   plan.Style,
		Character: character,
		Profile:   profile,
	})
	if err != nil {
		line.AudioStatus = app.DialogueAudioStatusError
		line.AudioError = err.Error()
		return preparedDialogueLine{line: line}, err
	}
	line.Audio = audio
	line.AudioStatus = app.DialogueAudioStatusReady
	if fallback {
		line.AudioError = "speech_text 为空，已降级使用 text 合成"
	}
	return preparedDialogueLine{line: line}, nil
}

func workflowNodeDialogueLines(node app.TeachingWorkflowNode, character app.Character) []app.DialogueLine {
	if len(node.Lines) > 0 {
		lines := make([]app.DialogueLine, len(node.Lines))
		copy(lines, node.Lines)
		for index := range lines {
			if strings.TrimSpace(lines[index].Speaker) == "" {
				lines[index].Speaker = node.Speaker
			}
			if strings.TrimSpace(lines[index].Speaker) == "" {
				lines[index].Speaker = character.DisplayName
			}
		}
		return lines
	}
	text := strings.TrimSpace(node.Line)
	speechText := strings.TrimSpace(node.SpeechText)
	if text == "" && speechText == "" {
		text = strings.TrimSpace(node.Summary)
	}
	if text == "" && speechText == "" {
		text = strings.TrimSpace(node.Title)
	}
	if text == "" && speechText == "" {
		return nil
	}
	return []app.DialogueLine{{
		Speaker:    firstNonEmpty(node.Speaker, character.DisplayName, character.ID),
		Text:       text,
		SpeechText: speechText,
		Expression: "soft_smile",
	}}
}

func nodeNeedsGeneratedDialogue(node app.TeachingWorkflowNode) bool {
	if node.Kind != "opening" && node.Kind != "lesson" && node.Kind != "summary" && node.Kind != "choice" {
		return false
	}
	return len(node.Lines) == 0
}

func nodeNeedsPreparedVoice(node app.TeachingWorkflowNode) bool {
	return node.Kind == "opening" || node.Kind == "lesson" || node.Kind == "summary" || node.Kind == "choice"
}

func workflowNodeActIndex(nodes []app.TeachingWorkflowNode, nodeID string) int {
	index := 0
	for _, node := range nodes {
		if node.Kind == "opening" || node.Kind == "lesson" || node.Kind == "summary" {
			index++
		}
		if node.ID == nodeID {
			if index == 0 {
				return 1
			}
			return index
		}
	}
	return 1
}

func countTeachingActs(nodes []app.TeachingWorkflowNode) int {
	count := 0
	for _, node := range nodes {
		if node.Kind == "opening" || node.Kind == "lesson" || node.Kind == "summary" || node.Kind == "choice" {
			count++
		}
	}
	return count
}

func countLessonNodes(nodes []app.TeachingWorkflowNode) int {
	count := 0
	for _, node := range nodes {
		if node.Kind == "lesson" {
			count++
		}
	}
	return count
}

func previousTeachingNode(nodes []app.TeachingWorkflowNode, nodeID string) app.TeachingWorkflowNode {
	var previous app.TeachingWorkflowNode
	for _, node := range nodes {
		if node.ID == nodeID {
			return previous
		}
		if node.Kind == "opening" || node.Kind == "lesson" || node.Kind == "summary" {
			previous = node
		}
	}
	return app.TeachingWorkflowNode{}
}

func coveredTeachingPoints(nodes []app.TeachingWorkflowNode, nodeID string) []string {
	points := make([]string, 0, len(nodes))
	for _, node := range nodes {
		if node.ID == nodeID {
			break
		}
		if node.Kind != "opening" && node.Kind != "lesson" && node.Kind != "summary" {
			continue
		}
		if summary := strings.TrimSpace(node.Summary); summary != "" {
			points = append(points, summary)
		}
	}
	return points
}

func mergeGeneratedActNode(planned app.TeachingWorkflowNode, generated app.TeachingWorkflowNode) app.TeachingWorkflowNode {
	if strings.TrimSpace(planned.ID) == "" {
		planned.ID = strings.TrimSpace(generated.ID)
	}
	if strings.TrimSpace(planned.Kind) == "" {
		planned.Kind = strings.TrimSpace(generated.Kind)
	}
	planned.Title = firstNonEmpty(strings.TrimSpace(generated.Title), strings.TrimSpace(planned.Title))
	planned.Summary = firstNonEmpty(strings.TrimSpace(generated.Summary), strings.TrimSpace(planned.Summary))
	planned.Speaker = firstNonEmpty(strings.TrimSpace(generated.Speaker), strings.TrimSpace(planned.Speaker))
	planned.Decision = firstNonEmpty(strings.TrimSpace(generated.Decision), strings.TrimSpace(planned.Decision))
	if len(generated.Lines) > 0 {
		planned.Lines = generated.Lines
	}
	if len(generated.Choices) > 0 || planned.Kind == "summary" {
		planned.Choices = generated.Choices
	}
	syncWorkflowNodeLegacyFields(&planned)
	return planned
}

func assignChoiceTargets(node *app.TeachingWorkflowNode) bool {
	if node == nil || strings.TrimSpace(node.ID) == "" || len(node.Choices) == 0 {
		return false
	}
	changed := false
	seen := map[string]int{}
	for index := range node.Choices {
		if strings.TrimSpace(node.Choices[index].ID) == "" {
			node.Choices[index].ID = fmt.Sprintf("choice-%d", index+1)
			changed = true
		}
		if strings.TrimSpace(node.Choices[index].TargetNodeID) == "" {
			base := choiceBranchNodeID(*node, node.Choices[index], index)
			if count := seen[base]; count > 0 {
				node.Choices[index].TargetNodeID = fmt.Sprintf("%s-%d", base, count+1)
			} else {
				node.Choices[index].TargetNodeID = base
			}
			changed = true
			seen[base]++
			continue
		}
		seen[node.Choices[index].TargetNodeID]++
	}
	return changed
}

func choiceBranchNodeID(node app.TeachingWorkflowNode, choice app.SceneChoice, index int) string {
	base := firstNonEmpty(choice.ID, choice.Label, fmt.Sprintf("choice-%d", index+1))
	return strings.TrimSpace(node.ID) + "-choice-" + stableWorkflowIDPart(base, fmt.Sprintf("choice-%d", index+1))
}

func stableWorkflowIDPart(value string, fallback string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var builder strings.Builder
	lastDash := false
	for _, char := range value {
		if unicode.IsLetter(char) || unicode.IsDigit(char) {
			builder.WriteRune(char)
			lastDash = false
			continue
		}
		if !lastDash && builder.Len() > 0 {
			builder.WriteByte('-')
			lastDash = true
		}
	}
	part := strings.Trim(builder.String(), "-")
	if part == "" {
		part = fallback
	}
	return part
}

func workflowChoiceByID(node app.TeachingWorkflowNode, choiceID string) (app.SceneChoice, int, bool) {
	choiceID = strings.TrimSpace(choiceID)
	if choiceID == "" {
		return app.SceneChoice{}, -1, false
	}
	for index, choice := range node.Choices {
		if choice.ID == choiceID {
			return choice, index, true
		}
	}
	return app.SceneChoice{}, -1, false
}

func normalizeActDecision(node app.TeachingWorkflowNode, decision agent.ActDecision) agent.ActDecision {
	if decision != "" {
		return decision
	}
	if node.FreeDiscussion || node.Kind == "free_discussion" {
		return agent.ActDecisionFreeDiscussion
	}
	if node.Kind == "summary" {
		return agent.ActDecisionSummarize
	}
	return agent.ActDecisionContinue
}

func normalizeStoredDecision(node app.TeachingWorkflowNode) agent.ActDecision {
	return normalizeActDecision(node, agent.ActDecision(strings.TrimSpace(node.Decision)))
}

func plannedNodeForDecision(current app.TeachingWorkflowNode, nextIndex int, decision agent.ActDecision) (app.TeachingWorkflowNode, bool) {
	switch decision {
	case agent.ActDecisionContinue:
		return app.TeachingWorkflowNode{
			ID:            fmt.Sprintf("lesson-%d", nextIndex),
			Kind:          "lesson",
			Title:         fmt.Sprintf("第%d幕", nextIndex),
			Speaker:       current.Speaker,
			BackgroundKey: firstNonEmpty(current.BackgroundKey, "lesson"),
			BackgroundURL: current.BackgroundURL,
			Status:        app.WorkflowNodeStatusPending,
			VoiceStatus:   app.WorkflowNodeStatusPending,
		}, true
	case agent.ActDecisionSummarize:
		return app.TeachingWorkflowNode{
			ID:            "summary",
			Kind:          "summary",
			Title:         "总结回收",
			Speaker:       current.Speaker,
			BackgroundKey: firstNonEmpty(current.BackgroundKey, "summary"),
			BackgroundURL: current.BackgroundURL,
			Status:        app.WorkflowNodeStatusPending,
			VoiceStatus:   app.WorkflowNodeStatusPending,
		}, true
	case agent.ActDecisionFreeDiscussion:
		return app.TeachingWorkflowNode{
			ID:             "free-discussion",
			Kind:           "free_discussion",
			Title:          "自由讨论",
			Speaker:        current.Speaker,
			BackgroundKey:  firstNonEmpty(current.BackgroundKey, "discussion"),
			BackgroundURL:  current.BackgroundURL,
			FreeDiscussion: true,
			Status:         app.WorkflowNodeStatusReady,
			VoiceStatus:    app.WorkflowNodeStatusReady,
			ReadyAt:        time.Now().UTC(),
		}, true
	default:
		return app.TeachingWorkflowNode{}, false
	}
}

func shouldQueueNextAct(node app.TeachingWorkflowNode) bool {
	if node.Kind == "free_discussion" {
		return false
	}
	decision := normalizeStoredDecision(node)
	if node.Status != app.WorkflowNodeStatusReady {
		return false
	}
	return decision == agent.ActDecisionContinue || decision == agent.ActDecisionSummarize || decision == agent.ActDecisionFreeDiscussion
}

func shouldQueueWorkflowFollowups(node app.TeachingWorkflowNode) bool {
	if node.Kind == "free_discussion" || node.FreeDiscussion {
		return false
	}
	if !workflowNodeIsReady(node) {
		return false
	}
	if len(node.Choices) > 0 {
		return true
	}
	return shouldQueueNextAct(node)
}

func syncWorkflowNodeLegacyFields(node *app.TeachingWorkflowNode) {
	if node == nil {
		return
	}
	if len(node.Lines) == 0 {
		if strings.TrimSpace(node.Line) == "" && strings.TrimSpace(node.SpeechText) != "" {
			node.Line = node.SpeechText
		}
		if strings.TrimSpace(node.SpeechText) == "" && strings.TrimSpace(node.Line) != "" {
			node.SpeechText = node.Line
		}
		return
	}
	visible := make([]string, 0, len(node.Lines))
	speech := make([]string, 0, len(node.Lines))
	for index := range node.Lines {
		if strings.TrimSpace(node.Lines[index].SpeechText) == "" {
			node.Lines[index].SpeechText = node.Lines[index].Text
		}
		if strings.TrimSpace(node.Lines[index].Text) == "" {
			node.Lines[index].Text = node.Lines[index].SpeechText
		}
		if text := strings.TrimSpace(node.Lines[index].Text); text != "" {
			visible = append(visible, text)
		}
		if text := strings.TrimSpace(node.Lines[index].SpeechText); text != "" {
			speech = append(speech, text)
		}
	}
	node.Line = strings.Join(visible, "\n")
	node.SpeechText = strings.Join(speech, " ")
}

func dialogueSpeechText(line app.DialogueLine) (string, bool) {
	speechText := strings.TrimSpace(line.SpeechText)
	if speechText != "" {
		return speechText, false
	}
	return strings.TrimSpace(line.Text), true
}

func normalizeLanguageCode(language string) string {
	return app.NormalizeLanguageCode(language)
}

func isChineseLanguage(language string) bool {
	return app.IsChineseLanguage(language)
}

func isJapaneseLanguage(language string) bool {
	return app.IsJapaneseLanguage(language)
}

func containsJapaneseKana(text string) bool {
	for _, char := range text {
		if (char >= '\u3040' && char <= '\u30ff') || (char >= '\uff66' && char <= '\uff9f') {
			return true
		}
	}
	return false
}

func looksLikeChineseSentence(text string) bool {
	hanCount := 0
	for _, char := range text {
		if char >= '\u4e00' && char <= '\u9fff' {
			hanCount++
		}
	}
	return hanCount >= 4
}

func voiceIDForSynthesis(profile app.VoiceProfile, character app.Character) string {
	if profile.Extra != nil {
		if speaker := strings.TrimSpace(profile.Extra["speaker"]); speaker != "" {
			return speaker
		}
	}
	return firstNonEmpty(profile.VoiceID, character.VoiceID)
}

func resolveSceneCharacter(request app.SceneGenerateRequest) (app.Character, error) {
	if len(request.Characters) == 0 {
		return app.Character{}, fmt.Errorf("characters 不能为空")
	}
	character := request.Characters[0]
	if strings.TrimSpace(character.ID) == "" {
		return app.Character{}, fmt.Errorf("character.id 不能为空")
	}
	return character, nil
}

func firstSceneSpeaker(request app.SceneGenerateRequest) string {
	if len(request.Characters) == 0 {
		return ""
	}
	return firstNonEmpty(request.Characters[0].DisplayName, request.Characters[0].ID)
}

func firstSceneBackground(request app.SceneGenerateRequest) string {
	if len(request.Characters) == 0 {
		return ""
	}
	character := request.Characters[0]
	if value := strings.TrimSpace(character.Assets.BackgroundURL); value != "" {
		return value
	}
	if character.Assets.Backgrounds != nil {
		if value := strings.TrimSpace(character.Assets.Backgrounds["opening"]); value != "" {
			return value
		}
	}
	return ""
}
