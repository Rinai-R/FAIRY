package fairy

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Rinai-R/FAIRY/internal/adapters/agent"
	"github.com/Rinai-R/FAIRY/internal/adapters/health"
	"github.com/Rinai-R/FAIRY/internal/app"
	"github.com/Rinai-R/FAIRY/internal/llm"
)

func textMaterial(text string) app.MaterialSource {
	return app.MaterialSource{Mode: app.MaterialSourceText, Text: text}
}

type stubLLM struct {
	lastRequest llm.Request
	requests    []llm.Request
	content     string
	contents    []string
	calls       int
	err         error
	errs        []error
}

func (s *stubLLM) Validate(profile llm.Profile) error {
	if s.err != nil {
		return s.err
	}
	if profile.Endpoint == "" && profile.APIKey == "" && profile.Model == "" {
		return nil
	}
	if profile.APIKey == "" {
		return errors.New("llm api key 不能为空")
	}
	return nil
}

func (s *stubLLM) CompleteJSON(_ context.Context, request llm.Request) (string, error) {
	s.lastRequest = request
	s.requests = append(s.requests, request)
	s.calls++
	if s.err != nil {
		return "", s.err
	}
	if len(s.errs) > 0 {
		index := s.calls - 1
		if index >= len(s.errs) {
			index = len(s.errs) - 1
		}
		if s.errs[index] != nil {
			return "", s.errs[index]
		}
	}
	if len(s.contents) > 0 {
		index := s.calls - 1
		if index >= len(s.contents) {
			index = len(s.contents) - 1
		}
		return s.contents[index], nil
	}
	return s.content, nil
}

func validPlanJSON() string {
	return `{"material_summary":"材料围绕 Go 调度器的 GMP 模型展开。","expanded_notes":["G 是 goroutine，代表等待运行的任务。","M 是系统线程，承载实际执行。","P 是处理器上下文，管理本地队列并连接 G 和 M。"],"act_count":3,"acts":[{"index":1,"id":"opening","kind":"opening","title":"开场","theme":"建立 GMP 的直觉","teaching_goal":"让玩家先知道 GMP 分别代表什么","must_cover":["G/M/P 的基本含义"],"misconception_to_address":"G、M、P 不是三个并列线程，而是不同层次的调度角色。","example_or_counterexample":"把 G 想成任务卡、M 想成执行者、P 想成工作台。","dramatic_role":"用轻松语气引入主题","choice_goal":"选择先看例子还是术语","decision":"continue"},{"index":2,"id":"lesson-1","kind":"lesson","title":"第一幕","theme":"G、M、P 如何协作","teaching_goal":"解释三者关系","must_cover":["G 等待执行","M 负责执行","P 管理队列"],"misconception_to_address":"不要把 goroutine 当成系统线程，G 需要 M 承载执行。","example_or_counterexample":"小组作业里，任务卡等待，组员执行，桌面保存待办清单。","dramatic_role":"把比喻落回术语","choice_goal":"检查玩家是否理解协作关系","decision":"continue"},{"index":3,"id":"summary","kind":"summary","title":"总结","theme":"收束 GMP 模型","teaching_goal":"回顾核心关系","must_cover":["GMP 的整体协作"],"misconception_to_address":"调度不是单个对象独立完成，而是 G、M、P 的协作。","example_or_counterexample":"如果只有任务卡没有执行者，任务不会真正运行。","dramatic_role":"总结并开放自由讨论","decision":"free_discussion"}]}`
}

