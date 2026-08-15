package wasm

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/tetratelabs/wazero/api"

	"fairy/plugin"
)

type Instance struct {
	host           *Host
	name           string
	module         api.Module
	budget         Budget
	grant          Grant
	slots          chan struct{}
	mu             sync.Mutex
	calls          uint32
	hostCalls      uint32
	poison         error
	closed         bool
	state          map[string]string
	pendingIngress *ingressRequest
	tick           uint64
	due            bool
	lastEvent      []byte
	lastAction     []byte
	lastTool       []byte
	events         EventQueue
	current        plugin.Correlation
	currentSpan    string
}

func (h *Host) Load(ctx context.Context, name string, binary []byte, budget Budget) (*Instance, error) {
	return h.LoadGranted(ctx, name, binary, budget, Grant{})
}

func (h *Host) LoadGranted(ctx context.Context, name string, binary []byte, budget Budget, grant Grant) (*Instance, error) {
	if err := budget.validate(); err != nil {
		return nil, err
	}
	if err := grant.validate(); err != nil {
		return nil, err
	}
	h.mu.Lock()
	limit := h.pages
	h.mu.Unlock()
	if budget.MaxMemoryPages > limit {
		return nil, coded(plugin.CodeBudgetExceeded, "instance memory pages exceed host limit")
	}
	module, err := h.Instantiate(ctx, name, binary)
	if err != nil {
		return nil, err
	}
	keep := false
	defer func() {
		if keep {
			return
		}
		_ = module.Close(ctx)
	}()
	if err := requireABIExports(module); err != nil {
		return nil, err
	}
	if pages := memoryPages(module); pages > budget.MaxMemoryPages {
		return nil, coded(plugin.CodeBudgetExceeded, fmt.Sprintf("module memory pages %d exceed budget %d", pages, budget.MaxMemoryPages))
	}
	instance := &Instance{
		host:   h,
		name:   name,
		module: module,
		budget: budget,
		grant:  grant,
		slots:  make(chan struct{}, budget.MaxConcurrent),
		state:  make(map[string]string),
	}
	h.track(instance)
	keep = true
	return instance, nil
}

func (i *Instance) Init(ctx context.Context, envelope []byte) ([]byte, error) {
	return i.invoke(ctx, plugin.ExportInit, envelope)
}

func (i *Instance) Handle(ctx context.Context, envelope []byte) ([]byte, error) {
	return i.invoke(ctx, plugin.ExportHandle, envelope)
}

func (i *Instance) Shutdown(ctx context.Context) error {
	_, err := i.invoke(ctx, plugin.ExportShutdown, nil)
	return err
}

func (i *Instance) Close(ctx context.Context) error {
	if i == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	i.mu.Lock()
	if i.closed {
		err := i.poison
		i.mu.Unlock()
		return err
	}
	i.closed = true
	if i.poison == nil {
		i.poison = ErrHostClosed
	}
	module := i.module
	host := i.host
	name := i.name
	err := i.poison
	i.mu.Unlock()
	if host != nil {
		host.drop(name)
	}
	if module != nil {
		return module.Close(ctx)
	}
	return err
}

