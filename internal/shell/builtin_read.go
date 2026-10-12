package shell

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/andrinoff/ash/internal/term"
	"golang.org/x/sys/unix"
)

// bRead implements both bash's and fish's read.
func bRead(sh *Shell, io *IO, args []string) int {
	var (
		raw, silent, list, line, shellMode bool
		prompt, initial                    string
		delim                              = "\n"
		nchars                             = -1
		timeout                            time.Duration
		hasTimeout                         bool
		o                                  = setOpts{scope: setAuto}
		names                              []string
		fd                                 = 0
		bashStyle                          bool
	)
	number := func(opt, v string) (int, bool) {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			fmt.Fprintf(io.Err, "read: %s: invalid number '%s'\n", opt, v)
			return 0, false
		}
		return n, true
	}
	setTimeout := func(v string) bool {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil || f < 0 {
			fmt.Fprintf(io.Err, "read: %s: invalid timeout specification\n", v)
			return false
		}
		timeout, hasTimeout = time.Duration(f*float64(time.Second)), true
		return true
	}
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
		if strings.HasPrefix(arg, "--") {
			opt, v, hasV := strings.Cut(arg, "=")
			value := func() string {
				if hasV {
					return v
				}
				if i+1 < len(a) {
					i++
					return a[i]
				}
				return ""
			}
			switch opt {
			case "--prompt-str", "--prompt":
				// fish's --prompt is a command; ash shows it as text
				prompt = value()
			case "--right-prompt":
				value()
			case "--command":
				initial = value()
			case "--silent":
				silent = true
			case "--nchars":
				n, ok := number(opt, value())
				if !ok {
					return 2
				}
				nchars = n
			case "--delimiter":
				delim = value()
			case "--list", "--array":
				list = true
			case "--null":
				delim = "\x00"
			case "--line":
				line = true
			case "--local":
				o.scope = setLocal
			case "--global":
				o.scope = setGlobal
			case "--universal":
				o.scope = setUniversal
			case "--function":
				o.scope = setFunction
			case "--export":
				o.export = 1
			case "--unexport":
				o.export = -1
			case "--shell", "--tokenize":
				shellMode = true
			}
			continue
		}
		// Short options, combined (-rs) and with attached values (-n1).
		for j := 1; j < len(arg); j++ {
			c := arg[j]
			attached := j+1 < len(arg)
			value := func() string {
				if attached {
					v := arg[j+1:]
					j = len(arg)
					return v
				}
				if i+1 < len(a) {
					i++
					return a[i]
				}
				return ""
			}
			switch c {
			case 'r':
				raw, bashStyle = true, true
			case 's':
				silent = true
			case 'e':
				// bash: use readline; the line editor is used at a terminal anyway
			case 'p', 'P':
				prompt = value()
			case 'n', 'N':
				n, ok := number("-"+string(c), value())
				if !ok {
					return 2
				}
				nchars = n
				if c == 'N' {
					delim = ""
				}
			case 't':
				// bash: -t SECONDS; fish: -t is --tokenize
				if attached || bashStyle || i+1 < len(a) && isNumber(a[i+1]) {
					if !setTimeout(value()) {
						return 2
					}
				} else {
					shellMode = true
				}
			case 'd':
				delim = value()
				if delim == "" {
					delim = "\x00"
				}
			case 'a':
				list = true
			case 'z':
				delim = "\x00"
			case 'L':
				line = true
			case 'u':
				n, ok := number("-u", value())
				if !ok {
					return 2
				}
				fd = n
			case 'i', 'c':
				initial = value() // text to start editing from
			case 'l':
				o.scope = setLocal
			case 'g':
				o.scope = setGlobal
			case 'U':
				o.scope = setUniversal
			case 'f':
				o.scope = setFunction
			case 'x':
				o.export = 1
			case 'S':
				shellMode = true
			}
		}
	}
	_ = shellMode
	if delim != "" {
		// bash uses only the first character of -d
		_, size := utf8.DecodeRuneInString(delim)
		delim = delim[:size]
	}
	noNames := len(names) == 0
	if noNames {
		names = []string{"REPLY"}
	}
	in := io.fd(fd)
	if in == nil {
		fmt.Fprintf(io.Err, "read: %d: invalid file descriptor\n", fd)
		return 1
	}
	if hasTimeout && timeout == 0 {
		// bash: just report whether input is waiting
		if inputReady(in) {
			return 0
		}
		return 1
	}
	spec := readSpec{delim: delim, nchars: nchars, raw: raw || line, stop: sh.interrupted}
	if hasTimeout {
		spec.deadline = time.Now().Add(timeout)
	}
	useEditor := in.TTY && sh.shared.readLine != nil && !silent && nchars < 0 && delim == "\n" && !hasTimeout
	var restore func()
	if !useEditor {
		if prompt != "" && in.TTY {
			io.Err.WriteString(prompt)
		}
		restore = readTermMode(in, silent, nchars >= 0 || delim != "\n", io.Err)
	}
	record := func() (*readText, readEnd) {
		if useEditor {
			return sh.readEditorLine(prompt, initial, spec.raw)
		}
		return readRecord(in, &spec)
	}
	defer func() {
		if restore != nil {
			restore()
		}
	}()
	status := func(end readEnd) int {
		switch end {
		case readTimeout:
			return 142
		case readInterrupted:
			return 130
		}
		return 1
	}
	if line && !list && !noNames {
		// fish: one line per variable
		for i, n := range names {
			t, end := record()
			if end != readOK && len(t.b) == 0 {
				for _, rest := range names[i:] {
					sh.SetVar(rest, []string{}, o)
				}
				return status(end)
			}
			sh.SetVar(n, []string{string(t.b)}, o)
			if end != readOK {
				for _, rest := range names[i+1:] {
					sh.SetVar(rest, []string{}, o)
				}
				return status(end)
			}
		}
		return 0
	}
	t, end := record()
	if end != readOK && len(t.b) == 0 {
		for _, n := range names {
			sh.SetVar(n, []string{}, o)
		}
		return status(end)
	}
	ifs := " \t\n"
	if v, ok := sh.getVar("IFS"); ok {
		ifs = strings.Join(v, "")
	}
	switch {
	case list:
		sh.SetVar(names[0], t.fields(ifs), o)
	case noNames || line:
		// bash keeps the whole line in REPLY, whitespace and all
		sh.SetVar(names[0], []string{string(t.b)}, o)
	default:
		for i, f := range t.split(ifs, len(names)) {
			sh.SetVar(names[i], []string{f}, o)
		}
	}
	if end != readOK {
		return status(end)
	}
	return 0
}

