package mock

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/Rinai-R/FAIRY/internal/adapters/agent"
	"github.com/Rinai-R/FAIRY/internal/adapters/health"
	"github.com/Rinai-R/FAIRY/internal/app"
)

type MockEngine struct{}

func wantsJapaneseSpeech(plan app.LanguagePlan) bool {
	language := plan.Normalize()
	return language.Mode != "same" && app.IsJapaneseLanguage(language.SpeechLanguage)
}

func (MockEngine) GenerateAct(_ context.Context, input agent.ActInput) (agent.ActOutput, error) {
	if err := input.Validate(); err != nil {
		return agent.ActOutput{}, err
	}
	character := input.Request.Characters[0]
	speaker := firstNonEmpty(character.DisplayName, character.ID, "讲解者")
	point := mockTeachingPoint(input)
	actID := firstNonEmpty(input.PlannedNode.ID, fmt.Sprintf("lesson-%d", input.ActIndex))
	kind := firstNonEmpty(input.PlannedNode.Kind, "lesson")
	title := firstNonEmpty(input.PlannedNode.Title, fmt.Sprintf("第%d幕", input.ActIndex))
	decision := agent.ActDecisionContinue
	if kind == "opening" || (input.ActIndex == 1 && input.PreviousNode.ID == "") {
		actID = "opening"
		kind = "opening"
		title = firstNonEmpty(input.PlannedNode.Title, "开场对白")
	}
	if kind == "summary" || input.ActIndex >= 4 {
		actID = firstNonEmpty(input.PlannedNode.ID, "summary")
		kind = "summary"
		title = firstNonEmpty(input.PlannedNode.Title, "总结回收")
		decision = agent.ActDecisionFreeDiscussion
	}
	node := app.TeachingWorkflowNode{
		ID:         actID,
		Kind:       kind,
		Title:      title,
		Summary:    point,
		Speaker:    speaker,
		NextNodeID: fmt.Sprintf("lesson-%d", input.ActIndex+1),
		Lines: []app.DialogueLine{
			{Speaker: speaker, Text: "我们先把这一幕的直觉抓住：" + point + "。", SpeechText: mockSpeech("まず、この場面の直感をつかみましょう。", input.Request.Runtime.Language), Expression: "soft_smile"},
			{Speaker: speaker, Text: "不要急着背定义，先看它解决了材料里的哪个问题。", SpeechText: mockSpeech("定義を急いで覚える前に、それが資料のどんな問題を解くのか見てみましょう。", input.Request.Runtime.Language), Expression: "thinking"},
			{Speaker: speaker, Text: "接下来我会用一个小选择，让你决定我们从例子还是术语继续。", SpeechText: mockSpeech("次は小さな選択で、例から進むか用語から進むかを決めましょう。", input.Request.Runtime.Language), Expression: "curious"},
		},
		Choices: []app.SceneChoice{
			{ID: "example", Label: "先看例子", Text: "先用例子讲清楚。", Hint: "更直观"},
			{ID: "term", Label: "先拆术语", Text: "先解释术语。", Hint: "更严谨"},
		},
	}
	if kind == "summary" {
		node.FreeDiscussion = false
		node.NextNodeID = "free-discussion"
		node.Choices = nil
		node.Lines = append(node.Lines, app.DialogueLine{
			Speaker: speaker, Text: "到这里，主线已经可以收束了；之后你再自由提问，我会只围绕这份材料继续陪你拆。", SpeechText: mockSpeech("ここで本筋はいったんまとめられます。この後は自由に質問してください。", input.Request.Runtime.Language), Expression: "calm",
		})
	}
	return agent.ActOutput{
		Node:          node,
		Decision:      decision,
		CoveredPoints: append(input.CoveredPoints, point),
		Summary:       point,
	}, nil
}

