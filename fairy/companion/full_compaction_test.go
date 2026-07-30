package companion

import (
	"strings"
	"testing"

	"fairy/memory"
)

func TestNormalizeCompactionSummaryRequiresStructuredSections(t *testing.T) {
	valid := `{
	  "currentGoal":"继续当前话题",
	  "userConstraints":"不要主动给建议",
	  "relationship":"熟悉的私人陪伴关系",
	  "keyFacts":["用户周末去爬山"],
	  "completedWork":"已确认出发时间",
	  "openQuestions":"天气是否合适",
	  "nextSteps":"等待用户继续",
	  "sourceRefs":["turn-1"]
	}`
	got, err := normalizeCompactionSummary(valid)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "\n") || !strings.Contains(got, `"currentGoal":"继续当前话题"`) {
		t.Fatalf("normalized summary = %q", got)
	}
	for _, invalid := range []string{
		`{"currentGoal":"only"}`,
		`{"currentGoal":"x","userConstraints":"x","relationship":"x","keyFacts":[],"completedWork":"x","openQuestions":"x","nextSteps":"x","sourceRefs":[],"extra":true}`,
		`plain summary`,
	} {
		if _, err := normalizeCompactionSummary(invalid); err == nil {
			t.Fatalf("normalizeCompactionSummary(%q) error = nil", invalid)
		}
	}
}

func TestSelectRecentCompleteTurnTailKeepsWholeTurnsWithinBudget(t *testing.T) {
	messages := []memory.MessageRecord{
		{TurnID: "turn-1", Sequence: 1, Role: "user", Content: strings.Repeat("旧", 80)},
		{TurnID: "turn-1", Sequence: 2, Role: "assistant", Content: strings.Repeat("旧", 80)},
		{TurnID: "turn-2", Sequence: 3, Role: "user", Content: "最近问题"},
		{TurnID: "turn-2", Sequence: 4, Role: "assistant", Content: "最近回答"},
	}
	tail := selectRecentCompleteTurnTail(messages, 10)
	if len(tail) != 2 || tail[0].Sequence != 3 || tail[1].Sequence != 4 {
		t.Fatalf("tail = %#v", tail)
	}
}

func TestSelectRecentCompleteTurnTailRejectsUnpairedFragment(t *testing.T) {
	messages := []memory.MessageRecord{
		{TurnID: "turn-1", Sequence: 1, Role: "user", Content: "问题"},
		{TurnID: "turn-1", Sequence: 2, Role: "assistant", Content: "回答"},
		{TurnID: "turn-2", Sequence: 3, Role: "user", Content: "未完成"},
	}
	if tail := selectRecentCompleteTurnTail(messages, 100); len(tail) != 0 {
		t.Fatalf("tail = %#v", tail)
	}
}
