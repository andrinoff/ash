package shell

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"sync"
	"syscall"

	"golang.org/x/sys/unix"
)

// Descriptors above 2.
//
// The process's own descriptors above 2 belong to the Go runtime (its
// signal pipe, the kqueue), the line editor and the shell's open files, so
// ash never hands them to scripts. Descriptors a script opens with
// `exec 3>file` live in a table instead (sh.fds), backed by private
// copies, and are given to child processes at their numbers. The
// descriptors ash inherited at startup (`ash script 3<input`) start out
// in that table.

// fdEntry is one descriptor in the shell's table.
type fdEntry struct {
	s     *Stream
	owned bool // the shell's own copy: close it when the entry goes away
}

// inheritedFds are the descriptors above 2 that ash was started with.
var inheritedFds = openWithoutCloexec()

// openWithoutCloexec lists the open descriptors above 2 that are not
// close-on-exec. At startup those are the inherited ones: everything the
// runtime opens is close-on-exec (and it may already have opened its
// poller during package init, if stdin was non-blocking), while anything
// that survived the exec into ash can't be.
func openWithoutCloexec() []int {
	var fds []int
	for fd := 3; fd < 256; fd++ {
		if fl, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0); err == nil && fl&unix.FD_CLOEXEC == 0 {
			fds = append(fds, fd)
		}
	}
	return fds
}

var (
	inheritedOnce  sync.Once
	inheritedFiles map[int]*os.File
)

// inheritedTable wraps the inherited descriptors. The files are made once
// and kept, so the garbage collector never closes them.
func inheritedTable() map[int]fdEntry {
	inheritedOnce.Do(func() {
		inheritedFiles = map[int]*os.File{}
		for _, fd := range inheritedFds {
			inheritedFiles[fd] = os.NewFile(uintptr(fd), fmt.Sprintf("/dev/fd/%d", fd))
		}
	})
	if len(inheritedFiles) == 0 {
		return nil
	}
	m := make(map[int]fdEntry, len(inheritedFiles))
	for fd, f := range inheritedFiles {
		m[fd] = fdEntry{s: &Stream{File: f, inTable: true}, owned: true}
	}
	return m
}

// shellFd returns the stream the shell has for descriptor n >= 3 (closed
// if the script closed it), or nil.
func (sh *Shell) shellFd(n int) *Stream {
	return sh.fds[n].s
}

// ioFd returns io's stream for descriptor n, falling back to the shell's
// table. An IO made before an exec replaced a table entry (the caller of
// a function that ran `exec 3>file`) follows the table.
func (sh *Shell) ioFd(io *IO, n int) *Stream {
	s := io.fd(n)
	if n > 2 && (s == nil || s.inTable && s != sh.fds[n].s) {
		return sh.fds[n].s
	}
	return s
}

// setShellFd replaces descriptor n in the table (s == nil removes it). The
// map is copied first: subshells running in other goroutines share it.
func (sh *Shell) setShellFd(n int, s *Stream, owned bool) {
	m := make(map[int]fdEntry, len(sh.fds)+1)
	for k, v := range sh.fds {
		m[k] = v
	}
	if old, ok := m[n]; ok && old.owned && old.s.File != nil && (s == nil || old.s.File != s.File) {
		old.s.File.Close()
	}
	if s == nil {
		delete(m, n)
	} else {
		s.inTable = true
		m[n] = fdEntry{s: s, owned: owned}
	}
	sh.fds = m
}

// maxShellFd is the highest descriptor in the table (2 if none).
func (sh *Shell) maxShellFd() int {
	max := 2
	for n := range sh.fds {
		if n > max {
			max = n
		}
	}
	return max
}

// dupFile returns a close-on-exec copy of f numbered at least min.
func dupFile(f *os.File, min int) (*os.File, error) {
	fd, err := unix.FcntlInt(f.Fd(), unix.F_DUPFD_CLOEXEC, min)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), f.Name()), nil
}

