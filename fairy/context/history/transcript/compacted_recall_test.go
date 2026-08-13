package transcript

import (
	"math"
	"strings"
	"testing"
)

func TestValidateCompactedTranscriptRecall(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name           string
		conversationID string
		cutoff         uint64
		query          string
		limit          int
		want           string
		wantErr        bool
	}{
		{name: "trimmed", conversationID: "conversation", cutoff: 4, query: "  海边约定  ", limit: 5, want: "海边约定"},
		{name: "missing conversation", cutoff: 4, query: "海边", limit: 5, wantErr: true},
		{name: "untrimmed conversation", conversationID: " conversation ", cutoff: 4, query: "海边", limit: 5, wantErr: true},
		{name: "missing query", conversationID: "conversation", cutoff: 4, limit: 5, wantErr: true},
		{name: "control character", conversationID: "conversation", cutoff: 4, query: "海\n边", limit: 5, wantErr: true},
		{name: "long query", conversationID: "conversation", cutoff: 4, query: strings.Repeat("海", MaxTranscriptRecallQueryRunes+1), limit: 5, wantErr: true},
		{name: "limit zero", conversationID: "conversation", cutoff: 4, query: "海边", limit: 0, wantErr: true},
		{name: "limit exceeded", conversationID: "conversation", cutoff: 4, query: "海边", limit: MaxCompactedTranscriptTurns + 1, wantErr: true},
		{name: "cutoff exceeded", conversationID: "conversation", cutoff: math.MaxUint64, query: "海边", limit: 5, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := validateCompactedTranscriptRecall(test.conversationID, test.cutoff, test.query, test.limit)
			if (err != nil) != test.wantErr {
				t.Fatalf("validateCompactedTranscriptRecall() error = %v, wantErr %v", err, test.wantErr)
			}
			if got != test.want {
				t.Fatalf("validateCompactedTranscriptRecall() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestSearchCompactedTranscriptZeroCutoffDoesNotRequireDatabase(t *testing.T) {
	result, err := (&Store{}).SearchCompactedTranscript(t.Context(), "conversation", 0, "海边", 5)
	if err != nil {
		t.Fatalf("SearchCompactedTranscript() error = %v", err)
	}
	if result.Turns == nil || len(result.Turns) != 0 || result.Truncated {
		t.Fatalf("result = %#v", result)
	}
}

func TestSearchCompactedTranscriptRequiresContextAfterZeroCutoff(t *testing.T) {
	_, err := (&Store{}).SearchCompactedTranscript(nil, "conversation", 1, "海边", 5)
	if err == nil || err.Error() != "context is required" {
		t.Fatalf("SearchCompactedTranscript(nil context) error = %v, want context is required", err)
	}
}
