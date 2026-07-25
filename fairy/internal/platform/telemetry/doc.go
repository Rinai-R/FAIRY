// Package telemetry is the platform boundary for logs and runtime metrics.
//
// Log ring buffers and HTTP/message metrics currently live in fairy/observability;
// logger construction lives in fairy/logx. Bootstrap and runtime composition
// should prefer this package over importing those facades directly.
package telemetry
