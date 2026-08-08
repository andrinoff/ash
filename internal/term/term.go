// Package term holds the small amount of terminal plumbing ash needs:
// termios get/set, raw mode, window size and foreground process groups.
package term

import (
	"os"
	"os/signal"
	"sync"
	"syscall"

	"golang.org/x/sys/unix"
)

// GetState returns the terminal settings of fd.
func GetState(fd int) (*unix.Termios, error) {
	return unix.IoctlGetTermios(fd, ioctlGetTermios)
}

// SetState applies terminal settings, waiting for output to drain.
func SetState(fd int, t *unix.Termios) error {
	return unix.IoctlSetTermios(fd, ioctlSetTermiosDrain, t)
}

// SetStateNow applies terminal settings immediately.
func SetStateNow(fd int, t *unix.Termios) error {
	return unix.IoctlSetTermios(fd, ioctlSetTermios, t)
}

// IsTerminal reports whether fd is a terminal.
func IsTerminal(fd int) bool {
	_, err := GetState(fd)
	return err == nil
}

// Raw returns a copy of t configured for character-at-a-time input. Output
// processing is left on so "\n" still moves to the start of the next line.
func Raw(t *unix.Termios) *unix.Termios {
	r := *t
	r.Iflag &^= unix.IGNBRK | unix.BRKINT | unix.PARMRK | unix.ISTRIP | unix.INLCR | unix.IGNCR | unix.ICRNL | unix.IXON
	r.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG | unix.IEXTEN
	r.Cflag &^= unix.CSIZE | unix.PARENB
	r.Cflag |= unix.CS8
	r.Cc[unix.VMIN] = 1
	r.Cc[unix.VTIME] = 0
	return &r
}

// Size returns the window size of fd as columns and rows. It falls back
// to $COLUMNS/$LINES and then 80x24.
func Size(fd int) (int, int) {
	ws, err := unix.IoctlGetWinsize(fd, unix.TIOCGWINSZ)
	if err == nil && ws.Col > 0 && ws.Row > 0 {
		return int(ws.Col), int(ws.Row)
	}
	return 80, 24
}

// Foreground returns the foreground process group of the terminal.
func Foreground(fd int) (int, error) {
	return unix.IoctlGetInt(fd, unix.TIOCGPGRP)
}

var ttouMu sync.Mutex

// SetForeground hands the terminal to process group pgid. SIGTTOU is
// ignored for the duration of the call because a background process that
// calls tcsetpgrp would otherwise be stopped (or loop forever, since Go
// catches the signal). syscall.ForkLock is held so no child is forked
// while SIGTTOU is ignored and inherits that.
func SetForeground(fd, pgid int) error {
	ttouMu.Lock()
	defer ttouMu.Unlock()
	syscall.ForkLock.Lock()
	defer syscall.ForkLock.Unlock()
	signal.Ignore(syscall.SIGTTOU)
	defer signal.Reset(syscall.SIGTTOU)
	return unix.IoctlSetPointerInt(fd, unix.TIOCSPGRP, pgid)
}

// Stdin/Stdout file descriptors, for convenience.
var (
	StdinFd  = int(os.Stdin.Fd())
	StdoutFd = int(os.Stdout.Fd())
)
