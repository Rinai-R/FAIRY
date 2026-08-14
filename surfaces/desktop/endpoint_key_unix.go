//go:build unix

package main

import (
	"fmt"
	"io/fs"
	"os"
	"syscall"
)

func validateEndpointKeyOwner(path string, info fs.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("Desktop installation identity path %q has unsupported owner metadata", path)
	}
	if int(stat.Uid) != os.Getuid() {
		return fmt.Errorf("Desktop installation identity path %q must be owned by the current user", path)
	}
	return nil
}
