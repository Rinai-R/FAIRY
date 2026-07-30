package companion

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"fairy/memory"
)

func TestRetentionEngineBoundsConcurrentJobs(t *testing.T) {
	engine := newRetentionEngineWithCapacity(nil, 2, 1)
	defer engine.close()
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
	if err := engine.run(job); err != nil {
		t.Fatal(err)
	}
	if err := engine.run(job); err != nil {
		t.Fatal(err)
	}
	if err := engine.run(job); !errors.Is(err, errRetentionOverloaded) {
		t.Fatalf("third admission error = %v", err)
	}
	<-started
	<-started
	close(release)
	engine.close()
	if maximum.Load() != 2 {
		t.Fatalf("maximum active jobs = %d", maximum.Load())
	}
}

func TestKnowledgeLearningAdmissionIsBoundedAndNonBlocking(t *testing.T) {
	service := &CompanionService{}
	engine := &retentionEngine{
		service:        service,
		knowledgeQueue: make(chan memory.KnowledgeIngestTask, 1),
	}
	first := memory.KnowledgeIngestTask{ID: "first"}
	second := memory.KnowledgeIngestTask{ID: "second"}
	started := time.Now()
	engine.admitKnowledgeTasks([]memory.KnowledgeIngestTask{first, second})
	if time.Since(started) > 100*time.Millisecond {
		t.Fatal("knowledge admission blocked")
	}
	if len(engine.knowledgeQueue) != 1 || (<-engine.knowledgeQueue).ID != "first" {
		t.Fatal("knowledge queue did not retain the first task")
	}
	service.backgroundErrorMu.Lock()
	backgroundErr := service.backgroundError
	service.backgroundErrorMu.Unlock()
	if !errors.Is(backgroundErr, errRetentionOverloaded) {
		t.Fatalf("background error = %v", backgroundErr)
	}
}

func TestRetentionEngineCloseRejectsNewWork(t *testing.T) {
	engine := newRetentionEngineWithCapacity(nil, 1, 1)
	engine.close()
	if err := engine.run(func() {}); !errors.Is(err, errRetentionClosed) {
		t.Fatalf("run after close error = %v", err)
	}
}
