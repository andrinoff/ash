// Package repl runs ash interactively: it owns the prompt loop and
// connects the line editor, the interpreter and the history.
package repl

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/charmbracelet/x/ansi"
	"golang.org/x/sys/unix"

	"github.com/andrinoff/ash/internal/editor"
	"github.com/andrinoff/ash/internal/shell"
	"github.com/andrinoff/ash/internal/syntax"
	"github.com/andrinoff/ash/internal/term"
)

// REPL is an interactive session.
type REPL struct {
	fixLine  string // a corrected version of the last command, offered next
	cleanup  []func()
	sh       *shell.Shell
	ed       *editor.Editor
	in, out  *os.File // the terminal the editor uses
	hist     historyBackend
	cont     string
	contDone bool
	contFor  string // the starship the continuation came from
	tty      bool
	warned   bool

	// the last rendered prompt, written by the background renderer
	lastMu  sync.Mutex
	last    editor.Prompt
	lastDir string
	repos   repoCache      // repositories found for the history search
	prof    *shell.Profile // --profile: reported once the first prompt is up
	plug    pluginState
	// previewSh is a fork of sh for the expansion preview, which runs in
	// the background; nil when $ash_preview is 0.
	previewSh atomic.Pointer[shell.Shell]

	jobsQueued atomic.Bool // a look at the jobs is waiting in the editor's queue

	// edMu is held while the editor reads a line, at the prompt or for
	// the read builtin, which can run on other goroutines (in a
	// background job or a pipeline).
	edMu sync.Mutex
	// inBinding is set while shell code bound to a key runs, with the
	// editor suspended in the middle of reading a line.
	inBinding atomic.Bool

	saved   *unix.Termios // the terminal's modes when the session started
	hupOnce sync.Once
}

// exitProcess ends the process (tests replace it).
var exitProcess = os.Exit

// errTerminalBusy is what `read` gets when the editor is already reading
// the prompt for the user (for example from a background job).
var errTerminalBusy = errors.New("the terminal is busy")

// Run starts an interactive session and returns the exit status.
func Run(sh *shell.Shell, initCmds []string) int {
	r := &REPL{sh: sh, in: os.Stdin, out: os.Stdout}
	sh.Init()
	r.tty = term.IsTerminal(int(os.Stdout.Fd())) && os.Getenv("TERM") != "dumb"
	r.saved, _ = term.GetState(int(r.in.Fd()))

	r.hist = openHistory(sh)
	sh.History = r.hist
	r.ed = editor.New(r.in, r.out, r.editorConfig())
	sh.Editor = r.ed
	sh.SetReadLine(r.readLine)
	shell.EditorFunctions = editor.FunctionNames()
	shell.ExecHook = func() {
		// the config may exec tmux before there is a prompt
		p := sh.StopProfile()
		if p == nil {
			p, r.prof = r.prof, nil
		}
		if p != nil {
			p.WriteReport(os.Stderr, "exec", time.Since(p.Start))
		}
		r.saveAndClose()
	}

	r.handleSignals()
	r.importHistory()
	r.cleanup = append(r.cleanup, sh.SetupActionFile())

	sh.LoadUniversal()
	sh.HoldPlugins() // until the first prompt is up
	for _, c := range initCmds {
		sh.RunString(c, "", sh.StdIO())
	}
	if !sh.NoConfig {
		r.loadConfig()
	}
	r.prof = sh.StopProfile()
	if exit, st := sh.ShouldExit(); exit {
		return r.exit(st)
	}
	r.startPlugins()
	r.greet()
	return r.loop()
}

