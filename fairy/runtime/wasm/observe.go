package wasm

import (
	"bytes"
	"errors"
	"strconv"
	"time"

	"fairy/plugin"
	"fairy/runtime/observability"
)

const pluginLoggerPrefix = "plugin."

// SpanRecorder is the subset of message metrics needed to parent a plugin
// call under an existing message/Turn/tool trace.
type SpanRecorder interface {
	StartSpan(traceID, parentSpanID, operation, category string, attributes map[string]string) string
	FinishSpan(spanID, status string, attributes map[string]string)
}

// Observer records plugin correlation spans, live metrics, and redacted logs.
// A zero Observer is a no-op.
type Observer struct {
	Spans   SpanRecorder
	Metrics *observability.PluginMetrics
	Logs    *observability.LogStore
}

type callWatch struct {
	capability string
	attempt    uint32
	bytesIn    int
	started    bool
	spanID     string
	traceID    string
	begin      time.Time
}

func (i *Instance) queueEnter() {
	if i == nil || i.host == nil {
		return
	}
	i.host.observer.Metrics.QueueEnter(i.name)
}

func (i *Instance) queueLeave() {
	if i == nil || i.host == nil {
		return
	}
	i.host.observer.Metrics.QueueLeave(i.name)
}

func (i *Instance) watchCall(export string, envelope []byte) callWatch {
	watch := callWatch{
		capability: exportCapability(export),
		bytesIn:    len(envelope),
		begin:      time.Now(),
	}
	if i == nil {
		return watch
	}
	i.mu.Lock()
	watch.attempt = i.calls
	correlation := i.current
	i.mu.Unlock()
	watch.traceID = correlation.TraceID
	if watch.traceID != "" && i.host != nil && i.host.observer.Spans != nil {
		watch.spanID = i.host.observer.Spans.StartSpan(watch.traceID, "", "插件调用", "plugin", map[string]string{
			"pluginId":   i.name,
			"capability": watch.capability,
			"attempt":    strconv.FormatUint(uint64(watch.attempt), 10),
			"bytes":      strconv.Itoa(watch.bytesIn),
			"status":     "running",
		})
		i.mu.Lock()
		i.currentSpan = watch.spanID
		i.mu.Unlock()
	}
	if i.host != nil {
		i.host.observer.Metrics.Start(i.name)
		watch.started = true
	}
	return watch
}

func (i *Instance) finishCall(watch callWatch, out []byte, err error, poisoned bool) {
	if i == nil {
		return
	}
	code := stableErrorCode(err)
	status := "completed"
	if err != nil {
		status = "failed"
	}
	if watch.spanID != "" && i.host != nil && i.host.observer.Spans != nil {
		bytesOut := len(out)
		if bytesOut == 0 {
			bytesOut = watch.bytesIn
		}
		i.host.observer.Spans.FinishSpan(watch.spanID, status, map[string]string{
			"status":    status,
			"errorCode": code,
			"bytes":     strconv.Itoa(bytesOut),
			"duration":  strconv.FormatInt(time.Since(watch.begin).Milliseconds(), 10),
		})
	}
	i.mu.Lock()
	i.currentSpan = ""
	traceID := i.current.TraceID
	i.mu.Unlock()
	if watch.traceID != "" {
		traceID = watch.traceID
	}
	if i.host != nil {
		i.host.observer.Metrics.Finish(i.name, code, traceID, watch.started, poisoned)
	}
	i.logCall(watch, out, err)
}

func (i *Instance) observeHost(capability string, err error) {
	if i == nil || i.host == nil {
		return
	}
	code := stableErrorCode(err)
	i.mu.Lock()
	parent := i.currentSpan
	traceID := i.current.TraceID
	i.mu.Unlock()
	if traceID != "" && i.host.observer.Spans != nil {
		spanID := i.host.observer.Spans.StartSpan(traceID, parent, "插件 Host 调用", "plugin", map[string]string{
			"pluginId":   i.name,
			"capability": capability,
			"status":     "running",
		})
		status := "completed"
		if err != nil {
			status = "failed"
		}
		i.host.observer.Spans.FinishSpan(spanID, status, map[string]string{
			"status":     status,
			"errorCode":  code,
			"capability": capability,
		})
	}
	i.host.observer.Metrics.Host(i.name, code, traceID)
	level := "info"
	message := "plugin host call"
	if err != nil {
		level = "warn"
		message = "plugin host call failed"
		if code == plugin.CodeModuleTrap {
			level = "error"
		}
	}
	i.appendLog(level, message, map[string]string{
		"pluginInstanceId": i.name,
		"capability":       capability,
		"errorCode":        code,
		"traceId":          traceID,
	})
}

func (i *Instance) logCall(watch callWatch, out []byte, err error) {
	level := "info"
	message := "plugin call"
	if err != nil {
		level = "warn"
		message = "plugin call failed"
		if stableErrorCode(err) == plugin.CodeModuleTrap {
			level = "error"
		}
	}
	fields := map[string]string{
		"pluginInstanceId": i.name,
		"capability":       watch.capability,
		"attempt":          strconv.FormatUint(uint64(watch.attempt), 10),
		"bytes":            strconv.Itoa(max(watch.bytesIn, len(out))),
		"durationMs":       strconv.FormatInt(time.Since(watch.begin).Milliseconds(), 10),
		"traceId":          watch.traceID,
		"errorCode":        stableErrorCode(err),
	}
	i.appendLog(level, message, fields)
}

func (i *Instance) appendLog(level, message string, fields map[string]string) {
	if i == nil || i.host == nil || i.host.observer.Logs == nil {
		return
	}
	redacted := make(map[string]string, len(fields))
	for key, value := range fields {
		if value == "" {
			continue
		}
		redacted[key] = i.scrub(value)
	}
	i.host.observer.Logs.Append(observability.EntryInput{
		Level:   level,
		Logger:  pluginLoggerPrefix + i.name,
		Message: i.scrub(message),
		Fields:  observability.SortedFields(redacted),
	})
}

func (i *Instance) bindCorrelation(envelope []byte) {
	if i == nil {
		return
	}
	correlation := peekCorrelation(envelope)
	if correlation.PluginInstanceID == "" {
		correlation.PluginInstanceID = i.name
	}
	i.mu.Lock()
	i.current = correlation
	i.mu.Unlock()
}

func peekCorrelation(envelope []byte) plugin.Correlation {
	if len(envelope) == 0 {
		return plugin.Correlation{}
	}
	parsed, err := plugin.ParseEnvelope(bytes.NewReader(envelope))
	if err != nil {
		return plugin.Correlation{}
	}
	return parsed.Correlation
}

func exportCapability(export string) string {
	switch export {
	case plugin.ExportInit:
		return "init"
	case plugin.ExportHandle:
		return "handle"
	case plugin.ExportShutdown:
		return "shutdown"
	default:
		return export
	}
}

func stableErrorCode(err error) string {
	var coded *plugin.CodedError
	if errors.As(err, &coded) {
		return coded.Code
	}
	return ""
}
