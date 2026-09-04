package shell

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/andrinoff/ash/internal/term"
	"golang.org/x/sys/unix"
)

// bRead implements both bash's and fish's read.
func bRead(sh *Shell, io *IO, args []string) int {
	var (
		raw, silent, list, null, line, shellMode bool
		prompt, delim                            = "", "\n"
		nchars                                   = -1
		timeout                                  time.Duration
		o                                        = setOpts{scope: setAuto}
		names                                    []string
		fd                                       = 0
		arrayName                                string
		bashStyle                                bool
	)
	a := args[1:]
	for i := 0; i < len(a); i++ {
		arg := a[i]
		if arg == "--" {
			names = append(names, a[i+1:]...)
			break
		}
		if !strings.HasPrefix(arg, "-") || len(arg) < 2 {
			names = append(names, arg)
			continue
		}
		val := func() string {
			if i+1 < len(a) {
				i++
				return a[i]
			}
			return ""
		}
		switch arg {
		case "-r":
			raw, bashStyle = true, true
			continue
		case "-p", "--prompt-str", "-P":
			prompt = val()
			continue
		case "--prompt":
			// fish: a command whose output is the prompt
			prompt = val()
			continue
		case "-s", "--silent":
			silent = true
			continue
		case "-n", "--nchars":
			nchars, _ = strconv.Atoi(val())
			continue
		case "-t":
			if bashStyle || i+1 < len(a) && isNumber(a[i+1]) {
				f, _ := strconv.ParseFloat(val(), 64)
				timeout = time.Duration(f * float64(time.Second))
			} else {
				shellMode = true
			}
			continue
		case "--tokenize":
			shellMode = true
			continue
		case "-d", "--delimiter":
			delim = val()
			if delim == "" {
				delim = "\x00"
			}
			continue
		case "-a", "--list", "--array":
			list = true
			if bashStyle || i+1 < len(a) && !strings.HasPrefix(a[i+1], "-") && len(names) == 0 {
				arrayName = val()
			}
			continue
		case "-z", "--null":
			null = true
			delim = "\x00"
			continue
		case "-L", "--line":
			line = true
			continue
		case "-u":
			fd, _ = strconv.Atoi(val())
			continue
		case "-l", "--local":
			o.scope = setLocal
			continue
		case "-g", "--global":
			o.scope = setGlobal
			continue
		case "-U", "--universal":
			o.scope = setUniversal
			continue
		case "-f", "--function":
			o.scope = setFunction
			continue
		case "-x", "--export":
			o.export = 1
			continue
		case "-c", "--command":
			val()
			continue
		case "-S", "--shell":
			shellMode = true
			continue
		}
		// combined short flags like -rs
		for _, c := range arg[1:] {
			switch c {
			case 'r':
				raw = true
			case 's':
				silent = true
			case 'l':
				o.scope = setLocal
			case 'g':
				o.scope = setGlobal
			case 'x':
				o.export = 1
			case 'a':
				list = true
			case 'z':
				null = true
				delim = "\x00"
			case 'L':
				line = true
			}
		}
	}
	_ = null
	_ = shellMode
	if arrayName != "" {
		names = append([]string{arrayName}, names...)
	}
	if len(names) == 0 {
		names = []string{"REPLY"}
	}
	in := io.fd(fd)
	if in == nil {
		fmt.Fprintf(io.Err, "read: %d: invalid file descriptor\n", fd)
		return 1
	}
	var text string
	var ok bool
	if in.TTY && sh.shared.readLine != nil && !silent && nchars < 0 && delim == "\n" && timeout == 0 {
		s, err := sh.shared.readLine(prompt)
		if err != nil {
			return 1
		}
		text, ok = s, true
	} else {
		if prompt != "" && in.TTY {
			io.Err.WriteString(prompt)
		}
		var restore func()
		if silent && in.File != nil && term.IsTerminal(int(in.File.Fd())) {
			fdn := int(in.File.Fd())
			if st, err := term.GetState(fdn); err == nil {
				ns := *st
				ns.Lflag &^= unix.ECHO
				term.SetState(fdn, &ns)
				restore = func() { term.SetState(fdn, st); io.Err.WriteString("\n") }
			}
		}
		text, ok = readUntil(in, delim, nchars, timeout)
		if restore != nil {
			restore()
		}
	}
	if !ok && text == "" {
		for _, n := range names {
			sh.SetVar(n, []string{}, o)
		}
		return 1
	}
	if !raw && !line {
		// backslash-newline continuation and escapes (bash without -r)
		text = strings.ReplaceAll(text, "\\\n", "")
		var b strings.Builder
		for i := 0; i < len(text); i++ {
			if text[i] == '\\' && i+1 < len(text) {
				i++
			}
			b.WriteByte(text[i])
		}
		text = b.String()
	}
	if list {
		sh.SetVar(names[0], sh.ifsSplit(text), o)
		if !ok {
			return 1
		}
		return 0
	}
	if len(names) == 1 {
		v := text
		if !line {
			v = strings.Trim(v, " \t\n")
		}
		sh.SetVar(names[0], []string{v}, o)
	} else {
		fields := splitN(sh, text, len(names))
		for i, n := range names {
			if i < len(fields) {
				sh.SetVar(n, []string{fields[i]}, o)
			} else {
				sh.SetVar(n, []string{}, o)
			}
		}
	}
	if !ok {
		return 1
	}
	return 0
}

