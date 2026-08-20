package seekdb

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Error is a SeekDB engine or SQL error. Number preserves MySQL-compatible errno
// so existing duplicate/deadlock checks keep working.
type Error struct {
	Number  uint16
	Message string
}

func (e *Error) Error() string {
	if e == nil {
		return "SeekDB error"
	}
	if e.Number == 0 {
		return e.Message
	}
	return fmt.Sprintf("SeekDB error %d: %s", e.Number, e.Message)
}

func IsDuplicate(err error) bool {
	var engine *Error
	if !asError(err, &engine) {
		return false
	}
	return engine.Number == 1062
}

func IsLockDeadlock(err error) bool {
	var engine *Error
	if !asError(err, &engine) {
		return false
	}
	return engine.Number == 1213
}

func IsErrno(err error, number uint16) bool {
	var engine *Error
	if !asError(err, &engine) {
		return false
	}
	return engine.Number == number
}

func classifyEngineError(number uint16, message string) uint16 {
	if number == 1146 || number == 1054 || number == 1062 || number == 1213 {
		return number
	}
	lower := strings.ToLower(message)
	switch {
	case strings.Contains(lower, "table doesn't exist"), strings.Contains(lower, "unknown table"):
		return 1146
	case strings.Contains(lower, "unknown column"):
		return 1054
	case strings.Contains(lower, "duplicate"):
		return 1062
	case strings.Contains(lower, "deadlock"):
		return 1213
	}
	if _, code, ok := strings.Cut(message, "ret="); ok {
		code = strings.TrimRight(strings.TrimSpace(code), ")")
		if parsed, err := strconv.Atoi(code); err == nil && parsed == -5019 {
			return 1146
		}
	}
	return number
}

func asError(err error, target **Error) bool {
	return errors.As(err, target)
}
