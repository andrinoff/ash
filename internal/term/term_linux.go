//go:build linux

package term

import "golang.org/x/sys/unix"

const (
	ioctlGetTermios      = unix.TCGETS
	ioctlSetTermios      = unix.TCSETS
	ioctlSetTermiosDrain = unix.TCSETSW
	ioctlInputPending    = unix.TIOCINQ
)
