package knowledge

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

func ContainsDisallowedControl(value string) bool {
	for _, character := range value {
		if character == 0 || character < 32 && character != '\n' && character != '\r' && character != '\t' {
			return true
		}
	}
	return false
}

func validContentHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if character >= '0' && character <= '9' || character >= 'a' && character <= 'f' {
			continue
		}
		return false
	}
	return true
}
