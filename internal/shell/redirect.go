package shell

import (
	"errors"
	"io/fs"
	"os"
	"strconv"
	"strings"

	"github.com/andrinoff/ash/internal/syntax"
)

// redirect applies redirections to a copy of io.
func (sh *Shell) redirect(rs []*syntax.Redirect, base *IO) (*IO, func(), error) {
	nio := base.clone()
	var opened []*os.File
	cleanup := func() {
		for _, f := range opened {
			f.Close()
		}
	}
	for _, r := range rs {
		fd := r.Fd
		var target string
		if r.Op != syntax.RedirHeredoc && r.Op != syntax.RedirHeredocTabs {
			if r.Op == syntax.RedirHerestring {
				s, err := sh.expandString(r.Target, base)
				if err != nil {
					return nio, cleanup, err
				}
				target = s
			} else {
				fs, err := sh.expandFields([]*syntax.Word{r.Target}, base, 0)
				if err != nil {
					return nio, cleanup, err
				}
				if len(fs) != 1 {
					return nio, cleanup, errorf("%s: ambiguous redirect", wordText(r.Target))
				}
				target = fs[0]
			}
		}
		open := func(flags int, def int) error {
			if fd < 0 {
				fd = def
			}
			if s := sh.devStream(target, nio); s != nil {
				nio.setFd(fd, s)
				return nil
			}
			path := sh.absPath(target)
			if flags&os.O_TRUNC != 0 && (sh.opts.noclobber && (r.Op == syntax.RedirOut || r.Op == syntax.RedirAll) || r.Op == syntax.RedirNoClobber) {
				// Create it exclusively: two shells racing for a lock
				// file (set -C; echo $$ > lock) must not both win.
				f, err := os.OpenFile(path, flags|os.O_EXCL, 0o666)
				if err == nil {
					opened = append(opened, f)
					nio.setFd(fd, fileStream(f, false))
					return nil
				}
				if !errors.Is(err, fs.ErrExist) {
					return errorf("%s", errText(err))
				}
				if st, err := os.Stat(path); err == nil && st.Mode().IsRegular() {
					return errorf("%s: file exists (noclobber)", target)
				}
				// not a regular file (/dev/null): open it as usual
			}
			f, err := os.OpenFile(path, flags, 0o666)
			if err != nil {
				return errorf("%s", errText(err))
			}
			opened = append(opened, f)
			nio.setFd(fd, fileStream(f, false))
			return nil
		}
		var err error
		switch r.Op {
		case syntax.RedirIn:
			err = open(os.O_RDONLY, 0)
		case syntax.RedirOut, syntax.RedirClobber, syntax.RedirNoClobber:
			err = open(os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 1)
		case syntax.RedirAppend:
			err = open(os.O_WRONLY|os.O_CREATE|os.O_APPEND, 1)
		case syntax.RedirInOut:
			err = open(os.O_RDWR|os.O_CREATE, 0)
		case syntax.RedirAll, syntax.RedirAllAppend:
			flags := os.O_WRONLY | os.O_CREATE | os.O_TRUNC
			if r.Op == syntax.RedirAllAppend {
				flags = os.O_WRONLY | os.O_CREATE | os.O_APPEND
			}
			fd = 1
			err = open(flags, 1)
			if err == nil {
				nio.Err = nio.Out
			}
		case syntax.RedirDupOut, syntax.RedirDupIn:
			def := 1
			if r.Op == syntax.RedirDupIn {
				def = 0
			}
			if fd < 0 {
				fd = def
			}
			if target == "-" {
				nio.setFd(fd, &Stream{closed: true})
				continue
			}
			src := strings.TrimSuffix(target, "-")
			n, convErr := strconv.Atoi(src)
			if convErr != nil {
				if r.Op == syntax.RedirDupOut && r.Fd < 0 {
					// bash: >& file is &> file
					fd = 1
					err = open(os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 1)
					if err == nil {
						nio.Err = nio.Out
					}
					break
				}
				return nio, cleanup, errorf("%s: ambiguous redirect", target)
			}
			// (from exec 3>file or inherited, never one of the process's
			// own descriptors: those belong to the runtime)
			s := sh.ioFd(nio, n)
			if s == nil || s.closed {
				return nio, cleanup, errorf("%d: bad file descriptor", n)
			}
			nio.setFd(fd, s)
			if strings.HasSuffix(target, "-") {
				nio.setFd(n, &Stream{closed: true})
			}
		case syntax.RedirHeredoc, syntax.RedirHeredocTabs:
			if fd < 0 {
				fd = 0
			}
			body, err2 := sh.expandString(r.Heredoc, base)
			if err2 != nil {
				return nio, cleanup, err2
			}
			s, err2 := docStream(body)
			if err2 != nil {
				return nio, cleanup, err2
			}
			opened = append(opened, s.File)
			nio.setFd(fd, s)
		case syntax.RedirHerestring:
			if fd < 0 {
				fd = 0
			}
			s, err2 := docStream(target + "\n")
			if err2 != nil {
				return nio, cleanup, err2
			}
			opened = append(opened, s.File)
			nio.setFd(fd, s)
		}
		if err != nil {
			return nio, cleanup, err
		}
	}
	return nio, cleanup, nil
}

// docStream returns the read end of a pipe that yields body, for a heredoc
// or here-string. Every command reading it shares one read position, as
// with a file: an external command that doesn't read its input leaves it
// for the next one (`while read l; do grep -q x $l; done <<EOF`). The
// writer stops when the reader is closed.
func docStream(body string) (*Stream, error) {
	r, w, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	go func() {
		w.WriteString(body)
		w.Close()
	}()
	return fileStream(r, false), nil
}

// devStream emulates /dev/stdin, /dev/stdout, /dev/stderr and /dev/fd/N
// for streams that are not real file descriptors.
func (sh *Shell) devStream(target string, io *IO) *Stream {
	switch target {
	case "/dev/stdin":
		return io.In
	case "/dev/stdout":
		return io.Out
	case "/dev/stderr":
		return io.Err
	}
	if strings.HasPrefix(target, "/dev/fd/") {
		n, err := strconv.Atoi(target[len("/dev/fd/"):])
		if err == nil && n <= 2 {
			return io.fd(n)
		}
		if err == nil {
			if s := sh.ioFd(io, n); s != nil && !s.closed {
				return s
			}
		}
	}
	return nil
}

func wordText(w *syntax.Word) string {
	if s, ok := w.LitString(); ok {
		return s
	}
	return "word"
}
