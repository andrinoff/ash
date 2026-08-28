//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package history

import (
	"os"
	"syscall"
	"time"
)

// lockFile takes an exclusive flock(2) lock on f, waiting at most wait. It
// reports whether it got it.
func lockFile(f *os.File, wait time.Duration) bool {
	rc, err := f.SyscallConn()
	if err != nil {
		return false
	}
	deadline := time.Now().Add(wait)
	for {
		var lerr error
		if err := rc.Control(func(fd uintptr) {
			lerr = syscall.Flock(int(fd), syscall.LOCK_EX|syscall.LOCK_NB)
		}); err != nil {
			return false
		}
		switch {
		case lerr == nil:
			return true
		case lerr == syscall.EINTR:
			continue
		case lerr != syscall.EWOULDBLOCK || time.Now().After(deadline):
			return false
		}
		time.Sleep(time.Millisecond)
	}
}
