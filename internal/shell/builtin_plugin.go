package shell

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/andrinoff/ash/internal/plugin"
)

// Plugins are long-running programs that talk to ash in JSON lines (see
// docs/plugins.md). internal/plugin runs them. This file has the plugin
// builtin, the commands plugins add, and the shell side of the actions
// they send, which the interactive loop applies between commands with
// ApplyPluginActions.

func init() {
	builtins["plugin"] = bPlugin
	builtinHelp["plugin"] = "load, list, reload and unload plugins"
}

// Plugin timeouts and limits (variables so that tests can shorten them).
var (
	pluginRunTimeout      = 30 * time.Second
	pluginCompleteTimeout = 300 * time.Millisecond
	pluginHelloTimeout    = 5 * time.Second
	pluginCloseGrace      = 200 * time.Millisecond
	pluginStdinMax        = 16 << 20
)

// pluginHost is the plugin state a shell shares with its subshells.
type pluginHost struct {
	mgr  atomic.Pointer[plugin.Manager]
	wake atomic.Pointer[func()]

	mu      sync.Mutex
	lastDir string          // where the last cd event went, for the next one's "from"
	segs    map[string]bool // prompt segments plugins have set
}

// pluginManager returns the plugin manager, creating it on first use.
func (sh *Shell) pluginManager() *plugin.Manager {
	if m := sh.shared.plugins.mgr.Load(); m != nil {
		return m
	}
	return sh.pluginManagerAt(filepath.Join(sh.CacheDir(), "plugins"), sh.promptNow())
}

// promptState is what the prompt event reports.
type promptState struct {
	dir        string
	status     int
	pipestatus []int
	duration   time.Duration
}

func (sh *Shell) promptNow() promptState {
	return promptState{sh.dir, sh.status, append([]int(nil), sh.pipestatus...), sh.cmdDuration}
}

// pluginManagerAt creates the plugin manager, with logs in logDir; ps is
// the shell's state at the last prompt. It reads nothing else from the
// shell, so it may run on another goroutine.
func (sh *Shell) pluginManagerAt(logDir string, ps promptState) *plugin.Manager {
	h := &sh.shared.plugins
	h.mu.Lock()
	defer h.mu.Unlock()
	if m := h.mgr.Load(); m != nil {
		return m
	}
	m := plugin.New(plugin.Options{
		LogDir: logDir,
		Wake: func() {
			if f := h.wake.Load(); f != nil {
				(*f)()
			}
		},
		Reserved: func(name string) bool {
			_, ok := builtins[name]
			return ok
		},
	})
	h.lastDir = ps.dir
	// Plugins that say hello before the next prompt hear about this one.
	m.Prompt(ps.dir, ps.status, ps.pipestatus, ps.duration)
	// Each hook costs an atomic load when no plugin listens.
	sh.OnPrePrompt(func(sh *Shell) { m.Prompt(sh.dir, sh.status, sh.pipestatus, sh.cmdDuration) })
	sh.OnPreExec(func(sh *Shell, line string) { m.PreExec(line, sh.dir) })
	sh.OnPostExec(func(sh *Shell, line string, status int, d time.Duration) {
		m.PostExec(line, status, d, sh.dir)
	})
	sh.OnDirChanged(func(sh *Shell, dir string) {
		h.mu.Lock()
		from := h.lastDir
		h.lastDir = dir
		h.mu.Unlock()
		m.Cd(from, dir)
	})
	h.mgr.Store(m)
	return m
}

// builtinFunc returns the builtin called name: one of ash's own, or a
// command a plugin added. ash's own win.
func (sh *Shell) builtinFunc(name string) (BuiltinFunc, bool) {
	if b, ok := builtins[name]; ok {
		return b, true
	}
	if m := sh.shared.plugins.mgr.Load(); m != nil && m.HasCommand(name) {
		return bPluginCommand, true
	}
	return nil, false
}

