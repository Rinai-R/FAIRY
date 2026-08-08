package observability

import (
	"runtime"
	"time"
)

type ProcessMetrics struct {
	UptimeSeconds  uint64 `json:"uptimeSeconds"`
	GoVersion      string `json:"goVersion"`
	Goroutines     uint64 `json:"goroutines"`
	HeapAllocBytes uint64 `json:"heapAllocBytes"`
}

func SnapshotProcess(startedAt time.Time) ProcessMetrics {
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	uptime := time.Since(startedAt)
	if uptime < 0 {
		uptime = 0
	}
	return ProcessMetrics{
		UptimeSeconds: uint64(uptime / time.Second), GoVersion: runtime.Version(),
		Goroutines: uint64(runtime.NumGoroutine()), HeapAllocBytes: stats.HeapAlloc,
	}
}
