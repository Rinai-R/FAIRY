package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
	"unicode/utf8"

	agentcodex "github.com/Rinai-R/FAIRY/internal/adapters/agent/codex"
	"github.com/Rinai-R/FAIRY/internal/adapters/health"
	"github.com/Rinai-R/FAIRY/internal/adapters/scene"
	scenemock "github.com/Rinai-R/FAIRY/internal/adapters/scene/mock"
	"github.com/Rinai-R/FAIRY/internal/app"
)

type Engine struct {
	Runner *agentcodex.Runner
	Bin    string
}

type Options struct {
	CodexBin     string
	CodexModel   string
	CodexWorkDir string
	CodexTimeout int
}

func NewEngine(options Options) Engine {
	timeout := time.Duration(options.CodexTimeout) * time.Second
	return Engine{
		Runner: agentcodex.NewRunner(options.CodexBin, options.CodexModel, options.CodexWorkDir, timeout),
		Bin:    firstNonEmpty(options.CodexBin, agentcodex.DefaultBin),
	}
}

func (e Engine) Generate(ctx context.Context, input scene.Input) (app.SceneGenerateResponse, error) {
	req := input.Request
	if strings.TrimSpace(req.DocumentText) == "" && documentSource(req.Variables) == "" {
		return app.SceneGenerateResponse{}, fmt.Errorf("document_text、document_url 或 document_asset 不能为空")
	}
	if len(req.Characters) == 0 {
		return app.SceneGenerateResponse{}, fmt.Errorf("characters 至少需要 1 个角色")
	}

	body, err := json.MarshalIndent(struct {
		Request app.SceneGenerateRequest `json:"request"`
	}{Request: req}, "", "  ")
	if err != nil {
		return app.SceneGenerateResponse{}, err
	}

	var out app.SceneGenerateResponse
	if _, err := e.Runner.ExecJSON(ctx, agentcodex.ExecRequest{
		Prompt: buildPrompt(req, string(body)),
		Schema: sceneSchema,
	}, &out); err != nil {
		// Fall back to mock scene on Codex failure
		return scenemock.Engine{}.Generate(ctx, scene.Input{Request: req})
	}
	return normalizeResponse(req, out), nil
}

func (e Engine) Check(_ context.Context) health.Result {
	status := health.StatusOK
	message := "codex scene provider 可用"
	if _, err := exec.LookPath(e.Bin); err != nil {
		status = health.StatusDown
		message = "未找到 codex CLI: " + err.Error()
	}
	return health.Result{
		Domain:    "scene",
		Provider:  string(scene.ProviderCodex),
		Status:    status,
		Message:   message,
		CheckedAt: time.Now().UTC(),
	}
}

