// Package config owns editable application configuration exposed to the UI.
//
// It reads and writes model/search configuration, stores secrets through the
// secret package, emits configuration-change notifications, and returns
// redacted status DTOs. It does not own process startup, Wails windows, or the
// domain behavior that consumes those settings.
package config
