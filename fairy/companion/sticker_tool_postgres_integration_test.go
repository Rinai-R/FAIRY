//go:build integration

package companion

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"fairy/memory"
	"fairy/model"
	"fairy/session"
	"fairy/sticker"
)

type stickerToolIntegrationModel struct {
	requests []model.CompiledPromptRequest
}

type stickerExpressionIntegrationModel struct {
	requests []model.CompiledPromptRequest
}

type mixedStickerExpressionIntegrationModel struct {
	requests int
}

func (provider *mixedStickerExpressionIntegrationModel) ExecuteRequestContext(_ context.Context, _ model.CompiledPromptRequest) ([]model.StreamEvent, error) {
	provider.requests++
	if provider.requests == 1 {
		return []model.StreamEvent{{Type: "function_calls", FunctionCalls: []model.FunctionCall{{
			CallID: "sticker-call-mixed", Name: toolStickerSearch, Arguments: `{"query":"震惊"}`,
		}}}}, nil
	}
	return []model.StreamEvent{{Type: "text_delta", Data: `{"chains":[
		{"kind":"utterance","visualState":"idle","text":"前一句。"},
		{"kind":"sticker","visualState":"idle","stickerId":"sticker-1"},
		{"kind":"utterance","visualState":"idle","text":"后一句。"}
	]}`}}, nil
}

func (*mixedStickerExpressionIntegrationModel) ExecutePrompt(model.PromptLane, string, uint32, []model.PromptItem, string) ([]model.StreamEvent, error) {
	return nil, errors.New("unexpected ExecutePrompt")
}

func (provider *stickerExpressionIntegrationModel) ExecuteRequestContext(_ context.Context, request model.CompiledPromptRequest) ([]model.StreamEvent, error) {
	provider.requests = append(provider.requests, request)
	if len(provider.requests) == 1 {
		return []model.StreamEvent{{Type: "function_calls", FunctionCalls: []model.FunctionCall{{
			CallID: "sticker-call-final", Name: toolStickerSearch, Arguments: `{"query":"安静点头"}`,
		}}}}, nil
	}
	return []model.StreamEvent{{Type: "text_delta", Data: `{"chains":[{"kind":"sticker","visualState":"idle","stickerId":"sticker-1"}]}`}}, nil
}

func (*stickerExpressionIntegrationModel) ExecutePrompt(model.PromptLane, string, uint32, []model.PromptItem, string) ([]model.StreamEvent, error) {
	return nil, errors.New("unexpected ExecutePrompt")
}

func (provider *stickerToolIntegrationModel) ExecuteRequestContext(_ context.Context, request model.CompiledPromptRequest) ([]model.StreamEvent, error) {
	provider.requests = append(provider.requests, request)
	if len(provider.requests) == 1 {
		return []model.StreamEvent{{Type: "function_calls", FunctionCalls: []model.FunctionCall{{
			CallID: "sticker-call-1", Name: toolStickerSearch, Arguments: `{"query":"震惊又无语"}`,
		}}}}, nil
	}
	return companionIntegrationModel{chains: []ReplyChain{{VisualState: "idle", Text: "这也太离谱了。"}}}.ExecuteRequestContext(context.Background(), request)
}

func (*stickerToolIntegrationModel) ExecutePrompt(model.PromptLane, string, uint32, []model.PromptItem, string) ([]model.StreamEvent, error) {
	return nil, errors.New("unexpected ExecutePrompt")
}

