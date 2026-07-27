package memory

import "errors"

// ErrSemanticUnavailable means semantic embedding/vector search is not configured.
var ErrSemanticUnavailable = errors.New("semantic retrieval unavailable")