func buildPrompt(req app.SceneGenerateRequest, body string) string {
	topic := strings.TrimSpace(req.Topic)
	if topic == "" {
		topic = "未命名材料"
	}
	goal := strings.TrimSpace(req.LearningGoal)
	if goal == "" {
		goal = "理解核心概念，并能用自己的话复述。"
	}
	displayLanguage := firstNonEmpty(req.Runtime.Language.DisplayLanguage, "zh-CN")
	speechLanguage := firstNonEmpty(req.Runtime.Language.SpeechLanguage, displayLanguage)

	return fmt.Sprintf(`你是 FAIRY 的材料视觉小说编排 Agent。

任务：
- 基于用户提供的文档材料生成一个教学 Galgame 篇章。最重要的是：每幕必须包含多轮角色对话（lines），而不是一句独白。角色之间要像真实对话一样有来有回——讲解者解释一段、追问者追问或质疑、讲解者回应，这样交替进行。
- 文档材料可能是 document_text、variables.document_url 指向的网页、或 variables.document_asset_path 指向的本地文件；优先读取这些材料源。
- 不要替玩家发言，玩家只在每幕末尾的自由讨论环节参与。
- 场景必须围绕文档内容和学习目标，不要编造材料外事实。
- 如果 URL 或本地文件无法读取，opening_message 需要自然地请玩家补充正文。

结构要求：
- workflow.nodes 的结构是：opening → lesson-1 → free-1 → lesson-2 → free-2 → ... → lesson-N → free-N → summary → free-discussion
- 每个 lesson 节点覆盖材料中的一个教学点（按文档的章节/论证结构提取），lesson 的数量取决于材料长度（通常 3-8 个）。
- 每个 lesson 节点后面紧跟一个 free_discussion 节点，玩家可以针对刚讲的内容自由提问，也可以点跳过进入下一幕。
- 最后的 free-discussion 是全局自由讨论，玩家可以讨论任何与材料相关的内容。
- opening 也是多轮对话，角色之间自然引入主题，不要像 PPT 开场白。

lines 字段：
- 每个 lesson 和 opening 节点必须包含 lines 数组（而不是 line 字段）。lines 是这条教学点多轮角色对话，每条包含 speaker（说话人）、text（屏幕显示文本）、speech_text（语音合成文本，如果和显示语言不同）、expression（表情，可选）。
- 每幕的 lines 至少要有 6 条（角色之间有来有回），内容要自然——不是一个人在讲课，而是两个角色在讨论。追问者可以表示疑惑、要求举例、或者用另一种方式重述刚讲的概念。
- 禁止使用“镜头”“线索”“骨架”“纹理”“光束”“舞台”等抽象比喻——像真人聊天一样说话。

约束：
- topic: %s
- learning_goal: %s
- workflow.nodes[].lines[].text 必须使用屏幕显示语言：%s。
- workflow.nodes[].lines[].speech_text 必须使用语音合成语言：%s。
- opening_message 控制在 2 到 4 句，像角色开场对白。
- prompt.scene_instruction 必须携带材料摘要、互动约束和教学边界。
- prompt.response_contract 必须要求 display_text 与 speech_text 分离，speech_text 做角色化发声本地化；每轮自由讨论回应 2 到 5 句。
- prompt 内容不得包含“复杂度判定”“执行路径”“节点”“工作流”“OpenSpec”“Superpowers”等元信息。
- scene.variables 至少包含 mode、topic、learning_goal、outline、document_summary。
- workflow.nodes 的 kind 只能使用 opening、lesson、free_discussion、summary。
- JSON schema 中 workflow.nodes 是内部字段名，但所有用户可见文案不得出现“节点”“工作流”等工程词。
- scene.last_active_at 和 relation.updated_at 使用 RFC3339 时间字符串。
- image.prompt 必须包含角色、场景、mood、Galgame CG 风格。
- workflow.nodes[].background_url 只能使用输入角色资源里的背景 URL 或空字符串；优先通过 background_key 选择。

安全边界：
- 不要写代码，不要修改文件，不要执行命令。
- 只返回符合 schema 的 JSON 内容。

输入：
%s
`, topic, goal, displayLanguage, speechLanguage, body)
}

