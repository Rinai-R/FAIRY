package config

import (
	"testing"

	"fairy/secret"
)

func TestConfigServiceSaveAndClearModelConnection(t *testing.T) {
	service := NewConfigService(t.TempDir(), secret.NewTestStore())
	apiKey := "sk-test-secret"
	status, err := service.SaveModelConnection(ModelConnectionInput{
		Protocol:            "chat_completions",
		Endpoint:            "https://api.deepseek.com",
		Model:               "deepseek-v4-flash",
		ContextWindowTokens: 1048576,
		AuthMode:            "bearer_key",
	}, &apiKey)
	if err != nil {
		t.Fatalf("SaveModelConnection() error = %v", err)
	}
	if !status.Configured || status.AuthMode != "bearer_key" {
		t.Fatalf("status = %#v", status)
	}
	status, err = service.ClearModelConnection()
	if err != nil {
		t.Fatalf("ClearModelConnection() error = %v", err)
	}
	if status.Configured {
		t.Fatalf("status = %#v", status)
	}
}
