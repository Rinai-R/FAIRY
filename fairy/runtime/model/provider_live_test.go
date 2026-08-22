//go:build live

package model

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"fairy/runtime/config"
)

func TestLiveEndpointChatUsesExplicitThirdPartyProvider(t *testing.T) {
	protocol := strings.TrimSpace(os.Getenv("FAIRY_CHAT_TEST_PROTOCOL"))
	endpoint := strings.TrimSpace(os.Getenv("FAIRY_CHAT_TEST_BASE_URL"))
	modelName := strings.TrimSpace(os.Getenv("FAIRY_CHAT_TEST_MODEL"))
	apiKey := strings.TrimSpace(os.Getenv("FAIRY_CHAT_TEST_API_KEY"))
	if endpoint == "" && modelName == "" && apiKey == "" {
		t.Skip("no explicit third-party live chat credential")
	}
	if endpoint == "" || modelName == "" || apiKey == "" {
		t.Fatal("live chat smoke requires FAIRY_CHAT_TEST_BASE_URL, FAIRY_CHAT_TEST_MODEL, and FAIRY_CHAT_TEST_API_KEY together")
	}
	if protocol == "" {
		protocol = string(ProtocolChatCompletions)
	}
	if err := config.ValidateEndpointStrictProviderURL(endpoint); err != nil {
		t.Fatalf("live chat endpoint is not a third-party endpoint-strict provider: %v", err)
	}

	root := t.TempDir()
	secrets := config.NewTestSecretStore()
	status, err := config.NewConfigService(root, secrets).SaveModelConnection(config.ModelConnectionInput{
		Protocol:            protocol,
		Endpoint:            endpoint,
		Model:               modelName,
		ContextWindowTokens: 32768,
		AuthMode:            "bearer_key",
	}, &apiKey)
	if err != nil {
		t.Fatalf("save explicit third-party chat settings: %v", err)
	}
	if !status.Ready || !status.CredentialConfigured {
		t.Fatalf("saved third-party chat status is not ready: configured=%v ready=%v credential=%v", status.Configured, status.Ready, status.CredentialConfigured)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	events, err := NewEndpointModelService(root, secrets).ExecuteRequestContext(ctx, CompiledPromptRequest{
		Shape: ModelRequestShape{
			Lane:            PromptLaneRespond,
			Model:           modelName,
			Instructions:    `Output only one JSON object with this exact schema: {"chains":[{"visualState":"idle","text":"OK"}]}. Do not use Markdown or add fields.`,
			MaxOutputTokens: 512,
		},
		Input: []PromptItem{{Type: PromptItemUserMessage, Content: "Return the requested strict probe reply."}},
	})
	if err != nil {
		t.Fatal(err)
	}
	completed := false
	for _, event := range events {
		if event.Type == "failed" {
			t.Fatal("third-party chat provider emitted a failed event")
		}
		if event.Type == "completed" {
			completed = true
		}
	}
	if !completed {
		t.Fatal("third-party chat provider did not complete the stream")
	}
	var reply struct {
		Chains []struct {
			VisualState string `json:"visualState"`
			Text        string `json:"text"`
		} `json:"chains"`
	}
	text := CollectTextFromEvents(events)
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&reply); err != nil {
		t.Fatalf("third-party chat provider did not return the strict reply contract: %v (text_bytes=%d events=%s)", err, len(text), liveEventShape(events))
	}
	if len(reply.Chains) != 1 || reply.Chains[0].VisualState != "idle" || strings.TrimSpace(reply.Chains[0].Text) == "" {
		t.Fatal("third-party chat provider returned an invalid strict reply")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		t.Fatalf("third-party chat provider returned trailing content: %v", err)
	}
	t.Logf("third-party chat protocol=%s model=%s events=%d", protocol, modelName, len(events))
}

func liveEventShape(events []StreamEvent) string {
	shape := make([]string, 0, len(events))
	for _, event := range events {
		shape = append(shape, fmt.Sprintf("%s:%d:%s", event.Type, len(event.Data), event.FinishReason))
	}
	return strings.Join(shape, ",")
}