func TestGenerateActUsesUnderlyingLLMAdapter(t *testing.T) {
	t.Parallel()

	model := &stubLLM{
		contents: []string{
			validPlanJSON(),
			`{"decision":"continue","covered_points":["GMP"],"node":{"id":"opening","kind":"opening","title":"开场","summary":"注意力机制","lines":[{"speaker":"亚托莉","text":"第一句","speech_text":"一つ目。","expression":"soft_smile"},{"speaker":"亚托莉","text":"第二句","speech_text":"二つ目。","expression":"thinking"},{"speaker":"亚托莉","text":"第三句","speech_text":"三つ目。","expression":"curious"},{"speaker":"亚托莉","text":"第四句","speech_text":"四つ目。","expression":"calm"}],"choices":[{"id":"example","label":"看例子","text":"用例子继续。"}]}}`,
		},
	}
	out, err := NewEngine(Options{Model: model}).GenerateAct(context.Background(), agent.ActInput{
		Request: app.SceneGenerateRequest{
			MaterialSource: textMaterial("GMP 模型解释 goroutine、线程和处理器上下文如何协作。"),
			Characters:     []app.Character{{ID: "atri", DisplayName: "亚托莉"}},
			Runtime: app.RuntimeConfig{
				Agent: app.AgentProfile{
					Endpoint:  "https://example.com/v1",
					Model:     "deepseek-chat",
					APIKey:    "secret-key",
					ExtraBody: `{"reasoning_effort":"low"}`,
				},
			},
		},
		ActIndex: 1,
	})
	if err != nil {
		t.Fatalf("GenerateAct() error = %v", err)
	}
	if out.Node.TeachingGoal == "" || len(out.Node.MustCover) == 0 || out.Node.MisconceptionToAddress == "" || out.Node.ExampleOrCounterexample == "" {
		t.Fatalf("act plan metadata missing from generated node: %#v", out.Node)
	}
	if out.Node.MustCover[0] != "G/M/P 的基本含义" {
		t.Fatalf("node.MustCover = %#v, want current act plan must_cover", out.Node.MustCover)
	}
	if model.lastRequest.Profile.Model != "deepseek-chat" {
		t.Fatalf("model profile = %#v", model.lastRequest.Profile)
	}
	if model.lastRequest.Profile.ExtraBody == "" {
		t.Fatalf("extra body missing: %#v", model.lastRequest.Profile)
	}
	if len(model.lastRequest.Messages) != 2 {
		t.Fatalf("messages = %d", len(model.lastRequest.Messages))
	}
	if !strings.Contains(model.requests[1].Messages[1].Content, "前端注入 Prompt") {
		t.Fatalf("prompt contract missing: %#v", model.requests[1].Messages)
	}
	if !strings.Contains(model.requests[1].Messages[1].Content, "总规划书") {
		t.Fatalf("act plan missing: %#v", model.requests[1].Messages)
	}
	for _, want := range []string{
		"可以增加 acts/章节数量，不设硬性上限",
		"act_count 示例不是上限",
		"misconception_to_address",
		"example_or_counterexample",
		"材料摘要（runtime 已经从粘贴文本或单上传文件整理完成",
		"GMP 模型解释 goroutine",
	} {
		if !strings.Contains(model.requests[0].Messages[1].Content, want) {
			t.Fatalf("plan prompt missing %q:\n%s", want, model.requests[0].Messages[1].Content)
		}
	}
	for _, want := range []string{
		"node.lines 是视觉小说文本框逐次展示的单位",
		"中文或日文单条 lines[].text 不超过 52 个可见字符",
		"英文单条 lines[].text 不超过 120 个可见字符",
		"不限制当前幕或整篇章节数量",
		"不能把多条字幕合并成一条 speech_text",
		"可用角色差分 expression contract",
		"current_act_plan.misconception_to_address",
		"current_act_plan.example_or_counterexample",
		"角色口吻优先",
		"禁止使用讲课套话",
		"covered_points 是可选",
	} {
		if !strings.Contains(model.requests[1].Messages[1].Content, want) {
			t.Fatalf("generate prompt missing %q:\n%s", want, model.requests[1].Messages[1].Content)
		}
	}
	if !strings.Contains(model.lastRequest.Messages[0].Content, "角色台词改写 Agent") {
		t.Fatalf("rewrite prompt missing: %#v", model.lastRequest.Messages)
	}
	for _, want := range []string{
		"若原稿存在超长 line，必须优先拆短",
		"中文或日文单条 lines[].text 不超过 52 个可见字符",
		"英文单条 lines[].text 不超过 120 个可见字符",
		"不限制章节数量",
		"与同序号 text 一一对应",
		"角色口吻优先",
		"避免老师口吻",
		"covered_points 是可选内部追踪字段",
	} {
		if !strings.Contains(model.lastRequest.Messages[1].Content, want) {
			t.Fatalf("rewrite prompt missing %q:\n%s", want, model.lastRequest.Messages[1].Content)
		}
	}
	if out.Node.ID != "opening" {
		t.Fatalf("node id = %q", out.Node.ID)
	}
}

func TestGenerateActEmitsSubstepTraceEvents(t *testing.T) {
	t.Parallel()

	model := &stubLLM{
		contents: []string{
			validPlanJSON(),
			`{"decision":"continue","covered_points":["GMP"],"node":{"id":"opening","kind":"opening","title":"开场","summary":"建立 GMP 直觉","lines":[{"speaker":"亚托莉","text":"第一句","speech_text":"一つ目。","expression":"soft_smile"},{"speaker":"亚托莉","text":"第二句","speech_text":"二つ目。","expression":"thinking"},{"speaker":"亚托莉","text":"第三句","speech_text":"三つ目。","expression":"curious"},{"speaker":"亚托莉","text":"第四句","speech_text":"四つ目。","expression":"calm"}],"choices":[{"id":"example","label":"看例子","text":"用例子继续。"}]}}`,
			`{"decision":"continue","covered_points":["GMP"],"node":{"id":"opening","kind":"opening","title":"开场","summary":"建立 GMP 直觉","lines":[{"speaker":"亚托莉","text":"我们先看 G、M、P 的分工。","speech_text":"まず分担を見ましょう。","expression":"soft_smile"},{"speaker":"亚托莉","text":"G 像等待安排的任务卡。","speech_text":"G は待つカードです。","expression":"thinking"},{"speaker":"亚托莉","text":"M 是真正执行的系统线程。","speech_text":"M は実行するスレッドです。","expression":"curious"},{"speaker":"亚托莉","text":"P 像工作台，负责把任务交出去。","speech_text":"P は作業台です。","expression":"calm"}],"choices":[{"id":"example","label":"看例子","text":"用例子继续。"}]}}`,
		},
	}
	var traces []agent.ActTraceEvent
	_, err := NewEngine(Options{Model: model}).GenerateAct(context.Background(), agent.ActInput{
		Request: app.SceneGenerateRequest{
			MaterialSource: textMaterial("GMP 模型解释 goroutine、线程和处理器上下文如何协作。"),
			Characters:     []app.Character{{ID: "atri", DisplayName: "亚托莉"}},
		},
		PlannedNode: app.TeachingWorkflowNode{ID: "opening", Kind: "opening", Title: "开场"},
		ActIndex:    1,
		Trace: func(event agent.ActTraceEvent) {
			traces = append(traces, event)
		},
	})
	if err != nil {
		t.Fatalf("GenerateAct() error = %v", err)
	}
	for _, eventType := range []string{
		app.RuntimeEventTypeAgentActPlanDone,
		app.RuntimeEventTypeAgentDraftDone,
		app.RuntimeEventTypeAgentRewriteDone,
	} {
		event := requireTraceEvent(t, traces, eventType)
		if event.Level != app.RuntimeEventLevelInfo {
			t.Fatalf("%s level = %q, want info", eventType, event.Level)
		}
		if event.DurationMS <= 0 {
			t.Fatalf("%s duration = %d, want > 0", eventType, event.DurationMS)
		}
	}
}

