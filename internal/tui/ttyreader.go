package tui

import (
	"errors"
	"io"
	"os"
	"sync"
	"sync/atomic"

	"golang.org/x/sys/unix"
)

// ttyReader reads from a terminal and can be stopped from another
// goroutine. Stopping wakes a blocked Read without consuming input, so
// keys typed after the picker is done stay queued for the shell.
//
// It waits with select(2) because poll(2) and kqueue(2) do not work on
// terminal devices on macOS.
type ttyReader struct {
	fd           int
	stopR, stopW *os.File
	stopped      atomic.Bool
	closeOnce    sync.Once
}

func newTTYReader(f *os.File) (*ttyReader, error) {
	fd := int(f.Fd())
	r, w, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	if max(fd, int(r.Fd())) >= unix.FD_SETSIZE {
		r.Close()
		w.Close()
		return nil, errors.New("terminal file descriptor too large for select")
	}
	return &ttyReader{fd: fd, stopR: r, stopW: w}, nil
}

// Read returns io.EOF once the reader is stopped.
func (t *ttyReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	sfd := int(t.stopR.Fd())
	nfd := max(t.fd, sfd) + 1
	for {
		if t.stopped.Load() {
			return 0, io.EOF
		}
		var set unix.FdSet
		set.Set(t.fd)
		set.Set(sfd)
		if _, err := unix.Select(nfd, &set, nil, nil, nil); err != nil {
			if err == unix.EINTR {
				continue
			}
			return 0, err
		}
		if set.IsSet(sfd) || t.stopped.Load() {
			return 0, io.EOF
		}
		if !set.IsSet(t.fd) {
			continue
		}
		n, err := unix.Read(t.fd, p)
		switch {
		case err == unix.EINTR || err == unix.EAGAIN:
			continue
		case err != nil:
			return 0, err
		case n == 0:
			return 0, io.EOF
		}
		return n, nil
	}
}

// Stop makes the current and every later Read return io.EOF.
func (t *ttyReader) Stop() {
	if t.stopped.CompareAndSwap(false, true) {
		t.stopW.Write([]byte{0})
	}
}

func (t *ttyReader) Close() {
	t.Stop()
	t.closeOnce.Do(func() {
		t.stopR.Close()
		t.stopW.Close()
	})
}
