package tool

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	history "fairy/context/history/transcript"
	"fairy/runtime/model"
)

func TestTranscriptProjectionIsBoundedAndShared(t *testing.T) {
	result := history.CompactedTranscriptRecall{
		Turns: []history.CompactedTranscriptTurn{{
			TurnID: "turn-1",
			Messages: []history.MessageRecord{
				{MessageID: "message-1", Role: "user", Content: strings.Repeat("海", 900), Sequence: 1},
				{MessageID: "message-2", Role: "assistant", Content: "记得我们的约定", Sequence: 2},
			},
		}},
	}
	call := model.FunctionCall{CallID: "call-history-1", Name: ConversationHistorySearch, Arguments: `{"query":"约定"}`}
	items := TranscriptPromptItems(call, "ok", result)
	if len(items) != 3 || items[2].Type != model.PromptItemContextData {
		t.Fatalf("items = %#v", items)
	}
	var modelProjection TranscriptContext
	if err := json.Unmarshal([]byte(items[2].Content), &modelProjection); err != nil {
		t.Fatal(err)
	}
	detail := TranscriptRuntimeProjection("约定", "ok", result)
	if detail.Result == nil || !reflect.DeepEqual(*detail.Result, modelProjection) {
		t.Fatalf("runtime projection = %#v, model projection = %#v", detail.Result, modelProjection)
	}
	if utf8Runes := len([]rune(modelProjection.Turns[0].Messages[0].Content)); utf8Runes != maxTranscriptProjectionMessageRunes {
		t.Fatalf("projected runes = %d", utf8Runes)
	}
	if !modelProjection.Truncated || !detail.Receipt.Truncated || detail.Receipt.MessageCount != 2 {
		t.Fatalf("projection = %#v, receipt = %#v", modelProjection, detail.Receipt)
	}
	segments, err := TranscriptContextSegments(items, time.UnixMilli(1000))
	if err != nil {
		t.Fatal(err)
	}
	if len(segments) != 3 || segments[2].Kind != model.ContextSegmentContextData || len(segments[2].Dependencies) != 1 || segments[2].Dependencies[0] != segments[1].ID {
		t.Fatalf("segments = %#v", segments)
	}
}

func TestTranscriptFailureOmitsRawProjection(t *testing.T) {
	result := history.CompactedTranscriptRecall{Turns: []history.CompactedTranscriptTurn{{TurnID: "turn", Messages: []history.MessageRecord{{Content: "secret"}}}}}
	call := model.FunctionCall{CallID: "call-history-2", Name: ConversationHistorySearch, Arguments: `{"query":"secret"}`}
	items := TranscriptPromptItems(call, "failed", result)
	if len(items) != 2 {
		t.Fatalf("failure items = %#v", items)
	}
	detail := TranscriptRuntimeProjection("secret", "failed", result)
	if detail.Result != nil || detail.Receipt.Status != "failed" {
		t.Fatalf("failure detail = %#v", detail)
	}
}