// execRedirs applies the redirections of an `exec` without a command to
// the shell. In the main shell they last: 0-2 are replaced in the process
// and higher descriptors go in the table. In a subshell, or for a
// descriptor that a surrounding block or function call redirects, they
// last until that ends.
func (sh *Shell) execRedirs(rio *IO) int {
	base := sh.redirBase
	if base == nil || base == rio {
		return 0
	}
	var ns []int
	for n := 0; n <= 2; n++ {
		if rio.fd(n) != base.fd(n) {
			ns = append(ns, n)
		}
	}
	for n, s := range rio.Extra {
		if base.Extra[n] != s {
			ns = append(ns, n)
		}
	}
	sort.Ints(ns)
	// Copy every source first, so that `exec 3>&1 1>log` saves the old
	// stdout before replacing it.
	news := make([]*Stream, len(ns))
	for i, n := range ns {
		s := rio.fd(n)
		switch {
		case s == nil || s.closed:
			news[i] = &Stream{closed: true}
		case s.File != nil:
			f, err := dupFile(s.File, 10)
			if err != nil {
				for _, c := range news[:i] {
					if c.File != nil {
						c.File.Close()
					}
				}
				sh.errorMsg(base.Err, "exec: %d: %s", n, errText(err))
				return 1
			}
			news[i] = &Stream{File: f, TTY: s.TTY}
		default:
			news[i] = s
		}
	}
	for i, n := range ns {
		sh.setFdFor(base, n, news[i])
	}
	return 0
}

// setFdFor makes s descriptor n for the rest of base's context, and for
// the rest of the shell when n is the shell's own descriptor.
func (sh *Shell) setFdFor(base *IO, n int, s *Stream) {
	cur := sh.ioFd(base, n)
	if !sh.subshell && n <= 2 && cur != nil && (cur == sh.shellFd(n) || cur.File != nil && int(cur.File.Fd()) == n) {
		if s.File != nil {
			// replace the process's own stdin/stdout/stderr
			unix.Dup2(int(s.File.Fd()), n)
			s.File.Close()
			sh.setShellFd(n, nil, false)
			base.setFd(n, sh.StdIO().fd(n))
			return
		}
		sh.setShellFd(n, s, false)
		base.setFd(n, s)
		return
	}
	if !sh.subshell && n > 2 && cur == sh.shellFd(n) {
		sh.setShellFd(n, s, s.File != nil)
	}
	base.setFd(n, s)
}

// execProgram replaces the shell with the program at path. It gets the
// command's descriptors at their numbers; if the exec fails, the shell's
// own descriptors are put back.
func (sh *Shell) execProgram(path string, argv []string, io *IO) error {
	want := map[int]*Stream{}
	for n := 0; n <= 2; n++ {
		want[n] = io.fd(n)
	}
	for n, e := range sh.fds {
		if n > 2 {
			want[n] = e.s
		}
	}
	for n, s := range io.Extra {
		want[n] = s
	}
	max := 9
	for n := range want {
		if n > max {
			max = n
		}
	}
	type move struct {
		n, src, saved int // src -1: close n; saved -1: n was not open
	}
	var moves []move
	var keep []int // already numbered right: only close-on-exec to clear
	undo := func() {
		for i := len(moves) - 1; i >= 0; i-- {
			m := moves[i]
			if m.saved >= 0 {
				unix.Dup2(m.saved, m.n)
				unix.Close(m.saved)
			} else {
				unix.Close(m.n)
			}
			if m.src >= 0 {
				unix.Close(m.src)
			}
		}
		for _, n := range keep {
			unix.FcntlInt(uintptr(n), unix.F_SETFD, unix.FD_CLOEXEC)
		}
	}
	for n, s := range want {
		src := -1
		switch {
		case s == nil:
			continue
		case s.closed:
		case s.File != nil:
			if int(s.File.Fd()) == n {
				if n > 2 {
					if fl, err := unix.FcntlInt(uintptr(n), unix.F_GETFD, 0); err == nil && fl&unix.FD_CLOEXEC != 0 {
						unix.FcntlInt(uintptr(n), unix.F_SETFD, fl&^unix.FD_CLOEXEC)
						keep = append(keep, n)
					}
				}
				continue
			}
			// a copy above every target, so no move clobbers another's source
			d, err := unix.FcntlInt(s.File.Fd(), unix.F_DUPFD_CLOEXEC, max+1)
			if err != nil {
				undo()
				return err
			}
			src = d
		default:
			continue // not a real file: the program can't have it
		}
		saved, err := unix.FcntlInt(uintptr(n), unix.F_DUPFD_CLOEXEC, max+1)
		if err != nil {
			saved = -1
		}
		moves = append(moves, move{n: n, src: src, saved: saved})
	}
	for _, m := range moves {
		if m.src >= 0 {
			unix.Dup2(m.src, m.n)
		} else {
			unix.Close(m.n)
		}
	}
	env := sh.Environ()
	err := syscall.Exec(path, argv, env)
	if errors.Is(err, syscall.ENOEXEC) {
		// a script without a shebang: run it with ash, like sh does
		err = syscall.Exec(sh.Self(), append([]string{sh.Self(), path}, argv[1:]...), env)
	}
	undo()
	return err
}
