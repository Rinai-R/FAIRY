package model

import "testing"

func testContinuationItem(content string) PromptItem {
	return PromptItem{Type: PromptItemUserMessage, Content: content}
}

func testContinuationRequest(modelName string, input []PromptItem) CompiledPromptRequest {
	return CompiledPromptRequest{
		Shape: ModelRequestShape{
			Lane:            PromptLaneRespond,
			Model:           modelName,
			Instructions:    "stable",
			MaxOutputTokens: 160,
			PromptCacheKey:  "fairy:c:respond",
		},
		Input: input,
	}
}

func completeContinuationState() ContinuationState {
	return ContinuationState{
		PreviousResponseID: "resp_1",
		PreviousRequest:    testContinuationRequest("model", []PromptItem{testContinuationItem("first")}),
		ResponseItems: []PromptItem{{
			Type:    PromptItemAssistantMessage,
			Content: "answer",
		}},
		ResponseComplete: true,
	}
}

func TestDecideContinuationAllowsOnlyNewSuffix(t *testing.T) {
	previous := completeContinuationState()
	current := testContinuationRequest("model", []PromptItem{
		testContinuationItem("first"),
		{Type: PromptItemAssistantMessage, Content: "answer"},
		testContinuationItem("second"),
	})
	decision := DecideContinuation(true, &previous, current)
	if !decision.Incremental || decision.PreviousResponseID != "resp_1" || len(decision.NewItems) != 1 || decision.NewItems[0].Content != "second" {
		t.Fatalf("decision = %#v", decision)
	}
}

func TestDecideContinuationUnsupportedUsesFullRequest(t *testing.T) {
	previous := completeContinuationState()
	current := testContinuationRequest("model", []PromptItem{testContinuationItem("first"), testContinuationItem("second")})
	decision := DecideContinuation(false, &previous, current)
	if decision.Incremental || decision.FullReason != ContinuationCapabilityUnsupported {
		t.Fatalf("decision = %#v", decision)
	}
}

func TestDecideContinuationDistinctFailureReasons(t *testing.T) {
	previous := completeContinuationState()
	if got := DecideContinuation(true, &previous, testContinuationRequest("other-model", []PromptItem{testContinuationItem("first"), testContinuationItem("second")})); got.FullReason != ContinuationRequestShapeChanged {
		t.Fatalf("shape change = %#v", got)
	}
	if got := DecideContinuation(true, &previous, testContinuationRequest("model", []PromptItem{testContinuationItem("rewritten"), testContinuationItem("second")})); got.FullReason != ContinuationPrefixMismatch {
		t.Fatalf("prefix mismatch = %#v", got)
	}
	incomplete := previous
	incomplete.ResponseComplete = false
	if got := DecideContinuation(true, &incomplete, testContinuationRequest("model", []PromptItem{testContinuationItem("rewritten")})); got.FullReason != ContinuationPreviousResponseIncomplete {
		t.Fatalf("incomplete = %#v", got)
	}
}

func TestDecideContinuationMissingStateAndNotExtended(t *testing.T) {
	current := testContinuationRequest("model", []PromptItem{testContinuationItem("first")})
	if got := DecideContinuation(true, nil, current); got.FullReason != ContinuationNoPreviousState {
		t.Fatalf("missing = %#v", got)
	}
	previous := completeContinuationState()
	same := testContinuationRequest("model", []PromptItem{
		testContinuationItem("first"),
		{Type: PromptItemAssistantMessage, Content: "answer"},
	})
	if got := DecideContinuation(true, &previous, same); got.FullReason != ContinuationInputNotExtended {
		t.Fatalf("not extended = %#v", got)
	}
}

func TestMaterializeContinuationRequest(t *testing.T) {
	current := testContinuationRequest("model", []PromptItem{
		testContinuationItem("first"),
		{Type: PromptItemAssistantMessage, Content: "answer"},
		testContinuationItem("second"),
	})
	materialized, err := MaterializeContinuationRequest(current, ContinuationDecision{
		Incremental:        true,
		PreviousResponseID: "resp_1",
		NewItems:           []PromptItem{testContinuationItem("second")},
	})
	if err != nil {
		t.Fatalf("MaterializeContinuationRequest() error = %v", err)
	}
	if materialized.PreviousResponseID != "resp_1" || len(materialized.Input) != 1 || materialized.Input[0].Content != "second" {
		t.Fatalf("materialized = %#v", materialized)
	}

	current.PreviousResponseID = "resp_stale"
	full, err := MaterializeContinuationRequest(current, fullContinuation(ContinuationCapabilityUnsupported))
	if err != nil {
		t.Fatalf("full materialize error = %v", err)
	}
	if full.PreviousResponseID != "" || len(full.Input) != 3 {
		t.Fatalf("full = %#v", full)
	}
}

func TestMaterializeContinuationRejectsInvalidState(t *testing.T) {
	current := testContinuationRequest("model", []PromptItem{testContinuationItem("first"), testContinuationItem("second")})
	if _, err := MaterializeContinuationRequest(current, ContinuationDecision{
		Incremental:        true,
		PreviousResponseID: "resp_1",
		NewItems:           nil,
	}); err == nil {
		t.Fatal("empty suffix must fail")
	}
	if _, err := MaterializeContinuationRequest(current, ContinuationDecision{
		Incremental:        true,
		PreviousResponseID: " resp_1",
		NewItems:           []PromptItem{testContinuationItem("second")},
	}); err == nil {
		t.Fatal("invalid response id must fail")
	}
}

func TestResponseIDFromEvents(t *testing.T) {
	if got := ResponseIDFromEvents([]StreamEvent{{Type: "text_delta", Data: "x"}, {Type: "completed", Data: " resp_9 "}}); got != "resp_9" {
		t.Fatalf("ResponseIDFromEvents() = %q", got)
	}
}
