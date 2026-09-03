// Package shell is the ash interpreter.
package shell

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/andrinoff/ash/internal/syntax"
	"github.com/andrinoff/ash/internal/term"
)

const Version = "0.1.0"

// Editor is the part of the line editor the interpreter talks to, for the
// commandline and bind builtins.
type Editor interface {
	Buffer() (string, int)
	SetBuffer(buf string, cursor int)
	QueueFunctions(names ...string)
	Bind(mode, key, command string, setsMode string) error
	Unbind(mode, key string) bool
	Bindings() []string
	SetKeymap(name string)
	Keymap() string
	Repaint()
	PagerActive() bool
}

// History is the part of history the interpreter needs.
type History interface {
	Recent(n int) []string
	Search(query string, prefix bool) []string
	Delete(cmd string) int
	Clear()
	Import(kind string) (int, error)
}

// Shell holds interpreter state. Subshells are cheap clones.
type Shell struct {
	Interactive bool
	Login       bool
	JobControl  bool
	NoConfig    bool

	global *scope
	scope  *scope

	funcs    map[string]*Function
	funcsOwn bool
	aliases  map[string]string
	aliasOwn bool

	shared *shared // state common to the main shell and every subshell

	dir      string
	dirStack []string
	dirPrev  []string
	dirNext  []string

	status      int
	pipestatus  []int
	lastBg      int
	lastArg     string
	arg0        string
	lineno      int
	cmdDuration time.Duration

	ctrl       ctrlKind
	ctrlLevel  int
	exitStatus int

	opts       options
	noErrexit  int
	depth      int
	aliasStack []string

	subshell   bool
	inSubst    int
	inBlock    int
	jobCtx     *job       // enclosing job for commands run inside a pipeline/background subshell
	procFds    []*os.File // pipes from <(...) waiting for the current command
	curSrc     string     // source of the top-level code being run
	curStmt    string     // text of the current top-level statement (for job names)
	substCount int        // number of command substitutions run (for assignment status)

	callStack []frame
	srcStack  []string

	startTime time.Time
	rand      *rand.Rand

	Stdin  *os.File
	Stdout *os.File
	Stderr *os.File

	Editor  Editor
	History History

	// Hooks set by the interactive frontend.
	PromptHook  func(*Shell)
	OnDirChange func(dir string)
}

type frame struct {
	name string
	file string
}

type ctrlKind uint8

const (
	ctrlNone ctrlKind = iota
	ctrlBreak
	ctrlContinue
	ctrlReturn
	ctrlExit
)

type options struct {
	errexit   bool
	nounset   bool
	xtrace    bool
	pipefail  bool
	noclobber bool
	noglob    bool
	verbose   bool
	vi        bool
}

// shared is state that every subshell sees, not a copy of.
type shared struct {
	mu        sync.Mutex
	jobs      *jobTable
	intr      atomic.Bool
	paths     *pathCache
	events    *events
	abbrs     *abbrTable
	complete  *completions
	universal *universalStore
	traps     *traps
	theme     *Theme
	autoload  *autoloader
	integr    *integrations
	hooks     *hooks
	actions   *childActions
	ttyFd     int
	shellPgid int
	tmodes    atomic.Pointer[shellTmodes]
	self      string
	session   string
	termCols  atomic.Int32
	termRows  atomic.Int32
	readLine  func(prompt string) (string, error)
	transient atomic.Bool
}

