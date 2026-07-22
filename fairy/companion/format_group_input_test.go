package companion

import "testing"

func TestFormatAmbientTurnInputRequiresExactlyOneTarget(t *testing.T) {
	messages := []AmbientObservation{{MessageID: "1", SenderID: "2", SenderName: "甲", Text: "你好"}}
	if _, err := FormatAmbientTurnInput(messages, "missing"); err == nil {
		t.Fatal("missing target accepted")
	}
	messages = append(messages, messages[0])
	if _, err := FormatAmbientTurnInput(messages, "1"); err == nil {
		t.Fatal("duplicate target accepted")
	}
	input, err := FormatAmbientTurnInput([]AmbientObservation{
		{MessageID: "1", SenderID: "40001", SenderName: "甲", Text: "你好"},
		{MessageID: "2", SenderID: "40002", SenderName: "乙", Text: "在吗"},
	}, "2")
	if err != nil {
		t.Fatal(err)
	}
	if input != "[甲/40001] 你好\n[reply-target][乙/40002] 在吗" {
		t.Fatalf("input = %q", input)
	}
}