func TestActPlanTraceReportsRepairRetry(t *testing.T) {
	t.Parallel()

	model := &stubLLM{
		contents: []string{
			`{"material_summary":"","expanded_notes":[],"act_count":0,"acts":[]}`,
			validPlanJSON(),
			`{"decision":"continue","covered_points":["GMP"],"node":{"id":"opening","kind":"opening","title":"开场","summary":"建立 GMP 直觉","lines":[{"speaker":"亚托莉","text":"第一句","speech_text":"一つ目。","expression":"soft_smile"},{"speaker":"亚托莉","text":"第二句","speech_text":"二つ目。","expression":"thinking"},{"speaker":"亚托莉","text":"第三句","speech_text":"三つ目。","expression":"curious"},{"speaker":"亚托莉","text":"第四句","speech_text":"四つ目。","expression":"calm"}],"choices":[{"id":"example","label":"看例子","text":"用例子继续。"}]}}`,
		},
	}
	var traces []agent.ActTraceEvent
	_, err := NewEngine(Options{Model: model}).GenerateAct(context.Background(), agent.ActInput{
		Request: app.SceneGenerateRequest{
			MaterialSource: textMaterial("GMP 模型解释 goroutine、线程和处理器上下文如何协作。"),
			Characters:     []app.Character{{ID: "atri", DisplayName: "亚托莉"}},
		},
		PlannedNode: app.TeachingWorkflowNode{ID: "opening", Kind: "opening", Title: "开场"},
		ActIndex:    1,
		Trace: func(event agent.ActTraceEvent) {
			traces = append(traces, event)
		},
	})
	if err != nil {
		t.Fatalf("GenerateAct() error = %v", err)
	}
	retry := requireTraceEvent(t, traces, app.RuntimeEventTypeAgentActPlanRetry)
	if retry.Level != app.RuntimeEventLevelWarn || retry.RetryCount != 1 || !strings.Contains(retry.Detail, "act_plan") {
		t.Fatalf("actplan retry trace = %+v", retry)
	}
	requireTraceEvent(t, traces, app.RuntimeEventTypeAgentActPlanDone)
}

func TestGenerateActRetriesEmptyLLMContent(t *testing.T) {
	t.Parallel()

	model := &stubLLM{
		errs: []error{
			nil,
			llm.NewEmptyContentError(errors.New("llm chat completions 响应只有 reasoning_content，缺少 choices[0].message.content")),
			nil,
			nil,
		},
		contents: []string{
			validPlanJSON(),
			"",
			`{"decision":"continue","covered_points":["GMP"],"node":{"id":"opening","kind":"opening","title":"开场","summary":"建立 GMP 直觉","lines":[{"speaker":"亚托莉","text":"第一句","speech_text":"一つ目。","expression":"soft_smile"},{"speaker":"亚托莉","text":"第二句","speech_text":"二つ目。","expression":"thinking"},{"speaker":"亚托莉","text":"第三句","speech_text":"三つ目。","expression":"curious"},{"speaker":"亚托莉","text":"第四句","speech_text":"四つ目。","expression":"calm"}],"choices":[{"id":"example","label":"看例子","text":"用例子继续。"}]}}`,
			`{"decision":"continue","covered_points":["GMP"],"node":{"id":"opening","kind":"opening","title":"开场","summary":"建立 GMP 直觉","lines":[{"speaker":"亚托莉","text":"第一句","speech_text":"一つ目。","expression":"soft_smile"},{"speaker":"亚托莉","text":"第二句","speech_text":"二つ目。","expression":"thinking"},{"speaker":"亚托莉","text":"第三句","speech_text":"三つ目。","expression":"curious"},{"speaker":"亚托莉","text":"第四句","speech_text":"四つ目。","expression":"calm"}],"choices":[{"id":"example","label":"看例子","text":"用例子继续。"}]}}`,
		},
	}
	var traces []agent.ActTraceEvent
	_, err := NewEngine(Options{Model: model}).GenerateAct(context.Background(), agent.ActInput{
		Request: app.SceneGenerateRequest{
			MaterialSource: textMaterial("GMP 模型解释 goroutine、线程和处理器上下文如何协作。"),
			Characters:     []app.Character{{ID: "atri", DisplayName: "亚托莉"}},
		},
		PlannedNode: app.TeachingWorkflowNode{ID: "opening", Kind: "opening", Title: "开场"},
		ActIndex:    1,
		Trace: func(event agent.ActTraceEvent) {
			traces = append(traces, event)
		},
	})
	if err != nil {
		t.Fatalf("GenerateAct() error = %v", err)
	}
	if model.calls != 4 {
		t.Fatalf("llm calls = %d, want 4", model.calls)
	}
	retry := requireTraceEvent(t, traces, app.RuntimeEventTypeAgentDraftRetry)
	if retry.Level != app.RuntimeEventLevelWarn || retry.RetryCount != 1 || !strings.Contains(retry.Detail, "reasoning_content") {
		t.Fatalf("draft retry trace = %+v", retry)
	}
	lastMessage := model.requests[2].Messages[len(model.requests[2].Messages)-1]
	if lastMessage.Role != "user" || !strings.Contains(lastMessage.Content, "只返回修正后的 JSON object") {
		t.Fatalf("repair message = %+v", lastMessage)
	}
}

