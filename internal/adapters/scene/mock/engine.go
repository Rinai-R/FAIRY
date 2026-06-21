package mock

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Rinai-R/FAIRY/internal/adapters/health"
	"github.com/Rinai-R/FAIRY/internal/adapters/scene"
	"github.com/Rinai-R/FAIRY/internal/app"
)

type Engine struct{}

func (Engine) Generate(_ context.Context, input scene.Input) (app.SceneGenerateResponse, error) {
	req := input.Request
	materialText := app.SceneGenerateMaterialText(req)
	topic := strings.TrimSpace(req.Topic)
	if topic == "" {
		topic = inferTopic(materialText)
	}
	goal := strings.TrimSpace(req.LearningGoal)
	if goal == "" {
		goal = "理解文档的核心概念，并能用自己的话复述。"
	}

	activeCharacterID := "tutor"
	activeCharacter := app.Character{}
	if len(req.Characters) > 0 && req.Characters[0].ID != "" {
		activeCharacterID = req.Characters[0].ID
		activeCharacter = req.Characters[0]
	}

	now := time.Now().UTC()
	sceneID := "lesson-" + slug(topic)
	outline := outlineFor(materialText, goal)
	interaction := interactionFor(req.InteractionMode, topic)
	workflow := workflowFor(sceneID, topic, goal, materialText, activeCharacter, interaction, req.Runtime.Language)
	return app.SceneGenerateResponse{
		Scene: app.Scene{
			ID:           sceneID,
			Title:        "文档教学：" + topic,
			Location:     "interactive classroom",
			Phase:        "lesson",
			Variables:    mergeVariables(req.Variables, lessonVariables(topic, goal, outline, interaction.Mode)),
			LastActiveAt: now,
		},
		Session: app.Session{
			ID:                sceneID + ":default",
			UserID:            "default",
			ActiveCharacterID: activeCharacterID,
			ParticipantIDs:    participantIDs(req.Characters),
		},
		Relation: app.Relationship{
			UserID:    "default",
			Affinity:  0.36,
			Trust:     0.58,
			Tension:   0.02,
			Closeness: 0.4,
			UpdatedAt: now,
		},
		OpeningMessage: "读完啦。我们从「" + topic + "」开始，按主线走。每讲完一段我会停下来，你可以随时提问。准备好了吗？",
		Interaction:    interaction,
		Workflow:       workflow,
		Image: app.ImageRequest{
			Enabled:           true,
			Prompt:            sceneImagePrompt(topic, activeCharacter),
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
		},
		Prompt: promptFor(topic, goal, outline, materialText, req.Prompt),
	}, nil
}

