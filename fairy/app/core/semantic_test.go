package core

import (
	"testing"

	"fairy/runtime/config"
	"fairy/runtime/embedding"
	"fairy/runtime/model"
)

type semanticEmbedderPublisherRecorder struct {
	published []embedding.SemanticEmbedder
}

func (recorder *semanticEmbedderPublisherRecorder) ReplaceSemanticEmbedder(embedder embedding.SemanticEmbedder) {
	recorder.published = append(recorder.published, embedder)
}

func TestSemanticEmbeddingRuntimePublishesOnlyOnCommitAndDisables(t *testing.T) {
	secrets := config.NewTestSecretStore()
	credential, err := config.NewSecretValue("runtime-test-secret")
	if err != nil {
		t.Fatal(err)
	}
	settings := config.SiliconFlowSemanticEmbeddingDefaults()
	settings.ConnectionID = "semantic_embedding.runtime-test"
	if err := secrets.Save(settings.ConnectionID, credential); err != nil {
		t.Fatal(err)
	}
	publisher := &semanticEmbedderPublisherRecorder{}
	runtime := semanticEmbeddingRuntime{
		model: model.NewModelService(t.TempDir(), secrets),
		store: publisher,
	}

	commit, err := runtime.PrepareSemanticEmbedding(settings)
	if err != nil {
		t.Fatal(err)
	}
	if len(publisher.published) != 0 {
		t.Fatalf("prepare published %d embedders", len(publisher.published))
	}
	commit()
	if len(publisher.published) != 1 || publisher.published[0] == nil || !publisher.published[0].Ready() {
		t.Fatalf("commit published %#v", publisher.published)
	}
	runtime.DisableSemanticEmbedding()
	if len(publisher.published) != 2 || publisher.published[1] != nil {
		t.Fatalf("disable published %#v", publisher.published)
	}
}
