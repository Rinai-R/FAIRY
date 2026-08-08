package embedding

import (
	"errors"
	"sync"
	"testing"
)

func TestDynamicSemanticEmbedderPublishesImmutableSnapshots(t *testing.T) {
	first := &fixedSemanticEmbedder{ready: true, dims: Dimensions, modelID: "space-first", vectors: [][]float32{dynamicTestVector(1)}}
	second := &fixedSemanticEmbedder{ready: true, dims: Dimensions, modelID: "space-second", vectors: [][]float32{dynamicTestVector(2)}}
	dynamic := NewDynamicSemanticEmbedder(first)
	snapshot := Snapshot(dynamic)
	dynamic.Replace(second)
	if snapshot.ModelID() != "space-first" || dynamic.ModelID() != "space-second" {
		t.Fatalf("snapshot/current model IDs = %q/%q", snapshot.ModelID(), dynamic.ModelID())
	}
	value, err := ForContent(snapshot, "content")
	if err != nil {
		t.Fatal(err)
	}
	if value.ModelID != "space-first" || value.Vector.Slice()[0] != 1 {
		t.Fatalf("snapshot embedding = %#v", value)
	}
	dynamic.Replace(nil)
	if dynamic.Ready() || dynamic.ModelID() != "" || dynamic.Dims() != 0 || dynamic.Status() != SemanticStatusUnavailable {
		t.Fatalf("disabled dynamic embedder remained ready")
	}
	if _, err := dynamic.Embed([]string{"content"}); !errors.Is(err, ErrSemanticUnavailable) {
		t.Fatalf("disabled Embed() error = %v", err)
	}
	value, err = ForContent(dynamic, "content")
	if err != nil || value.Enabled() {
		t.Fatalf("disabled embeddingForContent() = %#v, %v", value, err)
	}
}

func dynamicTestVector(first float32) []float32 {
	vector := make([]float32, Dimensions)
	vector[0] = first
	return vector
}

type replacingSemanticEmbedder struct {
	modelID string
	vector  []float32
	replace func()
}

func (*replacingSemanticEmbedder) Ready() bool { return true }
func (*replacingSemanticEmbedder) Status() SemanticStatus {
	return SemanticStatusReady
}
func (embedder *replacingSemanticEmbedder) ModelID() string {
	return embedder.modelID
}
func (*replacingSemanticEmbedder) Dims() int { return Dimensions }
func (embedder *replacingSemanticEmbedder) Embed([]string) ([][]float32, error) {
	if embedder.replace != nil {
		embedder.replace()
	}
	return [][]float32{embedder.vector}, nil
}

func TestSnapshotKeepsEmbeddingSpaceDuringReplace(t *testing.T) {
	second := &fixedSemanticEmbedder{
		ready: true, dims: Dimensions, modelID: "space-second",
		vectors: [][]float32{dynamicTestVector(2)},
	}
	dynamic := NewDynamicSemanticEmbedder(nil)
	first := &replacingSemanticEmbedder{
		modelID: "space-first",
		vector:  dynamicTestVector(1),
		replace: func() { dynamic.Replace(second) },
	}
	dynamic.Replace(first)
	result, err := ForContent(Snapshot(dynamic), "snapshot query")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Enabled() || result.Vector.Slice()[0] != 1 || result.ModelID != "space-first" {
		t.Fatalf("query embedding = %#v", result)
	}
	if dynamic.ModelID() != "space-second" {
		t.Fatalf("current model ID = %q", dynamic.ModelID())
	}
}

func TestDynamicSemanticEmbedderConcurrentReplaceAndSnapshot(t *testing.T) {
	first := &fixedSemanticEmbedder{ready: true, dims: Dimensions, modelID: "space-first"}
	second := &fixedSemanticEmbedder{ready: true, dims: Dimensions, modelID: "space-second"}
	dynamic := NewDynamicSemanticEmbedder(first)
	var wait sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for iteration := 0; iteration < 1000; iteration++ {
				if iteration%2 == 0 {
					dynamic.Replace(first)
				} else {
					dynamic.Replace(second)
				}
				snapshot := Snapshot(dynamic)
				if snapshot == nil || (snapshot.ModelID() != "space-first" && snapshot.ModelID() != "space-second") {
					t.Errorf("unexpected snapshot: %#v", snapshot)
					return
				}
			}
		}()
	}
	wait.Wait()
}