func (r *REPL) editorConfig() editor.Config {
	sh := r.sh
	return editor.Config{
		Highlight:     r.highlight,
		Suggest:       r.suggest,
		Complete:      r.complete,
		Incomplete:    incomplete,
		ExpandAbbr:    r.expandAbbr,
		History:       r.hist,
		SearchHistory: r.searchHistory,
		SearchScopes:  r.searchScopes,
		RunCommand: func(cmd string) {
			r.inBinding.Store(true)
			defer r.inBinding.Store(false)
			r.runBinding(func() {
				sh.KeepStatus(func() {
					sh.ClearInterrupt()
					sh.RunString(cmd, "", sh.StdIO())
					sh.ResetControl()
					sh.ApplyChildActions(sh.StdIO())
				})
			})
			// a binding may have changed variables or the directory
			r.refreshPreview()
			if exit, _ := sh.ShouldExit(); exit {
				// leave now, not after the next line (which wouldn't run)
				r.ed.QueueFunctions("exit")
			}
		},
		EditCommand: func(buf string) (string, bool) {
			r.inBinding.Store(true)
			defer r.inBinding.Store(false)
			return r.editCommand(buf)
		},
		Explain: func(buf string) string { return sh.FormatExplain(sh.Explain(buf), true) },
		ResumeJob: func() string {
			if id := sh.LastStoppedJob(); id > 0 {
				return "fg %" + strconv.Itoa(id)
			}
			return ""
		},
		Style:   func(role string) string { return sh.Style(role).SGR() },
		Preview: r.preview,
	}
}

// preview describes what the word at the cursor expands to. The editor
// calls it in the background, so it must not touch r.sh, which the main
// goroutine may be changing: it works on the fork refreshPreview took.
func (r *REPL) preview(buf string, cursor int) string {
	if sh := r.previewSh.Load(); sh != nil {
		return sh.PreviewWord(buf, cursor)
	}
	return ""
}

// refreshPreview gives the preview a fresh fork of the shell, or turns it
// off when $ash_preview is 0. It runs on the main goroutine before each
// prompt and after key bindings.
func (r *REPL) refreshPreview() {
	if r.sh.Get("ash_preview") == "0" {
		r.previewSh.Store(nil)
		return
	}
	r.previewSh.Store(r.sh.Fork())
}

func incomplete(buf string) bool {
	_, err := syntax.Parse(buf)
	return syntax.IsIncomplete(err)
}

func (r *REPL) loop() int {
	sh := r.sh
	for {
		sh.ResetControl()
		sh.ClearInterrupt()
		r.applyPlugins() // a safe point: no command runs
		r.notifyJobs()
		sh.RunPrePrompt()
		sh.Emit("ash_prompt")
		if exit, st := sh.ShouldExit(); exit {
			return r.exit(st)
		}
		r.setTitle("")
		r.reportCwd()
		// wait for a `read` in a background job to finish with the editor
		r.edMu.Lock()
		if r.tty {
			r.ed.EnsureNewline(sh.Style("pager_progress").SGR() + "⏎")
		}
		if r.prof != nil {
			r.queueProfile()
		}
		r.refreshPreview()
		r.showPluginMessages()
		r.plug.atPrompt = true
		line, err := r.ed.ReadLine(r.readOpts())
		r.plug.atPrompt = false
		r.edMu.Unlock()
		if r.prof != nil {
			// typed-ahead input ended the line before the queued report ran
			r.printProfile(false)
		}
		switch err {
		case nil:
		case editor.ErrEOF:
			if sh.StoppedJobs() && !r.warned {
				r.warned = true
				fmt.Fprintln(os.Stderr, "ash: there are stopped jobs; press ctrl-d again to exit (they will be hung up)")
				sh.ResetControlAll() // an exit from a key binding, too
				continue
			}
			return r.exit(sh.Status())
		case editor.ErrInterrupt:
			sh.Emit("ash_cancel")
			continue
		default:
			return r.exit(sh.Status())
		}
		if strings.TrimSpace(line) == "" {
			if r.tty {
				// the editor marked the accepted line as a command that
				// started (OSC 133;C); mark it finished
				r.out.WriteString(ansi.FinalTermCmdFinished())
			}
			continue
		}
		r.runLine(line)
		if exit, st := sh.ShouldExit(); exit {
			if sh.StoppedJobs() && !r.warned {
				r.warned = true
				fmt.Fprintln(os.Stderr, "ash: there are stopped jobs; run exit again to exit (they will be hung up)")
				sh.ResetControlAll()
				continue
			}
			return r.exit(st)
		}
		// Only a command in between makes the next attempt to exit warn
		// again; resetting before running the line meant a second `exit`
		// warned forever.
		r.warned = false
	}
}

