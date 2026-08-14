package ledger

import (
	"os"
	"strings"
	"testing"
)

func TestValidateToolExecutionMetadataRejectsUnsafeValues(t *testing.T) {
	valid := CompleteToolExecutionInput{
		ID: "execution-1", ConversationID: "conversation-1", TurnID: "turn-1", CallID: "call-1",
		ResultMediaType: "image/png", ResultWidth: 1280, ResultHeight: 720,
		ResultByteCount: 1024, ResultSHA256: strings.Repeat("a", 64),
	}
	if err := validateCompleteToolExecution(valid); err != nil {
		t.Fatalf("validateCompleteToolExecution() error = %v", err)
	}
	oversized := valid
	oversized.ResultByteCount = MaxToolEvidenceBytes + 1
	if err := validateCompleteToolExecution(oversized); err == nil {
		t.Fatal("oversized result accepted")
	}
	invalidHash := valid
	invalidHash.ResultSHA256 = "screen-content"
	if err := validateCompleteToolExecution(invalidHash); err == nil {
		t.Fatal("invalid result hash accepted")
	}
}

func TestCreateToolExecutionRequiresDesktopToolAndFutureDeadline(t *testing.T) {
	input := CreateToolExecutionInput{
		ConversationID: "conversation-1", TurnID: "turn-1", CallID: "call-1",
		ToolName: ToolNameDesktopObserve, DeadlineAtUnixMS: 101,
	}
	if err := validateCreateToolExecution(input, 100); err != nil {
		t.Fatalf("validateCreateToolExecution() error = %v", err)
	}
	input.ToolName = "memory_search"
	if err := validateCreateToolExecution(input, 100); err == nil {
		t.Fatal("unsupported tool accepted")
	}
	input.ToolName = ToolNameDesktopObserve
	input.DeadlineAtUnixMS = 100
	if err := validateCreateToolExecution(input, 100); err == nil {
		t.Fatal("expired deadline accepted")
	}
}

func TestRecoveredToolExecutionSettlementRemainsSetBased(t *testing.T) {
	implementation, err := os.ReadFile("tool_execution.go")
	if err != nil {
		t.Fatal(err)
	}
	api, err := os.ReadFile("tool_execution_api.go")
	if err != nil {
		t.Fatal(err)
	}
	seekdb, err := os.ReadFile("store_seekdb.go")
	if err != nil {
		t.Fatal(err)
	}
	production := string(implementation) + string(api) + string(seekdb)
	for _, forbidden := range []string{
		"ListRecoverableToolExecutions",
		"ListPendingToolExecutions",
		"listRecoverableToolExecutionsPostgres",
		"listPendingToolExecutionsPostgres",
		"make([]ToolExecutionRecord",
	} {
		if strings.Contains(production, forbidden) {
			t.Fatalf("production tool execution recovery contains full-history marker %q", forbidden)
		}
	}
	for _, required := range []string{
		"WITH recoverable AS MATERIALIZED",
		"failed_executions AS",
		"failed_turns AS",
		"SELECT COUNT(*) FROM failed_turns",
		"desktop capture was interrupted by Core restart",
		"desktop capture evidence was lost during Core restart",
		"INNER JOIN tool_executions execution",
		"error_code = 'core_restarted'",
		"DESKTOP_CAPTURE_RECOVERY_FAILED",
	} {
		if !strings.Contains(production, required) {
			t.Fatalf("set-based recovery is missing %q", required)
		}
	}
}
