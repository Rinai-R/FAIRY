package companion

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRuntimeBeatDeliveryLedgerMetadataContainsOnlyDiagnostics(t *testing.T) {
	metadata := runtimeBeatDeliveryLedgerMetadata("published", beatKindFinal, 1, 2, 920, 370, 2)
	raw, err := json.Marshal(metadata)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	wire := string(raw)
	for _, required := range []string{
		`"status":"published"`,
		`"kind":"final"`,
		`"chainIndex":1`,
		`"playIndex":2`,
		`"targetIntervalMs":920`,
		`"paceWaitMs":370`,
		`"publishedPrefixCount":2`,
	} {
		if !strings.Contains(wire, required) {
			t.Fatalf("metadata missing %s: %s", required, wire)
		}
	}
	for _, forbidden := range []string{"displayText", "speechText", "prompt", "Authorization", "Bearer"} {
		if strings.Contains(wire, forbidden) {
			t.Fatalf("metadata contains forbidden field %q: %s", forbidden, wire)
		}
	}
}
