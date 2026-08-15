package wasm

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"fairy/plugin"
)

func TestLoadEchoGuestInitHandleShutdown(t *testing.T) {
	host, err := Open(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := host.Close(t.Context()); err != nil {
			t.Error(err)
		}
	})
	instance, err := host.Load(t.Context(), "echo", echoGuestWASM(), DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := instance.Close(t.Context()); err != nil && !errors.Is(err, ErrHostClosed) {
			t.Error(err)
		}
	})
	in, err := plugin.EncodeEnvelope(plugin.Envelope{
		ABIVersion:  plugin.ABIVersion,
		Kind:        "handle",
		Correlation: plugin.Correlation{PluginInstanceID: "echo-1"},
		Payload:     []byte(`{"ok":true}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if out, err := instance.Init(t.Context(), in); err != nil || len(out) != 0 {
		t.Fatalf("Init() = (%q, %v)", out, err)
	}
	out, err := instance.Handle(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out, in) {
		t.Fatalf("Handle() = %q, want echo of input", out)
	}
	if err := instance.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestLoadRejectsModuleWithoutABIExports(t *testing.T) {
	host, err := Open(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := host.Close(t.Context()); err != nil {
			t.Error(err)
		}
	})
	_, err = host.Load(t.Context(), "empty", emptyModule, DefaultBudget())
	if !errors.Is(err, plugin.ErrManifestInvalid) {
		t.Fatalf("Load(empty) = %v, want %v", err, plugin.ErrManifestInvalid)
	}
}

func TestHandleRejectsInputOverBudget(t *testing.T) {
	host, err := Open(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := host.Close(t.Context()); err != nil {
			t.Error(err)
		}
	})
	budget := DefaultBudget()
	budget.MaxInputBytes = 8
	instance, err := host.Load(t.Context(), "echo-in", echoGuestWASM(), budget)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.Close(t.Context()) })
	_, err = instance.Handle(t.Context(), bytes.Repeat([]byte("n"), 9))
	if !errors.Is(err, plugin.ErrBudgetExceeded) {
		t.Fatalf("Handle() = %v, want %v", err, plugin.ErrBudgetExceeded)
	}
	if _, err := instance.Handle(t.Context(), []byte("ok")); err != nil {
		t.Fatalf("Handle() after input budget rejection = %v", err)
	}
}

func TestHandleRejectsOutputOverBudget(t *testing.T) {
	host, err := Open(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := host.Close(t.Context()); err != nil {
			t.Error(err)
		}
	})
	budget := DefaultBudget()
	budget.MaxOutputBytes = 4
	instance, err := host.Load(t.Context(), "echo-out", echoGuestWASM(), budget)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.Close(t.Context()) })
	_, err = instance.Handle(t.Context(), []byte("hello-world"))
	if !errors.Is(err, plugin.ErrBudgetExceeded) {
		t.Fatalf("Handle() = %v, want %v", err, plugin.ErrBudgetExceeded)
	}
	if _, err := instance.Handle(t.Context(), []byte("ok")); err == nil {
		t.Fatal("poisoned instance accepted another call after output budget")
	}
}

func TestHandleRejectsCallBudget(t *testing.T) {
	host, err := Open(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := host.Close(t.Context()); err != nil {
			t.Error(err)
		}
	})
	budget := DefaultBudget()
	budget.MaxCalls = 1
	instance, err := host.Load(t.Context(), "echo-calls", echoGuestWASM(), budget)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.Close(t.Context()) })
	if _, err := instance.Handle(t.Context(), []byte("ok")); err != nil {
		t.Fatal(err)
	}
	_, err = instance.Handle(t.Context(), []byte("ok"))
	if !errors.Is(err, plugin.ErrBudgetExceeded) {
		t.Fatalf("second Handle() = %v, want %v", err, plugin.ErrBudgetExceeded)
	}
}

func TestLoadRejectsMemoryPagesOverHostLimit(t *testing.T) {
	budget := DefaultBudget()
	budget.MaxMemoryPages = 2
	host, err := open(t.Context(), budget)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := host.Close(t.Context()); err != nil {
			t.Error(err)
		}
	})
	_, err = host.Load(t.Context(), "fat", largeMemoryGuestWASM(8), budget)
	if err == nil {
		t.Fatal("Load() accepted a module larger than the memory page budget")
	}
}

func TestHandleRejectsMemoryGrowthOverBudget(t *testing.T) {
	host, err := Open(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := host.Close(t.Context()); err != nil {
			t.Error(err)
		}
	})
	budget := DefaultBudget()
	budget.MaxMemoryPages = 1
	instance, err := host.Load(t.Context(), "grow", growGuestWASM(), budget)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.Close(t.Context()) })
	_, err = instance.Handle(t.Context(), []byte("x"))
	if !errors.Is(err, plugin.ErrBudgetExceeded) && !errors.Is(err, plugin.ErrModuleTrap) {
		t.Fatalf("Handle() = %v, want memory page budget or trap", err)
	}
	if _, err := instance.Handle(t.Context(), []byte("x")); err == nil {
		t.Fatal("poisoned instance accepted another call after memory growth")
	}
}

func TestHandleCancelsInfiniteGuest(t *testing.T) {
	host, err := Open(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := host.Close(t.Context()); err != nil {
			t.Error(err)
		}
	})
	instance, err := host.Load(t.Context(), "spin", spinGuestWASM(), DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.Close(t.Context()) })
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	_, err = instance.Handle(ctx, []byte("x"))
	if !errors.Is(err, plugin.ErrCancelled) {
		t.Fatalf("Handle() = %v, want %v", err, plugin.ErrCancelled)
	}
	_, err = instance.Handle(t.Context(), []byte("x"))
	if err == nil {
		t.Fatal("poisoned instance accepted another call")
	}
}

func TestHandleSerializesConcurrentCalls(t *testing.T) {
	host, err := Open(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := host.Close(t.Context()); err != nil {
			t.Error(err)
		}
	})
	instance, err := host.Load(t.Context(), "echo-serial", echoGuestWASM(), DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.Close(t.Context()) })
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Go(func() {
			_, err := instance.Handle(t.Context(), []byte("ab"))
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestBudgetZeroIsInvalid(t *testing.T) {
	if err := (Budget{}).validate(); !errors.Is(err, ErrInvalidBudget) {
		t.Fatalf("validate() = %v", err)
	}
}

func TestGuestDiagnosticsDoNotEchoSecrets(t *testing.T) {
	host, err := Open(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := host.Close(t.Context()); err != nil {
			t.Error(err)
		}
	})
	_, err = host.Load(t.Context(), "empty", emptyModule, DefaultBudget())
	if err == nil {
		t.Fatal("Load(empty) error = nil")
	}
	text := err.Error()
	for _, secret := range []string{"FAIRY_API_TOKEN", "Bearer ", "sk-live", "password="} {
		if strings.Contains(text, secret) {
			t.Fatalf("error leaked %q: %v", secret, err)
		}
	}
}
