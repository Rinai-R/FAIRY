//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !windows

package seekdb

import (
	"errors"
	"io"
)

func acquireSeekDBClientLease(runtimePaths) (io.Closer, error) {
	return nil, errors.New("SeekDB embedded client leases are not supported on this platform")
}
