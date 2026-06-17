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

type stubLLM struct {
	lastRequest llm.Request
	requests    []llm.Request
	content     string
	contents    []string
	calls       int
	err         error
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
	return `{"material_summary":"材料围绕 Go 调度器的 GMP 模型展开。","expanded_notes":["G 是 goroutine，代表等待运行的任务。","M 是系统线程，承载实际执行。","P 是处理器上下文，管理本地队列并连接 G 和 M。"],"act_count":3,"acts":[{"index":1,"id":"opening","kind":"opening","title":"开场","theme":"建立 GMP 的直觉","teaching_goal":"让玩家先知道 GMP 分别代表什么","must_cover":["G/M/P 的基本含义"],"dramatic_role":"用轻松语气引入主题","choice_goal":"选择先看例子还是术语","decision":"continue"},{"index":2,"id":"lesson-1","kind":"lesson","title":"第一幕","theme":"G、M、P 如何协作","teaching_goal":"解释三者关系","must_cover":["G 等待执行","M 负责执行","P 管理队列"],"dramatic_role":"把比喻落回术语","choice_goal":"检查玩家是否理解协作关系","decision":"continue"},{"index":3,"id":"summary","kind":"summary","title":"总结","theme":"收束 GMP 模型","teaching_goal":"回顾核心关系","must_cover":["GMP 的整体协作"],"dramatic_role":"总结并开放自由讨论","decision":"free_discussion"}]}`
}