func (r *REPL) readOpts() editor.ReadOpts {
	o := editor.ReadOpts{
		Prompt:       r.promptFunc(),
		Wait:         150 * time.Millisecond,
		Continuation: r.continuation(),
		Transient:    r.transient,
		MarkPrompt:   r.tty,
		Initial:      r.nextBuffer(),
	}
	if o.Initial == "" {
		o.EmptySuggestion = r.fixLine
	}
	r.fixLine = ""
	// While a slow prompt renders, show the previous one if we're still
	// in the same directory, otherwise the instant built-in prompt.
	r.lastMu.Lock()
	last, lastDir := r.last, r.lastDir
	r.lastMu.Unlock()
	if last.Left != "" && lastDir == r.sh.Dir() {
		o.Placeholder = last
	} else {
		o.Placeholder = builtinPrompt(r.snapshot(), "insert")
	}
	inner := o.Prompt
	o.Prompt = func(km string) func() editor.Prompt {
		render := inner(km)
		dir := r.sh.Dir()
		// runs in the background, possibly after the next readOpts
		return func() editor.Prompt {
			p := render()
			r.lastMu.Lock()
			r.last, r.lastDir = p, dir
			r.lastMu.Unlock()
			return p
		}
	}
	return o
}

func (r *REPL) runLine(line string) {
	sh := r.sh
	dir := sh.Dir()
	start := time.Now()
	r.setTitle(line)
	sh.RunPreExec(line)
	sh.Emit("ash_preexec", line)
	sh.ClearInterrupt()
	status := sh.RunTopLevel(line, sh.StdIO())
	dur := time.Since(start)
	sh.SetCmdDuration(dur)
	if r.tty {
		r.out.WriteString(ansi.FinalTermCmdFinished(strconv.Itoa(status)))
		r.notifyLong(line, status, dur)
	}
	sh.ApplyChildActions(sh.StdIO())
	if bad, good := sh.TakeFix(); bad != "" {
		r.fixLine = fixLine(line, bad, good)
	}
	if !r.secret(line) {
		r.hist.Add(line, dir, start, status, dur)
	}
	r.repos.forget() // the command may have made or removed a repository
	sh.RunPostExec(line, status, dur)
	sh.Emit("ash_postexec", line)
	if sh.Status() != status {
		// event handlers must not clobber $status
		sh.SetStatus(status)
	}
}

// notifyLong sends a desktop notification (OSC 9, understood by Ghostty,
// iTerm2, kitty, WezTerm and others) when a command ran longer than
// $ash_notify_after seconds.
func (r *REPL) notifyLong(line string, status int, dur time.Duration) {
	v := r.sh.Get("ash_notify_after")
	if v == "" {
		return
	}
	secs, err := strconv.ParseFloat(v, 64)
	if err != nil || dur.Seconds() < secs {
		return
	}
	cmd := strings.Fields(line)
	if len(cmd) == 0 {
		return
	}
	verb := "finished"
	if status != 0 {
		verb = "failed (" + strconv.Itoa(status) + ")"
	}
	msg := cmd[0] + " " + verb + " after " + fmtDuration(dur)
	msg = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, msg)
	r.out.WriteString(ansi.Notify(msg))
}

// queueProfile has the --profile report printed once the first prompt is
// on the screen: functions passed to the editor's Do run after it draws,
// so the time taken there is when the prompt appeared.
func (r *REPL) queueProfile() {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		// no line editor; the prompt is printed next
		r.printProfile(false)
		return
	}
	r.ed.Do(func() { r.printProfile(true) })
}

func (r *REPL) printProfile(inEditor bool) {
	p := r.prof
	if p == nil {
		return
	}
	r.prof = nil
	var b strings.Builder
	p.WriteReport(&b, "first prompt", time.Since(p.Start))
	if inEditor && term.IsTerminal(int(os.Stderr.Fd())) {
		r.ed.Notify(b.String()) // above the prompt, which is then redrawn
		return
	}
	os.Stderr.WriteString(b.String())
}