// New creates a shell with the environment of the current process.
func New() *Shell {
	sh := &Shell{
		funcs:     map[string]*Function{},
		funcsOwn:  true,
		aliases:   map[string]string{},
		aliasOwn:  true,
		startTime: time.Now(),
		rand:      rand.New(rand.NewSource(time.Now().UnixNano())),
		Stdin:     os.Stdin,
		Stdout:    os.Stdout,
		Stderr:    os.Stderr,
		arg0:      "ash",
	}
	sh.global = newScope(nil, scopeGlobal)
	sh.scope = sh.global
	sh.shared = &shared{
		jobs:      newJobTable(),
		paths:     newPathCache(),
		events:    newEvents(),
		abbrs:     newAbbrTable(),
		complete:  newCompletions(),
		universal: &universalStore{},
		traps:     newTraps(),
		theme:     DefaultTheme(),
		autoload:  newAutoloader(),
		integr:    &integrations{},
		hooks:     &hooks{},
		actions:   &childActions{},
		ttyFd:     -1,
	}
	if exe, err := os.Executable(); err == nil {
		sh.shared.self = exe
	}
	sh.shared.session = randomKey(sh.rand, 16)
	sh.importEnv(os.Environ())
	wd, err := os.Getwd()
	if err != nil {
		wd = "/"
	}
	if pwd := sh.Get("PWD"); pwd != "" && filepath.IsAbs(pwd) && sameFile(pwd, wd) {
		wd = pwd
	}
	sh.dir = wd
	sh.SetVar("PWD", []string{wd}, setOpts{scope: setGlobal, export: 1})
	sh.initDefaults()
	return sh
}

func randomKey(r *rand.Rand, n int) string {
	const chars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, n)
	for i := range b {
		b[i] = chars[r.Intn(len(chars))]
	}
	return string(b)
}

func sameFile(a, b string) bool {
	sa, err1 := os.Stat(a)
	sb, err2 := os.Stat(b)
	return err1 == nil && err2 == nil && os.SameFile(sa, sb)
}

func (sh *Shell) initDefaults() {
	set := func(name, val string) {
		if !sh.IsSet(name) {
			sh.global.put(name, &Var{Vals: []string{val}})
		}
	}
	if !sh.IsSet("HOME") {
		if h, err := os.UserHomeDir(); err == nil {
			sh.global.put("HOME", &Var{Vals: []string{h}, Export: true})
		}
	}
	if !sh.IsSet("PATH") {
		sh.global.put("PATH", &Var{Vals: []string{"/usr/local/bin", "/usr/bin", "/bin", "/usr/sbin", "/sbin"}, Export: true, Path: true})
	}
	set("IFS", " \t\n")
	host, _ := os.Hostname()
	if i := strings.IndexByte(host, '.'); i > 0 {
		host = host[:i]
	}
	set("hostname", host)
	if !sh.IsSet("HOSTNAME") {
		sh.global.put("HOSTNAME", &Var{Vals: []string{host}})
	}
	if !sh.IsSet("USER") {
		if u := os.Getenv("LOGNAME"); u != "" {
			sh.global.put("USER", &Var{Vals: []string{u}, Export: true})
		}
	}
	sh.global.put("ASH_VERSION", &Var{Vals: []string{Version}})
	sh.global.put("version", &Var{Vals: []string{Version}})
	set("ash_greeting", "")
}

// Init finishes setup that depends on Interactive/Login being set.
func (sh *Shell) Init() {
	lvl := 0
	if v := sh.Get("SHLVL"); v != "" {
		fmt.Sscan(v, &lvl)
	}
	if sh.Interactive {
		lvl++
	}
	if lvl < 1 {
		lvl = 1
	}
	sh.SetVar("SHLVL", []string{fmt.Sprint(lvl)}, setOpts{scope: setGlobal, export: 1})
	if sh.Interactive {
		sh.setupTerminal()
	}
}

// Self returns the path of the running ash binary.
func (sh *Shell) Self() string {
	if sh.shared.self != "" {
		return sh.shared.self
	}
	return "ash"
}

// SessionKey is a random per-session key (used for STARSHIP_SESSION_KEY).
func (sh *Shell) SessionKey() string { return sh.shared.session }

// Dir is the shell's current directory.
func (sh *Shell) Dir() string { return sh.dir }

// Status is the exit status of the last command.
func (sh *Shell) Status() int { return sh.status }

// PipeStatus returns the statuses of the last pipeline.
func (sh *Shell) PipeStatus() []int {
	if len(sh.pipestatus) == 0 {
		return []int{sh.status}
	}
	return sh.pipestatus
}