const pluginUsage = `usage: plugin load [-w] NAME [--] COMMAND [ARGS...]
       plugin list [-v]
       plugin unload NAME...
       plugin reload [-w] NAME...

A plugin is a program, in any language, that talks to ash in JSON lines
on its stdin and stdout. It hears about prompts, commands and directory
changes, and can add commands and completions, set variables and prompt
segments, change directory and put text on the command line.
Executables in ~/.config/ash/plugins start with every interactive shell.
"help plugins" has the details.

  -w, --wait     wait until the plugin is ready (5 seconds at most)
  -v, --verbose  also show commands, completions, the command and the log
  -h, --help     show this help
`

func bPlugin(sh *Shell, io *IO, args []string) int {
	if len(args) < 2 {
		return sh.pluginList(io, nil)
	}
	sub, rest := args[1], args[2:]
	switch sub {
	case "load":
		return sh.pluginLoad(io, rest)
	case "list", "ls":
		return sh.pluginList(io, rest)
	case "unload":
		return sh.pluginUnload(io, rest)
	case "reload":
		return sh.pluginReload(io, rest)
	case "-h", "--help", "help":
		io.Out.WriteString(pluginUsage)
		return 0
	}
	fmt.Fprintf(io.Err, "plugin: unknown subcommand '%s' (see plugin --help)\n", sub)
	return 2
}

// pluginFlags takes -w/--wait off the front of args.
func pluginFlags(io *IO, sub string, args []string) (wait bool, rest []string, ok bool) {
	for len(args) > 0 && len(args[0]) > 1 && args[0][0] == '-' {
		switch args[0] {
		case "-w", "--wait":
			wait = true
		case "--":
			return wait, args[1:], true
		default:
			fmt.Fprintf(io.Err, "plugin: %s: unknown option %s\n", sub, args[0])
			return false, nil, false
		}
		args = args[1:]
	}
	return wait, args, true
}

func (sh *Shell) pluginLoad(io *IO, args []string) int {
	wait, args, ok := pluginFlags(io, "load", args)
	if !ok {
		return 2
	}
	if len(args) > 1 && args[1] == "--" {
		args = append(args[:1:1], args[2:]...)
	}
	if len(args) < 2 {
		fmt.Fprintln(io.Err, "plugin: load: usage: plugin load [-w] NAME [--] COMMAND [ARGS...]")
		return 2
	}
	name, argv := args[0], args[1:]
	if !plugin.ValidName(name) {
		fmt.Fprintf(io.Err, "plugin: load: '%s' isn't a valid name (use letters, digits, '_', '-' and '.')\n", name)
		return 2
	}
	path, err := sh.lookPath(argv[0])
	if err != nil {
		fmt.Fprintf(io.Err, "plugin: load: %s: %s\n", argv[0], pluginLookErr(err))
		return 127
	}
	m := sh.pluginManager()
	spec := plugin.Spec{Name: name, Path: path, Args: append([]string(nil), argv...), Dir: sh.dir, Env: pluginEnv(sh.Environ(), name)}
	if err := startPlugin(m, spec, sh.Self()); err != nil {
		if errors.Is(err, plugin.ErrLoaded) {
			fmt.Fprintf(io.Err, "plugin: load: %s is already loaded (plugin reload %s restarts it)\n", name, name)
		} else {
			fmt.Fprintf(io.Err, "plugin: load: %s: %v\n", name, err)
		}
		return 1
	}
	if wait {
		return pluginWait(io, m, name)
	}
	return 0
}

func pluginLookErr(err error) string {
	switch {
	case errors.Is(err, errIsDir):
		return "is a directory"
	case errors.Is(err, syscall.EACCES):
		return "permission denied"
	}
	return "command not found"
}

// startPlugin loads spec. A script without a #! line runs with ash, as it
// would from the command line.
func startPlugin(m *plugin.Manager, spec plugin.Spec, self string) error {
	err := m.Load(spec)
	if errors.Is(err, syscall.ENOEXEC) {
		spec.Args = append([]string{self, spec.Path}, spec.Args[1:]...)
		spec.Path = self
		err = m.Load(spec)
	}
	return err
}

