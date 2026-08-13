package session

import (
	"strings"
	"testing"
)

func TestValidateDesktopObservationIDUsesPersistedEvidenceBoundary(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		wantErr bool
	}{
		{name: "ascii", value: "observation-42"},
		{name: "unicode boundary", value: strings.Repeat("界", MaxDesktopObservationIDRunes)},
		{name: "empty", wantErr: true},
		{name: "leading whitespace", value: " observation-42", wantErr: true},
		{name: "trailing whitespace", value: "observation-42 ", wantErr: true},
		{name: "control", value: "observation\n42", wantErr: true},
		{name: "invalid utf8", value: string([]byte{0xff}), wantErr: true},
		{name: "too long", value: strings.Repeat("界", MaxDesktopObservationIDRunes+1), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateDesktopObservationID(tt.value)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidateDesktopObservationID(%q) error = %v, wantErr %v", tt.value, err, tt.wantErr)
			}
		})
	}
}