func TestPostgresStickerSearchCandidatesStayInsideTurnIntegration(t *testing.T) {
	store, _, cleanup := openCompanionIntegrationStore(t)
	defer cleanup()
	bootstrap, err := store.OpenOrCreateCharacterConversation("character-sticker-tool")
	if err != nil {
		t.Fatal(err)
	}
	provider := &stickerToolIntegrationModel{}
	service := newCompanionIntegrationService(store, "character-sticker-tool", provider)
	mustBindDesktopInteraction(t, service, bootstrap.Conversation.ID)
	search := &stickerSearchStub{
		active: true,
		searchResult: []sticker.Candidate{{
			ID: "sticker-1", Description: "震惊又无语", Tags: []string{"震惊", "无语"}, MIMEType: "image/png",
		}},
	}
	AttachStickerSearch(service, search)

	outcome, err := service.SubmitCompiledTurn(SubmitCompiledTurnRequest{
		ConversationID:        bootstrap.Conversation.ID,
		Input:                 "这是什么情况",
		MaxOutputTokens:       160,
		AvailableVisualStates: []VisualState{{ID: "idle", Description: "idle"}},
		OutputCapabilities:    session.OutputCapabilities{Sticker: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if outcome.ResponseText != "这也太离谱了。" || len(provider.requests) != 2 || search.searchCalls != 1 {
		t.Fatalf("outcome = %#v, model calls = %d, search calls = %d", outcome, len(provider.requests), search.searchCalls)
	}
	if names := toolNames(provider.requests[0].Tools); names[len(names)-1] != toolStickerSearch {
		t.Fatalf("first request tools = %v", names)
	}
	var sawCall, sawResult bool
	for _, item := range provider.requests[1].Input {
		switch item.Type {
		case model.PromptItemToolCall:
			sawCall = item.ToolCallID == "sticker-call-1" && item.ToolName == toolStickerSearch
		case model.PromptItemToolResult:
			if item.ToolCallID == "sticker-call-1" && item.Parts != nil {
				text := (*item.Parts)[0].Text
				sawResult = strings.Contains(text, `"id":"sticker-1"`) &&
					strings.Contains(text, `"description":"震惊又无语"`) &&
					!strings.Contains(text, "image/png")
			}
		}
	}
	if !sawCall || !sawResult {
		t.Fatalf("second request lost turn-local sticker tool result: %#v", provider.requests[1].Input)
	}
}

func TestPostgresStickerDeliveryAcknowledgementControlsTerminalStateIntegration(t *testing.T) {
	for _, test := range []struct {
		name          string
		status        session.ExpressionDeliveryStatus
		errorMessage  string
		wantError     bool
		wantAssistant bool
	}{
		{name: "surface success", status: session.ExpressionDeliverySucceeded, wantAssistant: true},
		{name: "surface failure", status: session.ExpressionDeliveryFailed, errorMessage: "platform image send failed", wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, _, cleanup := openCompanionIntegrationStore(t)
			defer cleanup()
			bootstrap, err := store.OpenOrCreateCharacterConversation("character-sticker-delivery-" + strings.ReplaceAll(test.name, " ", "-"))
			if err != nil {
				t.Fatal(err)
			}
			provider := &stickerExpressionIntegrationModel{}
			service := newCompanionIntegrationService(store, bootstrap.Conversation.CharacterID, provider)
			mustBindDesktopInteraction(t, service, bootstrap.Conversation.ID)
			AttachStickerSearch(service, &stickerSearchStub{
				active: true,
				searchResult: []sticker.Candidate{{
					ID: "sticker-1", Description: "安静点头", Tags: []string{"点头"}, MIMEType: "image/png",
				}},
			})
			AttachEventEmitter(service, func(event session.Event) {
				var payload beatReadyPayload
				if json.Unmarshal(event.Payload, &payload) != nil || payload.Type != "beat.ready" ||
					payload.Part == nil || payload.Part.Kind != session.ExpressionSticker {
					return
				}
				if err := service.ReportExpressionDelivery(session.ExpressionDeliveryResult{
					ConversationID: event.ConversationID, TurnID: event.TurnID, BeatID: payload.BeatID,
					Status: test.status, ErrorMessage: test.errorMessage,
				}); err != nil {
					t.Errorf("ReportExpressionDelivery: %v", err)
				}
			})

			outcome, submitErr := service.SubmitCompiledTurn(SubmitCompiledTurnRequest{
				ConversationID:        bootstrap.Conversation.ID,
				Input:                 "嗯",
				MaxOutputTokens:       160,
				AvailableVisualStates: []VisualState{{ID: "idle", Description: "idle"}},
				OutputCapabilities:    session.OutputCapabilities{Sticker: true},
			})
			if (submitErr != nil) != test.wantError {
				t.Fatalf("outcome = %#v, error = %v", outcome, submitErr)
			}
			reloaded, err := store.LoadConversation(bootstrap.Conversation.ID)
			if err != nil {
				t.Fatal(err)
			}
			hasAssistant := len(reloaded.Messages) == 2 && reloaded.Messages[1].Role == "assistant"
			if hasAssistant != test.wantAssistant {
				t.Fatalf("messages = %#v", reloaded.Messages)
			}
			if test.wantAssistant {
				assistant := reloaded.Messages[1]
				if assistant.Content != "" || len(assistant.Parts) != 1 ||
					assistant.Parts[0].Kind != memory.ExpressionSticker ||
					assistant.Parts[0].Sticker.Description != "安静点头" {
					t.Fatalf("assistant = %#v", assistant)
				}
			}
		})
	}
}

func TestPostgresMixedExpressionsDeliverInCompiledOrderIntegration(t *testing.T) {
	store, _, cleanup := openCompanionIntegrationStore(t)
	defer cleanup()
	bootstrap, err := store.OpenOrCreateCharacterConversation("character-mixed-sticker-delivery")
	if err != nil {
		t.Fatal(err)
	}
	service := newCompanionIntegrationService(store, bootstrap.Conversation.CharacterID, &mixedStickerExpressionIntegrationModel{})
	mustBindDesktopInteraction(t, service, bootstrap.Conversation.ID)
	AttachStickerSearch(service, &stickerSearchStub{
		active: true,
		searchResult: []sticker.Candidate{{
			ID: "sticker-1", Description: "震惊", Tags: []string{"震惊"}, MIMEType: "image/gif",
		}},
	})
	var delivered []beatReadyPayload
	AttachEventEmitter(service, func(event session.Event) {
		var payload beatReadyPayload
		if json.Unmarshal(event.Payload, &payload) != nil || payload.Type != "beat.ready" ||
			payload.Kind != "final" || payload.Part == nil {
			return
		}
		delivered = append(delivered, payload)
		if payload.Part.Kind == session.ExpressionSticker {
			if err := service.ReportExpressionDelivery(session.ExpressionDeliveryResult{
				ConversationID: event.ConversationID, TurnID: event.TurnID, BeatID: payload.BeatID,
				Status: session.ExpressionDeliverySucceeded,
			}); err != nil {
				t.Errorf("ReportExpressionDelivery: %v", err)
			}
		}
	})
	outcome, err := service.SubmitCompiledTurn(SubmitCompiledTurnRequest{
		ConversationID:        bootstrap.Conversation.ID,
		Input:                 "按顺序表达",
		MaxOutputTokens:       160,
		AvailableVisualStates: []VisualState{{ID: "idle", Description: "idle"}},
		OutputCapabilities:    session.OutputCapabilities{Sticker: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if outcome.ResponseText != "前一句。\n后一句。" || len(delivered) != 3 {
		t.Fatalf("outcome = %#v, delivered = %#v", outcome, delivered)
	}
	wantKinds := []session.ExpressionKind{session.ExpressionUtterance, session.ExpressionSticker, session.ExpressionUtterance}
	for index, payload := range delivered {
		if payload.ChainIndex != index || payload.PublishedPrefixCount != index+1 || payload.Part.Kind != wantKinds[index] {
			t.Fatalf("delivered[%d] = %#v", index, payload)
		}
	}
}