func isNumber(s string) bool {
	_, err := strconv.ParseFloat(s, 64)
	return err == nil
}

// readText is what read got: the text with backslash escapes removed,
// and which bytes were escaped (those never split fields). esc stays
// nil until something is escaped.
type readText struct {
	b   []byte
	esc []bool
}

func (t *readText) add(c byte, escaped bool) {
	if escaped && t.esc == nil {
		t.esc = make([]bool, len(t.b), cap(t.b))
	}
	t.b = append(t.b, c)
	if t.esc != nil {
		t.esc = append(t.esc, escaped)
	}
}

func (t *readText) trim(n int) {
	t.b = t.b[:n]
	if t.esc != nil {
		t.esc = t.esc[:n]
	}
}

// ifsScanner walks read's text for field splitting, by byte offset.
// Escaped characters never separate fields, and fields are cut from the
// original bytes, so text that isn't UTF-8 is kept as is.
type ifsScanner struct {
	t   *readText
	ifs string
}

// sep reports whether the character at byte offset i is an unescaped
// $IFS character, and its length.
func (s *ifsScanner) sep(i int) (bool, int) {
	c := s.t.b[i]
	r, size := rune(c), 1
	if c >= utf8.RuneSelf {
		r, size = utf8.DecodeRune(s.t.b[i:])
	}
	if s.t.esc != nil && s.t.esc[i] {
		return false, size
	}
	return strings.ContainsRune(s.ifs, r), size
}

// space reports whether byte i is IFS whitespace.
func (s *ifsScanner) space(i int) bool {
	c := s.t.b[i]
	if c != ' ' && c != '\t' && c != '\n' || s.t.esc != nil && s.t.esc[i] {
		return false
	}
	return strings.IndexByte(s.ifs, c) >= 0
}

func (s *ifsScanner) skipSpace(i int) int {
	for i < len(s.t.b) && s.space(i) {
		i++
	}
	return i
}

// word returns the field at i and where the next one starts. As in
// bash, a non-whitespace separator together with the IFS whitespace
// around it ends one field, so "a : b" with IFS=" :" is two fields.
func (s *ifsScanner) word(i int) (string, int) {
	b := s.t.b
	i = s.skipSpace(i)
	start, size := i, 0
	for i < len(b) {
		var isSep bool
		if isSep, size = s.sep(i); isSep {
			break
		}
		i += size
	}
	w := string(b[start:i])
	if i == len(b) {
		return w, i
	}
	white := s.space(i)
	i = s.skipSpace(i + size)
	if white && i < len(b) && !s.space(i) {
		if isSep, size := s.sep(i); isSep {
			i = s.skipSpace(i + size)
		}
	}
	return w, i
}

