package async

import (
	"strings"
	"testing"
	"time"
)

func TestPoolTuneChangesCapacity(t *testing.T) {
	pool, err := NewPool(1)
	if err != nil {
		t.Fatalf("NewPool() error = %v", err)
	}
	defer pool.Release()

	if got := pool.Cap(); got != 1 {
		t.Fatalf("initial cap = %d, want 1", got)
	}
	if err := pool.Tune(3); err != nil {
		t.Fatalf("Tune() error = %v", err)
	}
	if got := pool.Cap(); got != 3 {
		t.Fatalf("cap after Tune = %d, want 3", got)
	}
}

func TestPoolTuneRejectsInvalidSize(t *testing.T) {
	pool, err := NewPool(1)
	if err != nil {
		t.Fatalf("NewPool() error = %v", err)
	}
	defer pool.Release()

	err = pool.Tune(0)
	if err == nil {
		t.Fatal("Tune(0) error = nil")
	}
	if !strings.Contains(err.Error(), "必须大于 0") {
		t.Fatalf("Tune(0) error = %q", err)
	}
}

func TestPoolMetricsReflectSubmittedTask(t *testing.T) {
	pool, err := NewPool(1)
	if err != nil {
		t.Fatalf("NewPool() error = %v", err)
	}
	defer pool.Release()

	release := make(chan struct{})
	done := make(chan struct{})
	if err := pool.Submit(func() {
		defer close(done)
		<-release
	}); err != nil {
		t.Fatalf("Submit() error = %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for pool.Running() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("pool.Running() stayed at 0")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if pool.Cap() != 1 {
		t.Fatalf("Cap() = %d, want 1", pool.Cap())
	}
	if pool.Free() > pool.Cap() {
		t.Fatalf("Free() = %d exceeds Cap() = %d", pool.Free(), pool.Cap())
	}
	if pool.Waiting() < 0 {
		t.Fatalf("Waiting() = %d, want non-negative", pool.Waiting())
	}
	close(release)
	<-done
}

func TestNilPoolTuneReturnsError(t *testing.T) {
	var pool *Pool
	err := pool.Tune(2)
	if err == nil {
		t.Fatal("nil Pool Tune() error = nil")
	}
	if !strings.Contains(err.Error(), "未初始化") {
		t.Fatalf("nil Pool Tune() error = %q", err)
	}
}
