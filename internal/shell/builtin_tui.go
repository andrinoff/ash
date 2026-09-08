package shell

import (
	"bufio"
	"errors"
	"fmt"
	"image/color"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"charm.land/lipgloss/v2"
	"golang.org/x/sys/unix"

	"github.com/andrinoff/ash/internal/term"
	"github.com/andrinoff/ash/internal/tui"
)

// The pickers; tests replace them.
var (
	tuiChoose        = tui.Choose
	tuiPickDir       = tui.PickDir
	tuiBrowseHistory = tui.BrowseHistory
)

func init() {
	builtins["choose"] = bChoose
	builtins["__ash_pick_dir"] = bPickDir
	builtins["__ash_history_browse"] = bHistoryBrowse
	builtinHelp["choose"] = "pick from a list interactively"
	builtinHelp["__ash_pick_dir"] = "pick a recent directory and go there"
	builtinHelp["__ash_history_browse"] = "search the history interactively"
}

const chooseUsage = `usage: choose [OPTIONS] [--] [ITEMS...]

Pick from ITEMS, or from the lines of standard input, by typing to filter
them. The choice is printed; the status is 1 if the picker was dismissed.

  -m, --multi          let tab and space mark several items
      --limit N        mark at most N items (above 1, implies --multi)
      --header TEXT    show TEXT above the list
      --height N[%]    use N lines (or N% of the terminal)
  -q, --query TEXT     start with TEXT typed
      --prompt TEXT    the prompt in front of the query

Keys: up and down (or ctrl-p and ctrl-n), page up and page down move;
enter accepts; esc or ctrl-c cancels. With --multi, tab and space mark
the current item and move down, shift-tab marks it and moves up.
The query matches fuzzily; words separated by spaces must all match, and
'word, ^word, word$ and !word match exactly, at the start, at the end, or
exclude.
`

// bChoose implements choose: an interactive, filterable list picker.
func bChoose(sh *Shell, io *IO, args []string) int {
	opts := sh.pickerOptions()
	a := args[1:]
	for len(a) > 0 {
		arg := a[0]
		if arg == "--" {
			a = a[1:]
			break
		}
		if len(arg) < 2 || arg[0] != '-' {
			break
		}
		name, val, hasVal := strings.Cut(arg, "=")
		if !strings.HasPrefix(name, "--") {
			name, val, hasVal = arg, "", false
		}
		needsVal := false
		switch name {
		case "-m", "--multi":
			if hasVal {
				return sh.usage(io, "choose", "--multi takes no value")
			}
			opts.Multi = true
		case "-h", "--help":
			io.Out.WriteString(chooseUsage)
			return 0
		case "--limit", "--header", "--height", "-q", "--query", "--prompt":
			needsVal = true
		default:
			return sh.usage(io, "choose", "unknown option "+arg)
		}
		if needsVal && !hasVal {
			if len(a) < 2 {
				return sh.usage(io, "choose", name+" needs a value")
			}
			val, a = a[1], a[1:]
		}
		switch name {
		case "--limit":
			n, err := strconv.Atoi(val)
			if err != nil || n < 0 {
				return sh.usage(io, "choose", "invalid limit "+strconv.Quote(val))
			}
			opts.Limit = n
		case "--header":
			opts.Header = val
		case "--height":
			h, ok := sh.pickerHeight(val)
			if !ok {
				return sh.usage(io, "choose", "invalid height "+strconv.Quote(val))
			}
			opts.Height = h
		case "-q", "--query":
			opts.Query = val
		case "--prompt":
			opts.Prompt = val
		}
		a = a[1:]
	}

	items := a
	if len(items) == 0 {
		if io.In.TTY {
			fmt.Fprintln(io.Err, "choose: no items: pass them as arguments or on standard input")
			return 2
		}
		var err error
		if items, err = readLines(io.In); err != nil {
			fmt.Fprintf(io.Err, "choose: %s\n", errText(err))
			return 2
		}
	}
	if len(items) == 0 {
		return 1
	}

	var chosen []string
	status := sh.runPicker(io, "choose", func() (err error) {
		chosen, err = tuiChoose(items, opts)
		return err
	})
	if status != 0 {
		return status
	}
	w := bufio.NewWriter(io.Out)
	for _, c := range chosen {
		w.WriteString(c)
		w.WriteByte('\n')
	}
	if err := w.Flush(); err != nil {
		return 1
	}
	return 0
}

// pickerHeight parses a --height value: lines, or a percentage of the
// terminal's height.
func (sh *Shell) pickerHeight(s string) (int, bool) {
	pct := strings.HasSuffix(s, "%")
	n, err := strconv.Atoi(strings.TrimSuffix(s, "%"))
	if err != nil || n < 0 || pct && n > 100 {
		return 0, false
	}
	if pct {
		_, rows := sh.termSize()
		n = max(rows*n/100, 2)
	}
	return n, true
}

