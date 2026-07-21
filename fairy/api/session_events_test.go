package api

import "testing"

func TestSessionHarnessEventNameIsStable(t *testing.T) {
	if sessionHarnessEventName != "harness" {
		t.Fatalf("session harness SSE event name = %q", sessionHarnessEventName)
	}
}
