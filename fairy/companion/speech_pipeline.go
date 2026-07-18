package companion

import (
	"context"
	"sync"
)

// chainIndexUtterance marks TTS audio for a mid-ReAct utterance line. Such audio
// participates in playback order but must not drive reply-chain bubble reveal.
const chainIndexUtterance = -1

// speechPipelineJob is one semantic unit of speech to synthesize.
type speechPipelineJob struct {
	// PlayIndex is the monotonic playback order across the whole turn (utterance
	// audio first, then reply chains). It becomes speech.synthesized.index.
	PlayIndex int
	// ChainIndex is the reply-chain index for chain audio, or chainIndexUtterance
	// for mid-ReAct utterance audio.
	ChainIndex int
	// Resolve returns the final speakable text. It runs on the pipeline worker so
	// translation never blocks the ReAct loop. Returning ("", nil) skips synthesis
	// silently (e.g. an utterance whose translation was unusable).
	Resolve func() (string, error)
}

// speechPipelineResult is handed to the sink for one job outcome.
type speechPipelineResult struct {
	PlayIndex  int
	ChainIndex int
	Text       string
	Result     SpeechSynthesisResult
	Err        error
	// Skipped is true when the job produced no audio and no error (empty resolved
	// text, or a nil synthesizer). The sink emits nothing for skipped jobs.
	Skipped bool
}

// speechPipeline synthesizes queued speech jobs on a single worker in enqueue
// order and hands each outcome to sink. One job = one SynthesizeSpeech call,
// which keeps the timbre stable within a semantic unit. Emission stays serial
// (via the sink using publishLife), replacing the former worker pool.
type speechPipeline struct {
	jobs chan speechPipelineJob
	done chan struct{}
	once sync.Once
}

// newSpeechPipeline starts the worker. Jobs already in the queue when ctx is
// cancelled are drained without synthesizing (reported as errors) so Close never
// blocks on a cancelled turn.
func newSpeechPipeline(ctx context.Context, synth SpeechSynthesizer, capacity int, sink func(speechPipelineResult)) *speechPipeline {
	if capacity < 1 {
		capacity = 1
	}
	p := &speechPipeline{
		jobs: make(chan speechPipelineJob, capacity),
		done: make(chan struct{}),
	}
	go func() {
		defer close(p.done)
		for job := range p.jobs {
			sink(runSpeechJob(ctx, synth, job))
		}
	}()
	return p
}

func runSpeechJob(ctx context.Context, synth SpeechSynthesizer, job speechPipelineJob) speechPipelineResult {
	base := speechPipelineResult{PlayIndex: job.PlayIndex, ChainIndex: job.ChainIndex}
	if err := ctx.Err(); err != nil {
		base.Err = err
		return base
	}
	text := ""
	if job.Resolve != nil {
		resolved, err := job.Resolve()
		if err != nil {
			base.Err = err
			return base
		}
		text = resolved
	}
	base.Text = text
	if text == "" || synth == nil {
		base.Skipped = true
		return base
	}
	result, err := synth.SynthesizeSpeech(SpeechSynthesisRequest{Text: text})
	base.Result = result
	base.Err = err
	return base
}

// Enqueue appends a job. Only the turn's main goroutine enqueues, so ordering is
// deterministic. The buffered channel is sized to the turn's max job count.
func (p *speechPipeline) Enqueue(job speechPipelineJob) {
	p.jobs <- job
}

// Close stops accepting jobs and blocks until the worker drains the queue, so
// every speech.synthesized is emitted before the turn tears down.
func (p *speechPipeline) Close() {
	p.once.Do(func() { close(p.jobs) })
	<-p.done
}
