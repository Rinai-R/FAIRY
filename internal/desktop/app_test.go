package desktop

import (
	"log/slog"
	"testing"
	"time"

	"github.com/Rinai-R/FAIRY/internal/app"
	domainruntime "github.com/Rinai-R/FAIRY/internal/runtime"
)

func TestSessionEventsBridge(t *testing.T) {
	store := domainruntime.NewFileSessionStore(t.TempDir() + "/sessions.json")
	if _, err := store.BeginScene(app.SceneGenerateRequest{
		Topic:      "事件观测",
		Characters: []app.Character{{ID: "atri", DisplayName: "亚托莉"}},
	}, app.SceneGenerateResponse{
		Scene:   app.Scene{ID: "lesson", Title: "课程"},
		Session: app.Session{ID: "desktop-events", UserID: "default", ActiveCharacterID: "atri"},
	}); err != nil {
		t.Fatalf("BeginScene() error = %v", err)
	}
	if _, err := store.AppendEvent("desktop-events", app.RuntimeEvent{
		Level:     app.RuntimeEventLevelError,
		Type:      app.RuntimeEventTypeWorkflowNodeFailed,
		Stage:     app.RuntimeEventStageWorkflow,
		Message:   "workflow failed",
		NodeID:    "lesson-2",
		CreatedAt: time.Date(2026, 6, 20, 9, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}
	bridge := &App{
		runtime: domainruntime.NewRuntime(domainruntime.Dependencies{
			Sessions: store,
			Logger:   slog.Default(),
		}),
	}

	payload, err := bridge.SessionEvents("desktop-events")
	if err != nil {
		t.Fatalf("SessionEvents() error = %v", err)
	}
	if len(payload.Events) != 1 {
		t.Fatalf("events = %d, want 1", len(payload.Events))
	}
	if payload.Events[0].Type != app.RuntimeEventTypeWorkflowNodeFailed || payload.Events[0].NodeID != "lesson-2" {
		t.Fatalf("event = %+v", payload.Events[0])
	}
}
