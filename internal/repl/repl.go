// Package repl runs ash interactively: it owns the prompt loop and
// connects the line editor, the interpreter and the history.
package repl

import (
	"bytes"
	"fmt"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

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
	hist     historyBackend
	state    promptState
	cont     string
	contDone bool
	tty      bool
	warned   bool
	last     editor.Prompt
	lastDir  string
}

// Run starts an interactive session and returns the exit status.
func Run(sh *shell.Shell, initCmds []string) int {
	r := &REPL{sh: sh}
	sh.Init()
	r.tty = term.IsTerminal(int(os.Stdout.Fd())) && os.Getenv("TERM") != "dumb"

	r.hist = openHistory(sh)
	sh.History = r.hist
	r.ed = editor.New(os.Stdin, os.Stdout, r.editorConfig())
	sh.Editor = r.ed
	sh.SetReadLine(r.readLine)
	shell.EditorFunctions = editor.FunctionNames()
	shell.ExecHook = func() { r.saveAndClose() }

	r.handleSignals()
	r.importHistory()
	r.cleanup = append(r.cleanup, sh.SetupActionFile())

	sh.LoadUniversal()
	for _, c := range initCmds {
		sh.RunString(c, "", sh.StdIO())
	}
	if !sh.NoConfig {
		r.loadConfig()
	}
	if exit, st := sh.ShouldExit(); exit {
		return r.exit(st)
	}
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
		RunCommand: func(cmd string) {
			sh.KeepStatus(func() {
				sh.ClearInterrupt()
				sh.RunString(cmd, "", sh.StdIO())
				sh.ResetControl()
				sh.ApplyChildActions(sh.StdIO())
			})
		},
		EditCommand: r.editCommand,
		Style:       func(role string) string { return sh.Style(role).SGR() },
	}
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
		r.notifyJobs()
		sh.RunPrePrompt()
		sh.Emit("ash_prompt")
		if exit, st := sh.ShouldExit(); exit {
			return r.exit(st)
		}
		r.setTitle("")
		r.reportCwd()
		if r.tty {
			r.ed.EnsureNewline(sh.Style("pager_progress").SGR() + "⏎")
		}
		line, err := r.ed.ReadLine(r.readOpts())
		switch err {
		case nil:
		case editor.ErrEOF:
			if sh.StoppedJobs() && !r.warned {
				r.warned = true
				fmt.Fprintln(os.Stderr, "ash: there are stopped jobs; press ctrl-d again to exit (they will be hung up)")
				continue
			}
			return r.exit(sh.Status())
		case editor.ErrInterrupt:
			sh.Emit("ash_cancel")
			continue
		default:
			return r.exit(sh.Status())
		}
		r.warned = false
		if strings.TrimSpace(line) == "" {
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
	}
}

func (r *REPL) readOpts() editor.ReadOpts {
	o := editor.ReadOpts{
		Prompt:       r.promptFunc(),
		Wait:         150 * time.Millisecond,
		Continuation: r.continuation(),
		Transient:    r.transient,
		MarkPrompt:   r.tty,
		Initial:      r.sh.TakeNextBuffer(),
	}
	if o.Initial == "" {
		o.EmptySuggestion = r.fixLine
	}
	r.fixLine = ""
	// While a slow prompt renders, show the previous one if we're still
	// in the same directory, otherwise the instant built-in prompt.
	if r.last.Left != "" && r.lastDir == r.sh.Dir() {
		o.Placeholder = r.last
	} else {
		o.Placeholder = builtinPrompt(r.snapshot(), "insert")
	}
	inner := o.Prompt
	o.Prompt = func(km string) func() editor.Prompt {
		render := inner(km)
		dir := r.sh.Dir()
		return func() editor.Prompt {
			p := render()
			r.last, r.lastDir = p, dir
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
		os.Stdout.WriteString("\x1b]133;D;" + strconv.Itoa(status) + "\x07")
		r.notifyLong(line, status, dur)
	}
	sh.ApplyChildActions(sh.StdIO())
	if bad, good := sh.TakeFix(); bad != "" {
		r.fixLine = fixLine(line, bad, good)
	}
	if !r.secret(line) {
		r.hist.Add(line, dir, start, status, dur)
	}
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
	os.Stdout.WriteString("\x1b]9;" + msg + "\x07")
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
func (r *REPL) readLine(prompt string) (string, error) {
	line, err := r.ed.ReadLine(editor.ReadOpts{
		Prompt: func(string) func() editor.Prompt {
			return func() editor.Prompt { return editor.Prompt{Left: prompt} }
		},
		Simple: true,
	})
	if err == editor.ErrInterrupt {
		// ctrl-c in `read` stops the whole command, like in bash
		r.sh.Interrupt()
	}
	return line, err
}

func (r *REPL) notifyJobs() {
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
				r.ed.Do(func() {
					r.saveAndClose()
					sh.HangupJobs()
					os.Exit(129)
				})
				// if the editor isn't running, exit from here
				go func() {
					time.Sleep(200 * time.Millisecond)
					r.saveAndClose()
					sh.HangupJobs()
					os.Exit(129)
				}()
			case syscall.SIGCHLD:
				r.ed.Do(func() {
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

// LoadConfig sources the user's config (for `ash -i -c ...`).
func LoadConfig(sh *shell.Shell) {
	(&REPL{sh: sh}).loadConfig()
}

// loadConfig sources conf.d/*.ash then config.ash.
func (r *REPL) loadConfig() {
	sh := r.sh
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
	for _, fn := range []string{"ash_title", "fish_title"} {
		if out, ok := sh.CaptureFunction(fn, cmd); ok {
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
	os.Stdout.WriteString("\x1b]2;" + title + "\x07")
}

// reportCwd tells the terminal our directory (OSC 7), so new tabs and
// splits open in the same place.
func (r *REPL) reportCwd() {
	if !r.tty {
		return
	}
	host, _ := os.Hostname()
	u := url.URL{Scheme: "file", Host: host, Path: r.sh.Dir()}
	os.Stdout.WriteString("\x1b]7;" + u.String() + "\x1b\\")
}

func tildePath(p, home string) string {
	if home != "" && (p == home || strings.HasPrefix(p, home+"/")) {
		return "~" + p[len(home):]
	}
	return p
}
