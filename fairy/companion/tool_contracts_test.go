package companion

import (
	"slices"
	"strings"
	"testing"

	"fairy/model"
	"fairy/persona"
	"fairy/session"
)

func TestToolSpecsForInteraction(t *testing.T) {
	private := session.Resolved{Memory: session.MemoryPersonal}
	public := session.Resolved{
		Memory: session.MemoryPublic,
		Facts:  session.Facts{Initiation: session.InitiationAmbient},
	}
	tests := []struct {
		name             string
		resolved         session.Resolved
		webSearchEnabled bool
		want             []string
	}{
		{name: "private", resolved: private, want: []string{toolMemorySearch}},
		{name: "private with web", resolved: private, webSearchEnabled: true, want: []string{toolMemorySearch, toolWebSearch}},
		{name: "public", resolved: public, want: []string{toolPublicMemorySearch, toolSocialContextSearch, toolSocialExpressionSelect}},
		{name: "public with web", resolved: public, webSearchEnabled: true, want: []string{toolPublicMemorySearch, toolSocialContextSearch, toolSocialExpressionSelect, toolWebSearch}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := toolNames(respondToolSpecsForInteraction(tt.webSearchEnabled, tt.resolved))
			if !slices.Equal(got, tt.want) {
				t.Fatalf("tool names = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestModelDrivenToolBudget(t *testing.T) {
	tests := []struct {
		name     string
		resolved session.Resolved
		want     int
	}{
		{name: "private", resolved: session.Resolved{Memory: session.MemoryPersonal}, want: privateModelToolBudget},
		{
			name: "public ambient",
			resolved: session.Resolved{
				Memory: session.MemoryPublic,
				Facts:  session.Facts{Initiation: session.InitiationAmbient},
			},
			want: publicModelToolBudget,
		},
		{name: "public direct", resolved: session.Resolved{Memory: session.MemoryPublic}, want: privateModelToolBudget},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := modelDrivenToolBudget(tt.resolved); got != tt.want {
				t.Fatalf("budget = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestInstructionsForInteraction(t *testing.T) {
	private := session.Resolved{Memory: session.MemoryPersonal}
	if got := respondInstructionsForInteraction(false, private); got != persona.RespondInstructions {
		t.Fatal("private instructions without tools changed")
	}
	if got := respondInstructionsForInteraction(true, private); got != respondInstructionsAllowTools {
		t.Fatal("private instructions with tools changed")
	}

	public := session.Resolved{
		Memory: session.MemoryPublic,
		Facts:  session.Facts{Initiation: session.InitiationAmbient},
	}
	withoutTools := respondInstructionsForInteraction(false, public)
	withTools := respondInstructionsForInteraction(true, public)
	if withTools != withoutTools+respondInstructionsAllowPublicTools {
		t.Fatal("public tool instructions are not an exact suffix")
	}
	for _, forbidden := range []string{"personal memories", "Preferred name is optional"} {
		if strings.Contains(withTools, forbidden) {
			t.Fatalf("public instructions contain %q", forbidden)
		}
	}
}

func TestParseQuery(t *testing.T) {
	maxQuery := strings.Repeat("界", maxToolQueryRunes)
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
			got, err := parseToolQuery(tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseToolQuery() error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("parseToolQuery() = %q, want %q", got, tt.want)
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
