package transcript

import (
	"strings"
	"testing"
)

func TestValidateOptionalMessageIDUsesExactSafeUTF8Boundary(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		wantErr bool
	}{
		{name: "omitted"},
		{name: "ascii", value: "message-42"},
		{name: "unicode boundary", value: strings.Repeat("界", 128)},
		{name: "leading whitespace", value: " message-42", wantErr: true},
		{name: "trailing whitespace", value: "message-42 ", wantErr: true},
		{name: "control", value: "message\n42", wantErr: true},
		{name: "invalid utf8", value: string([]byte{0xff}), wantErr: true},
		{name: "too long", value: strings.Repeat("界", 129), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateOptionalMessageID(tt.value)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidateOptionalMessageID(%q) error = %v, wantErr %v", tt.value, err, tt.wantErr)
			}
		})
	}
}

func TestValidateEvidenceIDUsesExactSafeUTF8Boundary(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		wantErr bool
	}{
		{name: "ascii", value: "observation-42"},
		{name: "unicode boundary", value: strings.Repeat("界", 128)},
		{name: "empty", wantErr: true},
		{name: "leading whitespace", value: " observation-42", wantErr: true},
		{name: "trailing whitespace", value: "observation-42 ", wantErr: true},
		{name: "control", value: "observation\n42", wantErr: true},
		{name: "invalid utf8", value: string([]byte{0xff}), wantErr: true},
		{name: "too long", value: strings.Repeat("界", 129), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateEvidenceID(tt.value)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidateEvidenceID(%q) error = %v, wantErr %v", tt.value, err, tt.wantErr)
			}
		})
	}
}