// CmdDuration returns how long the last interactive command took.
func (sh *Shell) CmdDuration() time.Duration { return sh.cmdDuration }

// SetCmdDuration records the duration of the last interactive command.
func (sh *Shell) SetCmdDuration(d time.Duration) { sh.cmdDuration = d }

// clone makes a subshell: same state, but changes don't leak back.
func (sh *Shell) clone() *Shell {
	c := *sh
	memo := map[*scope]*scope{}
	c.scope = cloneChain(sh.scope, memo)
	c.global = cloneChain(sh.global, memo)
	c.funcsOwn = false
	sh.funcsOwn = false
	c.aliasOwn = false
	sh.aliasOwn = false
	c.dirStack = append([]string(nil), sh.dirStack...)
	c.callStack = append([]frame(nil), sh.callStack...)
	c.aliasStack = append([]string(nil), sh.aliasStack...)
	c.srcStack = append([]string(nil), sh.srcStack...)
	c.subshell = true
	c.procFds = nil // pending <(...) pipes belong to the parent's command
	c.ctrl = ctrlNone
	c.rand = rand.New(rand.NewSource(sh.rand.Int63()))
	return &c
}

func (sh *Shell) setFunc(name string, fn *Function) {
	if !sh.funcsOwn {
		m := make(map[string]*Function, len(sh.funcs)+1)
		for k, v := range sh.funcs {
			m[k] = v
		}
		sh.funcs = m
		sh.funcsOwn = true
	}
	if fn == nil {
		delete(sh.funcs, name)
	} else {
		sh.funcs[name] = fn
	}
}

func (sh *Shell) setAlias(name, val string, del bool) {
	if !sh.aliasOwn {
		m := make(map[string]string, len(sh.aliases)+1)
		for k, v := range sh.aliases {
			m[k] = v
		}
		sh.aliases = m
		sh.aliasOwn = true
	}
	if del {
		delete(sh.aliases, name)
	} else {
		sh.aliases[name] = val
	}
}

// Interrupt flags the running code to stop (Ctrl-C).
func (sh *Shell) Interrupt() { sh.shared.intr.Store(true) }

// ClearInterrupt resets the interrupt flag.
func (sh *Shell) ClearInterrupt() { sh.shared.intr.Store(false) }

func (sh *Shell) interrupted() bool { return sh.shared.intr.Load() }

// Stream is one end of a command's standard I/O. Builtins read and write
// through it; external commands need File, which is created on demand.
type Stream struct {
	File *os.File
	W    io.Writer
	R    io.Reader
	TTY  bool
	// Inherited is set on the shell's own standard input. Builtins that
	// read stdin when given no arguments (count, string, path) only do so
	// when input was piped or redirected to them, like fish.
	Inherited bool
	broken    *atomic.Bool
	closed    bool
}

func fileStream(f *os.File, tty bool) *Stream {
	return &Stream{File: f, TTY: tty}
}

func (s *Stream) Write(p []byte) (int, error) {
	if s == nil || s.closed {
		return 0, syscall.EBADF
	}
	var n int
	var err error
	if s.File != nil {
		n, err = s.File.Write(p)
	} else if s.W != nil {
		n, err = s.W.Write(p)
	} else {
		return len(p), nil
	}
	if err != nil && errors.Is(err, syscall.EPIPE) && s.broken != nil {
		s.broken.Store(true)
	}
	return n, err
}

func (s *Stream) WriteString(str string) (int, error) { return s.Write([]byte(str)) }

func (s *Stream) Read(p []byte) (int, error) {
	if s == nil || s.closed {
		return 0, syscall.EBADF
	}
	if s.File != nil {
		return s.File.Read(p)
	}
	if s.R != nil {
		return s.R.Read(p)
	}
	return 0, io.EOF
}

// IO is the set of streams a command runs with.
type IO struct {
	In, Out, Err *Stream
	Extra        map[int]*Stream
}

