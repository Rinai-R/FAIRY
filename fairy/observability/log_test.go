package observability

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestLogStoreOverflowAndQuery(t *testing.T) {
	store := NewLogStore(2)
	for _, message := range []string{"one", "two", "three", "four"} {
		store.Append(EntryInput{Level: "info", Logger: "companion.turn", Message: message})
	}
	snapshot := store.Query(LogFilter{})
	if len(snapshot.Entries) != 2 || snapshot.Entries[0].Sequence != 3 || snapshot.Entries[1].Sequence != 4 {
		t.Fatalf("entries = %#v", snapshot.Entries)
	}
	if snapshot.DroppedEntries != 2 || snapshot.LatestSequence != 4 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}

func TestLogEntryWithoutFieldsUsesJSONArray(t *testing.T) {
	store := NewLogStore(2)
	entry := store.Append(EntryInput{Level: "info", Message: "empty fields"})
	raw, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"fields":null`) || !strings.Contains(string(raw), `"fields":[]`) {
		t.Fatalf("wire entry = %s", raw)
	}
}

func TestLogStoreRedactsAndBoundsPublicFields(t *testing.T) {
	store := NewLogStore(2)
	fields := []FieldInput{
		{Key: "apiKey", Value: "sk-test"},
		{Key: "model", Value: "Authorization: Bearer abc"},
	}
	for i := len(fields); i < MaxFields+1; i++ {
		fields = append(fields, FieldInput{Key: "field", Value: "value"})
	}
	entry := store.Append(EntryInput{
		Level: "warn", Logger: strings.Repeat("l", MaxLoggerRunes+1),
		Message: "provider failed api_key=sk-inline Authorization: Bearer message-secret " + strings.Repeat("界", MaxMessageRunes),
		Fields:  fields,
	})
	if strings.Contains(entry.Message, "sk-inline") || strings.Contains(entry.Message, "message-secret") {
		t.Fatalf("message leaked credential: %q", entry.Message)
	}
	if !entry.MessageTruncated || !entry.FieldsTruncated || len(entry.Fields) != MaxFields {
		t.Fatalf("bounds not applied: %#v", entry)
	}
	if entry.Fields[0].Value != RedactedValue {
		t.Fatalf("apiKey = %q", entry.Fields[0].Value)
	}
	if strings.Contains(entry.Fields[1].Value, "abc") {
		t.Fatalf("field leaked credential: %q", entry.Fields[1].Value)
	}
}

func TestLogStoreSubscribeBacklogLiveAndSlowDisconnect(t *testing.T) {
	store := newLogStore(10, 1)
	store.Append(EntryInput{Level: "info", Logger: "companion", Message: "backlog"})
	backlog, ch, unsubscribe := store.Subscribe(LogFilter{MinimumLevel: "warn"})
	defer unsubscribe()
	if len(backlog) != 0 {
		t.Fatalf("backlog = %#v", backlog)
	}
	store.Append(EntryInput{Level: "warn", Logger: "companion", Message: "live"})
	store.Append(EntryInput{Level: "error", Logger: "companion", Message: "disconnect"})
	if stats := store.Stats(); stats.ActiveSubscribers != 0 || stats.SlowSubscriberDisconnects != 1 {
		t.Fatalf("stats = %#v", stats)
	}
	first, ok := <-ch
	if !ok || first.Message != "live" {
		t.Fatalf("first = %#v ok=%v", first, ok)
	}
	if _, ok := <-ch; ok {
		t.Fatal("slow subscriber channel remains open")
	}
}

func TestLogStoreCloseAndUnsubscribeAreIdempotent(t *testing.T) {
	store := NewLogStore(2)
	_, ch, unsubscribe := store.Subscribe(LogFilter{})
	store.Close()
	store.Close()
	unsubscribe()
	unsubscribe()
	if _, ok := <-ch; ok {
		t.Fatal("channel remains open")
	}
}

func TestLogStoreConcurrentAppendQuery(t *testing.T) {
	store := NewLogStore(2000)
	var wg sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				store.Append(EntryInput{Time: time.Now(), Level: "info", Message: "event"})
				_ = store.Query(LogFilter{Limit: 5})
			}
		}()
	}
	wg.Wait()
	if got := store.Query(LogFilter{}).LatestSequence; got != 800 {
		t.Fatalf("latest sequence = %d", got)
	}
}