func TestValidateActPlanAllowsMissingOptionalMisconception(t *testing.T) {
	t.Parallel()

	plan, err := parseActPlan(strings.Replace(validPlanJSON(), `"misconception_to_address":"G、M、P 不是三个并列线程，而是不同层次的调度角色。",`, "", 1))
	if err != nil {
		t.Fatalf("parseActPlan() error = %v", err)
	}
	if err := validateActPlan(plan); err != nil {
		t.Fatalf("validateActPlan() error = %v", err)
	}
}

func TestValidateActPlanAllowsMissingOptionalExampleOrCounterexample(t *testing.T) {
	t.Parallel()

	plan, err := parseActPlan(strings.Replace(validPlanJSON(), `"example_or_counterexample":"把 G 想成任务卡、M 想成执行者、P 想成工作台。",`, "", 1))
	if err != nil {
		t.Fatalf("parseActPlan() error = %v", err)
	}
	if err := validateActPlan(plan); err != nil {
		t.Fatalf("validateActPlan() error = %v", err)
	}
}

func TestValidateFairyActOutputAllowsMissingCoveredPointsForTeachingAct(t *testing.T) {
	t.Parallel()

	output := agent.ActOutput{
		Decision: agent.ActDecisionContinue,
		Node: app.TeachingWorkflowNode{
			Kind:    "lesson",
			Title:   "第一幕",
			Summary: "GMP 协作",
			Speaker: "亚托莉",
			Lines: []app.DialogueLine{
				{Speaker: "亚托莉", Text: "G 是等待运行的任务。", SpeechText: "G は待つタスクです。", Expression: "soft_smile"},
				{Speaker: "亚托莉", Text: "M 是真正执行的线程。", SpeechText: "M は実行するスレッドです。", Expression: "thinking"},
				{Speaker: "亚托莉", Text: "P 管理本地运行队列。", SpeechText: "P はキューを管理します。", Expression: "curious"},
				{Speaker: "亚托莉", Text: "三者配合推进调度。", SpeechText: "三つで進みます。", Expression: "calm"},
			},
			Choices: []app.SceneChoice{{ID: "next", Label: "继续", Text: "继续下一点。"}},
		},
	}
	err := validateFairyActOutput(agent.ActInput{}, output)
	if err != nil {
		t.Fatalf("validateFairyActOutput() error = %v", err)
	}

	output.CoveredPoints = []string{""}
	err = validateFairyActOutput(agent.ActInput{}, output)
	if err != nil {
		t.Fatalf("validateFairyActOutput() with blank covered_points error = %v", err)
	}
}

func TestGenerateActAllowsMissingCoveredPointsFromModel(t *testing.T) {
	t.Parallel()

	model := &stubLLM{
		contents: []string{
			validPlanJSON(),
			`{"decision":"continue","node":{"kind":"lesson","title":"第一幕","summary":"GMP 调度","speaker":"亚托莉","lines":[{"speaker":"亚托莉","text":"G 是等待运行的小任务。","speech_text":"G は待つ小さなタスクです。","expression":"soft_smile"},{"speaker":"亚托莉","text":"M 是真正执行代码的线程。","speech_text":"M は実行するスレッドです。","expression":"thinking"},{"speaker":"亚托莉","text":"P 管理本地运行队列。","speech_text":"P はキューを管理します。","expression":"curious"},{"speaker":"亚托莉","text":"三者配合，调度才能动起来。","speech_text":"三つで進みます。","expression":"calm"}],"choices":[{"id":"queue","label":"看队列","text":"继续讲运行队列。"}]}}`,
			`{"decision":"continue","node":{"kind":"lesson","title":"第一幕","summary":"GMP 调度","speaker":"亚托莉","lines":[{"speaker":"亚托莉","text":"G 就像等着被安排的小任务。","speech_text":"G は待つ小さなタスクです。","expression":"soft_smile"},{"speaker":"亚托莉","text":"M 才是真正执行代码的线程。","speech_text":"M は実行するスレッドです。","expression":"thinking"},{"speaker":"亚托莉","text":"P 会管理本地运行队列。","speech_text":"P はキューを管理します。","expression":"curious"},{"speaker":"亚托莉","text":"它们配合起来，程序才会继续向前跑。","speech_text":"三つでプログラムが進みます。","expression":"calm"}],"choices":[{"id":"queue","label":"看队列","text":"继续讲运行队列。"}]}}`,
		},
	}
	out, err := NewEngine(Options{Model: model}).GenerateAct(context.Background(), agent.ActInput{
		Request: app.SceneGenerateRequest{
			MaterialSource: textMaterial("GMP 模型解释 goroutine、线程和处理器上下文如何协作。"),
			Characters:     []app.Character{{ID: "atri", DisplayName: "亚托莉"}},
		},
		PlannedNode: app.TeachingWorkflowNode{
			ID:    "lesson-1",
			Kind:  "lesson",
			Title: "第一幕",
		},
		ActIndex: 2,
	})
	if err != nil {
		t.Fatalf("GenerateAct() error = %v", err)
	}
	if len(out.CoveredPoints) != 0 {
		t.Fatalf("covered_points = %#v, want optional field omitted", out.CoveredPoints)
	}
	if model.calls != 3 {
		t.Fatalf("llm calls = %d, want 3", model.calls)
	}
}

func TestLanguageBriefNormalizesAliases(t *testing.T) {
	t.Parallel()

	got := languageBrief(app.LanguagePlan{
		DisplayLanguage: "cn",
		SpeechLanguage:  "en",
	})
	for _, want := range []string{
		"display_language: zh-CN",
		"speech_language: en-US",
		"mode: translate_for_voice",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("languageBrief missing %q:\n%s", want, got)
		}
	}
}

