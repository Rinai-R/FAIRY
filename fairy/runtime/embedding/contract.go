package embedding

import (
	"context"
	"errors"
)

type SemanticStatus string

const (
	SemanticStatusUnavailable SemanticStatus = "unavailable"
	SemanticStatusReady       SemanticStatus = "ready"
	SemanticStatusUsed        SemanticStatus = "used"
)

var (
	ErrSemanticUnavailable             = errors.New("semantic retrieval unavailable")
	ErrSemanticCancellationUnsupported = errors.New("semantic embedder does not support context cancellation")
)

type SemanticEmbedder interface {
	Ready() bool
	Status() SemanticStatus
	ModelID() string
	Embed(texts []string) ([][]float32, error)
	Dims() int
}

// ContextSemanticEmbedder is the cancellable form required by bounded
// background work. Legacy in-process embedders can continue to implement
// SemanticEmbedder for synchronous call sites, but must fail closed before a
// background owner invokes them.
type ContextSemanticEmbedder interface {
	SemanticEmbedder
	EmbedContext(context.Context, []string) ([][]float32, error)
}

type UnavailableSemanticEmbedder struct{}

func (UnavailableSemanticEmbedder) Ready() bool            { return false }
func (UnavailableSemanticEmbedder) Status() SemanticStatus { return SemanticStatusUnavailable }
func (UnavailableSemanticEmbedder) ModelID() string        { return "" }
func (UnavailableSemanticEmbedder) Dims() int              { return 0 }
func (UnavailableSemanticEmbedder) Embed([]string) ([][]float32, error) {
	return nil, ErrSemanticUnavailable
}

func (UnavailableSemanticEmbedder) EmbedContext(ctx context.Context, _ []string) ([][]float32, error) {
	if ctx == nil {
		return nil, errors.New("embedding context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return nil, ErrSemanticUnavailable
}
