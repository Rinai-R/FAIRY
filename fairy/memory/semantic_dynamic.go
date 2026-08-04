package memory

import "sync"

// DynamicSemanticEmbedder keeps the Store identity stable while allowing Core to
// publish a new immutable provider after a successful configuration mutation.
type DynamicSemanticEmbedder struct {
	mu      sync.RWMutex
	current SemanticEmbedder
}

func NewDynamicSemanticEmbedder(initial SemanticEmbedder) *DynamicSemanticEmbedder {
	return &DynamicSemanticEmbedder{current: semanticEmbedderSnapshot(initial)}
}

func (embedder *DynamicSemanticEmbedder) Replace(next SemanticEmbedder) {
	if embedder == nil {
		return
	}
	next = semanticEmbedderSnapshot(next)
	embedder.mu.Lock()
	embedder.current = next
	embedder.mu.Unlock()
}

func (embedder *DynamicSemanticEmbedder) Snapshot() SemanticEmbedder {
	if embedder == nil {
		return nil
	}
	embedder.mu.RLock()
	current := embedder.current
	embedder.mu.RUnlock()
	return current
}

func (embedder *DynamicSemanticEmbedder) Ready() bool {
	current := embedder.Snapshot()
	return current != nil && current.Ready()
}

func (embedder *DynamicSemanticEmbedder) Status() SemanticStatus {
	current := embedder.Snapshot()
	if current == nil {
		return SemanticStatusUnavailable
	}
	return current.Status()
}

func (embedder *DynamicSemanticEmbedder) ModelID() string {
	current := embedder.Snapshot()
	if current == nil {
		return ""
	}
	return current.ModelID()
}

func (embedder *DynamicSemanticEmbedder) Embed(texts []string) ([][]float32, error) {
	current := embedder.Snapshot()
	if current == nil {
		return nil, ErrSemanticUnavailable
	}
	return current.Embed(texts)
}

func (embedder *DynamicSemanticEmbedder) Dims() int {
	current := embedder.Snapshot()
	if current == nil {
		return 0
	}
	return current.Dims()
}

type semanticEmbedderSnapshotter interface {
	Snapshot() SemanticEmbedder
}

func semanticEmbedderSnapshot(embedder SemanticEmbedder) SemanticEmbedder {
	if snapshotter, ok := embedder.(semanticEmbedderSnapshotter); ok {
		return snapshotter.Snapshot()
	}
	return embedder
}
