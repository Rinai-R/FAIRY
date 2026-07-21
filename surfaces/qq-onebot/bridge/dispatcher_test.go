package bridge

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/wdvxdr1123/ZeroBot/message"
)

type verifier struct{ self bool }

func (v verifier) IsSelfMessage(context.Context, string) (bool, error) { return v.self, nil }

func TestTriggerFiltersAllowlistSelfAndAtReply(t *testing.T) {
	allow := map[int64]struct{}{20001: {}}
	input, ok, err := TriggerInput(context.Background(), GroupEvent{ID: "m1", GroupID: 20001, UserID: 30001, SenderName: "小明", Message: message.ParseMessage([]byte(`[{"type":"at","data":{"qq":"10001"}},{"type":"text","data":{"text":" 你好 "}}]`))}, 10001, allow, nil)
	if err != nil || !ok || input != "小明：你好" {
		t.Fatalf("trigger=%q %v %v", input, ok, err)
	}
	if _, ok, err := TriggerInput(context.Background(), GroupEvent{ID: "m2", GroupID: 20002, UserID: 30001, Message: message.ParseMessage([]byte(`"[CQ:at,qq=10001] hi"`))}, 10001, allow, nil); err != nil || ok {
		t.Fatalf("non allowlist=%v %v", ok, err)
	}
	if _, ok, err := TriggerInput(context.Background(), GroupEvent{ID: "m3", GroupID: 20001, UserID: 10001, Message: message.ParseMessage([]byte(`"hello"`))}, 10001, allow, nil); err != nil || ok {
		t.Fatalf("self=%v %v", ok, err)
	}
	if _, ok, err := TriggerInput(context.Background(), GroupEvent{ID: "m4", GroupID: 20001, UserID: 30001, Message: message.ParseMessage([]byte(`[{"type":"reply","data":{"id":"7"}},{"type":"text","data":{"text":"问你"}}]`))}, 10001, allow, verifier{self: true}); err != nil || !ok {
		t.Fatalf("reply=%v %v", ok, err)
	}
}

func TestZeroBotCQAndArrayMessagesNormalizeEqually(t *testing.T) {
	array := message.ParseMessage([]byte(`[{"type":"at","data":{"qq":"10001"}},{"type":"text","data":{"text":" hello "}},{"type":"image","data":{"file":"ignored"}}]`))
	cq := message.ParseMessage([]byte(`"[CQ:at,qq=10001] hello [CQ:image,file=ignored]"`))
	arrayText, arrayMentions, _ := textAndTriggers(array)
	cqText, cqMentions, _ := textAndTriggers(cq)
	if arrayText != cqText || len(arrayMentions) != 1 || len(cqMentions) != 1 || arrayMentions[0] != cqMentions[0] {
		t.Fatalf("array=(%q,%v) cq=(%q,%v)", arrayText, arrayMentions, cqText, cqMentions)
	}
}

func TestDedupeEvictsOldestAtBound(t *testing.T) {
	d := NewDedupe(2048)
	for i := 0; i < 2048; i++ {
		if !d.Add(string(rune(i + 1))) {
			t.Fatal(i)
		}
	}
	if d.Add(string(rune(1))) {
		t.Fatal("duplicate accepted")
	}
	if !d.Add(string(rune(3000))) {
		t.Fatal("new id rejected")
	}
	if !d.Add(string(rune(1))) {
		t.Fatal("oldest id was not evicted")
	}
}

func TestLanesKeepGroupFIFOAndRejectSeventeenth(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	lanes := NewLanes(ctx, []string{"g1", "g2"}, 16)
	defer lanes.Close()
	var mu sync.Mutex
	order := []int{}
	gate := make(chan struct{})
	started := make(chan struct{})
	if err := lanes.Submit("g1", func(context.Context) { close(started); <-gate; mu.Lock(); order = append(order, -1); mu.Unlock() }); err != nil {
		t.Fatal(err)
	}
	<-started
	for i := 0; i < 16; i++ {
		n := i
		if err := lanes.Submit("g1", func(context.Context) { mu.Lock(); order = append(order, n); mu.Unlock() }); err != nil {
			t.Fatal(err)
		}
	}
	if err := lanes.Submit("g1", func(context.Context) {}); err != ErrLaneFull {
		t.Fatalf("overflow=%v", err)
	}
	close(gate)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		done := len(order) == 17
		mu.Unlock()
		if done {
			break
		}
		time.Sleep(time.Millisecond)
	}
	for i, n := range order {
		if n != i-1 {
			t.Fatalf("FIFO=%v", order)
		}
	}
}

func TestDifferentGroupLanesRunConcurrently(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	lanes := NewLanes(ctx, []string{"g1", "g2"}, 16)
	defer lanes.Close()
	started := make(chan string, 2)
	release := make(chan struct{})
	for _, group := range []string{"g1", "g2"} {
		group := group
		if err := lanes.Submit(group, func(context.Context) { started <- group; <-release }); err != nil {
			t.Fatal(err)
		}
	}
	seen := map[string]bool{}
	for len(seen) < 2 {
		select {
		case group := <-started:
			seen[group] = true
		case <-time.After(time.Second):
			t.Fatal("group lanes did not run concurrently")
		}
	}
	close(release)
}
