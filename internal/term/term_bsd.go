//go:build darwin || freebsd || netbsd || openbsd || dragonfly

package term

import "golang.org/x/sys/unix"

const (
	ioctlGetTermios      = unix.TIOCGETA
	ioctlSetTermios      = unix.TIOCSETA
	ioctlSetTermiosDrain = unix.TIOCSETAW
	ioctlInputPending    = 0x4004667f // FIONREAD: _IOR('f', 127, int)
)
