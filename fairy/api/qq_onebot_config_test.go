package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"fairy/config"

	"github.com/cloudwego/hertz/pkg/common/ut"
	"go.uber.org/zap"
)

func TestQQOneBotConfigRoutesRequireAuthAndRoundTripNormalizedAllowlist(t *testing.T) {
	root := t.TempDir()
	server, err := NewServer(&Dependencies{
		Config: config.NewConfigService(root, nil),
		Logger: zap.NewNop(),
	}, Options{Addr: "127.0.0.1:0", Token: "core-token", Logger: zap.NewNop()})
	if err != nil {
		t.Fatal(err)
	}

	unauthorized := ut.PerformRequest(server.Engine().Engine, http.MethodGet, "/v1/config/qq-onebot", nil)
	if unauthorized.Result().StatusCode() != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", unauthorized.Result().StatusCode())
	}

	putBody := []byte(`{"groupAllowlist":[" 00123 ","456","123"]}`)
	put := ut.PerformRequest(server.Engine().Engine, http.MethodPut, "/v1/config/qq-onebot", &ut.Body{Body: bytes.NewReader(putBody), Len: len(putBody)},
		ut.Header{Key: "Authorization", Value: "Bearer core-token"},
		ut.Header{Key: "Content-Type", Value: "application/json"},
	)
	if put.Result().StatusCode() != http.StatusOK {
		t.Fatalf("PUT status = %d body=%s", put.Result().StatusCode(), put.Result().Body())
	}
	assertQQOneBotResponse(t, put.Result().Body(), []string{"123", "456"})

	get := ut.PerformRequest(server.Engine().Engine, http.MethodGet, "/v1/config/qq-onebot", nil,
		ut.Header{Key: "Authorization", Value: "Bearer core-token"},
	)
	if get.Result().StatusCode() != http.StatusOK {
		t.Fatalf("GET status = %d body=%s", get.Result().StatusCode(), get.Result().Body())
	}
	assertQQOneBotResponse(t, get.Result().Body(), []string{"123", "456"})
}

func TestQQOneBotConfigPutRejectsInvalidWithoutChangingState(t *testing.T) {
	root := t.TempDir()
	service := config.NewConfigService(root, nil)
	if _, err := service.SaveQQOneBotSettings(config.QQOneBotSettings{GroupAllowlist: []string{"777"}}); err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(&Dependencies{Config: service, Logger: zap.NewNop()}, Options{Addr: "127.0.0.1:0", Token: "core-token", Logger: zap.NewNop()})
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"groupAllowlist":["-1"]}`)
	response := ut.PerformRequest(server.Engine().Engine, http.MethodPut, "/v1/config/qq-onebot", &ut.Body{Body: bytes.NewReader(body), Len: len(body)},
		ut.Header{Key: "Authorization", Value: "Bearer core-token"},
		ut.Header{Key: "Content-Type", Value: "application/json"},
	)
	if response.Result().StatusCode() != http.StatusBadRequest {
		t.Fatalf("invalid PUT status = %d body=%s", response.Result().StatusCode(), response.Result().Body())
	}
	settings, err := service.QQOneBotSettings()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(settings.GroupAllowlist, []string{"777"}) {
		t.Fatalf("allowlist after invalid PUT = %#v", settings.GroupAllowlist)
	}
}

func assertQQOneBotResponse(t *testing.T, raw []byte, want []string) {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload) != 2 || payload["schemaVersion"] != float64(1) {
		t.Fatalf("response fields = %#v", payload)
	}
	items, ok := payload["groupAllowlist"].([]any)
	if !ok || len(items) != len(want) {
		t.Fatalf("groupAllowlist = %#v", payload["groupAllowlist"])
	}
	for index, item := range items {
		if item != want[index] {
			t.Fatalf("groupAllowlist[%d] = %#v, want %q", index, item, want[index])
		}
	}
	for _, forbidden := range []string{"token", "authorization", "pmhq", "onebot-token", "core-token"} {
		if strings.Contains(strings.ToLower(string(raw)), forbidden) {
			t.Fatalf("response leaked forbidden %q: %s", forbidden, raw)
		}
	}
}
