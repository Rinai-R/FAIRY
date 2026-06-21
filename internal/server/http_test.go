package server

import (
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/Rinai-R/FAIRY/internal/app"
	"github.com/Rinai-R/FAIRY/internal/runtime"
	hertzserver "github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

func TestSessionEventsHTTP(t *testing.T) {
	rt := runtimeWithEvents(t)
	h := hertzserver.New()
	Register(h, rt, Options{})

	rec := ut.PerformRequest(h.Engine, "GET", "/api/v1/sessions/session-events/events", nil)
	if rec.Code != consts.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var response struct {
		Code int `json:"code"`
		Data struct {
			Events []app.RuntimeEvent `json:"events"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("Unmarshal() error = %v body = %s", err, rec.Body.String())
	}
	if response.Code != httpOKCode {
		t.Fatalf("code = %d body = %s", response.Code, rec.Body.String())
	}
	if len(response.Data.Events) != 2 {
		t.Fatalf("events = %d, want 2", len(response.Data.Events))
	}
	if response.Data.Events[0].Type != app.RuntimeEventTypeGenerationFailed || response.Data.Events[1].Type != app.RuntimeEventTypeVoiceSynthesizeFailed {
		t.Fatalf("events order = %+v", response.Data.Events)
	}
	if response.Data.Events[1].Provider != "volcengine" || response.Data.Events[1].NodeID != "lesson-1" {
		t.Fatalf("voice event = %+v", response.Data.Events[1])
	}
}

func runtimeWithEvents(t *testing.T) *runtime.Runtime {
	t.Helper()
	store := runtime.NewFileSessionStore(t.TempDir() + "/sessions.json")
	if _, err := store.BeginScene(app.SceneGenerateRequest{
		Topic:      "事件观测",
		Characters: []app.Character{{ID: "atri", DisplayName: "亚托莉"}},
	}, app.SceneGenerateResponse{
		Scene:   app.Scene{ID: "lesson", Title: "课程"},
		Session: app.Session{ID: "session-events", UserID: "default", ActiveCharacterID: "atri"},
	}); err != nil {
		t.Fatalf("BeginScene() error = %v", err)
	}
	later := time.Date(2026, 6, 20, 8, 1, 0, 0, time.UTC)
	earlier := later.Add(-time.Minute)
	if _, err := store.AppendEvent("session-events", app.RuntimeEvent{
		Level:     app.RuntimeEventLevelError,
		Type:      app.RuntimeEventTypeVoiceSynthesizeFailed,
		Stage:     app.RuntimeEventStageVoice,
		Message:   "voice quota exceeded",
		NodeID:    "lesson-1",
		Provider:  "volcengine",
		CreatedAt: later,
	}); err != nil {
		t.Fatalf("AppendEvent(voice) error = %v", err)
	}
	if _, err := store.AppendEvent("session-events", app.RuntimeEvent{
		Level:     app.RuntimeEventLevelError,
		Type:      app.RuntimeEventTypeGenerationFailed,
		Stage:     app.RuntimeEventStageGeneration,
		Message:   "generation failed",
		CreatedAt: earlier,
	}); err != nil {
		t.Fatalf("AppendEvent(generation) error = %v", err)
	}
	return runtime.NewRuntime(runtime.Dependencies{
		Sessions: store,
		Logger:   slog.Default(),
	})
}
