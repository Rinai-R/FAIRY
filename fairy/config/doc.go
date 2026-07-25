// Package config owns editable application configuration exposed to the UI.
//
// It reads and writes model/search configuration, stores secrets through the
// secret package, and returns redacted status DTOs. Mutations are synchronous:
// the Admin API persists first and returns the latest status; consumers read
// the durable configuration on their next operation. It does not own process
// startup, Wails windows, or the domain behavior that consumes those settings.
package config