func (o *IO) clone() *IO {
	c := *o
	if o.Extra != nil {
		c.Extra = make(map[int]*Stream, len(o.Extra))
		for k, v := range o.Extra {
			c.Extra[k] = v
		}
	}
	return &c
}

func (o *IO) fd(n int) *Stream {
	switch n {
	case 0:
		return o.In
	case 1:
		return o.Out
	case 2:
		return o.Err
	}
	return o.Extra[n]
}

func (o *IO) setFd(n int, s *Stream) {
	switch n {
	case 0:
		o.In = s
	case 1:
		o.Out = s
	case 2:
		o.Err = s
	default:
		if o.Extra == nil {
			o.Extra = map[int]*Stream{}
		}
		if s == nil {
			delete(o.Extra, n)
		} else {
			o.Extra[n] = s
		}
	}
}

// ReadsInput reports whether a builtin given no arguments should read
// its input: stdin must be piped or redirected, not the shell's own.
func (s *Stream) ReadsInput() bool {
	return s != nil && !s.TTY && !s.Inherited && !s.closed && (s.File != nil || s.R != nil)
}

// StdIO returns the shell's top-level streams.
func (sh *Shell) StdIO() *IO {
	tty := sh.shared.ttyFd >= 0
	in := fileStream(sh.Stdin, tty && term.IsTerminal(int(sh.Stdin.Fd())))
	in.Inherited = true
	return &IO{
		In:  in,
		Out: fileStream(sh.Stdout, tty && term.IsTerminal(int(sh.Stdout.Fd()))),
		Err: fileStream(sh.Stderr, tty && term.IsTerminal(int(sh.Stderr.Fd()))),
	}
}

// lockedBuffer collects command substitution output.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type shellError struct{ msg string }

func (e *shellError) Error() string { return e.msg }

func errorf(format string, args ...any) error {
	return &shellError{msg: fmt.Sprintf(format, args...)}
}

var errNotSet = errors.New("not set")

// errorMsg prints "ash: msg" to the given stream.
func (sh *Shell) errorMsg(w io.Writer, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if len(sh.srcStack) > 0 && !sh.Interactive {
		fmt.Fprintf(w, "ash: %s: %s\n", sh.srcStack[len(sh.srcStack)-1], msg)
		return
	}
	fmt.Fprintf(w, "ash: %s\n", msg)
}

func (sh *Shell) termSize() (int, int) {
	if c := sh.shared.termCols.Load(); c > 0 {
		return int(c), int(sh.shared.termRows.Load())
	}
	fd := sh.shared.ttyFd
	if fd < 0 {
		fd = int(sh.Stdout.Fd())
	}
	return term.Size(fd)
}

// SetTermSize records the terminal size (from SIGWINCH).
func (sh *Shell) SetTermSize(cols, rows int) {
	sh.shared.termCols.Store(int32(cols))
	sh.shared.termRows.Store(int32(rows))
}

// SetReadLine lets `read` use the interactive editor.
func (sh *Shell) SetReadLine(f func(prompt string) (string, error)) { sh.shared.readLine = f }

func (sh *Shell) optFlags() string {
	var b strings.Builder
	if sh.opts.errexit {
		b.WriteByte('e')
	}
	if sh.Interactive {
		b.WriteByte('i')
	}
	if sh.JobControl {
		b.WriteByte('m')
	}
	if sh.opts.noglob {
		b.WriteByte('f')
	}
	if sh.opts.nounset {
		b.WriteByte('u')
	}
	if sh.opts.verbose {
		b.WriteByte('v')
	}
	if sh.opts.xtrace {
		b.WriteByte('x')
	}
	if sh.opts.noclobber {
		b.WriteByte('C')
	}
	return b.String()
}

// HomeDir returns $HOME.
func (sh *Shell) HomeDir() string {
	if h := sh.Get("HOME"); h != "" {
		return h
	}
	h, _ := os.UserHomeDir()
	return h
}

// absPath resolves p against the shell's directory.
func (sh *Shell) absPath(p string) string {
	if p == "" {
		return sh.dir
	}
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(sh.dir, p)
}