func workflowFor(sceneID string, topic string, goal string, document string, character app.Character, interaction app.SceneInteraction, language app.LanguagePlan) app.TeachingWorkflow {
	speaker := firstNonEmpty(character.DisplayName, character.ID, "讲解者")
	background := firstBackgroundURL(character)
	points := teachingPoints(document, goal)
	nodes := []app.TeachingWorkflowNode{
		{
			ID:            "opening",
			Kind:          "opening",
			Title:         "开场对白",
			Summary:       topic,
			Speaker:       speaker,
			BackgroundKey: "opening",
			BackgroundURL: background,
			NextNodeID:    "lesson-1",
		},
	}
	for index, point := range points {
		lessonID := "lesson-" + string(rune('1'+index))
		freeID := "free-" + string(rune('1'+index))
		nextLessonID := "lesson-" + string(rune('1'+index+1))
		if index == len(points)-1 {
			nextLessonID = "summary"
		}
		nodes = append(nodes,
			app.TeachingWorkflowNode{
				ID:            lessonID,
				Kind:          "lesson",
				Title:         "第" + chineseNumber(index+1) + "幕",
				Summary:       point,
				Speaker:       speaker,
				BackgroundKey: "lesson",
				BackgroundURL: firstBackgroundURLByKey(character, "lesson", background),
				NextNodeID:    freeID,
			},
			app.TeachingWorkflowNode{
				ID:             freeID,
				Kind:           "free_discussion",
				Title:          "自由讨论：第" + chineseNumber(index+1) + "幕",
				Summary:        point,
				Speaker:        speaker,
				BackgroundKey:  "discussion",
				BackgroundURL:  firstBackgroundURLByKey(character, "discussion", background),
				FreeDiscussion: true,
				NextNodeID:     nextLessonID,
			},
		)
	}
	nodes = append(nodes,
		app.TeachingWorkflowNode{
			ID:            "summary",
			Kind:          "summary",
			Title:         "总结回收",
			Speaker:       speaker,
			BackgroundKey: "summary",
			BackgroundURL: firstBackgroundURLByKey(character, "summary", background),
			NextNodeID:    "free-discussion",
		},
		app.TeachingWorkflowNode{
			ID:             "free-discussion",
			Kind:           "free_discussion",
			Title:          "自由讨论",
			Speaker:        speaker,
			BackgroundKey:  "discussion",
			BackgroundURL:  firstBackgroundURLByKey(character, "discussion", background),
			FreeDiscussion: true,
		},
	)
	return app.TeachingWorkflow{
		ID:            sceneID + "-workflow",
		Title:         "教学剧情：" + topic,
		Goal:          goal,
		CurrentNodeID: "opening",
		Nodes:         nodes,
	}
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

func sceneImagePrompt(topic string, character app.Character) string {
	parts := []string{
		"anime visual novel character CG",
		"document-based lesson scene",
		"expressive face",
		"clean white interface atmosphere",
		"dark red accent detail",
		"topic: " + topic,
	}
	if character.DisplayName != "" {
		parts = append(parts, "character: "+character.DisplayName)
	}
	if character.Assets.CGPrompt != "" {
		parts = append(parts, character.Assets.CGPrompt)
	}
	if character.Assets.StylePrompt != "" {
		parts = append(parts, character.Assets.StylePrompt)
	}
	return strings.Join(parts, ", ")
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

func (Engine) Check(_ context.Context) health.Result {
	return health.Result{
		Domain:    "scene",
		Provider:  string(scene.ProviderMock),
		Status:    health.StatusOK,
		Message:   "互动教学舞台生成器可用",
		CheckedAt: time.Now().UTC(),
	}
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

func outlineFor(document string, goal string) string {
	text := strings.TrimSpace(document)
	if text == "" {
		return "1. 先确认学习目标；2. 补充材料线索；3. 用剧情段落讲清主线；4. 最后开放问答。"
	}
	points := teachingPoints(document, goal)
	return "1. 建立开场语境；2. 按材料结构推进 " + chineseNumber(len(points)) + " 幕讲解；3. 穿插选择问答检查理解；4. 总结回收后才进入自由讨论。学习目标：" + goal
}

func teachingPoints(document string, goal string) []string {
	text := strings.TrimSpace(document)
	points := make([]string, 0, 8)
	for _, rawLine := range strings.Split(text, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		trimmed := strings.TrimLeft(line, "# 　\t")
		headingLike := strings.HasPrefix(line, "#") || startsWithNumberedSection(trimmed)
		if headingLike && utf8.RuneCountInString(trimmed) >= 3 {
			points = appendUniquePoint(points, truncateRunes(trimmed, 42))
		}
		if len(points) >= 8 {
			return points
		}
	}

	if len(points) < 3 {
		for _, paragraph := range strings.Split(text, "\n\n") {
			paragraph = strings.Join(strings.Fields(paragraph), " ")
			if utf8.RuneCountInString(paragraph) < 18 {
				continue
			}
			points = appendUniquePoint(points, truncateRunes(paragraph, 48))
			if len(points) >= 8 {
				break
			}
		}
	}

	fallbacks := []string{
		"问题背景与学习目标",
		"材料中的核心概念",
		"概念之间的因果关系",
		"方法步骤和适用边界",
		"常见误解与检查问题",
	}
	if strings.TrimSpace(goal) != "" {
		fallbacks[0] = goal
	}
	for len(points) < 3 {
		points = appendUniquePoint(points, fallbacks[len(points)%len(fallbacks)])
	}
	return points
}

func appendUniquePoint(points []string, point string) []string {
	point = strings.Trim(strings.TrimSpace(point), "：:")
	if point == "" {
		return points
	}
	for _, existing := range points {
		if existing == point {
			return points
		}
	}
	return append(points, point)
}

func startsWithNumberedSection(value string) bool {
	runes := []rune(value)
	if len(runes) < 3 {
		return false
	}
	first := runes[0]
	return first >= '0' && first <= '9' && (runes[1] == '.' || runes[1] == '、' || runes[1] == ')')
}

func chineseNumber(value int) string {
	numbers := []string{"零", "一", "二", "三", "四", "五", "六", "七", "八", "九", "十"}
	if value >= 0 && value < len(numbers) {
		return numbers[value]
	}
	return "多"
}

func lessonVariables(topic string, goal string, outline string, interactionMode string) map[string]string {
	return map[string]string{
		"mode":             "teaching",
		"topic":            topic,
		"learning_goal":    goal,
		"outline":          outline,
		"interaction_mode": interactionMode,
	}
}

func interactionFor(mode string, topic string) app.SceneInteraction {
	if strings.TrimSpace(mode) != "choice" {
		return app.SceneInteraction{Mode: "dialogue"}
	}
	return app.SceneInteraction{
		Mode: "choice",
		Choices: []app.SceneChoice{
			{
				ID:    "ask-summary",
				Label: "先讲直觉",
				Text:  "先用直觉方式解释一下这份材料的核心意思。",
				Hint:  "适合刚开始建立整体印象。",
			},
			{
				ID:    "challenge",
				Label: "我来反驳",
				Text:  "我想挑战一下这个主题，请你基于材料回应我的质疑。",
				Hint:  "适合用辩论方式检查理解。",
			},
			{
				ID:    "example",
				Label: "举个例子",
				Text:  "围绕「" + topic + "」举一个贴近现实的例子。",
				Hint:  "适合把抽象概念落到情境里。",
			},
		},
	}
}

func promptFor(topic string, goal string, outline string, document string, base app.PromptConfig) app.PromptConfig {
	if base.System == "" {
		base.System = "你是 FAIRY 的文档 Galgame 教学 Agent，负责基于用户提供的材料进行玩家驱动的互动教学。"
	}
	if base.Developer == "" {
		base.Developer = "不要一次性生成完整剧本。你只扮演当前场景中的 Tutor，根据玩家每轮输入即时回应；必须基于文档内容教学，不要编造文档外事实。"
	}
	if base.SceneInstruction == "" {
		base.SceneInstruction = "当前互动主题是「" + topic + "」。学习目标：" + goal + "。舞台推进约束：" + outline + "。文档摘要材料：" + truncateRunes(document, 1600)
	}
	if base.ResponseContract == "" {
		base.ResponseContract = "每次回复适合语音播放，控制在 2 到 5 句；先回应玩家当前输入，再结合材料细节讲清直觉和术语，必要时提出一个小问题引导玩家继续互动。"
	}
	if len(base.StyleRules) == 0 {
		base.StyleRules = []string{
			"只围绕当前文档和学习目标教学。",
			"不要替玩家说话，不要预写完整剧情。",
			"先讲直觉，再讲术语。",
			"每轮最多推进一个知识点。",
			"不要跳出教师角色。",
		}
	}
	return base
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
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
