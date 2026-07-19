package semantic

import "errors"

// ErrUnavailable means semantic embedding/vector search is not configured.
var ErrUnavailable = errors.New("semantic retrieval unavailable")