func normalizeResponse(req app.SceneGenerateRequest, out app.SceneGenerateResponse) app.SceneGenerateResponse {
	now := time.Now().UTC()
	topic := firstNonEmpty(strings.TrimSpace(req.Topic), out.Scene.Variables["topic"], inferTopic(req.DocumentText))
	goal := firstNonEmpty(strings.TrimSpace(req.LearningGoal), out.Scene.Variables["learning_goal"], "理解核心概念，并能用自己的话复述。")
	activeCharacter := firstCharacter(req.Characters)

	if out.Scene.ID == "" {
		out.Scene.ID = "lesson-" + slug(topic)
	}
	if out.Scene.Title == "" {
		out.Scene.Title = "文档教学：" + topic
	}
	if out.Scene.Location == "" {
		out.Scene.Location = "interactive classroom"
	}
	if out.Scene.Phase == "" {
		out.Scene.Phase = "opening"
	}
	if out.Scene.LastActiveAt.IsZero() {
		out.Scene.LastActiveAt = now
	}
	out.Scene.Variables = mergeVariables(map[string]string{
		"mode":             "teaching",
		"topic":            topic,
		"learning_goal":    goal,
		"document_summary": truncateRunes(req.DocumentText, 600),
	}, out.Scene.Variables)

	if out.Session.ID == "" {
		out.Session.ID = out.Scene.ID + ":default"
	}
	if out.Session.UserID == "" {
		out.Session.UserID = "default"
	}
	if out.Session.ActiveCharacterID == "" {
		out.Session.ActiveCharacterID = activeCharacter.ID
	}
	if len(out.Session.ParticipantIDs) == 0 {
		out.Session.ParticipantIDs = participantIDs(req.Characters)
	} else {
		out.Session.ParticipantIDs = normalizeParticipantIDs(req.Characters, out.Session.ParticipantIDs)
	}

	if out.Relation.UserID == "" {
		out.Relation.UserID = out.Session.UserID
	}
	if out.Relation.UpdatedAt.IsZero() {
		out.Relation.UpdatedAt = now
	}
	if out.OpeningMessage == "" {
		out.OpeningMessage = "我已经读完这份材料了。我们从「" + topic + "」开始，顺着材料本身的思路往下走——它问了什么、怎么回答的、最后落到哪里。中间你可以随时说哪里不对或没听懂，等主线走完再随便追问。"
	}
	out.Interaction = normalizeInteraction(req.InteractionMode, out.Interaction, topic)
	out.Workflow = normalizeWorkflow(out.Workflow, out.Scene.ID, topic, goal, req.DocumentText, activeCharacter, out.Interaction)
	if out.Image.Prompt == "" {
		out.Image = app.ImageRequest{
			Enabled:           true,
			Prompt:            "anime visual novel character CG, teaching opening scene, topic: " + topic,
			ReferenceImageURL: referenceImageURL(activeCharacter),
			Style:             "galgame character cg",
			Size:              "1280x720",
			Extra: map[string]string{
				"purpose":        "galgame_character_cg",
				"character_id":   activeCharacter.ID,
				"character_name": activeCharacter.DisplayName,
				"mood":           "welcoming",
				"expression":     "soft_smile",
			},
		}
	}
	if out.Prompt.ResponseContract == "" {
		out.Prompt.ResponseContract = "display_text 用于屏幕显示；speech_text 用于语音合成，必须按当前角色口吻做自然发声本地化，不能机械直译。每轮 2 到 5 句，先回应玩家，再结合材料细节继续讲清楚。"
	}
	out.OpeningMessage = stripInternalMeta(out.OpeningMessage)
	out.Prompt.System = stripInternalMeta(out.Prompt.System)
	out.Prompt.Developer = stripInternalMeta(out.Prompt.Developer)
	out.Prompt.SceneInstruction = stripInternalMeta(out.Prompt.SceneInstruction)
	out.Prompt.ResponseContract = stripInternalMeta(out.Prompt.ResponseContract)
	return out
}

func normalizeInteraction(requested string, interaction app.SceneInteraction, topic string) app.SceneInteraction {
	mode := strings.TrimSpace(interaction.Mode)
	if mode == "" {
		mode = strings.TrimSpace(requested)
	}
	if mode != "choice" {
		return app.SceneInteraction{Mode: "dialogue"}
	}
	choices := make([]app.SceneChoice, 0, len(interaction.Choices))
	for _, choice := range interaction.Choices {
		if strings.TrimSpace(choice.ID) == "" || strings.TrimSpace(choice.Label) == "" || strings.TrimSpace(choice.Text) == "" {
			continue
		}
		choices = append(choices, choice)
	}
	if len(choices) == 0 {
		choices = []app.SceneChoice{
			{ID: "ask-summary", Label: "先讲直觉", Text: "先用直觉方式解释一下这份材料的核心意思。"},
			{ID: "ask-example", Label: "举个例子", Text: "围绕「" + topic + "」举一个贴近现实的例子。"},
			{ID: "challenge", Label: "我来反驳", Text: "我想挑战一下这个主题，请你基于材料回应我的质疑。"},
		}
	}
	return app.SceneInteraction{Mode: "choice", Choices: choices}
}

