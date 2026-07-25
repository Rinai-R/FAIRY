// Package config is the platform boundary for editable application configuration.
//
// Implementation currently lives in fairy/config; this package exposes the
// constructors and types bootstrap and runtime composition should prefer over
// reaching into business packages directly. Public callers continue to use
// fairy/config until that facade is retired.
package config