// split assigns the text to n variables the way bash's read does: fields
// are separated by $IFS, IFS whitespace around them is dropped, and the
// last variable gets the rest of the line.
func (t *readText) split(ifs string, n int) []string {
	s := &ifsScanner{t: t, ifs: ifs}
	i := s.skipSpace(0)
	out := make([]string, 0, n)
	for len(out) < n-1 {
		if i >= len(t.b) {
			out = append(out, "")
			continue
		}
		var w string
		w, i = s.word(i)
		out = append(out, w)
	}
	last := ""
	if i < len(t.b) {
		if w, next := s.word(i); next >= len(t.b) {
			// a single field: drop a trailing separator ("x:" gives "x")
			last = w
		} else {
			end := len(t.b)
			for end > i && s.space(end-1) {
				end--
			}
			last = string(t.b[i:end])
		}
	}
	return append(out, last)
}

// fields splits the text on $IFS into a list (read -a).
func (t *readText) fields(ifs string) []string {
	if ifs == "" {
		if len(t.b) == 0 {
			return []string{}
		}
		return []string{string(t.b)}
	}
	s := &ifsScanner{t: t, ifs: ifs}
	out := []string{}
	for i := s.skipSpace(0); i < len(t.b); {
		var w string
		w, i = s.word(i)
		out = append(out, w)
	}
	return out
}

type readEnd uint8

const (
	readOK          readEnd = iota // got a whole record
	readEOF                        // end of input (or an error)
	readTimeout                    // -t ran out
	readInterrupted                // ctrl-c
)

type readSpec struct {
	delim    string // record terminator; "" for none (read -N)
	nchars   int    // stop after this many characters, -1 for no limit
	raw      bool   // no backslash processing (-r)
	deadline time.Time
	stop     func() bool // reports an interrupt
}

// readRecord reads one record from in without consuming input past it.
// Unless raw, a backslash makes the next character literal (it can't end
// the record or split fields) and backslash-newline continues the line.
func readRecord(in *Stream, spec *readSpec) (*readText, readEnd) {
	src := newByteSource(in, spec)
	defer src.finish()
	t := &readText{}
	chars, tail := 0, 0 // characters read; continuation bytes still due
	escape := false
	delim := []byte(spec.delim)
	for {
		if spec.nchars >= 0 && chars >= spec.nchars && tail == 0 {
			return t, readOK
		}
		c, end := src.next()
		if end != readOK {
			return t, end
		}
		if !spec.raw && !escape && c == '\\' {
			escape = true
			continue
		}
		escaped := escape
		escape = false
		if escaped && c == '\n' {
			continue // line continuation
		}
		t.add(c, escaped)
		if tail > 0 && c&0xc0 == 0x80 {
			tail--
		} else {
			chars++
			tail = utf8Tail(c)
		}
		if len(delim) > 0 && !escaped && c == delim[len(delim)-1] && endsWithDelim(t, delim) {
			t.trim(len(t.b) - len(delim))
			return t, readOK
		}
	}
}

func endsWithDelim(t *readText, delim []byte) bool {
	n := len(t.b) - len(delim)
	if n < 0 || string(t.b[n:]) != string(delim) {
		return false
	}
	if t.esc != nil {
		for _, e := range t.esc[n:] {
			if e {
				return false
			}
		}
	}
	return true
}

// utf8Tail returns how many continuation bytes follow lead byte c.
func utf8Tail(c byte) int {
	switch {
	case c >= 0xc0 && c < 0xe0:
		return 1
	case c >= 0xe0 && c < 0xf0:
		return 2
	case c >= 0xf0 && c < 0xf8:
		return 3
	}
	return 0
}

// byteSource reads a stream one byte at a time without taking more than
// it returns: a regular file is read in blocks and the offset is moved
// back afterwards (like bash), anything else a byte per read call.
type byteSource struct {
	in    *Stream
	spec  *readSpec
	r     io.Reader
	br    *bufio.Reader
	file  *os.File // regular file, read in blocks
	block *[4096]byte
	buf   []byte
	pos   int
	poll  bool // wait with poll so timeouts and ctrl-c work
	one   [1]byte
	ended bool
}

var readBufs = sync.Pool{New: func() any { return new([4096]byte) }}

func newByteSource(in *Stream, spec *readSpec) *byteSource {
	s := &byteSource{in: in, spec: spec, r: in}
	if in.File == nil {
		if br, ok := in.R.(*bufio.Reader); ok {
			s.br = br
		}
		return s
	}
	st, err := in.File.Stat()
	if err == nil && st.Mode().IsRegular() {
		s.file = in.File
		return s
	}
	s.poll = !spec.deadline.IsZero() ||
		err == nil && st.Mode()&os.ModeCharDevice != 0 && term.IsTerminal(int(in.File.Fd()))
	return s
}

