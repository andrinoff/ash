//go:build !darwin

package shell

import "syscall"

func wsStopped(ws syscall.WaitStatus) (syscall.Signal, bool) {
	if !ws.Stopped() {
		return 0, false
	}
	return ws.StopSignal(), true
}

func wsContinued(ws syscall.WaitStatus) bool { return ws.Continued() }
