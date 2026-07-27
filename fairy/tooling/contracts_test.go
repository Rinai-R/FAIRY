package tooling

import (
	"slices"
	"strings"
	"testing"

	domain "fairy/interaction"
	"fairy/model"
	"fairy/persona"
)

func TestToolSpecsForInteraction(t *testing.T) {
	private := domain.Resolved{Memory: domain.MemoryPersonal}
	public := domain.Resolved{
		Memory: domain.MemoryPublic,
		Facts:  domain.Facts{Initiation: domain.InitiationAmbient},
	}
	tests := []struct {
		name             string
		resolved         domain.Resolved
		webSearchEnabled bool
		want             []string
	}{
		{name: "private", resolved: private, want: []string{MemorySearch}},
		{name: "private with web", resolved: private, webSearchEnabled: true, want: []string{MemorySearch, WebSearch}},
		{name: "public", resolved: public, want: []string{PublicMemorySearch, SocialContextSearch, SocialExpressionSelect}},
		{name: "public with web", resolved: public, webSearchEnabled: true, want: []string{PublicMemorySearch, SocialContextSearch, SocialExpressionSelect, WebSearch}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := toolNames(ToolSpecsForInteraction(tt.webSearchEnabled, tt.resolved))
			if !slices.Equal(got, tt.want) {
				t.Fatalf("tool names = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestModelDrivenToolBudget(t *testing.T) {
	tests := []struct {
		name     string
		resolved domain.Resolved
		want     int
	}{
		{name: "private", resolved: domain.Resolved{Memory: domain.MemoryPersonal}, want: PrivateModelToolBudget},
		{
			name: "public ambient",
			resolved: domain.Resolved{
				Memory: domain.MemoryPublic,
				Facts:  domain.Facts{Initiation: domain.InitiationAmbient},
			},
			want: PublicModelToolBudget,
		},
		{name: "public direct", resolved: domain.Resolved{Memory: domain.MemoryPublic}, want: PrivateModelToolBudget},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ModelDrivenToolBudget(tt.resolved); got != tt.want {
				t.Fatalf("budget = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestInstructionsForInteraction(t *testing.T) {
	private := domain.Resolved{Memory: domain.MemoryPersonal}
	if got := InstructionsForInteraction(false, private); got != persona.RespondInstructions {
		t.Fatal("private instructions without tools changed")
	}
	if got := InstructionsForInteraction(true, private); got != RespondInstructionsAllowTools {
		t.Fatal("private instructions with tools changed")
	}

	public := domain.Resolved{
		Memory: domain.MemoryPublic,
		Facts:  domain.Facts{Initiation: domain.InitiationAmbient},
	}
	withoutTools := InstructionsForInteraction(false, public)
	withTools := InstructionsForInteraction(true, public)
	if withTools != withoutTools+RespondInstructionsAllowPublicTools {
		t.Fatal("public tool instructions are not an exact suffix")
	}
	for _, forbidden := range []string{"personal memories", "Preferred name is optional"} {
		if strings.Contains(withTools, forbidden) {
			t.Fatalf("public instructions contain %q", forbidden)
		}
	}
}

func TestParseQuery(t *testing.T) {
	maxQuery := strings.Repeat("界", MaxQueryRunes)
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{name: "trimmed", input: `{"query":"  hello  "}`, want: "hello"},
		{name: "exact rune limit", input: `{"query":"` + maxQuery + `"}`, want: maxQuery},
		{name: "empty input", input: "", wantErr: true},
		{name: "non object", input: "[]", wantErr: true},
		{name: "missing query", input: `{}`, wantErr: true},
		{name: "empty query", input: `{"query":""}`, wantErr: true},
		{name: "oversized", input: `{"query":"` + maxQuery + `x"}`, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseQuery(tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseQuery() error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("ParseQuery() = %q, want %q", got, tt.want)
			}
		})
	}
}

func toolNames(specs []model.ToolSpec) []string {
	names := make([]string, len(specs))
	for i, spec := range specs {
		names[i] = spec.Name
	}
	return names
}
