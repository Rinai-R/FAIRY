// Package secrets is the platform boundary for encrypted credential storage.
//
// Implementation currently lives in fairy/secret; this package exposes the
// constructors and types bootstrap composition should prefer. Public callers
// continue to use fairy/secret until that facade is retired.
package secrets
