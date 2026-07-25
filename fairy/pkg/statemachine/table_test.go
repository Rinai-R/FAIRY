package statemachine

import "testing"

func TestTableAllowsOnlyDeclaredTransitions(t *testing.T) {
	table := MustTable(Edge[string]{From: "idle", To: "running"}, Edge[string]{From: "running", To: "done"})
	if !table.Allows("idle", "running") || table.Allows("idle", "done") || table.Allows("done", "running") {
		t.Fatal("transition table did not preserve declared edges")
	}
	if _, err := NewTable(Edge[string]{From: "a", To: "b"}, Edge[string]{From: "a", To: "b"}); err == nil {
		t.Fatal("duplicate transition accepted")
	}
}
