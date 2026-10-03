package shell

import "syscall"

// syscall.WaitStatus gets stopped and continued children backwards on
// darwin: it compares the stop signal with SIGSTOP where <sys/wait.h>
// compares it with SIGCONT. A child stopped by SIGSTOP then looks like it
// was continued, and a continued one looks stopped. Decode both by hand.

func wsStopped(ws syscall.WaitStatus) (syscall.Signal, bool) {
	if ws&0x7f != 0x7f {
		return 0, false
	}
	sig := syscall.Signal(ws >> 8)
	if sig == syscall.SIGCONT {
		return 0, false
	}
	return sig, true
}

func wsContinued(ws syscall.WaitStatus) bool {
	return ws&0x7f == 0x7f && syscall.Signal(ws>>8) == syscall.SIGCONT
}
