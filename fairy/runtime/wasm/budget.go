package wasm

import "errors"

const wasmPageSize = 65536

var ErrInvalidBudget = errors.New("plugin execution budget is invalid")

// Budget bounds one plugin instance. Zero values are invalid; they never mean unlimited.
type Budget struct {
	MaxMemoryPages uint32
	MaxCalls       uint32
	MaxHostCalls   uint32
	MaxInputBytes  uint32
	MaxOutputBytes uint32
	MaxConcurrent  uint32
}

func DefaultBudget() Budget {
	return Budget{
		MaxMemoryPages: 16,
		MaxCalls:       256,
		MaxHostCalls:   64,
		MaxInputBytes:  64 << 10,
		MaxOutputBytes: 64 << 10,
		MaxConcurrent:  1,
	}
}

func (b Budget) validate() error {
	if b.MaxMemoryPages == 0 || b.MaxCalls == 0 || b.MaxHostCalls == 0 || b.MaxConcurrent == 0 || b.MaxInputBytes == 0 || b.MaxOutputBytes == 0 {
		return ErrInvalidBudget
	}
	return nil
}
