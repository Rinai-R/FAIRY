package fairy

import (
	"strings"
	"testing"

	"github.com/Rinai-R/FAIRY/internal/adapters/agent"
	"github.com/Rinai-R/FAIRY/internal/app"
)

func TestStructureEvalPassesGoSchedulerGoldenCandidate(t *testing.T) {
	result := EvaluateAgentStructure(goSchedulerStructureSuite(), goSchedulerGoldenOutputs())
	if !result.Passed {
		t.Fatalf("EvaluateAgentStructure() failed: %+v", result.Issues)
	}
	if len(result.Acts) != 3 {
		t.Fatalf("act results = %d, want 3", len(result.Acts))
	}
	for _, act := range result.Acts {
		if !act.Passed {
			t.Fatalf("act %s failed: %+v", act.ActID, act.Issues)
		}
	}
}

func TestStructureEvalReportsMissingLinesAndCoveredPoints(t *testing.T) {
	outputs := goSchedulerGoldenOutputs()
	outputs[1].CoveredPoints = nil
	outputs[1].Node.Lines = []app.DialogueLine{
		evalLine("G 是 goroutine，负责表示等待执行的任务。"),
	}
	result := EvaluateAgentStructure(goSchedulerStructureSuite(), outputs)
	if result.Passed {
		t.Fatal("EvaluateAgentStructure() passed, want structural failure")
	}
	assertStructureIssue(t, result, "lesson-1", "lines", "台词数量不足")
	assertStructureIssue(t, result, "lesson-1", "covered_points", "teaching act 必须提供 covered_points")
}

func TestStructureEvalReportsChoiceLabelContract(t *testing.T) {
	outputs := goSchedulerGoldenOutputs()
	outputs[0].Node.Choices[0].Label = "我想先完整阅读这一整段分支回复正文内容"
	outputs[0].Node.Choices[0].Text = outputs[0].Node.Choices[0].Label
	result := EvaluateAgentStructure(goSchedulerStructureSuite(), outputs)
	if result.Passed {
		t.Fatal("EvaluateAgentStructure() passed, want choice label failure")
	}
	assertStructureIssue(t, result, "opening", "choices", "短按钮文案")
	assertStructureIssue(t, result, "opening", "choices", "label 不能与 text 相同")
}

func TestStructureEvalReportsMissingActWithoutSkippingRest(t *testing.T) {
	outputs := []agent.ActOutput{goSchedulerGoldenOutputs()[0]}
	result := EvaluateAgentStructure(goSchedulerStructureSuite(), outputs)
	if result.Passed {
		t.Fatal("EvaluateAgentStructure() passed, want missing act failure")
	}
	if len(result.Acts) != 3 {
		t.Fatalf("act results = %d, want all expected acts checked", len(result.Acts))
	}
	assertStructureIssue(t, result, "lesson-1", "act", "缺少候选输出")
	assertStructureIssue(t, result, "summary", "act", "缺少候选输出")
}

func TestFormatAgentStructureReportPassSummary(t *testing.T) {
	result := EvaluateAgentStructure(goSchedulerStructureSuite(), goSchedulerGoldenOutputs())
	report := FormatAgentStructureReport(result)
	assertReportContains(t, report,
		"FAIRY Agent 结构评估：go-scheduler-gmp",
		"状态：通过",
		"幕：3/3 通过",
		"问题：0",
		"[通过] opening",
		"[通过] lesson-1",
		"[通过] summary",
	)
	if strings.Contains(report, "[失败]") {
		t.Fatalf("report contains unexpected failure:\n%s", report)
	}
}