// ConfigDir is ~/.config/ash (respecting XDG_CONFIG_HOME).
func (sh *Shell) ConfigDir() string {
	if x := sh.Get("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "ash")
	}
	return filepath.Join(sh.HomeDir(), ".config", "ash")
}

// DataDir is ~/.local/share/ash (respecting XDG_DATA_HOME).
func (sh *Shell) DataDir() string {
	if x := sh.Get("XDG_DATA_HOME"); x != "" {
		return filepath.Join(x, "ash")
	}
	return filepath.Join(sh.HomeDir(), ".local", "share", "ash")
}

// CacheDir is ~/.cache/ash (respecting XDG_CACHE_HOME).
func (sh *Shell) CacheDir() string {
	if x := sh.Get("XDG_CACHE_HOME"); x != "" {
		return filepath.Join(x, "ash")
	}
	return filepath.Join(sh.HomeDir(), ".cache", "ash")
}

// parse parses source code, using fish quoting for .fish files.
func parse(src, file string) (*syntax.File, error) {
	return syntax.ParseOpts(src, syntax.Options{Lenient: file != "", Fish: strings.HasSuffix(file, ".fish")})
}

// FormatParseError renders a parse error with the offending line.
func FormatParseError(src, file string, err error) string {
	e, ok := err.(*syntax.Error)
	if !ok {
		return err.Error()
	}
	line, col := syntax.LineCol(src, e.Pos)
	lines := strings.Split(src, "\n")
	var b strings.Builder
	if file != "" {
		fmt.Fprintf(&b, "%s:%d:%d: %s\n", file, line, col, e.Msg)
	} else {
		fmt.Fprintf(&b, "syntax error: %s\n", e.Msg)
	}
	if line-1 < len(lines) {
		l := lines[line-1]
		if len(l) > 200 {
			l = l[:200]
		}
		fmt.Fprintf(&b, "  %s\n", l)
		pad := col - 1
		if pad > len(l) {
			pad = len(l)
		}
		ws := make([]byte, pad)
		for i := range ws {
			ws[i] = ' '
			if i < len(l) && l[i] == '\t' {
				ws[i] = '\t'
			}
		}
		fmt.Fprintf(&b, "  %s^\n", ws)
	}
	return b.String()
}

// SetArgs sets the positional parameters.
func (sh *Shell) SetArgs(args []string) {
	sh.global.put("argv", &Var{Vals: append([]string(nil), args...)})
}

// SetArg0 sets $0.
func (sh *Shell) SetArg0(s string) { sh.arg0 = s }

// CheckSyntax parses src without running it.
func CheckSyntax(src, file string) error {
	_, err := parse(src, file)
	return err
}

// Fork returns a subshell that can run concurrently with sh (for example
// to render a right prompt while the left one renders). Changes made in
// the fork don't affect sh.
func (sh *Shell) Fork() *Shell { return sh.clone() }

// CaptureFunction runs a function and returns what it printed. It reports
// false if no such function exists.
func (sh *Shell) CaptureFunction(name string, args ...string) (string, bool) {
	fn := sh.lookupFunc(name)
	if fn == nil {
		return "", false
	}
	buf := &lockedBuffer{}
	io := sh.StdIO()
	io.Out = &Stream{W: buf}
	saved, savedPipe := sh.status, sh.pipestatus
	sh.callFunc(fn, append([]string{name}, args...), io)
	sh.status, sh.pipestatus = saved, savedPipe
	return buf.String(), true
}

// ResetControlAll clears any pending control flow, including exit.
func (sh *Shell) ResetControlAll() { sh.ctrl = ctrlNone }

// SetStatus sets $status.
func (sh *Shell) SetStatus(s int) { sh.status = s }

// KeepStatus runs f and then restores $status and $pipestatus, so things
// like key bindings don't change what the prompt reports.
func (sh *Shell) KeepStatus(f func()) {
	st, ps := sh.status, sh.pipestatus
	f()
	sh.status, sh.pipestatus = st, ps
}
