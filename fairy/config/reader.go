package config

import "errors"

// Reader is a process-scoped handle for reading durable companion config from a
// harness root. Construct once in main and inject into consumers.
type Reader struct {
	root string
}

func NewReader(root string) *Reader {
	return &Reader{root: root}
}

func (r *Reader) Root() string {
	if r == nil {
		return ""
	}
	return r.root
}

func (r *Reader) ModelConnection() (ModelConnection, error) {
	if r == nil || r.root == "" {
		return ModelConnection{}, errors.New("config root is required")
	}
	return ReadModelConnection(r.root)
}

func (r *Reader) WebSearchSettings() (WebSearchSettings, error) {
	if r == nil || r.root == "" {
		return WebSearchSettings{}, errors.New("config root is required")
	}
	return ReadWebSearchSettings(r.root)
}
