package embedding

import "errors"

type SemanticStatus string

const (
	SemanticStatusUnavailable SemanticStatus = "unavailable"
	SemanticStatusReady       SemanticStatus = "ready"
	SemanticStatusUsed        SemanticStatus = "used"
)

var ErrSemanticUnavailable = errors.New("semantic retrieval unavailable")

type SemanticEmbedder interface {
	Ready() bool
	Status() SemanticStatus
	ModelID() string
	Embed(texts []string) ([][]float32, error)
	Dims() int
}

type UnavailableSemanticEmbedder struct{}

func (UnavailableSemanticEmbedder) Ready() bool            { return false }
func (UnavailableSemanticEmbedder) Status() SemanticStatus { return SemanticStatusUnavailable }
func (UnavailableSemanticEmbedder) ModelID() string        { return "" }
func (UnavailableSemanticEmbedder) Dims() int              { return 0 }
func (UnavailableSemanticEmbedder) Embed([]string) ([][]float32, error) {
	return nil, ErrSemanticUnavailable
}
