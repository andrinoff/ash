package main

import (
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// processStart asks the kernel when this process started, so --profile
// also counts the time before any Go code runs: loading the binary, the
// Go runtime and package initialization, about 2ms on a recent Mac.
func processStart() (time.Time, bool) {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", os.Getpid())
	if err != nil {
		return time.Time{}, false
	}
	tv := kp.Proc.P_starttime
	return time.Unix(int64(tv.Sec), int64(tv.Usec)*1000), true
}