// pluginEnv is the environment a plugin starts with: the shell's, with
// ASH_PLUGIN_NAME and ASH_PID added.
func pluginEnv(env []string, name string) []string {
	out := make([]string, 0, len(env)+2)
	for _, kv := range env {
		if !strings.HasPrefix(kv, "ASH_PLUGIN_NAME=") && !strings.HasPrefix(kv, "ASH_PID=") {
			out = append(out, kv)
		}
	}
	return append(out, "ASH_PLUGIN_NAME="+name, "ASH_PID="+strconv.Itoa(os.Getpid()))
}

func pluginWait(io *IO, m *plugin.Manager, name string) int {
	err := m.WaitReady(name, pluginHelloTimeout)
	if err == nil {
		return 0
	}
	in := pluginInfo(m, name)
	switch {
	case in.State == plugin.Dead:
		fmt.Fprintf(io.Err, "plugin: %s stopped before it was ready (%s)", name, in.Exit)
	case errors.Is(err, plugin.ErrNoHello):
		fmt.Fprintf(io.Err, "plugin: %s didn't say hello within %v", name, pluginHelloTimeout)
	default:
		fmt.Fprintf(io.Err, "plugin: %s: %v", name, err)
	}
	if in.Log != "" {
		fmt.Fprintf(io.Err, "; its log is %s", in.Log)
	}
	fmt.Fprintln(io.Err)
	return 1
}

func pluginInfo(m *plugin.Manager, name string) plugin.Info {
	for _, in := range m.List() {
		if in.Name == name {
			return in
		}
	}
	return plugin.Info{Name: name}
}

func (sh *Shell) pluginList(io *IO, args []string) int {
	verbose := false
	for _, a := range args {
		switch a {
		case "-v", "--verbose":
			verbose = true
		default:
			fmt.Fprintf(io.Err, "plugin: list: unknown option %s\n", a)
			return 2
		}
	}
	m := sh.shared.plugins.mgr.Load()
	if m == nil {
		return 0
	}
	list := m.List()
	w := 0
	for _, in := range list {
		w = max(w, len(in.Name))
	}
	for _, in := range list {
		pid, detail := strconv.Itoa(in.Pid), strings.Join(in.Events, " ")
		if in.State == plugin.Dead {
			pid, detail = "-", in.Exit
		}
		if in.DroppedEvents > 0 {
			detail += fmt.Sprintf(" (%d events dropped)", in.DroppedEvents)
		}
		line := fmt.Sprintf("%-*s  %-6s  %-8s  %s", w, in.Name, pid, in.State, detail)
		fmt.Fprintln(io.Out, strings.TrimRight(line, " "))
		if !verbose {
			continue
		}
		for _, c := range in.Commands {
			fmt.Fprintf(io.Out, "    command   %s", c.Name)
			if c.Description != "" {
				fmt.Fprintf(io.Out, "  (%s)", c.Description)
			}
			fmt.Fprintln(io.Out)
		}
		if len(in.Complete) > 0 {
			fmt.Fprintf(io.Out, "    completes %s\n", strings.Join(in.Complete, " "))
		}
		quoted := make([]string, len(in.Args))
		for i, a := range in.Args {
			quoted[i] = Quote(a)
		}
		fmt.Fprintf(io.Out, "    runs      %s\n", strings.Join(quoted, " "))
		if in.Log != "" {
			fmt.Fprintf(io.Out, "    log       %s\n", in.Log)
		}
		if in.DroppedActions > 0 {
			fmt.Fprintf(io.Out, "    ignored   %d actions (too many at once)\n", in.DroppedActions)
		}
	}
	return 0
}

func (sh *Shell) pluginUnload(io *IO, names []string) int {
	if len(names) == 0 {
		fmt.Fprintln(io.Err, "plugin: unload: usage: plugin unload NAME...")
		return 2
	}
	m := sh.shared.plugins.mgr.Load()
	status := 0
	for _, n := range names {
		if m == nil || m.Unload(n) != nil {
			fmt.Fprintf(io.Err, "plugin: unload: no plugin named '%s'\n", n)
			status = 1
		}
	}
	return status
}