func (i *Instance) invoke(ctx context.Context, export string, envelope []byte) ([]byte, error) {
	if ctx == nil {
		return nil, errors.New("plugin wasm host context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, cancelErr(err)
	}
	i.queueEnter()
	select {
	case i.slots <- struct{}{}:
		i.queueLeave()
	case <-ctx.Done():
		i.queueLeave()
		i.bindCorrelation(envelope)
		err := cancelErr(ctx.Err())
		i.finishCall(callWatch{
			capability: exportCapability(export),
			bytesIn:    len(envelope),
			traceID:    peekCorrelation(envelope).TraceID,
			begin:      time.Now(),
		}, nil, err, false)
		return nil, err
	}
	defer func() { <-i.slots }()

	i.bindCorrelation(envelope)
	watch := i.watchCall(export, envelope)

	if export != plugin.ExportShutdown && uint32(len(envelope)) > i.budget.MaxInputBytes {
		err := coded(plugin.CodeBudgetExceeded, "plugin input exceeds budget")
		i.finishCall(watch, nil, err, false)
		return nil, err
	}

	i.mu.Lock()
	if i.closed || i.poison != nil {
		err := i.poison
		i.mu.Unlock()
		if err == nil {
			err = ErrHostClosed
		}
		i.finishCall(watch, nil, err, false)
		return nil, err
	}
	if i.calls >= i.budget.MaxCalls {
		i.mu.Unlock()
		err := coded(plugin.CodeBudgetExceeded, "plugin call budget exhausted")
		i.finishCall(watch, nil, err, false)
		return nil, err
	}
	i.calls++
	module := i.module
	i.mu.Unlock()

	out, err := i.callGuest(ctx, module, export, envelope)
	if err != nil {
		i.kill(ctx, err)
		i.finishCall(watch, nil, err, true)
		return nil, err
	}
	if pages := memoryPages(module); pages > i.budget.MaxMemoryPages {
		err := coded(plugin.CodeBudgetExceeded, "plugin memory page budget exceeded")
		i.kill(ctx, err)
		i.finishCall(watch, out, err, true)
		return nil, err
	}
	i.finishCall(watch, out, nil, false)
	return out, nil
}

func (i *Instance) callGuest(ctx context.Context, module api.Module, export string, envelope []byte) ([]byte, error) {
	fn := module.ExportedFunction(export)
	if fn == nil {
		return nil, coded(plugin.CodeManifestInvalid, "missing plugin export "+export)
	}
	if export == plugin.ExportShutdown {
		_, err := fn.Call(ctx)
		return nil, guestCallError(err)
	}
	ptr, err := i.writeInput(ctx, module, envelope)
	if err != nil {
		return nil, err
	}
	results, err := fn.Call(ctx, uint64(ptr), uint64(len(envelope)))
	if err != nil {
		return nil, guestCallError(err)
	}
	if len(envelope) > 0 {
		if err := i.free(ctx, module, ptr, uint32(len(envelope))); err != nil {
			return nil, err
		}
	}
	if len(results) != 1 {
		return nil, coded(plugin.CodeModuleTrap, "plugin export returned an unexpected result")
	}
	outPtr := uint32(results[0] >> 32)
	outLen := uint32(results[0])
	if outLen == 0 {
		return []byte{}, nil
	}
	if outLen > i.budget.MaxOutputBytes {
		_ = i.free(ctx, module, outPtr, outLen)
		return nil, coded(plugin.CodeBudgetExceeded, "plugin output exceeds budget")
	}
	memory := module.Memory()
	if memory == nil {
		return nil, coded(plugin.CodeManifestInvalid, "plugin module memory is not exported")
	}
	buf, ok := memory.Read(outPtr, outLen)
	if !ok {
		return nil, coded(plugin.CodeModuleTrap, "plugin output pointer is outside module memory")
	}
	out := append([]byte(nil), buf...)
	if err := i.free(ctx, module, outPtr, outLen); err != nil {
		return nil, err
	}
	return out, nil
}

func (i *Instance) writeInput(ctx context.Context, module api.Module, envelope []byte) (uint32, error) {
	if len(envelope) == 0 {
		return 0, nil
	}
	alloc := module.ExportedFunction(plugin.ExportAlloc)
	if alloc == nil {
		return 0, coded(plugin.CodeManifestInvalid, "missing plugin export "+plugin.ExportAlloc)
	}
	results, err := alloc.Call(ctx, uint64(len(envelope)))
	if err != nil {
		return 0, guestCallError(err)
	}
	if len(results) != 1 || results[0] == 0 {
		return 0, coded(plugin.CodeBudgetExceeded, "plugin allocator refused input")
	}
	ptr := uint32(results[0])
	if !module.Memory().Write(ptr, envelope) {
		return 0, coded(plugin.CodeModuleTrap, "plugin input pointer is outside module memory")
	}
	return ptr, nil
}

func (i *Instance) free(ctx context.Context, module api.Module, ptr, size uint32) error {
	fn := module.ExportedFunction(plugin.ExportFree)
	if fn == nil {
		return coded(plugin.CodeManifestInvalid, "missing plugin export "+plugin.ExportFree)
	}
	if ptr == 0 {
		return nil
	}
	_, err := fn.Call(ctx, uint64(ptr), uint64(size))
	return guestCallError(err)
}

func (i *Instance) kill(ctx context.Context, cause error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.poison == nil {
		i.poison = cause
	}
	i.closed = true
	if i.module != nil {
		_ = i.module.Close(ctx)
	}
}

func requireABIExports(module api.Module) error {
	if module.Memory() == nil {
		return coded(plugin.CodeManifestInvalid, "plugin module memory is not exported")
	}
	for _, name := range plugin.RequiredExports() {
		if module.ExportedFunction(name) == nil {
			return coded(plugin.CodeManifestInvalid, "missing plugin export "+name)
		}
	}
	return nil
}

func memoryPages(module api.Module) uint32 {
	memory := module.Memory()
	if memory == nil {
		return 0
	}
	return (memory.Size() + wasmPageSize - 1) / wasmPageSize
}

func guestCallError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return cancelErr(err)
	}
	text := strings.ToLower(err.Error())
	if strings.Contains(text, "context canceled") || strings.Contains(text, "deadline exceeded") {
		return coded(plugin.CodeCancelled, "plugin execution cancelled")
	}
	return coded(plugin.CodeModuleTrap, "plugin module trapped")
}

func cancelErr(err error) error {
	return coded(plugin.CodeCancelled, err.Error())
}

func coded(code, message string) error {
	return &plugin.CodedError{Code: code, Message: message}
}
