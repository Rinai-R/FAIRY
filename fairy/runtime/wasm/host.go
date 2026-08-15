package wasm

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"

	"fairy/plugin"
	"fairy/runtime/observability"
)

var (
	ErrHostClosed           = errors.New("plugin wasm host is closed")
	ErrModuleNameRequired   = errors.New("plugin module name is required")
	ErrModuleBinaryRequired = errors.New("plugin module binary is required")
)

// Host is the deny-by-default wazero engine. It never instantiates WASI or
// grants filesystem, network, environment, or credential access.
type Host struct {
	mu        sync.Mutex
	runtime   wazero.Runtime
	pages     uint32
	closed    bool
	http      *http.Client
	instances map[string]*Instance
	observer  Observer
}

func Open(ctx context.Context) (*Host, error) {
	return OpenWith(ctx, Observer{})
}

func OpenWith(ctx context.Context, observer Observer) (*Host, error) {
	return open(ctx, DefaultBudget(), observer)
}

func open(ctx context.Context, budget Budget, observer Observer) (*Host, error) {
	if ctx == nil {
		return nil, errors.New("plugin wasm host context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := budget.validate(); err != nil {
		return nil, err
	}
	runtime := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfig().
		WithCloseOnContextDone(true).
		WithMemoryLimitPages(budget.MaxMemoryPages))
	host := &Host{
		runtime: runtime,
		pages:   budget.MaxMemoryPages,
		http: &http.Client{
			Transport: &http.Transport{Proxy: nil},
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		instances: make(map[string]*Instance),
		observer:  observer,
	}
	if err := host.install(ctx); err != nil {
		_ = runtime.Close(ctx)
		return nil, err
	}
	return host, nil
}

func (h *Host) install(ctx context.Context) error {
	_, err := h.runtime.NewHostModuleBuilder(plugin.HostNamespace).
		NewFunctionBuilder().WithFunc(h.hostCall).Export(plugin.HostExportCall).
		Instantiate(ctx)
	if err != nil {
		return fmt.Errorf("instantiating plugin host module: %w", err)
	}
	return nil
}

func (h *Host) track(instance *Instance) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.instances[instance.name] = instance
}

func (h *Host) drop(name string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.instances, name)
}

func (h *Host) Close(ctx context.Context) error {
	if h == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	h.mu.Lock()
	runtime := h.runtime
	h.runtime = nil
	h.closed = true
	h.instances = map[string]*Instance{}
	h.mu.Unlock()
	if runtime == nil {
		return nil
	}
	return runtime.Close(ctx)
}

func (h *Host) SnapshotMetrics() observability.PluginMetricsSnapshot {
	if h == nil {
		return observability.PluginMetricsSnapshot{Instances: []observability.PluginInstanceMetrics{}}
	}
	return h.observer.Metrics.Snapshot()
}

func (h *Host) Instantiate(ctx context.Context, name string, binary []byte) (api.Module, error) {
	if name == "" {
		return nil, ErrModuleNameRequired
	}
	if len(binary) == 0 {
		return nil, ErrModuleBinaryRequired
	}
	if ctx == nil {
		return nil, errors.New("plugin wasm host context is required")
	}
	h.mu.Lock()
	runtime := h.runtime
	closed := h.closed
	h.mu.Unlock()
	if closed || runtime == nil {
		return nil, ErrHostClosed
	}
	compiled, err := runtime.CompileModule(ctx, binary)
	if err != nil {
		return nil, fmt.Errorf("compiling plugin module %s: %w", name, err)
	}
	defer compiled.Close(ctx)
	module, err := runtime.InstantiateModule(ctx, compiled, denyByDefaultModuleConfig().WithName(name))
	if err != nil {
		return nil, fmt.Errorf("instantiating plugin module %s: %w", name, err)
	}
	return module, nil
}

func denyByDefaultModuleConfig() wazero.ModuleConfig {
	return wazero.NewModuleConfig().WithStartFunctions()
}
