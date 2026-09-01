package shell

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

// integrations is the state behind the integrate builtin. A shell and its
// subshells share one. Each tool's hooks are registered once and check the
// state when they fire, so running `integrate` again (say, re-sourcing the
// config) does not stack hooks.
type integrations struct {
	mu      sync.Mutex
	enabled map[string]string // tool -> its executable ("" for built-in tools)
	hooked  map[string]bool   // hooks already registered
	binds   []integBinding    // key bindings waiting for the line editor
	regexps map[string]*regexp.Regexp

	// jump answers `z`: the integration enabled last (z or zoxide).
	jump func(sh *Shell, io *IO, args []string) int

	// bg counts background work (data file writes, zoxide add, atuin);
	// WaitIntegrations waits for it.
	bg sync.WaitGroup

	z      zStore
	zoxide zoxideState
	mise   miseState
	atuin  atuinState
	direnv string // direnv executable
}

// integration describes one tool the integrate builtin knows.
type integration struct {
	name  string
	exe   string // program that must be in $PATH; "" for built-in tools
	about string
	// setup enables the tool and returns what -v reports.
	setup func(sh *Shell, io *IO, exe string) string
}

// integrationList holds the known tools, sorted by name; each tool's file
// registers itself.
var integrationList []integration

func registerIntegration(t integration) {
	integrationList = append(integrationList, t)
	sort.Slice(integrationList, func(i, j int) bool { return integrationList[i].name < integrationList[j].name })
}

func init() {
	builtins["integrate"] = bIntegrate
	builtinHelp["integrate"] = "set up starship, zoxide, fzf, mise, direnv, atuin or z"
}

const integrateUsage = `usage: integrate [-v] TOOL...

Set up a tool that ash works with. A tool that is not installed is
skipped with status 1 and no output, so one config works everywhere.

%s
  -v, --verbose  explain what was set up, or why a tool was skipped
  -h, --help     show this help

With no TOOL, list the tools and whether they are enabled.
`

func bIntegrate(sh *Shell, io *IO, args []string) int {
	verbose := false
	var tools []string
	for i := 1; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			tools = append(tools, args[i+1:]...)
			i = len(args)
		case a == "-v" || a == "--verbose":
			verbose = true
		case a == "-h" || a == "--help":
			var b strings.Builder
			for _, t := range integrationList {
				fmt.Fprintf(&b, "  %-9s %s\n", t.name, t.about)
			}
			fmt.Fprintf(io.Out, integrateUsage, b.String())
			return 0
		case len(a) > 1 && a[0] == '-':
			fmt.Fprintf(io.Err, "integrate: unknown option %s\n", a)
			return 2
		default:
			tools = append(tools, a)
		}
	}
	if len(tools) == 0 {
		return sh.listIntegrations(io)
	}
	status := 0
	for _, name := range tools {
		t := findIntegration(name)
		if t == nil {
			fmt.Fprintf(io.Err, "integrate: unknown tool '%s' (known: %s)\n", name, integrationNames())
			status = 2
			continue
		}
		if st := sh.integrate(io, t, verbose); st > status {
			status = st
		}
	}
	return status
}

func findIntegration(name string) *integration {
	for i := range integrationList {
		if integrationList[i].name == name {
			return &integrationList[i]
		}
	}
	return nil
}

func integrationNames() string {
	names := make([]string, len(integrationList))
	for i, t := range integrationList {
		names[i] = t.name
	}
	return strings.Join(names, ", ")
}

func (sh *Shell) integrate(io *IO, t *integration, verbose bool) int {
	exe := ""
	if t.exe != "" {
		p, ok := sh.LookPath(t.exe)
		if !ok {
			if verbose {
				fmt.Fprintf(io.Err, "integrate: %s: not installed (no %s in $PATH)\n", t.name, t.exe)
			}
			return 1
		}
		exe = p
	}
	in := sh.shared.integr
	in.mu.Lock()
	if in.enabled == nil {
		in.enabled = map[string]string{}
	}
	in.enabled[t.name] = exe
	in.mu.Unlock()
	msg := t.setup(sh, io, exe)
	if verbose {
		fmt.Fprintf(io.Err, "integrate: %s: %s\n", t.name, msg)
	}
	return 0
}

func (sh *Shell) listIntegrations(io *IO) int {
	in := sh.shared.integr
	for _, t := range integrationList {
		state, where := "available", "built in"
		if exe, ok := in.on(t.name); ok {
			state = "enabled"
			if exe != "" {
				where = exe
			}
		} else if t.exe != "" {
			if p, ok := sh.LookPath(t.exe); ok {
				where = p
			} else {
				state, where = "not installed", ""
			}
		}
		fmt.Fprintln(io.Out, strings.TrimRight(fmt.Sprintf("%-9s %-13s %s", t.name, state, where), " "))
	}
	return 0
}

