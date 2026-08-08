package extraction

import (
	"slices"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	memoryretrieval "fairy/context/memory/retrieval"
)

func TestBuildExtractionRetrievalProjectionIsBoundedAndSearchable(t *testing.T) {
	turns := []Turn{
		{
			TurnID:           "turn-1",
			UserMessage:      strings.Repeat("很长的用户事实", 600) + "\n喜欢围棋\t周末复盘",
			AssistantMessage: "我记住了\r以后接着聊",
		},
		{
			TurnID:           "turn-2",
			UserMessage:      "!!!\n\t",
			AssistantMessage: "后序标记甲乙丙",
		},
	}
	originalUser := turns[0].UserMessage
	originalAssistant := turns[0].AssistantMessage

	first := BuildRetrievalProjection(turns)
	second := BuildRetrievalProjection(turns)
	firstText := strings.Join(first, " ")

	if len(first) == 0 {
		t.Fatal("projection is empty")
	}
	if !slices.Equal(first, second) {
		t.Fatalf("projection is not deterministic:\nfirst:  %q\nsecond: %q", first, second)
	}
	if got := utf8.RuneCountInString(firstText); got > memoryretrieval.MaxQueryRunes {
		t.Fatalf("projection rune count = %d, want <= %d", got, memoryretrieval.MaxQueryRunes)
	}
	for _, fragment := range first {
		if strings.TrimSpace(fragment) != fragment || strings.Contains(fragment, " ") {
			t.Fatalf("projection fragment spacing is not normalized: %q", fragment)
		}
		for _, character := range fragment {
			if unicode.IsControl(character) {
				t.Fatalf("projection contains control character %U", character)
			}
		}
		if _, err := memoryretrieval.BuildFTSQuery(fragment); err != nil {
			t.Fatalf("projection fragment does not satisfy retrieval query contract: %v", err)
		}
	}
	if turns[0].UserMessage != originalUser || turns[0].AssistantMessage != originalAssistant {
		t.Fatal("projection mutated extraction evidence")
	}
}

func TestBuildExtractionRetrievalProjectionKeepsEverySearchableFieldRepresented(t *testing.T) {
	turns := []Turn{
		{TurnID: "turn-1", UserMessage: strings.Repeat("前序超长内容", 1000), AssistantMessage: "助手甲标记"},
		{TurnID: "turn-2", UserMessage: "用户乙标记", AssistantMessage: "助手乙标记"},
		{TurnID: "turn-3", UserMessage: "用户丙标记", AssistantMessage: "助手丙标记"},
	}

	projection := strings.Join(BuildRetrievalProjection(turns), " ")
	for _, marker := range []string{"助手甲标记", "用户乙标记", "助手乙标记", "用户丙标记", "助手丙标记"} {
		if !strings.Contains(projection, marker) {
			t.Errorf("projection does not contain later field marker %q", marker)
		}
	}
}

func TestBuildExtractionRetrievalProjectionEmptyWithoutSearchableTrigram(t *testing.T) {
	turns := []Turn{{TurnID: "turn-1", UserMessage: "!!", AssistantMessage: "甲乙"}}
	if got := BuildRetrievalProjection(turns); len(got) != 0 {
		t.Fatalf("projection = %q, want empty", got)
	}
}