// readLines reads the non-empty lines of r, without line endings.
func readLines(r io.Reader) ([]string, error) {
	var out []string
	br := bufio.NewReaderSize(r, 64*1024)
	for {
		line, err := br.ReadString('\n')
		line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		if line != "" {
			out = append(out, line)
		}
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return out, err
		}
	}
}

// bPickDir picks a directory from the directory history (newest first)
// and changes to it. Arguments are the initial query.
func bPickDir(sh *Shell, io *IO, args []string) int {
	seen := map[string]bool{sh.dir: true}
	var dirs []string
	for i := len(sh.dirPrev) - 1; i >= 0; i-- {
		d := sh.dirPrev[i]
		if seen[d] {
			continue
		}
		seen[d] = true
		if st, err := os.Stat(d); err == nil && st.IsDir() {
			dirs = append(dirs, d)
		}
	}
	if len(dirs) == 0 {
		fmt.Fprintf(io.Err, "%s: no previous directories\n", args[0])
		return 1
	}
	opts := sh.pickerOptions()
	opts.Query = strings.Join(args[1:], " ")
	var dir string
	status := sh.runPicker(io, args[0], func() (err error) {
		dir, err = tuiPickDir(dirs, opts)
		return err
	})
	if status != 0 {
		return status
	}
	if status = sh.changeDir(dir, io, false); status == 0 && sh.Editor != nil {
		sh.Editor.Repaint() // the prompt shows the directory
	}
	return status
}

// HistoryWalker is implemented by histories that remember when, where and
// with what status each command ran. The history browser shows those
// details when the shell's History has them.
type HistoryWalker interface {
	// EachRecent calls f for each entry, newest first, until f returns
	// false. status is negative when unknown.
	EachRecent(f func(cmd string, when time.Time, dir string, status int) bool)
}

// bHistoryBrowse searches the history and puts the chosen command on the
// command line, or prints it when there is no line editor. Arguments are
// the initial query; without them, from a key binding, the command line
// is.
func bHistoryBrowse(sh *Shell, io *IO, args []string) int {
	if sh.History == nil {
		fmt.Fprintf(io.Err, "%s: history is not available\n", args[0])
		return 1
	}
	var entries []tui.HistoryItem
	if w, ok := sh.History.(HistoryWalker); ok {
		w.EachRecent(func(cmd string, when time.Time, dir string, status int) bool {
			entries = append(entries, tui.HistoryItem{Cmd: cmd, When: when, Dir: dir, Status: status})
			return true
		})
	} else {
		recent := sh.History.Recent(0)
		entries = make([]tui.HistoryItem, len(recent))
		for i, c := range recent {
			entries[i] = tui.HistoryItem{Cmd: c, Status: -1}
		}
	}
	if len(entries) == 0 {
		return 1
	}
	// From a key binding the line being edited is the query, and the
	// choice replaces it. Typed as a command, the choice goes on the next
	// command line instead.
	binding := sh.Editor != nil && sh.Editor.Reading()
	opts := sh.pickerOptions()
	if len(args) > 1 {
		opts.Query = strings.Join(args[1:], " ")
	} else if binding {
		opts.Query, _ = sh.Editor.Buffer()
	}
	var cmd string
	status := sh.runPicker(io, args[0], func() (err error) {
		cmd, err = tuiBrowseHistory(entries, opts)
		return err
	})
	if status != 0 {
		return status
	}
	switch {
	case binding:
		sh.Editor.SetBuffer(cmd, len(cmd))
	case sh.Editor != nil && !sh.subshell:
		sh.shared.actions.buffer = cmd
	default:
		fmt.Fprintln(io.Out, cmd)
	}
	return 0
}

// raiseSignal sends a signal to the shell's own process; tests replace it.
var raiseSignal = func(s syscall.Signal) { syscall.Kill(os.Getpid(), s) }

// runPicker runs a picker for builtin name and turns the outcome into an
// exit status: 0 for a choice, 1 when dismissed, 128+n for signal n, and 2
// for errors (which it reports).
func (sh *Shell) runPicker(io *IO, name string, pick func() error) int {
	if sh.jobCtx != nil && sh.jobCtx.background {
		// It would fight the line editor over the terminal.
		fmt.Fprintf(io.Err, "%s: cannot run in the background\n", name)
		return 2
	}
	err := sh.withTerminal(pick)
	var ie *tui.InterruptedError
	switch {
	case err == nil:
		return 0
	case errors.Is(err, tui.ErrCanceled):
		return 1
	case errors.As(err, &ie):
		s, ok := ie.Signal.(syscall.Signal)
		if !ok {
			return 130
		}
		// The picker caught the signal to restore the terminal. A script
		// that does not trap it would have died of it, and still does;
		// interactive shells and traps have had it delivered already.
		if !sh.Interactive && !sh.hasTrap(s) {
			raiseSignal(s)
		}
		return 128 + int(s)
	}
	fmt.Fprintf(io.Err, "%s: %v\n", name, err)
	return 2
}

