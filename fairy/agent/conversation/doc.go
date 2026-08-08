// Package conversation owns FAIRY's reactive conversation lifecycle: request
// admission, context assembly, the bounded agent loop, cancellation, ordered
// reply delivery, terminal persistence, and runtime records.
//
// Tool contracts and projections, knowledge processing, retention workers,
// storage, and model transports are injected capabilities owned by other
// packages. Process-level composition remains in core.
package conversation
