package runtime

import "errors"

var ErrTurnNotFound = errors.New("turn does not belong to conversation")

func nullableString(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

func cloneStringPtr(value *string) *string {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}