func (sh *Shell) pluginReload(io *IO, args []string) int {
	wait, names, ok := pluginFlags(io, "reload", args)
	if !ok {
		return 2
	}
	if len(names) == 0 {
		fmt.Fprintln(io.Err, "plugin: reload: usage: plugin reload [-w] NAME...")
		return 2
	}
	m := sh.shared.plugins.mgr.Load()
	status := 0
	for _, n := range names {
		if m == nil {
			fmt.Fprintf(io.Err, "plugin: reload: no plugin named '%s'\n", n)
			status = 1
			continue
		}
		if err := m.Reload(n, sh.dir, pluginEnv(sh.Environ(), n)); err != nil {
			if errors.Is(err, plugin.ErrUnknown) {
				fmt.Fprintf(io.Err, "plugin: reload: no plugin named '%s'\n", n)
			} else {
				fmt.Fprintf(io.Err, "plugin: reload: %s: %v\n", n, err)
			}
			status = 1
			continue
		}
		if wait && pluginWait(io, m, n) != 0 {
			status = 1
		}
	}
	return status
}

// bPluginCommand runs a command a plugin added: the arguments, the
// directory and, when it is piped, stdin go to the plugin, which answers
// with the output and the exit status. ctrl-c stops waiting.
func bPluginCommand(sh *Shell, io *IO, args []string) int {
	m := sh.shared.plugins.mgr.Load()
	if m == nil {
		return 127
	}
	owner := m.CommandPlugin(args[0])
	var stdin *string
	if io.In.ReadsInput() {
		s, err := readPluginStdin(io.In)
		if err != nil {
			sh.errorMsg(io.Err, "%s: %v", args[0], err)
			return 1
		}
		stdin = &s
	}
	ctx, cancel := context.WithTimeout(context.Background(), pluginRunTimeout)
	defer cancel()
	stop := sh.cancelOnInterrupt(cancel)
	res, err := m.Run(ctx, args[0], args[1:], sh.dir, stdin)
	stop()
	if err != nil {
		switch {
		case sh.interrupted():
			return 130
		case errors.Is(err, context.DeadlineExceeded):
			sh.errorMsg(io.Err, "%s: plugin %s didn't answer within %v", args[0], owner, pluginRunTimeout)
			return 124
		case errors.Is(err, plugin.ErrUnknown):
			sh.errorMsg(io.Err, "%s: plugin %s isn't running anymore", args[0], owner)
			return 127
		}
		sh.errorMsg(io.Err, "%s: %v", args[0], err)
		return 1
	}
	io.Out.WriteString(res.Stdout)
	io.Err.WriteString(res.Stderr)
	return res.Status & 0xff
}

func readPluginStdin(in *Stream) (string, error) {
	b, err := io.ReadAll(io.LimitReader(in, int64(pluginStdinMax)+1))
	if err != nil {
		return "", err
	}
	if len(b) > pluginStdinMax {
		return "", fmt.Errorf("more than %d MiB on stdin", pluginStdinMax>>20)
	}
	return string(b), nil
}

// cancelOnInterrupt calls cancel when ctrl-c is pressed, until stop is
// called.
func (sh *Shell) cancelOnInterrupt(cancel func()) (stop func()) {
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(20 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				if sh.interrupted() {
					cancel()
					return
				}
			}
		}
	}()
	return func() { close(done) }
}

// PluginUpdate is what applying plugin actions leaves for the frontend.
type PluginUpdate struct {
	Messages   []string // to show above the prompt
	Buffer     string   // to put on the command line, if HasBuffer
	HasBuffer  bool
	Redraw     bool // variables, segments or the directory changed
	DirChanged bool
}

