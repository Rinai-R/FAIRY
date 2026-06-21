package fairy

import (
	"fmt"
	"strings"

	"github.com/Rinai-R/FAIRY/internal/adapters/agent"
	"github.com/Rinai-R/FAIRY/internal/app"
)

type StructureEvalSuite struct {
	Name string
	Acts []StructureEvalActExpectation
}

type StructureEvalActExpectation struct {
	ID             string
	Kind           string
	MinLines       int
	RequireChoices bool
	AllowNoChoices bool
}

type StructureEvalResult struct {
	SuiteName string
	Passed    bool
	Acts      []StructureEvalActResult
	Issues    []StructureEvalIssue
}

type StructureEvalActResult struct {
	ActID  string
	Passed bool
	Issues []StructureEvalIssue
}

type StructureEvalIssue struct {
	ActID   string
	Field   string
	Message string
}

func EvaluateAgentStructure(suite StructureEvalSuite, outputs []agent.ActOutput) StructureEvalResult {
	result := StructureEvalResult{
		SuiteName: strings.TrimSpace(suite.Name),
		Passed:    true,
	}
	outputByAct := map[string]agent.ActOutput{}
	for _, output := range outputs {
		id := strings.TrimSpace(output.Node.ID)
		if id == "" {
			continue
		}
		if _, exists := outputByAct[id]; !exists {
			outputByAct[id] = output
		}
	}
	for index, expected := range suite.Acts {
		actID := strings.TrimSpace(expected.ID)
		if actID == "" {
			actID = fmt.Sprintf("act-%d", index+1)
		}
		actResult := StructureEvalActResult{ActID: actID, Passed: true}
		output, ok := outputByAct[actID]
		if !ok {
			actResult.addIssue("act", "缺少候选输出")
			result.addActResult(actResult)
			continue
		}
		evaluateActStructure(expected.withID(actID), output, &actResult)
		result.addActResult(actResult)
	}
	return result
}

func FormatAgentStructureReport(result StructureEvalResult) string {
	suiteName := firstNonEmpty(strings.TrimSpace(result.SuiteName), "未命名套件")
	passedActs := 0
	for _, act := range result.Acts {
		if act.Passed {
			passedActs++
		}
	}

	var builder strings.Builder
	fmt.Fprintf(&builder, "FAIRY Agent 结构评估：%s\n", suiteName)
	fmt.Fprintf(&builder, "状态：%s\n", structureEvalStatusText(result.Passed))
	fmt.Fprintf(&builder, "幕：%d/%d 通过\n", passedActs, len(result.Acts))
	fmt.Fprintf(&builder, "问题：%d\n", len(result.Issues))
	for _, act := range result.Acts {
		fmt.Fprintf(&builder, "\n[%s] %s\n", structureEvalStatusText(act.Passed), firstNonEmpty(strings.TrimSpace(act.ActID), "未命名幕"))
		for _, issue := range act.Issues {
			field := firstNonEmpty(strings.TrimSpace(issue.Field), "unknown")
			message := firstNonEmpty(strings.TrimSpace(issue.Message), "未提供诊断")
			fmt.Fprintf(&builder, "- %s：%s\n", field, message)
		}
	}
	return builder.String()
}

func structureEvalStatusText(passed bool) string {
	if passed {
		return "通过"
	}
	return "失败"
}

func evaluateActStructure(expected StructureEvalActExpectation, output agent.ActOutput, result *StructureEvalActResult) {
	node := output.Node
	kind := firstNonEmpty(strings.TrimSpace(expected.Kind), strings.TrimSpace(node.Kind))
	if strings.TrimSpace(expected.Kind) != "" && strings.TrimSpace(node.Kind) != "" && strings.TrimSpace(expected.Kind) != strings.TrimSpace(node.Kind) {
		result.addIssue("kind", fmt.Sprintf("节点类型 = %q，期望 %q", node.Kind, expected.Kind))
	}
	minLines := expected.MinLines
	if minLines <= 0 {
		minLines = minimumTeachingLines(kind)
	}
	if len(node.Lines) < minLines {
		result.addIssue("lines", fmt.Sprintf("台词数量不足: %d/%d", len(node.Lines), minLines))
	}
	if shouldEvaluateChoices(expected, kind) {
		evaluateChoiceQuality(node.Choices, result)
	}
}

func evaluateChoiceQuality(choices []app.SceneChoice, result *StructureEvalActResult) {
	if len(choices) == 0 {
		result.addIssue("choices", "opening/lesson 必须提供选项")
		return
	}
	for index, choice := range choices {
		label := strings.TrimSpace(choice.Label)
		text := strings.TrimSpace(choice.Text)
		if strings.TrimSpace(choice.ID) == "" {
			result.addIssue("choices", fmt.Sprintf("choices[%d].id 不能为空", index))
		}
		if label == "" {
			result.addIssue("choices", fmt.Sprintf("choices[%d].label 不能为空", index))
		}
		if text == "" {
			result.addIssue("choices", fmt.Sprintf("choices[%d].text 不能为空", index))
		}
	}
}

func shouldEvaluateChoices(expected StructureEvalActExpectation, kind string) bool {
	if expected.AllowNoChoices {
		return false
	}
	if expected.RequireChoices {
		return true
	}
	return strings.TrimSpace(kind) == "opening" || strings.TrimSpace(kind) == "lesson"
}

func (expected StructureEvalActExpectation) withID(id string) StructureEvalActExpectation {
	expected.ID = id
	return expected
}

func (result *StructureEvalResult) addActResult(act StructureEvalActResult) {
	if len(act.Issues) > 0 {
		act.Passed = false
		result.Passed = false
		result.Issues = append(result.Issues, act.Issues...)
	}
	result.Acts = append(result.Acts, act)
}

func (result *StructureEvalActResult) addIssue(field string, message string) {
	result.Passed = false
	result.Issues = append(result.Issues, StructureEvalIssue{
		ActID:   result.ActID,
		Field:   strings.TrimSpace(field),
		Message: strings.TrimSpace(message),
	})
}