func TestFormatAgentStructureReportListsFailureDetails(t *testing.T) {
	outputs := goSchedulerGoldenOutputs()
	outputs[1].CoveredPoints = nil
	outputs[1].Node.Lines = []app.DialogueLine{
		evalLine("G 是 goroutine，负责表示等待执行的任务。"),
	}
	result := EvaluateAgentStructure(goSchedulerStructureSuite(), outputs)
	report := FormatAgentStructureReport(result)
	assertReportContains(t, report,
		"状态：失败",
		"幕：2/3 通过",
		"问题：2",
		"[失败] lesson-1",
		"- lines：台词数量不足: 1/4",
		"- covered_points：teaching act 必须提供 covered_points",
	)
}

func goSchedulerStructureSuite() StructureEvalSuite {
	return StructureEvalSuite{
		Name: "go-scheduler-gmp",
		Acts: []StructureEvalActExpectation{
			{
				ID:   "opening",
				Kind: "opening",
			},
			{
				ID:   "lesson-1",
				Kind: "lesson",
			},
			{
				ID:             "summary",
				Kind:           "summary",
				AllowNoChoices: true,
			},
		},
	}
}

func goSchedulerGoldenOutputs() []agent.ActOutput {
	return []agent.ActOutput{
		{
			Decision:      agent.ActDecisionContinue,
			CoveredPoints: []string{"GMP"},
			Node: app.TeachingWorkflowNode{
				ID:      "opening",
				Kind:    "opening",
				Title:   "开场",
				Summary: "建立 GMP 直觉",
				Lines: []app.DialogueLine{
					evalLine("我们先把 GMP 看成一组分工，不要急着背定义。"),
					evalLine("G、M、P 不是三个并列线程，而是调度里的不同层次。"),
					evalLine("G 像任务卡，记录等待安排的工作。"),
					evalLine("M 像执行者，P 像工作台，把任务卡交给执行者。"),
				},
				Choices: []app.SceneChoice{{ID: "terms", Label: "拆术语", Text: "先拆开 G、M、P 的角色。"}},
			},
		},
		{
			Decision:      agent.ActDecisionContinue,
			CoveredPoints: []string{"goroutine", "系统线程", "运行队列"},
			Node: app.TeachingWorkflowNode{
				ID:      "lesson-1",
				Kind:    "lesson",
				Title:   "第一幕",
				Summary: "G、M、P 如何协作",
				Lines: []app.DialogueLine{
					evalLine("G 代表 goroutine，是等待执行的轻量任务。"),
					evalLine("M 是系统线程，真正承载代码执行。"),
					evalLine("goroutine 不是系统线程，G 要被 M 承载才会运行。"),
					evalLine("P 管理本地运行队列，就像小组作业里的工作台。"),
				},
				Choices: []app.SceneChoice{{ID: "queue", Label: "看队列", Text: "继续解释本地运行队列。"}},
			},
		},
		{
			Decision:      agent.ActDecisionFreeDiscussion,
			CoveredPoints: []string{"协作"},
			Node: app.TeachingWorkflowNode{
				ID:      "summary",
				Kind:    "summary",
				Title:   "总结",
				Summary: "回收 GMP 模型",
				Lines: []app.DialogueLine{
					evalLine("GMP 调度靠 G、M、P 协作，不是单个对象独立完成。"),
					evalLine("如果只有任务卡没有执行者，任务不会真正运行。"),
					evalLine("所以理解调度时，要同时看任务、线程和队列如何配合。"),
				},
			},
		},
	}
}

func evalLine(text string) app.DialogueLine {
	return app.DialogueLine{Speaker: "亚托莉", Text: text, SpeechText: text, Expression: "soft_smile"}
}

func assertStructureIssue(t *testing.T, result StructureEvalResult, actID string, field string, contains string) {
	t.Helper()
	for _, issue := range result.Issues {
		if issue.ActID == actID && issue.Field == field && strings.Contains(issue.Message, contains) {
			return
		}
	}
	t.Fatalf("missing structure issue act=%s field=%s contains=%q; got %+v", actID, field, contains, result.Issues)
}

func assertReportContains(t *testing.T, report string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(report, want) {
			t.Fatalf("report missing %q:\n%s", want, report)
		}
	}
}
