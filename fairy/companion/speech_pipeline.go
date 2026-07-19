package companion

import (
	"context"
	"sync"
)

// chainIndexUtterance marks TTS audio for a mid-ReAct utterance line. Such audio
// participates in playback order but must not drive reply-chain bubble reveal.
const chainIndexUtterance = -1

const (
	beatKindUtterance = "utterance"
	beatKindFinal     = "final"
)

// speechPipelineJob is one semantic unit of speech to synthesize, paired with
// the display text that must be revealed only after the job completes (齐套).
type speechPipelineJob struct {
	BeatID      string
	Kind        string
	PlayIndex   int
	ChainIndex  int
	DisplayText string
	VisualState string
	Reason      string
	// Resolve returns the final speakable text. It runs on the pipeline worker so
	// translation never blocks the ReAct loop. Returning ("", nil) skips synthesis
	// but still delivers beat.ready with display text only.
	Resolve func() (string, error)
}

// speechPipelineResult is handed to the sink for one job outcome.
type speechPipelineResult struct {
	BeatID      string
	Kind        string
	PlayIndex   int
	ChainIndex  int
	DisplayText string
	VisualState string
	Reason      string
	Text        string // speakable text (may differ from DisplayText after translate)
	Result      SpeechSynthesisResult
	Err         error
	// Skipped is true when synthesis produced no audio (empty speech text or nil
	// synthesizer). The sink still emits beat.ready with display text.
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
	base := speechPipelineResult{
		BeatID:      job.BeatID,
		Kind:        job.Kind,
		PlayIndex:   job.PlayIndex,
		ChainIndex:  job.ChainIndex,
		DisplayText: job.DisplayText,
		VisualState: job.VisualState,
		Reason:      job.Reason,
	}
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
// every beat.ready is emitted before the turn tears down.
func (p *speechPipeline) Close() {
	p.once.Do(func() { close(p.jobs) })
	<-p.done
}
