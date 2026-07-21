package main

import (
	"encoding/json"
	"testing"

	"fairy/coreclient"
)

func TestFinalBeatTextRequiresFinalKind(t *testing.T) {
	payload, _ := json.Marshal(map[string]any{"type": "beat.ready", "kind": "utterance", "displayText": "skip"})
	if _, ok := finalBeatText(coreclient.HarnessEvent{Payload: payload}); ok {
		t.Fatal("utterance accepted as final")
	}
	payload, _ = json.Marshal(map[string]any{"type": "beat.ready", "kind": "final", "displayText": "你好"})
	text, ok := finalBeatText(coreclient.HarnessEvent{Payload: payload})
	if !ok || text != "你好" {
		t.Fatalf("text=%q ok=%v", text, ok)
	}
}

func TestConfigValidationAndExactTokens(t *testing.T) {
	valid := Config{
		CoreEndpoint: "http://127.0.0.1:8787", CoreToken: " core-token ",
		OneBotWebhookEndpoint: "http://127.0.0.1:3002", OneBotAPIEndpoint: "http://127.0.0.1:3001",
		OneBotToken: " onebot-token ", GroupAllowlist: []string{"20001"},
	}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	invalid := []Config{
		{},
		{CoreEndpoint: "http://core.example.com", CoreToken: "x", OneBotWebhookEndpoint: "http://127.0.0.1:2", OneBotAPIEndpoint: "http://127.0.0.1:1", OneBotToken: "x", GroupAllowlist: []string{"2"}},
		{CoreEndpoint: "http://127.0.0.1:1", CoreToken: "x", OneBotWebhookEndpoint: "ws://127.0.0.1:2", OneBotAPIEndpoint: "http://127.0.0.1:1", OneBotToken: "x", GroupAllowlist: []string{"2"}},
		{CoreEndpoint: "http://127.0.0.1:1", CoreToken: "x", OneBotWebhookEndpoint: "http://example.com:2", OneBotAPIEndpoint: "http://127.0.0.1:1", OneBotToken: "x", GroupAllowlist: []string{"2"}},
		{CoreEndpoint: "http://127.0.0.1:1", CoreToken: "x", OneBotWebhookEndpoint: "http://127.0.0.1:2", OneBotAPIEndpoint: "http://127.0.0.1:1", OneBotToken: "x"},
	}
	for i, cfg := range invalid {
		if err := cfg.Validate(); err == nil {
			t.Fatalf("invalid config %d accepted", i)
		}
	}
}

func TestConfigFromEnvPreservesExactTokens(t *testing.T) {
	t.Setenv("FAIRY_CORE_ENDPOINT", "http://127.0.0.1:8787")
	t.Setenv("FAIRY_CORE_TOKEN", " core-token ")
	t.Setenv("FAIRY_ONEBOT_WEBHOOK_ENDPOINT", "http://127.0.0.1:3002")
	t.Setenv("FAIRY_ONEBOT_API_ENDPOINT", "http://127.0.0.1:3001")
	t.Setenv("FAIRY_ONEBOT_TOKEN", " onebot-token ")
	t.Setenv("FAIRY_ONEBOT_GROUP_ALLOWLIST", "20001,20002")
	cfg, err := configFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CoreToken != " core-token " || cfg.OneBotToken != " onebot-token " {
		t.Fatalf("tokens were changed: Core=%q OneBot=%q", cfg.CoreToken, cfg.OneBotToken)
	}
	if len(cfg.GroupAllowlist) != 2 {
		t.Fatalf("allowlist = %#v", cfg.GroupAllowlist)
	}
}