func normalizeWorkflow(workflow app.TeachingWorkflow, sceneID string, topic string, goal string, document string, character app.Character, interaction app.SceneInteraction) app.TeachingWorkflow {
	if strings.TrimSpace(workflow.ID) == "" {
		workflow.ID = sceneID + "-workflow"
	}
	if strings.TrimSpace(workflow.Title) == "" {
		workflow.Title = "教学剧情：" + topic
	}
	if strings.TrimSpace(workflow.Goal) == "" {
		workflow.Goal = goal
	}
	nodes := make([]app.TeachingWorkflowNode, 0, len(workflow.Nodes))
	for _, node := range workflow.Nodes {
		if strings.TrimSpace(node.ID) == "" || strings.TrimSpace(node.Kind) == "" || strings.TrimSpace(node.Title) == "" {
			continue
		}
		if !validWorkflowKind(node.Kind) {
			continue
		}
		if strings.TrimSpace(node.SpeechText) == "" {
			node.SpeechText = node.Line
		}
		node.Title = cleanVisibleScriptText(node.Title)
		node.Summary = cleanVisibleScriptText(node.Summary)
		node.Line = cleanVisibleScriptText(node.Line)
		node.SpeechText = cleanVisibleScriptText(node.SpeechText)
		node.Challenge = cleanVisibleScriptText(node.Challenge)
		node.Lines = cleanDialogueLines(node.Lines, node.Speaker)
		if len(node.Lines) > 0 {
			if strings.TrimSpace(node.Line) == "" {
				node.Line = node.Lines[0].Text
			}
			if strings.TrimSpace(node.SpeechText) == "" {
				node.SpeechText = firstNonEmpty(node.Lines[0].SpeechText, node.Lines[0].Text)
			}
		}
		node.Choices = cleanChoices(node.Choices)
		nodes = append(nodes, node)
	}
	if !workflowMeetsTeachingQuality(nodes) {
		nodes = fallbackWorkflowNodes(topic, document, character, interaction)
	}
	workflow.Nodes = nodes
	if strings.TrimSpace(workflow.CurrentNodeID) == "" || !workflowHasNode(workflow.Nodes, workflow.CurrentNodeID) {
		workflow.CurrentNodeID = workflow.Nodes[0].ID
	}
	return workflow
}

func fallbackWorkflowNodes(topic string, document string, character app.Character, interaction app.SceneInteraction) []app.TeachingWorkflowNode {
	speaker := firstNonEmpty(character.DisplayName, character.ID, "讲解者")
	background := firstBackgroundURL(character)
	points := fallbackTeachingPoints(document)
	if len(points) < 2 {
		points = []string{"核心概念", "关键机制", "应用与边界"}
	}
	nodes := []app.TeachingWorkflowNode{
		{ID: "opening", Kind: "opening", Title: "开场对白", Speaker: speaker, BackgroundKey: "opening", BackgroundURL: background, NextNodeID: "lesson-1"},
	}
	for i, point := range points {
		lessonID := "lesson-" + string(rune('1'+i))
		freeID := "free-" + string(rune('1'+i))
		nextLessonID := "lesson-" + string(rune('1'+i+1))
		if i == len(points)-1 {
			nextLessonID = "summary"
		}
		nodes = append(nodes,
			app.TeachingWorkflowNode{
				ID: lessonID, Kind: "lesson", Title: "第" + chineseNumber(i+1) + "幕", Summary: point,
				Speaker: speaker, BackgroundKey: "lesson",
				BackgroundURL: firstBackgroundURLByKey(character, "lesson", background), NextNodeID: freeID,
			},
			app.TeachingWorkflowNode{
				ID: freeID, Kind: "free_discussion", Title: "自由讨论", Summary: point,
				Speaker: speaker, BackgroundKey: "discussion",
				BackgroundURL:  firstBackgroundURLByKey(character, "discussion", background),
				FreeDiscussion: true, NextNodeID: nextLessonID,
			},
		)
	}
	nodes = append(nodes,
		app.TeachingWorkflowNode{
			ID: "summary", Kind: "summary", Title: "总结回收",
			Speaker: speaker, BackgroundKey: "summary",
			BackgroundURL: firstBackgroundURLByKey(character, "summary", background), NextNodeID: "free-discussion",
		},
		app.TeachingWorkflowNode{
			ID: "free-discussion", Kind: "free_discussion", Title: "自由讨论",
			Speaker: speaker, BackgroundKey: "discussion",
			BackgroundURL: firstBackgroundURLByKey(character, "discussion", background), FreeDiscussion: true,
		},
	)
	return nodes
}