func TestDiscussUsesUnderlyingLLMAdapter(t *testing.T) {
	t.Parallel()

	model := &stubLLM{
		content: `{"display_text":"我们继续拆这个问题。","speech_text":"この問題を続けて見ていきましょう。","emotion":"calm","expression":"thinking","motion":"idle"}`,
	}
	out, err := NewEngine(Options{Model: model}).Discuss(context.Background(), agent.DiscussInput{
		Turn: app.TurnRequest{
			Character: app.Character{ID: "atri"},
			User:      app.UserInput{UserID: "default", Text: "为什么？"},
		},
	})
	if err != nil {
		t.Fatalf("Discuss() error = %v", err)
	}
	if out.DisplayText == "" || out.SpeechText == "" {
		t.Fatalf("output missing text: %#v", out)
	}
	if !strings.Contains(model.lastRequest.Messages[0].Content, "最终自由讨论 Agent") {
		t.Fatalf("system prompt not forwarded: %#v", model.lastRequest.Messages)
	}
}

func TestGenerateActAllowsPlannedNodeToOwnScaffoldFields(t *testing.T) {
	t.Parallel()

	model := &stubLLM{
		contents: []string{
			validPlanJSON(),
			`{"decision":"continue","covered_points":["GMP"],"node":{"summary":"GMP 初始化","speaker":"亚托莉","lines":[{"speaker":"亚托莉","text":"第一句","speech_text":"一つ目。","expression":"soft_smile"},{"speaker":"亚托莉","text":"第二句","speech_text":"二つ目。","expression":"thinking"},{"speaker":"亚托莉","text":"第三句","speech_text":"三つ目。","expression":"curious"},{"speaker":"亚托莉","text":"第四句","speech_text":"四つ目。","expression":"calm"}],"choices":[{"id":"term","label":"拆术语","text":"先解释术语。"}]}}`,
		},
	}
	out, err := NewEngine(Options{Model: model}).GenerateAct(context.Background(), agent.ActInput{
		Request: app.SceneGenerateRequest{
			MaterialSource: textMaterial("GMP 模型解释 goroutine、线程和处理器上下文如何协作。"),
			Characters:     []app.Character{{ID: "atri", DisplayName: "亚托莉"}},
			Runtime: app.RuntimeConfig{
				Agent: app.AgentProfile{
					Endpoint: "https://example.com/v1",
					Model:    "deepseek-chat",
					APIKey:   "secret-key",
				},
			},
		},
		PlannedNode: app.TeachingWorkflowNode{
			ID:    "lesson-1",
			Kind:  "lesson",
			Title: "第一幕",
		},
		ActIndex: 1,
	})
	if err != nil {
		t.Fatalf("GenerateAct() error = %v", err)
	}
	if out.Node.ID != "" {
		t.Fatalf("raw act output should not invent scaffold id, got %q", out.Node.ID)
	}
	if len(out.Node.Lines) != 4 {
		t.Fatalf("lines = %d, want 4", len(out.Node.Lines))
	}
}

func TestGenerateActRetriesWhenOutputViolatesContract(t *testing.T) {
	t.Parallel()

	model := &stubLLM{
		contents: []string{
			validPlanJSON(),
			`{"decision":"continue","covered_points":["GMP"],"node":{"kind":"lesson","title":"第一幕","lines":[{"speaker":"亚托莉","text":"第一幕","speech_text":"第一幕","expression":"soft_smile"}]}}`,
			`{"decision":"continue","covered_points":["GMP"],"node":{"kind":"lesson","title":"第一幕","summary":"GMP 调度","speaker":"亚托莉","lines":[{"speaker":"亚托莉","text":"G 代表 goroutine，是等待运行的轻量任务。","speech_text":"G は goroutine です。","expression":"soft_smile"},{"speaker":"亚托莉","text":"M 代表系统线程，真正承载执行。","speech_text":"M は OS スレッドです。","expression":"thinking"},{"speaker":"亚托莉","text":"P 代表处理器上下文，管理本地运行队列。","speech_text":"P は実行コンテキストです。","expression":"curious"},{"speaker":"亚托莉","text":"三者合起来，才让任务排队、领取并执行。","speech_text":"三つがそろって、タスクの待機、受け取り、実行ができます。","expression":"calm"}],"choices":[{"id":"queue","label":"继续队列","text":"继续讲运行队列。"}]}}`,
		},
	}
	out, err := NewEngine(Options{Model: model}).GenerateAct(context.Background(), agent.ActInput{
		Request: app.SceneGenerateRequest{
			MaterialSource: textMaterial("GMP 模型解释 goroutine、线程和处理器上下文如何协作。"),
			Characters:     []app.Character{{ID: "atri", DisplayName: "亚托莉"}},
		},
		PlannedNode: app.TeachingWorkflowNode{
			ID:    "lesson-1",
			Kind:  "lesson",
			Title: "第一幕",
		},
		ActIndex: 2,
	})
	if err != nil {
		t.Fatalf("GenerateAct() error = %v", err)
	}
	if model.calls != 4 {
		t.Fatalf("llm calls = %d, want 4", model.calls)
	}
	if !strings.Contains(model.requests[2].Messages[len(model.requests[2].Messages)-1].Content, "上一次输出不符合 FAIRY JSON 合约") {
		t.Fatalf("repair prompt missing: %#v", model.requests[2].Messages)
	}
	if out.Node.Summary != "GMP 调度" {
		t.Fatalf("summary = %q", out.Node.Summary)
	}
}