// ApplyPluginActions applies what plugins asked for since the last call.
// Variables, prompt segments and directory changes take effect here;
// messages and command-line text are returned for the frontend. Call it
// on the main goroutine when no command is running: before a prompt, or
// while the line editor waits for input.
func (sh *Shell) ApplyPluginActions() PluginUpdate {
	var u PluginUpdate
	m := sh.shared.plugins.mgr.Load()
	if m == nil {
		return u
	}
	acts := m.Actions()
	for _, a := range acts {
		switch a.Kind {
		case plugin.ActSet:
			if v := sh.global.get(a.Name); v != nil && slicesEqual(v.Vals, a.Values) && (v.Export || !a.Export) {
				continue // nothing changes; don't redraw
			}
			o := setOpts{scope: setGlobal}
			if a.Export {
				o.export = 1
			}
			if err := sh.SetVar(a.Name, a.Values, o); err != nil {
				m.Logf(a.Plugin, "set: %v", err)
				continue
			}
			u.Redraw = true
		case plugin.ActUnset:
			switch err := sh.UnsetVar(a.Name, setGlobal); {
			case err == nil:
				u.Redraw = true
			case err != errNotSet:
				m.Logf(a.Plugin, "unset: %v", err)
			}
		case plugin.ActSegment:
			if sh.setPluginSegment(a.Name, a.Text) {
				u.Redraw = true
			}
		case plugin.ActCd:
			changed, msg := sh.pluginCd(a.Text)
			if msg != "" {
				m.Logf(a.Plugin, "%s", msg)
				u.Messages = append(u.Messages, "ash: plugin "+a.Plugin+": "+msg)
			}
			if changed {
				u.Redraw, u.DirChanged = true, true
			}
		case plugin.ActBuffer:
			u.Buffer, u.HasBuffer = a.Text, true
		case plugin.ActMessage:
			u.Messages = append(u.Messages, strings.TrimRight(a.Text, "\n"))
		}
	}
	return u
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// setPluginSegment sets the variable ash_plugin_NAME, or erases it for an
// empty text. It reports whether anything changed.
func (sh *Shell) setPluginSegment(name, text string) bool {
	h := &sh.shared.plugins
	h.mu.Lock()
	if h.segs == nil {
		h.segs = map[string]bool{}
	}
	if text == "" {
		delete(h.segs, name)
	} else {
		h.segs[name] = true
	}
	h.mu.Unlock()
	v := "ash_plugin_" + name
	cur := sh.global.get(v)
	if text == "" {
		return cur != nil && sh.UnsetVar(v, setGlobal) == nil
	}
	if cur != nil && len(cur.Vals) == 1 && cur.Vals[0] == text {
		return false
	}
	return sh.SetVar(v, []string{text}, setOpts{scope: setGlobal}) == nil
}

// pluginCd changes directory for a plugin. A relative path is taken from
// the current directory ($CDPATH doesn't apply). It returns whether the
// directory changed, and the error, if any.
func (sh *Shell) pluginCd(dir string) (bool, string) {
	if dir == "" {
		return false, ""
	}
	dir = sh.expandTildeString(dir)
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(sh.dir, dir)
	}
	before := sh.dir
	errb := &lockedBuffer{}
	cio := &IO{In: &Stream{}, Out: &Stream{W: io.Discard}, Err: &Stream{W: errb}}
	if sh.changeDir(dir, cio, false) != 0 {
		return false, strings.TrimSpace(errb.String())
	}
	return sh.dir != before, ""
}

// PluginSegment is a prompt segment set by a plugin.
type PluginSegment struct {
	Name, Text string
}

// PluginSegments returns the prompt segments plugins have set (the
// variables ash_plugin_NAME), sorted by name.
func (sh *Shell) PluginSegments() []PluginSegment {
	h := &sh.shared.plugins
	h.mu.Lock()
	if len(h.segs) == 0 {
		h.mu.Unlock()
		return nil
	}
	names := make([]string, 0, len(h.segs))
	for n := range h.segs {
		names = append(names, n)
	}
	h.mu.Unlock()
	sort.Strings(names)
	var out []PluginSegment
	for _, n := range names {
		if t := sh.Get("ash_plugin_" + n); t != "" {
			out = append(out, PluginSegment{Name: n, Text: t})
		}
	}
	return out
}

