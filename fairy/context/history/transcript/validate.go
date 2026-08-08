package transcript

import (
	"fmt"
	"strings"
)

func ValidateID(label, value string) error {
	if value == "" || strings.TrimSpace(value) != value || ContainsDisallowedControl(value) {
		return fmt.Errorf("%s is invalid", label)
	}
	return nil
}

func ValidateContent(label, value string) error {
	if value == "" || strings.TrimSpace(value) == "" || ContainsDisallowedControl(value) {
		return fmt.Errorf("%s is invalid", label)
	}
	return nil
}

func ContainsDisallowedControl(value string) bool {
	for _, character := range value {
		if character == 0 || character < 32 && character != '\n' && character != '\r' && character != '\t' {
			return true
		}
	}
	return false
}

func databaseInt64(label string, value uint64) (int64, error) {
	if value > uint64(1<<63-1) {
		return 0, fmt.Errorf("%s exceeds database integer range", label)
	}
	return int64(value), nil
}
