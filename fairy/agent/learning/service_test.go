package learning

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"fairy/context/knowledge"
	"fairy/context/memory/extraction"
)

func TestRetentionEngineBoundsConcurrentJobs(t *testing.T) {
	engine := newWithCapacity(Options{}, 2, 1)
	defer engine.Close()
	release := make(chan struct{})
	started := make(chan struct{}, 2)
	var active atomic.Int64
	var maximum atomic.Int64
	job := func() {
		current := active.Add(1)
		started <- struct{}{}
		for {
			old := maximum.Load()
			if current <= old || maximum.CompareAndSwap(old, current) {
				break
			}
		}
		<-release
		active.Add(-1)
	}
	if err := engine.ScheduleCompaction(job); err != nil {
		t.Fatal(err)
	}
	if err := engine.ScheduleCompaction(job); err != nil {
		t.Fatal(err)
	}
	if err := engine.ScheduleCompaction(job); !errors.Is(err, ErrOverloaded) {
		t.Fatalf("third admission error = %v", err)
	}
	<-started
	<-started
	close(release)
	engine.Close()
	if maximum.Load() != 2 {
		t.Fatalf("maximum active jobs = %d", maximum.Load())
	}
}

func TestKnowledgeLearningAdmissionIsBoundedAndNonBlocking(t *testing.T) {
	var backgroundErr atomic.Value
	engine := &Service{
		options:        Options{ObserveError: func(err error) { backgroundErr.Store(err) }},
		knowledgeQueue: make(chan knowledge.IngestTask, 1),
	}
	first := knowledge.IngestTask{ID: "first"}
	second := knowledge.IngestTask{ID: "second"}
	started := time.Now()
	engine.AdmitKnowledgeTasks([]knowledge.IngestTask{first, second})
	if time.Since(started) > 100*time.Millisecond {
		t.Fatal("knowledge admission blocked")
	}
	if len(engine.knowledgeQueue) != 1 || (<-engine.knowledgeQueue).ID != "first" {
		t.Fatal("knowledge queue did not retain the first task")
	}
	observed, _ := backgroundErr.Load().(error)
	if !errors.Is(observed, ErrOverloaded) {
		t.Fatalf("background error = %v", observed)
	}
}

func TestRetentionEngineCloseRejectsNewWork(t *testing.T) {
	engine := newWithCapacity(Options{}, 1, 1)
	engine.Close()
	if err := engine.ScheduleCompaction(func() {}); !errors.Is(err, ErrClosed) {
		t.Fatalf("run after close error = %v", err)
	}
}

func TestRetentionEngineCloseCancelsExtractionBeforeCommitBarrier(t *testing.T) {
	engine := newWithCapacity(Options{}, 1, 1)
	started := make(chan struct{})
	finished := make(chan struct{})
	var commitCalls atomic.Int64
	var commitErr error
	if err := engine.runContext(func(ctx context.Context) {
		close(started)
		<-ctx.Done()
		commitErr = engine.commitExtractionMutations(ctx, "conversation-1", func() ([]extraction.MutationResult, error) {
			commitCalls.Add(1)
			return []extraction.MutationResult{{Status: "applied", MemoryID: "memory-1"}}, nil
		})
		close(finished)
	}); err != nil {
		t.Fatal(err)
	}
	<-started
	engine.Close()
	<-finished
	if !errors.Is(commitErr, ErrClosed) || commitCalls.Load() != 0 {
		t.Fatalf("commit after close = calls:%d error:%v", commitCalls.Load(), commitErr)
	}
	if engine.TakeCommittedCoverage("conversation-1") {
		t.Fatal("shutdown extraction published committed coverage")
	}
}

func TestRetentionCoverageSignalIsProcessLocalAndSingleConsumption(t *testing.T) {
	engine := newWithCapacity(Options{}, 1, 1)
	defer engine.Close()
	engine.coverageCommitted.Store("conversation-1", struct{}{})
	if !engine.TakeCommittedCoverage("conversation-1") {
		t.Fatal("committed coverage signal was not available")
	}
	if engine.TakeCommittedCoverage("conversation-1") {
		t.Fatal("committed coverage signal was consumed more than once")
	}
	if engine.TakeCommittedCoverage("conversation-2") {
		t.Fatal("unknown conversation reported committed coverage")
	}
}
