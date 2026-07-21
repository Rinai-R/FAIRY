package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"fairy/core"
	"fairy/coreclient"
	"fairy/observability"
)

func TestHelpExposesOnlySupportedSurface(t *testing.T) {
	root := NewRootCmd(testDependencies(&fakeClient{}))
	output := new(bytes.Buffer)
	root.SetOut(output)
	root.SetErr(output)
	root.SetArgs([]string{"--help"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"serve", "status", "doctor", "session", "turn", "events", "logs", "metrics", "config", "character", "profile", "db"} {
		if !strings.Contains(output.String(), name) {
			t.Fatalf("help missing %q:\n%s", name, output)
		}
	}
	for _, forbidden := range []string{"\n  shell ", "\n  sql ", "\n  api "} {
		if strings.Contains(strings.ToLower(output.String()), forbidden) {
			t.Fatalf("help contains forbidden surface %q", forbidden)
		}
	}
}

func TestFreshTreesKeepFlagsAndEnvironmentIsolated(t *testing.T) {
	t.Setenv("FAIRY_ENDPOINT", "http://127.0.0.1:9000")
	var configs []ConnectionConfig
	deps := testDependencies(&fakeClient{status: validStatus("/tmp")})
	deps.ClientFactory = func(config ConnectionConfig) (APIClient, error) {
		configs = append(configs, config)
		return &fakeClient{status: validStatus("/tmp")}, nil
	}
	for _, args := range [][]string{
		{"status", "--endpoint", "http://127.0.0.1:9100"},
		{"status"},
	} {
		root := NewRootCmd(deps)
		root.SetOut(io.Discard)
		root.SetErr(io.Discard)
		root.SetArgs(args)
		if err := root.ExecuteContext(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if configs[0].Endpoint != "http://127.0.0.1:9100" || configs[1].Endpoint != "http://127.0.0.1:9000" {
		t.Fatalf("configs = %#v", configs)
	}
}

func TestStatusOutputAndSecretWhitespace(t *testing.T) {
	client := &fakeClient{status: validStatus("/tmp/fairy")}
	deps := testDependencies(client)
	output := new(bytes.Buffer)
	root := NewRootCmd(deps)
	root.SetOut(output)
	root.SetErr(output)
	root.SetArgs([]string{"status"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `"configRoot":"/tmp/fairy"`) {
		t.Fatalf("output = %q", output.String())
	}

	deps.Getenv = func(key string) string {
		if key == "FAIRY_API_TOKEN" {
			return " secret "
		}
		return ""
	}
	root = NewRootCmd(deps)
	root.SetArgs([]string{"status"})
	if err := root.ExecuteContext(context.Background()); err == nil || !strings.Contains(err.Error(), "whitespace") {
		t.Fatalf("error = %v", err)
	}
}

func TestNoArgumentAndExplicitServeUseRunner(t *testing.T) {
	for _, args := range [][]string{nil, {"serve", "--addr", "127.0.0.1:9999"}} {
		var got core.Options
		deps := testDependencies(&fakeClient{})
		deps.Serve = func(ctx context.Context, options core.Options) error {
			got = options
			return nil
		}
		root := NewRootCmd(deps)
		root.SetArgs(args)
		if err := root.ExecuteContext(context.Background()); err != nil {
			t.Fatal(err)
		}
		if got.Addr == "" {
			t.Fatalf("args=%v options=%#v", args, got)
		}
	}
}

func TestLocalValidationDoesNotCreateClient(t *testing.T) {
	var factories int
	deps := testDependencies(&fakeClient{})
	deps.Stdin = strings.NewReader(`{"ok":true}`)
	deps.ClientFactory = func(config ConnectionConfig) (APIClient, error) {
		factories++
		return &fakeClient{}, nil
	}
	for _, args := range [][]string{
		{"session", "open", "--surface", "web_widget"},
		{"session", "open", "--surface", "im_group"},
		{"config", "delete", "web-search"},
	} {
		root := NewRootCmd(deps)
		root.SetArgs(args)
		if err := root.ExecuteContext(context.Background()); err == nil {
			t.Fatalf("args %v succeeded", args)
		}
	}
	if factories != 0 {
		t.Fatalf("client factories = %d", factories)
	}
}

func TestConfigApplyReadsStdinAndCapturesOutput(t *testing.T) {
	client := &fakeClient{raw: json.RawMessage(`{"configured":true}`)}
	deps := testDependencies(client)
	deps.Stdin = strings.NewReader(`{"model":"demo","apiKey":"secret"}`)
	output := new(bytes.Buffer)
	root := NewRootCmd(deps)
	root.SetOut(output)
	root.SetErr(output)
	root.SetArgs([]string{"config", "apply", "model", "--file", "-"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "secret") || !strings.Contains(output.String(), "configured") {
		t.Fatalf("output = %q", output.String())
	}
	if !strings.Contains(string(client.applied), "secret") {
		t.Fatalf("payload = %q", client.applied)
	}
}

func TestTurnSendWritesJSONLAndReturnsTerminalError(t *testing.T) {
	for _, state := range []string{"completed", "failed", "interrupted"} {
		t.Run(state, func(t *testing.T) {
			events := []coreclient.SSEEvent{{
				Event: state,
				Data:  mustJSON(coreclient.HarnessEvent{ConversationID: "c1", TurnID: "t1", Sequence: 1, State: state, Payload: json.RawMessage(`{}`)}),
			}}
			if state == "failed" {
				events = []coreclient.SSEEvent{
					{Event: "planning", Data: mustJSON(coreclient.HarnessEvent{ConversationID: "c1", TurnID: "t1", Sequence: 1, State: "planning", Payload: json.RawMessage(`{}`)})},
					{Event: "failed", Data: mustJSON(coreclient.HarnessEvent{ConversationID: "c1", TurnID: "t1", Sequence: 2, State: "failed", Payload: json.RawMessage(`{"error":"invalid provider reply"}`)})},
				}
			}
			client := &fakeClient{
				stream: &fakeStream{events: events},
				turn:   coreclient.SubmitTurnResponse{Outcome: coreclient.TurnOutcome{ConversationID: "c1", TurnID: "t1"}, Surface: "desktop"},
			}
			root := NewRootCmd(testDependencies(client))
			output := new(bytes.Buffer)
			root.SetOut(output)
			root.SetErr(output)
			root.SetArgs([]string{"turn", "send", "--conversation", "c1", "--input", "hello"})
			err := root.ExecuteContext(context.Background())
			if (state == "completed" && err != nil) || (state != "completed" && err == nil) {
				t.Fatalf("state=%s error=%v", state, err)
			}
			if !strings.Contains(output.String(), `"state":"`+state+`"`) {
				t.Fatalf("output = %q", output.String())
			}
			if state == "failed" && !strings.Contains(output.String(), `"state":"planning"`) {
				t.Fatalf("failed turn omitted planning event: %q", output.String())
			}
		})
	}
}

func TestLogsFollowClosesStreamAndReturnsCleanlyOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	closed := make(chan struct{})
	client := &fakeClient{
		openLogs: func(ctx context.Context, _ coreclient.LogQuery, _ time.Duration) (coreclient.EventStream, error) {
			return &blockingStream{ctx: ctx, started: started, closed: closed}, nil
		},
	}
	root := NewRootCmd(testDependencies(client))
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"logs", "--follow", "--level", "warn"})
	done := make(chan error, 1)
	go func() { done <- root.ExecuteContext(ctx) }()

	select {
	case <-started:
		cancel()
	case <-time.After(time.Second):
		t.Fatal("logs follow did not start reading")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("logs follow cancellation error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("logs follow did not return after cancellation")
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("logs follow did not close stream")
	}
}

func TestDoctorStoppedCoreWritesFailure(t *testing.T) {
	client := &fakeClient{statusErr: errors.New("connection refused")}
	root := NewRootCmd(testDependencies(client))
	output := new(bytes.Buffer)
	root.SetOut(output)
	root.SetErr(output)
	root.SetArgs([]string{"doctor", "--endpoint", "http://127.0.0.1:65534"})
	err := root.ExecuteContext(context.Background())
	if err == nil || !strings.Contains(err.Error(), "65534") || !strings.Contains(output.String(), `"status":"fail"`) {
		t.Fatalf("error=%v output=%q", err, output.String())
	}
}

func TestDoctorFailsRequiredInfrastructureDependency(t *testing.T) {
	client := &fakeClient{status: coreclient.Status{
		Database:  coreclient.DependencyStatus{Ready: true, Mode: "production"},
		Qdrant:    coreclient.DependencyStatus{Ready: false, Mode: "production", Error: "collection contract mismatch"},
		SecretKey: coreclient.DependencyStatus{Ready: true, Mode: "production"},
	}}
	report, err := runDoctor(context.Background(), client, "http://core.test")
	if err == nil || !strings.Contains(err.Error(), "collection contract mismatch") {
		t.Fatalf("runDoctor() error = %v", err)
	}
	var qdrant doctorCheck
	for _, check := range report.Checks {
		if check.Name == "qdrant" {
			qdrant = check
		}
	}
	if qdrant.Status != "fail" || qdrant.Detail != "collection contract mismatch" {
		t.Fatalf("qdrant check = %#v", qdrant)
	}
}

func testDependencies(client APIClient) Dependencies {
	return Dependencies{
		Getenv: func(string) string { return "" },
		Stdin:  strings.NewReader(""),
		ClientFactory: func(ConnectionConfig) (APIClient, error) {
			return client, nil
		},
		Serve: func(context.Context, core.Options) error { return nil },
	}
}

type fakeClient struct {
	status    coreclient.Status
	statusErr error
	raw       json.RawMessage
	applied   []byte
	stream    coreclient.EventStream
	openLogs  func(context.Context, coreclient.LogQuery, time.Duration) (coreclient.EventStream, error)
	turn      coreclient.SubmitTurnResponse
}

func (f *fakeClient) Status(context.Context) (coreclient.Status, error) { return f.status, f.statusErr }
func (f *fakeClient) OpenSession(context.Context, coreclient.OpenSessionRequest) (coreclient.OpenSessionResponse, error) {
	return coreclient.OpenSessionResponse{ConversationID: "c1"}, nil
}
func (f *fakeClient) SubmitTurn(context.Context, string, coreclient.SubmitTurnRequest) (coreclient.SubmitTurnResponse, error) {
	return f.turn, nil
}
func (f *fakeClient) CancelTurn(context.Context, string, string) error { return nil }
func (f *fakeClient) OpenEvents(context.Context, string, time.Duration) (coreclient.EventStream, error) {
	if f.stream == nil {
		return &fakeStream{}, nil
	}
	return f.stream, nil
}
func (f *fakeClient) GetConfig(context.Context, string) (json.RawMessage, error) {
	if f.raw == nil {
		return json.RawMessage(`{"configured":false}`), nil
	}
	return f.raw, nil
}
func (f *fakeClient) ApplyConfig(_ context.Context, _ string, payload []byte) (json.RawMessage, error) {
	f.applied = append([]byte(nil), payload...)
	return f.raw, nil
}
func (f *fakeClient) DeleteConfig(context.Context, string) (json.RawMessage, error) {
	return f.raw, nil
}
func (f *fakeClient) GetProfile(context.Context) (json.RawMessage, error) {
	return json.RawMessage(`{"revision":0}`), nil
}
func (f *fakeClient) ApplyProfile(context.Context, []byte) (json.RawMessage, error) {
	return f.raw, nil
}
func (f *fakeClient) DeleteProfile(context.Context) (json.RawMessage, error) { return f.raw, nil }
func (f *fakeClient) ListCharacters(context.Context) (coreclient.CharacterCatalog, error) {
	return coreclient.CharacterCatalog{Characters: []coreclient.CharacterRecord{}}, nil
}
func (f *fakeClient) CreateCharacter(context.Context, []byte) (json.RawMessage, error) {
	return f.raw, nil
}
func (f *fakeClient) ActivateCharacter(context.Context, string, uint64) (json.RawMessage, error) {
	return f.raw, nil
}
func (f *fakeClient) Logs(context.Context, coreclient.LogQuery) (coreclient.LogResponse, error) {
	return coreclient.LogResponse{Entries: []observability.LogEntry{}}, nil
}
func (f *fakeClient) OpenLogs(ctx context.Context, query coreclient.LogQuery, timeout time.Duration) (coreclient.EventStream, error) {
	if f.openLogs != nil {
		return f.openLogs(ctx, query, timeout)
	}
	return f.stream, nil
}
func (f *fakeClient) Metrics(context.Context) (coreclient.Metrics, error) {
	return coreclient.Metrics{}, nil
}

type fakeStream struct {
	mu     sync.Mutex
	events []coreclient.SSEEvent
	closed bool
}

type blockingStream struct {
	ctx     context.Context
	started chan struct{}
	closed  chan struct{}
	once    sync.Once
}

func (s *blockingStream) Next() (coreclient.SSEEvent, error) {
	s.once.Do(func() { close(s.started) })
	<-s.ctx.Done()
	return coreclient.SSEEvent{}, s.ctx.Err()
}

func (s *blockingStream) Close() error {
	close(s.closed)
	return nil
}

func (s *fakeStream) Next() (coreclient.SSEEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.events) == 0 {
		return coreclient.SSEEvent{}, io.EOF
	}
	event := s.events[0]
	s.events = s.events[1:]
	return event, nil
}

func (s *fakeStream) Close() error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	return nil
}

func mustJSON(value any) []byte {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return raw
}

func validStatus(root string) coreclient.Status {
	return coreclient.Status{
		Bootstrap: json.RawMessage(`{}`), ConfigRoot: root,
		WebSearch: json.RawMessage(`{}`), SemanticEmbedding: json.RawMessage(`{}`),
	}
}