func isNumber(s string) bool {
	_, err := strconv.ParseFloat(s, 64)
	return err == nil
}

// splitN splits on IFS into at most n fields, the last taking the rest.
func splitN(sh *Shell, s string, n int) []string {
	ifs := " \t\n"
	if v, ok := sh.getVar("IFS"); ok {
		ifs = strings.Join(v, "")
	}
	isIFS := func(r rune) bool { return strings.ContainsRune(ifs, r) }
	s = strings.TrimLeftFunc(s, func(r rune) bool { return isIFS(r) && (r == ' ' || r == '\t' || r == '\n') })
	var out []string
	for len(out) < n-1 && s != "" {
		i := strings.IndexFunc(s, isIFS)
		if i < 0 {
			break
		}
		out = append(out, s[:i])
		s = s[i:]
		s = strings.TrimLeftFunc(s, func(r rune) bool { return r == ' ' || r == '\t' || r == '\n' })
		if s != "" && isIFS([]rune(s)[0]) {
			s = s[1:]
			s = strings.TrimLeftFunc(s, func(r rune) bool { return r == ' ' || r == '\t' || r == '\n' })
		}
	}
	s = strings.TrimRight(s, " \t\n")
	if s != "" || len(out) < n {
		out = append(out, s)
	}
	return out
}

// readUntil reads one record from a stream without reading past it.
func readUntil(in *Stream, delim string, nchars int, timeout time.Duration) (string, bool) {
	var b []byte
	buf := make([]byte, 1)
	deadline := time.Time{}
	if timeout > 0 {
		deadline = time.Now().Add(timeout)
	}
	var r io.Reader = in
	if in.File == nil && in.R != nil {
		if br, ok := in.R.(*bufio.Reader); ok {
			r = br
		}
	}
	for {
		if nchars >= 0 && len([]rune(string(b))) >= nchars {
			return string(b), true
		}
		if !deadline.IsZero() && in.File != nil {
			left := time.Until(deadline)
			if left <= 0 {
				return string(b), false
			}
			fds := []unix.PollFd{{Fd: int32(in.File.Fd()), Events: unix.POLLIN}}
			n, err := unix.Poll(fds, int(left/time.Millisecond)+1)
			if err != nil || n == 0 {
				return string(b), false
			}
		}
		n, err := r.Read(buf)
		if n == 1 {
			if string(buf[0]) == delim {
				return string(b), true
			}
			b = append(b, buf[0])
			continue
		}
		if err != nil {
			return string(b), false
		}
	}
}