// PluginCandidate is a completion a plugin offered.
type PluginCandidate struct {
	Text        string
	Description string
}

// PluginComplete asks the plugin that completes cmd (it named cmd under
// "complete" in its hello) for candidates for line, with the cursor at
// byte offset cursor. It waits 300ms at most, and reports false if no
// plugin completes cmd or the plugin didn't answer in time. Answers are
// kept for a few seconds; one that comes too late is used the next time.
func (sh *Shell) PluginComplete(cmd, line string, cursor int) ([]PluginCandidate, bool) {
	m := sh.shared.plugins.mgr.Load()
	if m == nil || !m.Completes(cmd) {
		return nil, false
	}
	cands, ok := m.Complete(cmd, line, cursor, pluginCompleteTimeout)
	if !ok {
		return nil, false
	}
	out := make([]PluginCandidate, len(cands))
	for i, c := range cands {
		out[i] = PluginCandidate{Text: c.Text, Description: c.Description}
	}
	return out, true
}

// PluginCommand is a command a plugin added.
type PluginCommand struct {
	Name, Description, Plugin string
}

// PluginCommands lists the commands plugins added, sorted by name.
func (sh *Shell) PluginCommands() []PluginCommand {
	m := sh.shared.plugins.mgr.Load()
	if m == nil {
		return nil
	}
	cmds := m.Commands()
	out := make([]PluginCommand, 0, len(cmds))
	for _, c := range cmds {
		if _, shadowed := builtins[c.Name]; !shadowed {
			out = append(out, PluginCommand{Name: c.Name, Description: c.Description, Plugin: c.Plugin})
		}
	}
	return out
}

// SetPluginWake sets f to be called, from any goroutine, when plugins
// have sent actions; the interactive loop uses it to apply them while it
// waits at the prompt. f must not block.
func (sh *Shell) SetPluginWake(f func()) { sh.shared.plugins.wake.Store(&f) }

// AutoloadPlugins starts every executable file in ~/.config/ash/plugins,
// named after the file without its extension. The directory is read and
// the plugins started in the background, so the prompt doesn't wait;
// problems are reported above a later prompt.
func (sh *Shell) AutoloadPlugins() {
	dir := filepath.Join(sh.ConfigDir(), "plugins")
	logDir := filepath.Join(sh.CacheDir(), "plugins")
	cwd, env, self, ps := sh.dir, sh.Environ(), sh.Self(), sh.promptNow()
	go func() {
		ents, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		var m *plugin.Manager
		for _, e := range ents {
			name, ok := autoloadName(e.Name())
			if !ok {
				continue
			}
			path := filepath.Join(dir, e.Name())
			st, err := os.Stat(path)
			if err != nil || !st.Mode().IsRegular() || st.Mode()&0o111 == 0 {
				continue
			}
			if m == nil {
				m = sh.pluginManagerAt(logDir, ps)
			}
			spec := plugin.Spec{Name: name, Path: path, Args: []string{path}, Dir: cwd, Env: pluginEnv(env, name)}
			if err := startPlugin(m, spec, self); err != nil && !errors.Is(err, plugin.ErrLoaded) {
				m.Notify(name, "ash: plugin "+name+": "+err.Error())
			}
		}
	}()
}

// autoloadName is the plugin name for a file in the plugins directory,
// or false for files to skip (hidden ones, editor backups).
func autoloadName(file string) (string, bool) {
	if strings.HasPrefix(file, ".") || strings.HasSuffix(file, "~") {
		return "", false
	}
	name := strings.TrimSuffix(file, filepath.Ext(file))
	return name, plugin.ValidName(name)
}

// ClosePlugins sends the exit event to the plugins that listen for it and
// stops them all, after giving them a moment (200ms at most) to exit on
// their own. The shell calls it when it exits.
func (sh *Shell) ClosePlugins() {
	if m := sh.shared.plugins.mgr.Load(); m != nil {
		m.Close(pluginCloseGrace)
	}
}
