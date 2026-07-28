package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"fairy/observability"

	"github.com/cloudwego/hertz/pkg/app"
)

func TestHandleLogStreamRejectsSubscriberCapacityBeforeSSE(t *testing.T) {
	store := observability.NewLogStore(2)
	unsubscribes := make([]func(), 0, observability.DefaultSubscriberCapacity)
	for index := 0; index < observability.DefaultSubscriberCapacity; index++ {
		_, _, unsubscribe, err := store.Subscribe(observability.LogFilter{})
		if err != nil {
			t.Fatal(err)
		}
		unsubscribes = append(unsubscribes, unsubscribe)
	}
	t.Cleanup(func() {
		for _, unsubscribe := range unsubscribes {
			unsubscribe()
		}
	})

	server := &Server{rt: &Dependencies{Logs: store}}
	var request app.RequestContext
	server.handleLogStream(context.Background(), &request)
	if got := request.Response.StatusCode(); got != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", got, http.StatusServiceUnavailable)
	}
	if body := string(request.Response.Body()); !strings.Contains(body, observability.ErrLogSubscriberCapacity.Error()) {
		t.Fatalf("body = %s", body)
	}
	if got := store.Stats().ActiveSubscribers; got != observability.DefaultSubscriberCapacity {
		t.Fatalf("active subscribers = %d, want %d", got, observability.DefaultSubscriberCapacity)
	}
}