func fallbackTeachingPoints(document string) []string {
	text := strings.TrimSpace(document)
	if text == "" {
		return nil
	}
	var points []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		trimmed := strings.TrimLeft(line, "# 　\t")
		if strings.HasPrefix(line, "#") && utf8.RuneCountInString(trimmed) >= 3 {
			points = append(points, truncateRunes(trimmed, 42))
			if len(points) >= 8 {
				break
			}
		}
	}
	if len(points) < 2 {
		for _, para := range strings.Split(text, "\n\n") {
			para = strings.Join(strings.Fields(para), " ")
			if utf8.RuneCountInString(para) >= 18 {
				points = append(points, truncateRunes(para, 48))
				if len(points) >= 8 {
					break
				}
			}
		}
	}
	return points
}

func workflowMeetsTeachingQuality(nodes []app.TeachingWorkflowNode) bool {
	if len(nodes) < 5 {
		return false
	}
	lessonCount := 0
	hasOpening := false
	summaryIndex := -1
	finalFreeIndex := -1
	for i, node := range nodes {
		switch node.Kind {
		case "opening":
			hasOpening = true
		case "lesson":
			lessonCount++
		case "summary":
			summaryIndex = i
		case "free_discussion":
			finalFreeIndex = i
		}
	}
	if !hasOpening || lessonCount < 2 {
		return false
	}
	if finalFreeIndex != len(nodes)-1 {
		return false
	}
	if summaryIndex < 0 || summaryIndex >= finalFreeIndex {
		return false
	}
	return true
}

func cleanChoices(choices []app.SceneChoice) []app.SceneChoice {
	out := make([]app.SceneChoice, 0, len(choices))
	for _, choice := range choices {
		choice.Label = cleanVisibleScriptText(choice.Label)
		choice.Text = cleanVisibleScriptText(choice.Text)
		choice.Hint = cleanVisibleScriptText(choice.Hint)
		out = append(out, choice)
	}
	return out
}

func cleanDialogueLines(lines []app.DialogueLine, fallbackSpeaker string) []app.DialogueLine {
	out := make([]app.DialogueLine, 0, len(lines))
	for _, line := range lines {
		line.Speaker = firstNonEmpty(cleanVisibleScriptText(line.Speaker), fallbackSpeaker)
		line.Text = cleanVisibleScriptText(line.Text)
		line.SpeechText = cleanVisibleScriptText(line.SpeechText)
		line.Expression = cleanVisibleScriptText(line.Expression)
		if strings.TrimSpace(line.Text) == "" && strings.TrimSpace(line.SpeechText) == "" {
			continue
		}
		if strings.TrimSpace(line.Text) == "" {
			line.Text = line.SpeechText
		}
		if strings.TrimSpace(line.SpeechText) == "" {
			line.SpeechText = line.Text
		}
		out = append(out, line)
	}
	return out
}

func firstBackgroundURL(character app.Character) string {
	return firstBackgroundURLByKey(character, "opening", character.Assets.BackgroundURL)
}

func firstBackgroundURLByKey(character app.Character, key string, fallback string) string {
	if character.Assets.Backgrounds != nil {
		if value := strings.TrimSpace(character.Assets.Backgrounds[key]); value != "" {
			return value
		}
	}
	return strings.TrimSpace(fallback)
}

func validWorkflowKind(kind string) bool {
	switch kind {
	case "opening", "lesson", "choice", "challenge", "free_discussion", "summary":
		return true
	default:
		return false
	}
}

func workflowHasNode(nodes []app.TeachingWorkflowNode, id string) bool {
	for _, node := range nodes {
		if node.ID == id {
			return true
		}
	}
	return false
}

func firstCharacter(characters []app.Character) app.Character {
	for _, character := range characters {
		if character.ID != "" {
			return character
		}
	}
	return app.Character{ID: "tutor", DisplayName: "讲解者"}
}

func documentSource(variables map[string]string) string {
	for _, key := range []string{"document_url", "source_url", "document_asset_path", "material_file_path"} {
		if value := strings.TrimSpace(variables[key]); value != "" {
			return value
		}
	}
	return ""
}

func participantIDs(characters []app.Character) []string {
	ids := make([]string, 0, len(characters))
	for _, character := range characters {
		if character.ID != "" {
			ids = append(ids, character.ID)
		}
	}
	return ids
}