// on reports whether a tool is integrated, and its executable.
func (in *integrations) on(tool string) (string, bool) {
	in.mu.Lock()
	defer in.mu.Unlock()
	exe, ok := in.enabled[tool]
	return exe, ok
}

// once reports whether key is seen for the first time; hooks use it to
// register themselves a single time.
func (in *integrations) once(key string) bool {
	in.mu.Lock()
	defer in.mu.Unlock()
	if in.hooked[key] {
		return false
	}
	if in.hooked == nil {
		in.hooked = map[string]bool{}
	}
	in.hooked[key] = true
	return true
}

// IntegrationEnabled reports whether `integrate TOOL` succeeded in this
// shell (for the frontend, e.g. to skip its own ctrl-r when fzf has it).
func (sh *Shell) IntegrationEnabled(tool string) bool {
	_, ok := sh.shared.integr.on(tool)
	return ok
}

// WaitIntegrations waits up to timeout for background work started by the
// integrations (z data file writes, zoxide add, atuin history end); the
// frontend calls it before exiting. It reports whether everything finished.
func (sh *Shell) WaitIntegrations(timeout time.Duration) bool {
	done := make(chan struct{})
	go func() {
		sh.shared.integr.bg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
}

// background runs f in a goroutine that WaitIntegrations waits for.
func (in *integrations) background(f func()) {
	in.bg.Add(1)
	go func() {
		defer in.bg.Done()
		f()
	}()
}

// integBinding is a key binding an integration installs.
type integBinding struct{ key, command string }

// integrateBind installs key bindings in the default and vi insert modes,
// like fzf's and atuin's own scripts do. Without a line editor yet (the
// config can run before it starts) they wait for the first prompt.
func (sh *Shell) integrateBind(bs ...integBinding) {
	in := sh.shared.integr
	in.mu.Lock()
	in.binds = append(in.binds, bs...)
	in.mu.Unlock()
	if in.once("binds") {
		sh.OnPrePrompt(func(sh *Shell) { sh.applyIntegrationBinds() })
	}
	sh.applyIntegrationBinds()
}

func (sh *Shell) applyIntegrationBinds() {
	ed := sh.Editor
	if ed == nil {
		return
	}
	in := sh.shared.integr
	in.mu.Lock()
	bs := in.binds
	in.binds = nil
	in.mu.Unlock()
	for _, b := range bs {
		ed.Bind("default", b.key, b.command, "")
		ed.Bind("insert", b.key, b.command, "")
	}
}

// integrationEval runs shell code a tool printed for us to apply (mise
// hook-env output) without disturbing $status, $_, $LINENO or errexit, and
// without letting it exit or return from anything.
func (sh *Shell) integrationEval(src, name string) {
	if strings.TrimSpace(src) == "" {
		return
	}
	status, pipe, last, line, ctrl := sh.status, sh.pipestatus, sh.lastArg, sh.lineno, sh.ctrl
	sh.noErrexit++
	sh.RunString(src, name, sh.StdIO())
	sh.noErrexit--
	sh.status, sh.pipestatus, sh.lastArg, sh.lineno, sh.ctrl = status, pipe, last, line, ctrl
}

// privateMode reports whether history-like recording is off (fish's
// private mode), which z, zoxide and atuin honor.
func (sh *Shell) privateMode() bool { return sh.Get("fish_private_mode") != "" }

// integRun runs c with its stdout captured and returns the output with
// trailing newlines removed (like command substitution) and the status.
func integRun(c *exec.Cmd) (string, int) {
	var out bytes.Buffer
	c.Stdout = &out
	st := integStatus(c.Run())
	return strings.TrimRight(out.String(), "\n"), st
}

// integStatus turns an exec error into a shell exit status.
func integStatus(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return 128 + int(ws.Signal())
		}
		return ee.ExitCode()
	}
	if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
		return 127
	}
	return 126
}

// integReader is what a child should read for a builtin's stdin: the file
// itself when there is one (so an interactive tool gets the terminal).
func integReader(s *Stream) io.Reader {
	if s == nil {
		return nil
	}
	if s.File != nil {
		return s.File
	}
	return s
}

// integWriter is the writer counterpart of integReader.
func integWriter(s *Stream) io.Writer {
	if s == nil {
		return nil
	}
	if s.File != nil {
		return s.File
	}
	return s
}

// integSplit0 splits NUL-terminated output (fzf --print0), dropping empty
// items.
func integSplit0(s string) []string {
	var out []string
	for len(s) > 0 {
		i := strings.IndexByte(s, 0)
		if i < 0 {
			i = len(s)
		}
		if i > 0 {
			out = append(out, s[:i])
		}
		if i == len(s) {
			break
		}
		s = s[i+1:]
	}
	return out
}

// integIsDir reports whether p is a directory (following symlinks).
func integIsDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}