// hasTrap reports whether a trap or --on-signal handler is set for s.
func (sh *Shell) hasTrap(s syscall.Signal) bool {
	name := strings.TrimPrefix(signalName(s), "SIG")
	t := sh.shared.traps
	t.mu.Lock()
	defer t.mu.Unlock()
	_, code := t.handlers[name]
	_, fn := t.funcs[name]
	return code || fn
}

// withTerminal runs f with the shell's process group in the foreground of
// its terminal. In "ls | choose", ls's process group gets the terminal
// while choose runs inside the shell, which reading or configuring the
// terminal from the background would stop (SIGTTIN, SIGTTOU). The
// terminal goes back to that group afterwards.
func (sh *Shell) withTerminal(f func() error) error {
	fd := sh.shared.ttyFd
	if !sh.JobControl || fd < 0 {
		return f()
	}
	fg, err := term.Foreground(fd)
	pg := unix.Getpgrp()
	if err != nil || fg == pg {
		return f()
	}
	if err := term.SetForeground(fd, pg); err != nil {
		return f()
	}
	defer term.SetForeground(fd, fg) // fails harmlessly if the group is gone
	return f()
}

// pickerOptions are the options every picker starts from: the shell's
// environment (for TERM, NO_COLOR and HOME) and its look.
func (sh *Shell) pickerOptions() tui.ChooseOptions {
	return tui.ChooseOptions{Env: sh.Environ(), Styles: sh.pickerStyles()}
}

var (
	defaultThemeOnce sync.Once
	defaultTheme     *Theme
)

// pickerStyles is tui's monochrome look, with the completion pager's
// colours laid over it where the user changed them (with $fish_pager_color_*,
// $ash_pager_color_* or a theme), so pickers and pager look alike.
func (sh *Shell) pickerStyles() *tui.Styles {
	defaultThemeOnce.Do(func() { defaultTheme = DefaultTheme() })
	custom := func(role string) (Style, bool) {
		s := sh.Style(role)
		return s, s != defaultTheme.styles[role]
	}
	st := tui.DefaultStyles()
	if s, ok := custom("pager_completion"); ok {
		st.Text = lipStyle(s)
	}
	if s, ok := custom("pager_prefix"); ok {
		st.Match = lipStyle(s)
	}
	if s, ok := custom("pager_description"); ok {
		st.Dim = lipStyle(s)
		st.Header = lipStyle(s)
	}
	if s, ok := custom("pager_progress"); ok {
		st.Counter = lipStyle(s)
	}
	bg, customBg := custom("pager_selected_background")
	if s, ok := custom("pager_selected_completion"); ok || customBg {
		if ok {
			st.Current = lipStyle(s)
		} else {
			st.Current = st.Text.Bold(true)
		}
		if customBg {
			st.Current = withBackground(st.Current, bg)
		}
	}
	if s, ok := custom("pager_selected_prefix"); ok || customBg {
		if ok {
			st.CurrentMatch = lipStyle(s)
		} else {
			st.CurrentMatch = st.Match
		}
		if customBg {
			st.CurrentMatch = withBackground(st.CurrentMatch, bg)
		}
	}
	return &st
}

// lipStyle converts a shell style to a Lip Gloss style.
func lipStyle(s Style) lipgloss.Style {
	l := lipgloss.NewStyle().Bold(s.Bold).Faint(s.Dim).Italic(s.Italic).Underline(s.Underline).Reverse(s.Reverse)
	if c := lipColor(s.FG); c != nil {
		l = l.Foreground(c)
	}
	if c := lipColor(s.BG); c != nil {
		l = l.Background(c)
	}
	return l
}

// withBackground gives l the background of a selected_background style.
func withBackground(l lipgloss.Style, bg Style) lipgloss.Style {
	if c := lipColor(bg.BG); c != nil {
		l = l.Background(c)
	}
	if bg.Reverse {
		l = l.Reverse(true)
	}
	return l
}

func lipColor(c Color) color.Color {
	switch c.Kind {
	case 1:
		return lipgloss.Color(strconv.Itoa(int(c.Index)))
	case 2:
		return color.RGBA{R: c.R, G: c.G, B: c.B, A: 0xff}
	}
	return nil
}