func chineseNumber(value int) string {
	numbers := []string{"零", "一", "二", "三", "四", "五", "六", "七", "八", "九", "十"}
	if value >= 0 && value < len(numbers) {
		return numbers[value]
	}
	return "多"
}

func normalizeParticipantIDs(characters []app.Character, ids []string) []string {
	known := map[string]bool{}
	for _, character := range characters {
		if character.ID != "" {
			known[character.ID] = true
		}
	}
	out := make([]string, 0, len(ids))
	seen := map[string]bool{}
	for _, id := range ids {
		if known[id] && !seen[id] {
			out = append(out, id)
			seen[id] = true
		}
	}
	if len(out) == 0 {
		return participantIDs(characters)
	}
	return out
}

func stripInternalMeta(value string) string {
	lines := strings.Split(value, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.Contains(line, "复杂度判定") || strings.Contains(line, "执行路径") || strings.Contains(line, "内部策略") || strings.Contains(line, "OpenSpec") || strings.Contains(line, "Superpowers") {
			continue
		}
		out = append(out, line)
	}
	return cleanVisibleScriptText(strings.Join(out, "\n"))
}

func cleanVisibleScriptText(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	replacer := strings.NewReplacer(
		"教学工作流", "教学剧情",
		"工作流", "剧情",
		"脚本节点", "剧情段落",
		"自由讨论节点", "自由讨论",
		"节点", "段落",
		"复杂度判定", "",
		"执行路径", "",
		"内部策略", "",
		"OpenSpec", "",
		"Superpowers", "",
		"Phase 0", "",
		"AGENTS.md", "",
		"RTK", "",
		"Codex", "Agent",
	)
	value = replacer.Replace(value)
	lines := strings.Split(value, "\n")
	for index, line := range lines {
		lines[index] = strings.Join(strings.Fields(line), " ")
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func referenceImageURL(character app.Character) string {
	if character.Assets.ReferenceImageURL != "" {
		return character.Assets.ReferenceImageURL
	}
	if character.Assets.PortraitURL != "" {
		return character.Assets.PortraitURL
	}
	return character.AvatarURL
}

func inferTopic(document string) string {
	lines := strings.Split(strings.TrimSpace(document), "\n")
	for _, line := range lines {
		line = strings.Trim(strings.TrimSpace(line), "# 　\t")
		if utf8.RuneCountInString(line) >= 2 {
			return truncateRunes(line, 24)
		}
	}
	return "新的文档"
}

func mergeVariables(base map[string]string, extra map[string]string) map[string]string {
	out := map[string]string{}
	for key, value := range base {
		out[key] = value
	}
	for key, value := range extra {
		out[key] = value
	}
	return out
}

func slug(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	replacer := strings.NewReplacer(" ", "-", "/", "-", "\\", "-", ":", "-", "，", "-", "。", "-", "：", "-")
	value = replacer.Replace(value)
	if value == "" {
		return "document"
	}
	return truncateRunes(value, 48)
}

func truncateRunes(value string, limit int) string {
	if limit <= 0 || utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return string(runes[:limit])
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

const sceneSchema = `{
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "scene": {
      "type": "object",
      "additionalProperties": false,
      "properties": {
        "id": { "type": "string" },
        "title": { "type": "string" },
        "location": { "type": "string" },
        "phase": { "type": "string" },
        "variables": {
          "type": "object",
          "additionalProperties": false,
          "properties": {
            "mode": { "type": "string" },
            "topic": { "type": "string" },
            "learning_goal": { "type": "string" },
            "outline": { "type": "string" },
            "document_summary": { "type": "string" },
            "difficulty": { "type": "string" },
            "interaction": { "type": "string" }
          },
          "required": ["mode", "topic", "learning_goal", "outline", "document_summary", "difficulty", "interaction"]
        },
        "last_active_at": { "type": "string" }
      },
      "required": ["id", "title", "location", "phase", "variables", "last_active_at"]
    },
    "session": {
      "type": "object",
      "additionalProperties": false,
      "properties": {
        "id": { "type": "string" },
        "user_id": { "type": "string" },
        "active_character_id": { "type": "string" },
        "participant_ids": {
          "type": "array",
          "items": { "type": "string" }
        }
      },
      "required": ["id", "user_id", "active_character_id", "participant_ids"]
    },
    "relation": {
      "type": "object",
      "additionalProperties": false,
      "properties": {
        "user_id": { "type": "string" },
        "affinity": { "type": "number" },
        "trust": { "type": "number" },
        "tension": { "type": "number" },
        "closeness": { "type": "number" },
        "updated_at": { "type": "string" }
      },
      "required": ["user_id", "affinity", "trust", "tension", "closeness", "updated_at"]
    },
    "opening_message": { "type": "string" },
    "interaction": {
      "type": "object",
      "additionalProperties": false,
      "properties": {
        "mode": {
          "type": "string",
          "enum": ["dialogue", "choice"]
        },
        "choices": {
          "type": "array",
          "items": {
            "type": "object",
            "additionalProperties": false,
            "properties": {
              "id": { "type": "string" },
              "label": { "type": "string" },
              "text": { "type": "string" },
              "hint": { "type": "string" }
            },
            "required": ["id", "label", "text", "hint"]
          }
        }
      },
      "required": ["mode", "choices"]
    },
    "workflow": {
      "type": "object",
      "additionalProperties": false,
      "properties": {
        "id": { "type": "string" },
        "title": { "type": "string" },
        "goal": { "type": "string" },
        "current_node_id": { "type": "string" },
        "nodes": {
          "type": "array",
          "items": {
            "type": "object",
            "additionalProperties": false,
            "properties": {
              "id": { "type": "string" },
              "kind": {
                "type": "string",
                "enum": ["opening", "lesson", "choice", "challenge", "free_discussion", "summary"]
              },
              "title": { "type": "string" },
              "summary": { "type": "string" },
              "speaker": { "type": "string" },
              "line": { "type": "string" },
              "speech_text": { "type": "string" },
              "lines": {
                "type": "array",
                "items": {
                  "type": "object",
                  "additionalProperties": false,
                  "properties": {
                    "speaker": { "type": "string" },
                    "text": { "type": "string" },
                    "speech_text": { "type": "string" },
                    "expression": { "type": "string" }
                  },
                  "required": ["speaker", "text", "speech_text", "expression"]
                }
              },
              "challenge": { "type": "string" },
              "background_key": { "type": "string" },
              "background_url": { "type": "string" },
              "choices": {
                "type": "array",
                "items": {
                  "type": "object",
                  "additionalProperties": false,
                  "properties": {
                    "id": { "type": "string" },
                    "label": { "type": "string" },
                    "text": { "type": "string" },
                    "hint": { "type": "string" }
                  },
                  "required": ["id", "label", "text", "hint"]
                }
              },
              "next_node_id": { "type": "string" },
              "free_discussion": { "type": "boolean" }
            },
            "required": ["id", "kind", "title", "summary", "speaker", "background_key", "background_url", "choices", "next_node_id", "free_discussion"]
          }
        }
      },
      "required": ["id", "title", "goal", "current_node_id", "nodes"]
    },
    "image": {
      "type": "object",
      "additionalProperties": false,
      "properties": {
        "enabled": { "type": "boolean" },
        "prompt": { "type": "string" },
        "reference_image_url": { "type": "string" },
        "style": { "type": "string" },
        "size": { "type": "string" },
        "extra": {
          "type": "object",
          "additionalProperties": false,
          "properties": {
            "purpose": { "type": "string" },
            "character_id": { "type": "string" },
            "character_name": { "type": "string" },
            "mood": { "type": "string" },
            "expression": { "type": "string" }
          },
          "required": ["purpose", "character_id", "character_name", "mood", "expression"]
        }
      },
      "required": ["enabled", "prompt", "reference_image_url", "style", "size", "extra"]
    },
    "prompt": {
      "type": "object",
      "additionalProperties": false,
      "properties": {
        "system": { "type": "string" },
        "developer": { "type": "string" },
        "scene_instruction": { "type": "string" },
        "response_contract": { "type": "string" },
        "style_rules": {
          "type": "array",
          "items": { "type": "string" }
        }
      },
      "required": ["system", "developer", "scene_instruction", "response_contract", "style_rules"]
    }
  },
  "required": ["scene", "session", "relation", "opening_message", "interaction", "workflow", "image", "prompt"]
}`
