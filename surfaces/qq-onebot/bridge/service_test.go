package bridge

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/RomiChan/websocket"
)

func TestCheckOneBotUsesBearerAndValidatesSelfID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer exact-token" {
			t.Errorf("authorization = %q", r.Header.Get("Authorization"))
		}
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.WriteJSON(map[string]any{"post_type": "meta_event", "self_id": 10001})
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	cfg := Config{OneBotEndpoint: "ws" + strings.TrimPrefix(server.URL, "http"), OneBotToken: "exact-token", SelfID: "10001"}
	if err := checkOneBot(ctx, cfg); err != nil {
		t.Fatal(err)
	}
}