func TestRewriteActAllowsCoveredPointsDropped(t *testing.T) {
	t.Parallel()

	model := &stubLLM{
		contents: []string{
			validPlanJSON(),
			`{"decision":"continue","covered_points":["G 等待执行","M 负责执行"],"node":{"kind":"lesson","title":"第一幕","summary":"GMP 调度","speaker":"亚托莉","lines":[{"speaker":"亚托莉","text":"G 是等待运行的 goroutine。","speech_text":"G は待つタスクです。","expression":"soft_smile"},{"speaker":"亚托莉","text":"M 负责真正执行代码。","speech_text":"M は実行します。","expression":"thinking"},{"speaker":"亚托莉","text":"P 管理本地运行队列。","speech_text":"P はキューを管理します。","expression":"curious"},{"speaker":"亚托莉","text":"三者配合推进调度。","speech_text":"三つで進みます。","expression":"calm"}],"choices":[{"id":"queue","label":"看队列","text":"继续讲运行队列。"}]}}`,
			`{"decision":"continue","node":{"kind":"lesson","title":"第一幕","summary":"GMP 调度","speaker":"亚托莉","lines":[{"speaker":"亚托莉","text":"G 是等待运行的 goroutine。","speech_text":"G は待つタスクです。","expression":"soft_smile"},{"speaker":"亚托莉","text":"M 负责真正执行代码。","speech_text":"M は実行します。","expression":"thinking"},{"speaker":"亚托莉","text":"P 管理本地运行队列。","speech_text":"P はキューを管理します。","expression":"curious"},{"speaker":"亚托莉","text":"三者配合推进调度。","speech_text":"三つで進みます。","expression":"calm"}],"choices":[{"id":"queue","label":"看队列","text":"继续讲运行队列。"}]}}`,
		},
	}
	out, err := NewEngine(Options{Model: model}).GenerateAct(context.Background(), agent.ActInput{
		Request: app.SceneGenerateRequest{
			MaterialSource: textMaterial("GMP 模型解释 goroutine、线程和处理器上下文如何协作。"),
			Characters:     []app.Character{{ID: "atri", DisplayName: "亚托莉"}},
		},
		PlannedNode: app.TeachingWorkflowNode{
			ID:    "lesson-1",
			Kind:  "lesson",
			Title: "第一幕",
		},
		ActIndex: 2,
	})
	if err != nil {
		t.Fatalf("GenerateAct() error = %v", err)
	}
	if model.calls != 3 {
		t.Fatalf("llm calls = %d, want 3: plan + draft + rewrite", model.calls)
	}
	if len(out.CoveredPoints) != 0 {
		t.Fatalf("covered_points = %#v, want optional field omitted", out.CoveredPoints)
	}
}

func TestValidateRewriteActPreservesDecision(t *testing.T) {
	t.Parallel()

	err := validateRewriteActPreservesDraft(
		agent.ActOutput{Decision: agent.ActDecisionFreeDiscussion, CoveredPoints: []string{"GMP"}},
		agent.ActOutput{Decision: agent.ActDecisionContinue, CoveredPoints: []string{"GMP"}},
	)
	if err == nil {
		t.Fatal("validateRewriteActPreservesDraft() error = nil")
	}
	if !strings.Contains(err.Error(), "decision") {
		t.Fatalf("error = %v, want decision detail", err)
	}
}

func TestGenerateActReturnsTypedContractErrorAfterRepairExhausted(t *testing.T) {
	t.Parallel()

	model := &stubLLM{
		contents: []string{
			validPlanJSON(),
			`{"decision":"teleport","covered_points":["GMP"],"node":{"kind":"lesson","title":"第一幕","summary":"GMP 调度","speaker":"亚托莉","lines":[{"speaker":"亚托莉","text":"G 代表 goroutine。","speech_text":"G は goroutine です。","expression":"soft_smile"},{"speaker":"亚托莉","text":"M 代表线程。","speech_text":"M はスレッドです。","expression":"thinking"},{"speaker":"亚托莉","text":"P 负责上下文。","speech_text":"P は文脈です。","expression":"curious"},{"speaker":"亚托莉","text":"三者一起调度。","speech_text":"三つで調度します。","expression":"calm"}],"choices":[{"id":"queue","label":"继续队列","text":"继续讲运行队列。"}]}}`,
			`{"decision":"teleport","covered_points":["GMP"],"node":{"kind":"lesson","title":"第一幕","summary":"GMP 调度","speaker":"亚托莉","lines":[{"speaker":"亚托莉","text":"G 代表 goroutine。","speech_text":"G は goroutine です。","expression":"soft_smile"},{"speaker":"亚托莉","text":"M 代表线程。","speech_text":"M はスレッドです。","expression":"thinking"},{"speaker":"亚托莉","text":"P 负责上下文。","speech_text":"P は文脈です。","expression":"curious"},{"speaker":"亚托莉","text":"三者一起调度。","speech_text":"三つで調度します。","expression":"calm"}],"choices":[{"id":"queue","label":"继续队列","text":"继续讲运行队列。"}]}}`,
		},
	}
	_, err := NewEngine(Options{Model: model}).GenerateAct(context.Background(), agent.ActInput{
		Request: app.SceneGenerateRequest{
			MaterialSource: textMaterial("GMP 模型解释 goroutine、线程和处理器上下文如何协作。"),
			Characters:     []app.Character{{ID: "atri", DisplayName: "亚托莉"}},
		},
		PlannedNode: app.TeachingWorkflowNode{
			ID:    "lesson-1",
			Kind:  "lesson",
			Title: "第一幕",
		},
		ActIndex: 2,
	})
	if err == nil {
		t.Fatal("GenerateAct() error = nil, want contract error")
	}
	if !agent.IsContractError(err) {
		t.Fatalf("GenerateAct() error = %T %v, want typed contract error", err, err)
	}
	if !strings.Contains(err.Error(), "decision 不支持") {
		t.Fatalf("GenerateAct() error = %v, want decision contract detail", err)
	}
	if model.calls != 3 {
		t.Fatalf("llm calls = %d, want 3", model.calls)
	}
}

