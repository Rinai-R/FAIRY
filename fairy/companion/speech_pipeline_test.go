package companion

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
)

type recordingSynth struct {
	mu    sync.Mutex
	texts []string
}

func (r *recordingSynth) SynthesizeSpeech(req SpeechSynthesisRequest) (SpeechSynthesisResult, error) {
	r.mu.Lock()
	r.texts = append(r.texts, req.Text)
	r.mu.Unlock()
	return SpeechSynthesisResult{
		SpeakerID: "speaker",
		MimeType:  "audio/mpeg",
		Format:    "mp3",
		DataURL:   "data:audio/mpeg;base64," + req.Text,
	}, nil
}

func TestSpeechPipelineEmitsInEnqueueOrder(t *testing.T) {
	synth := &recordingSynth{}
	var mu sync.Mutex
	var results []speechPipelineResult
	pipeline := newSpeechPipeline(context.Background(), synth, 8, func(res speechPipelineResult) {
		mu.Lock()
		results = append(results, res)
		mu.Unlock()
	})

	// Utterance audio first (chainIndex = -1), then two reply chains.
	pipeline.Enqueue(speechPipelineJob{PlayIndex: 0, ChainIndex: chainIndexUtterance, Resolve: func() (string, error) { return "等我看看", nil }})
	pipeline.Enqueue(speechPipelineJob{PlayIndex: 1, ChainIndex: 0, Resolve: func() (string, error) { return "找到了", nil }})
	pipeline.Enqueue(speechPipelineJob{PlayIndex: 2, ChainIndex: 1, Resolve: func() (string, error) { return "就是这个", nil }})
	pipeline.Close()

	if len(results) != 3 {
		t.Fatalf("results = %d, want 3", len(results))
	}
	for i, res := range results {
		if res.PlayIndex != i {
			t.Fatalf("results[%d].PlayIndex = %d, want %d", i, res.PlayIndex, i)
		}
		if res.Err != nil || res.Skipped {
			t.Fatalf("results[%d] = %#v", i, res)
		}
	}
	if results[0].ChainIndex != chainIndexUtterance {
		t.Fatalf("first job chainIndex = %d, want utterance", results[0].ChainIndex)
	}
	if results[1].ChainIndex != 0 || results[2].ChainIndex != 1 {
		t.Fatalf("chain order = %d,%d", results[1].ChainIndex, results[2].ChainIndex)
	}
	if len(synth.texts) != 3 {
		t.Fatalf("synth calls = %d, want 3 (one per semantic unit)", len(synth.texts))
	}
	if synth.texts[0] != "等我看看" || synth.texts[1] != "找到了" || synth.texts[2] != "就是这个" {
		t.Fatalf("synth texts out of order: %#v", synth.texts)
	}
}

func TestSpeechPipelineSkipsEmptyResolve(t *testing.T) {
	synth := &recordingSynth{}
	var mu sync.Mutex
	var results []speechPipelineResult
	pipeline := newSpeechPipeline(context.Background(), synth, 4, func(res speechPipelineResult) {
		mu.Lock()
		results = append(results, res)
		mu.Unlock()
	})
	pipeline.Enqueue(speechPipelineJob{PlayIndex: 0, ChainIndex: chainIndexUtterance, Resolve: func() (string, error) { return "", nil }})
	pipeline.Close()
	if len(results) != 1 || !results[0].Skipped {
		t.Fatalf("results = %#v, want single skipped", results)
	}
	if len(synth.texts) != 0 {
		t.Fatalf("synth should not be called for empty text, got %#v", synth.texts)
	}
}

func TestSpeechPipelineDoesNotSynthesizeAfterCancel(t *testing.T) {
	var calls atomic.Int32
	synth := countingCancelSynth{calls: &calls}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var mu sync.Mutex
	var results []speechPipelineResult
	pipeline := newSpeechPipeline(ctx, synth, 4, func(res speechPipelineResult) {
		mu.Lock()
		results = append(results, res)
		mu.Unlock()
	})
	pipeline.Enqueue(speechPipelineJob{PlayIndex: 0, ChainIndex: 0, Resolve: func() (string, error) { return "取消后不应合成", nil }})
	pipeline.Close()
	if calls.Load() != 0 {
		t.Fatalf("synth called %d times after cancel, want 0", calls.Load())
	}
	if len(results) != 1 || results[0].Err == nil {
		t.Fatalf("results = %#v, want single error from cancelled ctx", results)
	}
}

type countingCancelSynth struct {
	calls *atomic.Int32
}

func (c countingCancelSynth) SynthesizeSpeech(req SpeechSynthesisRequest) (SpeechSynthesisResult, error) {
	c.calls.Add(1)
	return SpeechSynthesisResult{}, nil
}