func TestGenerateActUsesUnderlyingLLMAdapter(t *testing.T) {
	t.Parallel()

	model := &stubLLM{
		contents: []string{
			validPlanJSON(),
			`{"decision":"continue","node":{"id":"opening","kind":"opening","title":"开场","summary":"注意力机制","lines":[{"speaker":"亚托莉","text":"第一句","speech_text":"一つ目。","expression":"soft_smile"},{"speaker":"亚托莉","text":"第二句","speech_text":"二つ目。","expression":"thinking"},{"speaker":"亚托莉","text":"第三句","speech_text":"三つ目。","expression":"curious"}],"choices":[{"id":"example","label":"看例子","text":"用例子继续。"}]}}`,
		},
	}
	out, err := NewEngine(Options{Model: model}).GenerateAct(context.Background(), agent.ActInput{
		Request: app.SceneGenerateRequest{
			Characters: []app.Character{{ID: "atri", DisplayName: "亚托莉"}},
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
	if !strings.Contains(model.lastRequest.Messages[0].Content, "角色台词改写 Agent") {
		t.Fatalf("rewrite prompt missing: %#v", model.lastRequest.Messages)
	}
	if out.Node.ID != "opening" {
		t.Fatalf("node id = %q", out.Node.ID)
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
			`{"decision":"continue","node":{"summary":"GMP 初始化","speaker":"亚托莉","lines":[{"speaker":"亚托莉","text":"第一句","speech_text":"一つ目。","expression":"soft_smile"},{"speaker":"亚托莉","text":"第二句","speech_text":"二つ目。","expression":"thinking"},{"speaker":"亚托莉","text":"第三句","speech_text":"三つ目。","expression":"curious"}],"choices":[{"id":"term","label":"拆术语","text":"先解释术语。"}]}}`,
		},
	}
	out, err := NewEngine(Options{Model: model}).GenerateAct(context.Background(), agent.ActInput{
		Request: app.SceneGenerateRequest{
			Characters: []app.Character{{ID: "atri", DisplayName: "亚托莉"}},
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
	if len(out.Node.Lines) != 3 {
		t.Fatalf("lines = %d, want 3", len(out.Node.Lines))
	}
}

func TestGenerateActRetriesWhenOutputViolatesContract(t *testing.T) {
	t.Parallel()

	model := &stubLLM{
		contents: []string{
			validPlanJSON(),
			`{"decision":"continue","node":{"kind":"lesson","title":"第一幕","lines":[{"speaker":"亚托莉","text":"第一幕","speech_text":"第一幕","expression":"soft_smile"}]}}`,
			`{"decision":"continue","node":{"kind":"lesson","title":"第一幕","summary":"GMP 调度","speaker":"亚托莉","lines":[{"speaker":"亚托莉","text":"G 代表 goroutine，是等待运行的轻量任务。","speech_text":"G は goroutine です。","expression":"soft_smile"},{"speaker":"亚托莉","text":"M 代表系统线程，真正承载执行。","speech_text":"M は OS スレッドです。","expression":"thinking"},{"speaker":"亚托莉","text":"P 代表处理器上下文，管理本地运行队列。","speech_text":"P は実行コンテキストです。","expression":"curious"}],"choices":[{"id":"queue","label":"继续队列","text":"继续讲运行队列。"}]}}`,
		},
	}
	out, err := NewEngine(Options{Model: model}).GenerateAct(context.Background(), agent.ActInput{
		Request: app.SceneGenerateRequest{
			Characters: []app.Character{{ID: "atri", DisplayName: "亚托莉"}},
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

func TestGenerateActReusesActPlanCacheForSameSession(t *testing.T) {
	t.Parallel()

	model := &stubLLM{
		contents: []string{
			validPlanJSON(),
			`{"decision":"continue","node":{"kind":"opening","title":"开场","summary":"建立 GMP 直觉","lines":[{"speaker":"亚托莉","text":"我们先认识 G、M、P。","speech_text":"まず G、M、P を見ましょう。","expression":"soft_smile"},{"speaker":"亚托莉","text":"G 是等待运行的任务。","speech_text":"G は待っているタスクです。","expression":"thinking"},{"speaker":"亚托莉","text":"M 和 P 会决定它怎么跑起来。","speech_text":"M と P がどう動くかを決めます。","expression":"curious"}],"choices":[{"id":"next","label":"继续","text":"继续第一幕。"}]}}`,
			`{"decision":"continue","node":{"kind":"opening","title":"开场","summary":"建立 GMP 直觉","lines":[{"speaker":"亚托莉","text":"嗯，我们先别急着背。G、M、P 这三个名字，先把它们当成一组分工来看。","speech_text":"うん、まず急いで覚えなくて大丈夫です。G、M、P は役割の分担として見てみましょう。","expression":"soft_smile"},{"speaker":"亚托莉","text":"G 是等着被运行的小任务，像排队等叫号的人。","speech_text":"G は実行を待つ小さなタスクです。順番を待っている人みたいなものです。","expression":"thinking"},{"speaker":"亚托莉","text":"M 和 P 会一起决定它什么时候、在哪里真正跑起来。","speech_text":"M と P が、それをいつ、どこで実際に動かすかを決めます。","expression":"curious"}],"choices":[{"id":"next","label":"继续","text":"继续第一幕。"}]}}`,
			`{"decision":"continue","node":{"kind":"lesson","title":"第一幕","summary":"GMP 协作","lines":[{"speaker":"亚托莉","text":"G 像任务，排队等待。","speech_text":"G はタスクのように待ちます。","expression":"soft_smile"},{"speaker":"亚托莉","text":"M 是真正工作的线程。","speech_text":"M は実際に働くスレッドです。","expression":"thinking"},{"speaker":"亚托莉","text":"P 管着能运行的上下文。","speech_text":"P は実行の文脈を管理します。","expression":"curious"}],"choices":[{"id":"queue","label":"看队列","text":"继续讲队列。"}]}}`,
			`{"decision":"continue","node":{"kind":"lesson","title":"第一幕","summary":"GMP 协作","lines":[{"speaker":"亚托莉","text":"你可以把 G 想成等待安排的小任务，它不会自己决定何时上场。","speech_text":"G は、割り当てを待つ小さなタスクだと思ってください。自分だけでは出番を決めません。","expression":"soft_smile"},{"speaker":"亚托莉","text":"M 才是真正干活的系统线程，负责把任务跑起来。","speech_text":"M は実際に働く OS スレッドで、タスクを動かします。","expression":"thinking"},{"speaker":"亚托莉","text":"而 P 像工作台，管理可以运行的上下文和本地队列。这样三者才配合起来。","speech_text":"そして P は作業台のように、実行できる文脈とローカルキューを管理します。だから三つが協力できるんです。","expression":"curious"}],"choices":[{"id":"queue","label":"看队列","text":"继续讲队列。"}]}}`,
		},
	}
	engine := NewEngine(Options{Model: model})
	request := app.SceneGenerateRequest{
		Topic:        "Go 调度器",
		DocumentText: "GMP 模型解释 goroutine、线程和处理器上下文如何协作。",
		Characters:   []app.Character{{ID: "atri", DisplayName: "亚托莉"}},
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
			"```json\n" + `{"decision":"continue","node":{"kind":"lesson","title":"第一幕","summary":"GMP","lines":[{"speaker":"亚托莉","text":"G 是 goroutine。","speech_text":"G は goroutine です。","expression":"soft_smile"},{"speaker":"亚托莉","text":"M 是系统线程。","speech_text":"M は OS スレッドです。","expression":"thinking"},{"speaker":"亚托莉","text":"P 是处理器上下文。","speech_text":"P はコンテキストです。","expression":"curious"}],"choices":[{"id":"next","label":"继续","text":"继续下一点。"}]}}` + "\n```",
		},
	}
	_, err := NewEngine(Options{Model: model}).GenerateAct(context.Background(), agent.ActInput{
		Request: app.SceneGenerateRequest{
			Characters: []app.Character{{ID: "atri", DisplayName: "亚托莉"}},
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