func (MockEngine) Discuss(_ context.Context, input agent.DiscussInput) (agent.Output, error) {
	if err := input.Validate(); err != nil {
		return agent.Output{}, err
	}
	character := input.Turn.Character
	if character.ID == "" && len(input.Turn.Characters) > 0 {
		character = input.Turn.Characters[0]
	}
	prefix := "我听到了。"
	if input.Turn.Prompt.SceneInstruction != "" {
		prefix = "按照当前场景，我听到了。"
	}
	displayText := "我们回到你刚刚的问题：" + input.Turn.User.Text + "。先按材料主线对齐，再看你卡住的是直觉、术语还是例子。"
	if input.Turn.User.Text != "" {
		displayText = fmt.Sprintf("%s「%s」先按材料主线对齐，再看你卡住的是直觉、术语还是例子。", prefix, input.Turn.User.Text)
	}
	speechText := displayText
	if wantsJapaneseSpeech(input.Turn.Runtime.Language) {
		speechText = "今の質問に戻りましょう。まず資料の流れに合わせて、直感、用語、例のどこで止まったのか確認します。"
	}
	return agent.Output{
		DisplayText: displayText,
		SpeechText:  speechText,
		Segments: []app.Segment{{
			Text: displayText, SpeechText: speechText, Emotion: "calm", Expression: "thinking", Motion: "idle_talk",
		}},
		Emotion:    "calm",
		Expression: "thinking",
		Motion:     "idle_talk",
		Voice:      app.VoicePlan{VoiceID: character.VoiceID, Style: "clear", Speed: 1, Pitch: 1},
		MemoryWrites: []app.MemoryWrite{
			{
				Type:       "dialogue_note",
				Content:    "用户提到：" + input.Turn.User.Text,
				Importance: 0.3,
				Emotion:    "gentle",
				Tags:       []string{"dialogue"},
			},
		},
	}, nil
}

func mockTeachingPoint(input agent.ActInput) string {
	if input.Choice.ID != "" {
		return "根据你的选择「" + input.Choice.Label + "」继续展开"
	}
	if text := strings.TrimSpace(input.PlannedNode.Summary); text != "" && !genericPlannedPoint(text) {
		return text
	}
	if text := strings.TrimSpace(input.PlannedNode.Title); text != "" && !genericPlannedPoint(text) {
		return text
	}
	if text := documentTeachingPoint(input.Request.DocumentText, input.ActIndex); text != "" {
		return text
	}
	if text := strings.TrimSpace(input.Request.LearningGoal); text != "" {
		return text
	}
	if text := strings.TrimSpace(input.Request.Topic); text != "" {
		return text
	}
	return "材料核心概念"
}

var genericPlannedPointPattern = regexp.MustCompile(`^(第[0-9一二三四五六七八九十]+幕|第[0-9一二三四五六七八九十]+节|开场|开场对白|总结|总结回收|自由讨论)$`)

func genericPlannedPoint(text string) bool {
	normalized := strings.TrimSpace(text)
	if normalized == "" {
		return true
	}
	return genericPlannedPointPattern.MatchString(normalized)
}

func documentTeachingPoint(document string, actIndex int) string {
	paragraphs := teachingParagraphs(document)
	if len(paragraphs) == 0 {
		return ""
	}
	index := actIndex - 1
	if index < 0 {
		index = 0
	}
	if index >= len(paragraphs) {
		index = len(paragraphs) - 1
	}
	return paragraphs[index]
}

func teachingParagraphs(document string) []string {
	var paragraphs []string
	for _, raw := range strings.Split(document, "\n") {
		text := strings.TrimSpace(raw)
		text = strings.Trim(text, "#*- \t")
		if text == "" || strings.HasPrefix(text, "```") {
			continue
		}
		if len([]rune(text)) < 12 {
			continue
		}
		paragraphs = append(paragraphs, shortenRunes(text, 90))
		if len(paragraphs) >= 8 {
			break
		}
	}
	return paragraphs
}

func shortenRunes(text string, limit int) string {
	runes := []rune(strings.TrimSpace(text))
	if len(runes) <= limit {
		return string(runes)
	}
	return string(runes[:limit]) + "..."
}

func mockSpeech(text string, plan app.LanguagePlan) string {
	if wantsJapaneseSpeech(plan) {
		return text
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func (MockEngine) Check(_ context.Context) health.Result {
	return health.Result{
		Domain:    "agent",
		Provider:  string(agent.ProviderMock),
		Status:    health.StatusOK,
		Message:   "mock agent 可用",
		CheckedAt: time.Now().UTC(),
	}
}