func TestGenerateActAcceptsMissingDecisionForRuntimeNormalization(t *testing.T) {
	t.Parallel()

	model := &stubLLM{
		contents: []string{
			validPlanJSON(),
			`{"covered_points":["GMP"],"node":{"kind":"lesson","title":"第一幕","summary":"GMP 调度","speaker":"亚托莉","lines":[{"speaker":"亚托莉","text":"G 代表等待运行的轻量任务。","speech_text":"G は待機中の軽いタスクです。","expression":"soft_smile"},{"speaker":"亚托莉","text":"M 代表真正执行任务的系统线程。","speech_text":"M は実行する OS スレッドです。","expression":"thinking"},{"speaker":"亚托莉","text":"P 负责把可运行任务交给 M。","speech_text":"P は実行可能なタスクを M に渡します。","expression":"curious"},{"speaker":"亚托莉","text":"三者配合，调度器才能持续推进程序。","speech_text":"三つが協力してプログラムを進めます。","expression":"calm"}],"choices":[{"id":"queue","label":"继续队列","text":"继续讲运行队列。"}]}}`,
			`{"covered_points":["GMP"],"node":{"kind":"lesson","title":"第一幕","summary":"GMP 调度","speaker":"亚托莉","lines":[{"speaker":"亚托莉","text":"G 代表等待运行的轻量任务。","speech_text":"G は待機中の軽いタスクです。","expression":"soft_smile"},{"speaker":"亚托莉","text":"M 代表真正执行任务的系统线程。","speech_text":"M は実行する OS スレッドです。","expression":"thinking"},{"speaker":"亚托莉","text":"P 负责把可运行任务交给 M。","speech_text":"P は実行可能なタスクを M に渡します。","expression":"curious"},{"speaker":"亚托莉","text":"三者配合，调度器才能持续推进程序。","speech_text":"三つが協力してプログラムを進めます。","expression":"calm"}],"choices":[{"id":"queue","label":"继续队列","text":"继续讲运行队列。"}]}}`,
		},
	}
	out, err := NewEngine(Options{Model: model}).GenerateAct(context.Background(), agent.ActInput{
		Request: app.SceneGenerateRequest{
			MaterialSource: textMaterial("GMP 模型解释 goroutine、线程和处理器上下文如何协作。"),
			Characters:     []app.Character{{ID: "atri", DisplayName: "亚托莉"}},
		},
		PlannedNode: app.TeachingWorkflowNode{
			ID:    "lesson-1",
			Kind:  "lesson",
			Title: "第一幕",
		},
		ActIndex: 2,
	})
	if err != nil {
		t.Fatalf("GenerateAct() error = %v", err)
	}
	if out.Decision != "" {
		t.Fatalf("decision = %q, want empty for runtime normalization", out.Decision)
	}
}

func TestGenerateActReusesActPlanCacheForSameSession(t *testing.T) {
	t.Parallel()

	model := &stubLLM{
		contents: []string{
			validPlanJSON(),
			`{"decision":"continue","covered_points":["GMP"],"node":{"kind":"opening","title":"开场","summary":"建立 GMP 直觉","lines":[{"speaker":"亚托莉","text":"我们先认识 G、M、P。","speech_text":"まず G、M、P を見ましょう。","expression":"soft_smile"},{"speaker":"亚托莉","text":"G 是等待运行的任务。","speech_text":"G は待っているタスクです。","expression":"thinking"},{"speaker":"亚托莉","text":"M 负责承载真正执行。","speech_text":"M は実際の実行を担います。","expression":"curious"},{"speaker":"亚托莉","text":"P 会把任务和线程接起来。","speech_text":"P はタスクとスレッドをつなぎます。","expression":"calm"}],"choices":[{"id":"next","label":"继续","text":"继续第一幕。"}]}}`,
			`{"decision":"continue","covered_points":["GMP"],"node":{"kind":"opening","title":"开场","summary":"建立 GMP 直觉","lines":[{"speaker":"亚托莉","text":"嗯，我们先别急着背。G、M、P 是一组分工。","speech_text":"うん、急いで覚えなくて大丈夫です。G、M、P は役割分担です。","expression":"soft_smile"},{"speaker":"亚托莉","text":"G 是等着被运行的小任务。","speech_text":"G は実行を待つ小さなタスクです。","expression":"thinking"},{"speaker":"亚托莉","text":"M 是真正干活的系统线程。","speech_text":"M は実際に働く OS スレッドです。","expression":"curious"},{"speaker":"亚托莉","text":"P 决定任务何时被交给 M。","speech_text":"P はタスクをいつ M に渡すか決めます。","expression":"calm"}],"choices":[{"id":"next","label":"继续","text":"继续第一幕。"}]}}`,
			`{"decision":"continue","covered_points":["GMP"],"node":{"kind":"lesson","title":"第一幕","summary":"GMP 协作","lines":[{"speaker":"亚托莉","text":"G 像任务，排队等待。","speech_text":"G はタスクのように待ちます。","expression":"soft_smile"},{"speaker":"亚托莉","text":"M 是真正工作的线程。","speech_text":"M は実際に働くスレッドです。","expression":"thinking"},{"speaker":"亚托莉","text":"P 管着能运行的上下文。","speech_text":"P は実行の文脈を管理します。","expression":"curious"},{"speaker":"亚托莉","text":"三者一起决定调度顺序。","speech_text":"三つが一緒に順番を決めます。","expression":"calm"}],"choices":[{"id":"queue","label":"看队列","text":"继续讲队列。"}]}}`,
			`{"decision":"continue","covered_points":["GMP"],"node":{"kind":"lesson","title":"第一幕","summary":"GMP 协作","lines":[{"speaker":"亚托莉","text":"你可以把 G 想成等待安排的小任务。","speech_text":"G は割り当てを待つ小さなタスクです。","expression":"soft_smile"},{"speaker":"亚托莉","text":"它不会自己决定何时上场。","speech_text":"自分だけでは出番を決めません。","expression":"thinking"},{"speaker":"亚托莉","text":"M 才是真正干活的系统线程。","speech_text":"M は実際に働く OS スレッドです。","expression":"curious"},{"speaker":"亚托莉","text":"P 像工作台，管理本地队列。","speech_text":"P は作業台のようにローカルキューを管理します。","expression":"calm"}],"choices":[{"id":"queue","label":"看队列","text":"继续讲队列。"}]}}`,
		},
	}
	engine := NewEngine(Options{Model: model})
	request := app.SceneGenerateRequest{
		Topic:          "Go 调度器",
		MaterialSource: textMaterial("GMP 模型解释 goroutine、线程和处理器上下文如何协作。"),
		Characters:     []app.Character{{ID: "atri", DisplayName: "亚托莉"}},
	}
	session := app.Session{ID: "lesson:gmp"}

	if _, err := engine.GenerateAct(context.Background(), agent.ActInput{
		Request: request,
		Session: session,
		PlannedNode: app.TeachingWorkflowNode{
			ID:    "opening",
			Kind:  "opening",
			Title: "开场",
		},
		ActIndex: 1,
	}); err != nil {
		t.Fatalf("GenerateAct(opening) error = %v", err)
	}
	if _, err := engine.GenerateAct(context.Background(), agent.ActInput{
		Request: request,
		Session: session,
		PlannedNode: app.TeachingWorkflowNode{
			ID:    "lesson-1",
			Kind:  "lesson",
			Title: "第一幕",
		},
		ActIndex: 2,
	}); err != nil {
		t.Fatalf("GenerateAct(lesson-1) error = %v", err)
	}
	if model.calls != 5 {
		t.Fatalf("llm calls = %d, want 5: plan once + two act generations + two rewrites", model.calls)
	}
}

