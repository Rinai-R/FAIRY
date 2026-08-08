package transcript

import (
	"strings"
	"testing"

	historyprojection "fairy/context/history/projection"
)

func TestPromptProjectionCodecRoundTrip(t *testing.T) {
	state := historyprojection.State{
		Version: historyprojection.Version,
		Omissions: []historyprojection.Omission{{
			StartMessageSequence: 1, EndMessageSequence: 2,
			Reason: "memory_committed", MemoryID: "memory-1",
		}},
		RecentTailStartSequence: 3,
	}
	encoded, err := historyprojection.Encode(state)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := historyprojection.Decode(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Version != historyprojection.Version ||
		len(decoded.Omissions) != 1 ||
		decoded.Omissions[0].MemoryID != "memory-1" {
		t.Fatalf("decoded = %#v", decoded)
	}
}

func TestPromptProjectionCodecRejectsInvalidStates(t *testing.T) {
	tests := []struct {
		name    string
		encoded string
		want    string
	}{
		{name: "unknown field", encoded: `{"version":1,"omissions":[],"fallback":true}`, want: "unknown field"},
		{name: "unknown version", encoded: `{"version":2,"omissions":[]}`, want: "unsupported"},
		{name: "missing omissions", encoded: `{"version":1}`, want: "must be an array"},
		{name: "invalid reason", encoded: `{"version":1,"omissions":[{"segmentId":"x","reason":"expired"}]}`, want: "reason"},
		{name: "range crosses tail", encoded: `{"version":1,"omissions":[{"startMessageSequence":1,"endMessageSequence":2,"reason":"memory_committed","memoryId":"m"}],"recentTailStartSequence":2}`, want: "crosses recent tail"},
		{name: "trailing data", encoded: `{"version":1,"omissions":[]} {}`, want: "trailing data"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := historyprojection.Decode([]byte(test.encoded))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("DecodePromptProjection() error = %v", err)
			}
		})
	}
}

func TestApplyPromptProjectionOmitsOnlyActiveProjectionRows(t *testing.T) {
	messages := []MessageRecord{
		{ID: "one", Sequence: 1},
		{ID: "two", Sequence: 2},
		{ID: "three", Sequence: 3},
		{ID: "four", Sequence: 4},
	}
	got := applyPromptProjection(messages, historyprojection.State{
		Version: historyprojection.Version,
		Omissions: []historyprojection.Omission{{
			StartMessageSequence: 1, EndMessageSequence: 2,
			Reason: "memory_committed", MemoryID: "memory-1",
		}},
		RecentTailStartSequence: 3,
	})
	if len(got) != 2 || got[0].ID != "three" || got[1].ID != "four" {
		t.Fatalf("projected messages = %#v", got)
	}
	if len(messages) != 4 {
		t.Fatalf("complete transcript was mutated: %#v", messages)
	}
}