// importHistory brings in fish (or zsh, or bash) history the first time
// ash runs, in the background so startup doesn't wait for it.
func (r *REPL) importHistory() {
	h, ok := r.hist.(*storeHistory)
	if !ok {
		return
	}
	go func() {
		n, err := h.AutoImport()
		if err == nil && n > 0 {
			r.ed.Notify(r.sh.Style("pager_progress").SGR() + "ash: imported " + strconv.Itoa(n) + " commands from your previous shell's history" + "\x1b[0m")
		}
	}()
}

// readLine is used by the read builtin when stdin is the terminal.
//
// The editor reads one line at a time, so `read` must not use it while
// the prompt is being read. Shell code bound to a key runs with the editor
// suspended in the middle of the command line; a `read` there gets a
// nested line of its own from the editor, which keeps the command line,
// typeahead and kill ring. Anything else that finds the editor busy is a
// background job trying to read the terminal while the user is at the
// prompt, and fails like a background read of a terminal does.
func (r *REPL) readLine(prompt, initial string) (string, error) {
	if r.edMu.TryLock() {
		defer r.edMu.Unlock()
	} else if !r.inBinding.Load() {
		return "", errTerminalBusy
	}
	line, err := r.ed.ReadLine(editor.ReadOpts{
		Prompt: func(string) func() editor.Prompt {
			return func() editor.Prompt { return editor.Prompt{Left: prompt} }
		},
		Initial: initial,
		Simple:  true,
	})
	if err == editor.ErrInterrupt {
		// ctrl-c in `read` stops the whole command, like in bash
		r.sh.Interrupt()
	}
	return line, err
}

func (r *REPL) notifyJobs() {
	// This covers a look at the jobs that is queued, or was dropped.
	r.jobsQueued.Store(false)
	var buf bytes.Buffer
	if r.sh.NotifyJobs(&buf) {
		os.Stderr.Write(buf.Bytes())
	}
}

func (r *REPL) handleSignals() {
	sh := r.sh
	ch := make(chan os.Signal, 16)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGQUIT, syscall.SIGTSTP, syscall.SIGTTIN,
		syscall.SIGTERM, syscall.SIGHUP, syscall.SIGCHLD)
	go func() {
		for s := range ch {
			switch s {
			case syscall.SIGINT:
				sh.Interrupt()
			case syscall.SIGHUP:
				r.ed.Do(func() { r.hangup(true) })
				// if the editor isn't running, exit from here
				go func() {
					time.Sleep(200 * time.Millisecond)
					r.hangup(false)
				}()
			case syscall.SIGCHLD:
				// One look at the jobs covers every child that ended
				// before it. Queueing one per signal would fill the
				// editor's queue when a config runs many commands, and
				// what came after (starting the plugins) would be lost.
				if !r.jobsQueued.CompareAndSwap(false, true) {
					continue
				}
				r.ed.Do(func() {
					r.jobsQueued.Store(false)
					var buf bytes.Buffer
					if sh.NotifyJobs(&buf) {
						r.ed.Notify(buf.String())
						// the job count in the prompt changed
						r.ed.Repaint()
					}
				})
			}
		}
	}()
}

// hangup ends the session on SIGHUP, once, from whichever of the editor
// or the fallback timer gets there first. At the prompt (on the editor's
// goroutine) it exits the way ctrl-d does, running ash_exit handlers and
// the EXIT trap, and takes the terminal out of the editor's raw mode in
// case it is still there (kill -HUP). Otherwise a command is running on
// the main goroutine, so it only saves the history and hangs up the jobs.
func (r *REPL) hangup(atPrompt bool) {
	r.hupOnce.Do(func() {
		status := 129
		if atPrompt {
			status = r.exit(status)
			r.restoreTerminal()
		} else {
			r.saveAndClose()
			r.sh.HangupJobs()
		}
		exitProcess(status)
	})
}

