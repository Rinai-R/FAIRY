package coalescer

import (
	"sync/atomic"
	"testing"
)

func TestOwnerCoalescesBusyStartsIntoOnePendingRerun(t *testing.T) {
	var owner Owner
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32

	done := make(chan bool, 1)
	go func() {
		if !owner.Start() {
			t.Error("first start should own")
			done <- false
			return
		}
		for {
			if calls.Add(1) == 1 {
				close(started)
				<-release
			}
			if !owner.Finish() {
				done <- true
				return
			}
		}
	}()
	<-started
	if owner.Start() {
		t.Fatal("busy start should coalesce")
	}
	if owner.Start() {
		t.Fatal("second busy start should still coalesce")
	}
	active, pending := owner.Snapshot()
	if !active || !pending {
		t.Fatalf("snapshot active=%v pending=%v", active, pending)
	}
	close(release)
	if ok := <-done; !ok {
		t.Fatal("owner loop failed")
	}
	if calls.Load() != 2 {
		t.Fatalf("calls = %d, want 2", calls.Load())
	}
	active, pending = owner.Snapshot()
	if active || pending {
		t.Fatalf("finished snapshot active=%v pending=%v", active, pending)
	}
}

func TestOwnerAbortClearsPending(t *testing.T) {
	var owner Owner
	if !owner.Start() {
		t.Fatal("start")
	}
	if owner.Start() {
		t.Fatal("should coalesce")
	}
	owner.Abort()
	active, pending := owner.Snapshot()
	if active || pending {
		t.Fatalf("aborted snapshot active=%v pending=%v", active, pending)
	}
	if !owner.Start() {
		t.Fatal("restart after abort")
	}
	owner.Abort()
}