func (s *byteSource) next() (byte, readEnd) {
	if s.ended {
		return 0, readEOF
	}
	switch {
	case s.file != nil:
		if s.pos >= len(s.buf) {
			if s.buf == nil {
				s.block = readBufs.Get().(*[4096]byte)
				s.buf = s.block[:]
			}
			n, _ := s.file.Read(s.buf[:cap(s.buf)])
			s.buf, s.pos = s.buf[:n], 0
			if n == 0 {
				s.ended = true
				return 0, readEOF
			}
		}
		c := s.buf[s.pos]
		s.pos++
		return c, readOK
	case s.br != nil:
		c, err := s.br.ReadByte()
		if err != nil {
			s.ended = true
			return 0, readEOF
		}
		return c, readOK
	}
	for {
		if s.poll {
			if end := s.wait(); end != readOK {
				return 0, end
			}
		}
		n, err := s.r.Read(s.one[:])
		if n == 1 {
			return s.one[0], readOK
		}
		if err != nil {
			if errors.Is(err, syscall.EINTR) {
				continue
			}
			if errors.Is(err, syscall.EAGAIN) && s.in.File != nil {
				s.poll = true // a non-blocking descriptor: wait instead of spinning
				continue
			}
			s.ended = true
			return 0, readEOF
		}
	}
}

// wait blocks until the file has input, the deadline passes or the
// shell is interrupted.
func (s *byteSource) wait() readEnd {
	fd := int32(s.in.File.Fd())
	for {
		ms := 100
		if !s.spec.deadline.IsZero() {
			left := time.Until(s.spec.deadline)
			if left <= 0 {
				return readTimeout
			}
			if left < 100*time.Millisecond {
				ms = int(left/time.Millisecond) + 1
			}
		}
		fds := []unix.PollFd{{Fd: fd, Events: unix.POLLIN}}
		n, err := unix.Poll(fds, ms)
		if s.spec.stop != nil && s.spec.stop() {
			return readInterrupted
		}
		if err != nil && err != unix.EINTR {
			return readOK // let the read report the error
		}
		if n > 0 {
			return readOK
		}
	}
}

// finish gives back what was read past the record.
func (s *byteSource) finish() {
	if s.file != nil && s.pos < len(s.buf) {
		s.file.Seek(int64(s.pos-len(s.buf)), io.SeekCurrent)
	}
	if s.block != nil {
		readBufs.Put(s.block)
		s.block, s.buf = nil, nil
	}
}

// inputReady reports whether reading in would not block (read -t 0).
func inputReady(in *Stream) bool {
	if in.File == nil {
		return in.R != nil
	}
	fds := []unix.PollFd{{Fd: int32(in.File.Fd()), Events: unix.POLLIN}}
	n, err := unix.Poll(fds, 0)
	return err == nil && n > 0
}

// readTermMode prepares a terminal for read: no echo for -s, and
// character-at-a-time input for -n, -N and -d, like bash. It returns a
// function that restores the settings, or nil.
func readTermMode(in *Stream, silent, cbreak bool, errOut *Stream) func() {
	if !silent && !cbreak || in.File == nil {
		return nil
	}
	fd := int(in.File.Fd())
	st, err := term.GetState(fd)
	if err != nil {
		return nil
	}
	ns := *st
	if silent {
		ns.Lflag &^= unix.ECHO
	}
	if cbreak {
		ns.Lflag &^= unix.ICANON
		ns.Cc[unix.VMIN] = 1
		ns.Cc[unix.VTIME] = 0
	}
	term.SetState(fd, &ns)
	return func() {
		term.SetState(fd, st)
		if silent {
			errOut.WriteString("\n")
		}
	}
}

// readEditorLine reads a line with the interactive editor, handling
// backslashes like readRecord (a trailing one continues the line).
func (sh *Shell) readEditorLine(prompt, initial string, raw bool) (*readText, readEnd) {
	t := &readText{}
	for {
		s, err := sh.shared.readLine(prompt, initial)
		if err != nil {
			if sh.interrupted() {
				return t, readInterrupted
			}
			return t, readEOF
		}
		if raw {
			for i := 0; i < len(s); i++ {
				t.add(s[i], false)
			}
			return t, readOK
		}
		more := false
		for i := 0; i < len(s); i++ {
			if s[i] != '\\' {
				t.add(s[i], false)
				continue
			}
			if i+1 == len(s) {
				more = true
				break
			}
			i++
			if s[i] != '\n' { // the editor may already have joined a continuation line
				t.add(s[i], true)
			}
		}
		if !more {
			return t, readOK
		}
		prompt, initial = "", ""
	}
}
