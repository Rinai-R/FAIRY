//go:build windows

package seekdb

import "os"

func interruptProcess(process *os.Process) error {
	return process.Kill()
}
