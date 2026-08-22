//go:build !endpointstrict

package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"fairy/runtime/config"

	"github.com/cloudwego/hertz/pkg/common/ut"
	"go.uber.org/zap"
)

func TestQQOneBotConfigRoutesRequireAuthAndRefuseExternalAccess(t *testing.T) {
	root := t.TempDir()
	service := config.NewConfigService(root, nil)
	if _, err := service.SaveQQOneBotSettings(config.QQOneBotSettings{GroupAllowlist: []string{"123"}}); err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(&Dependencies{
		Config: service,
		Logger: zap.NewNop(),
	}, Options{Addr: "127.0.0.1:0", Token: "core-token", Logger: zap.NewNop()})
	if err != nil {
		t.Fatal(err)
	}

	unauthorized := ut.PerformRequest(server.Engine().Engine, http.MethodGet, "/v1/config/qq-onebot", nil)
	if unauthorized.Result().StatusCode() != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", unauthorized.Result().StatusCode())
	}

	putBody := []byte(`{"groupAllowlist":["456"]}`)
	put := ut.PerformRequest(server.Engine().Engine, http.MethodPut, "/v1/config/qq-onebot", &ut.Body{Body: bytes.NewReader(putBody), Len: len(putBody)},
		ut.Header{Key: "Authorization", Value: "Bearer core-token"},
		ut.Header{Key: "Content-Type", Value: "application/json"},
	)
	if put.Result().StatusCode() != http.StatusForbidden {
		t.Fatalf("PUT status = %d body=%s", put.Result().StatusCode(), put.Result().Body())
	}
	assertNoQQSecrets(t, put.Result().Body())

	get := ut.PerformRequest(server.Engine().Engine, http.MethodGet, "/v1/config/qq-onebot", nil,
		ut.Header{Key: "Authorization", Value: "Bearer core-token"},
	)
	if get.Result().StatusCode() != http.StatusForbidden {
		t.Fatalf("GET status = %d body=%s", get.Result().StatusCode(), get.Result().Body())
	}
	assertNoQQSecrets(t, get.Result().Body())
	if strings.Contains(string(get.Result().Body()), "123") || strings.Contains(string(put.Result().Body()), "456") {
		t.Fatalf("HTTP config leaked allowlist: get=%s put=%s", get.Result().Body(), put.Result().Body())
	}
	settings, err := service.QQOneBotSettings()
	if err != nil {
		t.Fatal(err)
	}
	if len(settings.GroupAllowlist) != 1 || settings.GroupAllowlist[0] != "123" {
		t.Fatalf("file allowlist changed through HTTP: %#v", settings)
	}
}

func assertNoQQSecrets(t *testing.T, raw []byte) {
	t.Helper()
	var payload map[string]any
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &payload)
	}
	text := strings.ToLower(string(raw))
	for _, forbidden := range []string{"token", "authorization", "pmhq", "onebot-token", "core-token", "sk-live"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("response leaked forbidden %q: %s", forbidden, raw)
		}
	}
}
