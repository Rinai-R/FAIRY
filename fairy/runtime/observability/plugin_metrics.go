package observability

import (
	"sort"
	"sync"

	"fairy/plugin"
)

// PluginInstanceMetrics is the public per-instance diagnostic projection.
type PluginInstanceMetrics struct {
	InstanceID       string `json:"instanceId"`
	Calls            uint64 `json:"calls"`
	HostCalls        uint64 `json:"hostCalls"`
	BudgetExceeded   uint64 `json:"budgetExceeded"`
	CapabilityDenied uint64 `json:"capabilityDenied"`
	Traps            uint64 `json:"traps"`
	Restarts         uint64 `json:"restarts"`
	Cancelled        uint64 `json:"cancelled"`
	QueueDepth       uint64 `json:"queueDepth"`
	LastErrorCode    string `json:"lastErrorCode,omitempty"`
	LastTraceID      string `json:"lastTraceId,omitempty"`
}

// PluginMetricsSnapshot is the host-wide plugin diagnostic projection.
type PluginMetricsSnapshot struct {
	Calls            uint64                  `json:"calls"`
	HostCalls        uint64                  `json:"hostCalls"`
	InFlight         uint64                  `json:"inFlight"`
	QueueWaiters     uint64                  `json:"queueWaiters"`
	BudgetExceeded   uint64                  `json:"budgetExceeded"`
	CapabilityDenied uint64                  `json:"capabilityDenied"`
	Traps            uint64                  `json:"traps"`
	Restarts         uint64                  `json:"restarts"`
	Cancelled        uint64                  `json:"cancelled"`
	Instances        []PluginInstanceMetrics `json:"instances"`
}

// PluginMetrics records bounded plugin execution, permission, queue, and
// trap/restart counters. A nil receiver is a no-op.
type PluginMetrics struct {
	mu        sync.Mutex
	calls     uint64
	hostCalls uint64
	inFlight  uint64
	waiters   uint64
	budget    uint64
	denied    uint64
	traps     uint64
	restarts  uint64
	cancelled uint64
	instances map[string]*PluginInstanceMetrics
}

func NewPluginMetrics() *PluginMetrics {
	return &PluginMetrics{instances: make(map[string]*PluginInstanceMetrics)}
}

func (m *PluginMetrics) QueueEnter(instanceID string) {
	if m == nil || instanceID == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.waiters++
	m.ensure(instanceID).QueueDepth++
}

func (m *PluginMetrics) QueueLeave(instanceID string) {
	if m == nil || instanceID == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.waiters > 0 {
		m.waiters--
	}
	instance := m.ensure(instanceID)
	if instance.QueueDepth > 0 {
		instance.QueueDepth--
	}
}

func (m *PluginMetrics) Start(instanceID string) {
	if m == nil || instanceID == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.inFlight++
}

func (m *PluginMetrics) Finish(instanceID, code, traceID string, started, poisoned bool) {
	if m == nil || instanceID == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if started && m.inFlight > 0 {
		m.inFlight--
	}
	m.calls++
	instance := m.ensure(instanceID)
	instance.Calls++
	if traceID != "" {
		instance.LastTraceID = traceID
	}
	m.classifyLocked(instance, code, poisoned)
}

func (m *PluginMetrics) Host(instanceID, code, traceID string) {
	if m == nil || instanceID == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.hostCalls++
	instance := m.ensure(instanceID)
	instance.HostCalls++
	if traceID != "" {
		instance.LastTraceID = traceID
	}
	m.classifyLocked(instance, code, false)
}

func (m *PluginMetrics) classifyLocked(instance *PluginInstanceMetrics, code string, poisoned bool) {
	if code != "" {
		instance.LastErrorCode = code
	}
	switch code {
	case plugin.CodeCapabilityDenied:
		m.denied++
		instance.CapabilityDenied++
	case plugin.CodeBudgetExceeded:
		m.budget++
		instance.BudgetExceeded++
	case plugin.CodeModuleTrap:
		m.traps++
		instance.Traps++
	case plugin.CodeCancelled:
		m.cancelled++
		instance.Cancelled++
	}
	if poisoned {
		m.restarts++
		instance.Restarts++
	}
}

func (m *PluginMetrics) ensure(instanceID string) *PluginInstanceMetrics {
	instance := m.instances[instanceID]
	if instance == nil {
		instance = &PluginInstanceMetrics{InstanceID: instanceID}
		m.instances[instanceID] = instance
	}
	return instance
}

func (m *PluginMetrics) Snapshot() PluginMetricsSnapshot {
	if m == nil {
		return PluginMetricsSnapshot{Instances: []PluginInstanceMetrics{}}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	snapshot := PluginMetricsSnapshot{
		Calls:            m.calls,
		HostCalls:        m.hostCalls,
		InFlight:         m.inFlight,
		QueueWaiters:     m.waiters,
		BudgetExceeded:   m.budget,
		CapabilityDenied: m.denied,
		Traps:            m.traps,
		Restarts:         m.restarts,
		Cancelled:        m.cancelled,
		Instances:        make([]PluginInstanceMetrics, 0, len(m.instances)),
	}
	for _, instance := range m.instances {
		snapshot.Instances = append(snapshot.Instances, *instance)
	}
	sort.Slice(snapshot.Instances, func(i, j int) bool {
		return snapshot.Instances[i].InstanceID < snapshot.Instances[j].InstanceID
	})
	return snapshot
}