func TestGenerateActAcceptsCompleteFencedJSON(t *testing.T) {
	t.Parallel()

	model := &stubLLM{
		contents: []string{
			validPlanJSON(),
			"```json\n" + `{"decision":"continue","covered_points":["GMP"],"node":{"kind":"lesson","title":"第一幕","summary":"GMP","lines":[{"speaker":"亚托莉","text":"G 是 goroutine。","speech_text":"G は goroutine です。","expression":"soft_smile"},{"speaker":"亚托莉","text":"M 是系统线程。","speech_text":"M は OS スレッドです。","expression":"thinking"},{"speaker":"亚托莉","text":"P 是处理器上下文。","speech_text":"P はコンテキストです。","expression":"curious"},{"speaker":"亚托莉","text":"这三者共同完成调度。","speech_text":"三つが一緒にスケジューリングします。","expression":"calm"}],"choices":[{"id":"next","label":"继续","text":"继续下一点。"}]}}` + "\n```",
		},
	}
	_, err := NewEngine(Options{Model: model}).GenerateAct(context.Background(), agent.ActInput{
		Request: app.SceneGenerateRequest{
			MaterialSource: textMaterial("GMP 模型解释 goroutine、线程和处理器上下文如何协作。"),
			Characters:     []app.Character{{ID: "atri", DisplayName: "亚托莉"}},
		},
		PlannedNode: app.TeachingWorkflowNode{
			ID:    "lesson-1",
			Kind:  "lesson",
			Title: "第一幕",
		},
		ActIndex: 1,
	})
	if err != nil {
		t.Fatalf("GenerateAct() error = %v", err)
	}
}

func TestDiscussRetriesWhenOutputLeaksWorkflowContext(t *testing.T) {
	t.Parallel()

	model := &stubLLM{
		contents: []string{
			`{"display_text":"OpenSpec 复杂度判定如下。","speech_text":"OpenSpec です。","emotion":"calm","expression":"thinking","motion":"idle"}`,
			`{"display_text":"我们回到材料本身：这个问题可以先从调度队列理解。","speech_text":"資料そのものに戻りましょう。まずスケジューラーのキューから考えます。","emotion":"calm","expression":"thinking","motion":"idle"}`,
		},
	}
	out, err := NewEngine(Options{Model: model}).Discuss(context.Background(), agent.DiscussInput{
		Turn: app.TurnRequest{
			Character: app.Character{ID: "atri", DisplayName: "亚托莉"},
			User:      app.UserInput{UserID: "default", Text: "为什么？"},
		},
	})
	if err != nil {
		t.Fatalf("Discuss() error = %v", err)
	}
	if model.calls != 2 {
		t.Fatalf("llm calls = %d, want 2", model.calls)
	}
	if strings.Contains(out.DisplayText, "OpenSpec") {
		t.Fatalf("display_text still leaked workflow context: %q", out.DisplayText)
	}
}

func TestCheckFailsWhenModelAdapterMissing(t *testing.T) {
	t.Parallel()

	result := NewEngine(Options{}).Check(context.Background())
	if result.Status != health.StatusDown {
		t.Fatalf("status = %q", result.Status)
	}
}

func requireTraceEvent(t *testing.T, events []agent.ActTraceEvent, eventType string) agent.ActTraceEvent {
	t.Helper()
	for _, event := range events {
		if event.Type == eventType {
			return event
		}
	}
	t.Fatalf("missing trace event %s in %+v", eventType, events)
	return agent.ActTraceEvent{}
}
