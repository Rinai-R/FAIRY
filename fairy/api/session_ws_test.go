package api

import "testing"

func TestSessionPlaneDocumentsWebSocketOnly(t *testing.T) {
	removed := []string{
		"POST /v1/sessions",
		"POST /v1/sessions/:id/turns",
		"POST /v1/sessions/:id/group-participation",
		"GET /v1/sessions/:id/events",
		"POST /v1/sessions/:id/turns/:turnId/cancel",
	}
	if len(removed) != 5 {
		t.Fatalf("removed session HTTP/SSE routes = %d", len(removed))
	}
}
