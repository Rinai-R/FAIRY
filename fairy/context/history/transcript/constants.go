package transcript

import "errors"

var ErrEndpointBindingMismatch = errors.New("endpoint conversation binding does not match stored interaction facts")

const (
	MaxMessagePageLimit     = 200
	DefaultMessagePageLimit = 50
)