// restoreTerminal undoes the editor's terminal setup, for leaving from
// the middle of reading a line.
func (r *REPL) restoreTerminal() {
	if r.saved != nil {
		term.SetStateNow(int(r.in.Fd()), r.saved)
	}
	r.out.WriteString(ansi.ResetModeBracketedPaste)
	if r.ed.Keymap() == "vi" {
		r.out.WriteString(ansi.SetCursorStyle(0)) // the default shape
	}
}

// LoadConfig reads the configuration of an interactive shell that doesn't
// run the prompt loop (ash -ic CMD), like Run does before the first
// prompt.
func LoadConfig(sh *shell.Shell) {
	(&REPL{sh: sh}).loadConfig()
}

// loadConfig sources conf.d/*.ash then config.ash.
func (r *REPL) loadConfig() {
	sh := r.sh
	defer sh.ProfilePhase("config")()
	dir := sh.ConfigDir()
	files, _ := filepath.Glob(filepath.Join(dir, "conf.d", "*.ash"))
	sort.Strings(files)
	cfg := filepath.Join(dir, "config.ash")
	if _, err := os.Stat(cfg); err == nil {
		files = append(files, cfg)
	}
	for _, f := range files {
		sh.RunFile(f, nil, sh.StdIO())
		if exit, _ := sh.ShouldExit(); exit {
			return
		}
		sh.ResetControl()
	}
}

func (r *REPL) greet() {
	sh := r.sh
	for _, fn := range []string{"ash_greeting", "fish_greeting"} {
		if sh.Function(fn) != nil {
			sh.CallFunction(fn, nil, sh.StdIO())
			return
		}
	}
	for _, v := range []string{"ash_greeting", "fish_greeting"} {
		if g := sh.Get(v); g != "" {
			fmt.Println(g)
			return
		}
	}
}

func (r *REPL) exit(status int) int {
	sh := r.sh
	if p := r.prof; p != nil {
		// the config exited before there was a prompt
		r.prof = nil
		p.WriteReport(os.Stderr, "exit", time.Since(p.Start))
	}
	sh.Emit("ash_exit")
	sh.RunExitTrap()
	r.saveAndClose()
	sh.HangupJobs()
	if exit, st := sh.ShouldExit(); exit {
		status = st
	}
	return status
}

func (r *REPL) saveAndClose() {
	if r.hist != nil {
		r.hist.Close()
	}
	r.sh.ClosePlugins()
	for _, f := range r.cleanup {
		f()
	}
	r.cleanup = nil
}

// setTitle sets the terminal title: the directory at the prompt, the
// command (and directory) while one runs. ash_title/fish_title override.
func (r *REPL) setTitle(cmd string) {
	if !r.tty {
		return
	}
	sh := r.sh
	var title string
	// like fish: the running command is $argv[1], and at the prompt
	// there are no arguments (functions test `set -q argv[1]`)
	var args []string
	if cmd != "" {
		args = []string{cmd}
	}
	for _, fn := range []string{"ash_title", "fish_title"} {
		if out, ok := sh.CaptureFunction(fn, args...); ok {
			title = strings.TrimSpace(out)
			goto set
		}
	}
	title = tildePath(sh.Dir(), sh.HomeDir())
	if cmd != "" {
		f := strings.Fields(cmd)
		name := f[0]
		for _, w := range f {
			if !strings.Contains(w, "=") && w != "sudo" && w != "command" && w != "exec" && w != "time" {
				name = w
				break
			}
		}
		title = filepath.Base(name) + " " + title
	}
set:
	title = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, title)
	r.out.WriteString(ansi.SetWindowTitle(title))
}

// reportCwd tells the terminal our directory (OSC 7), so new tabs and
// splits open in the same place.
func (r *REPL) reportCwd() {
	if !r.tty {
		return
	}
	host, _ := os.Hostname()
	r.out.WriteString(ansi.NotifyWorkingDirectory(host, r.sh.Dir()))
}

func tildePath(p, home string) string {
	if home != "" && (p == home || strings.HasPrefix(p, home+"/")) {
		return "~" + p[len(home):]
	}
	return p
}
