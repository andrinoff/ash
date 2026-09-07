package shell

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/andrinoff/ash/internal/tui"
)

// pickerStub stands in for the tui pickers: it records what a builtin
// asked for and answers with result or err.
type pickerStub struct {
	calls   int
	items   []string
	entries []tui.HistoryItem
	opts    tui.ChooseOptions
	result  []string
	err     error
	raised  []syscall.Signal // signals the builtin sent to the shell
}

func stubPickers(t *testing.T, s *pickerStub) {
	t.Helper()
	oc, od, oh, or := tuiChoose, tuiPickDir, tuiBrowseHistory, raiseSignal
	t.Cleanup(func() { tuiChoose, tuiPickDir, tuiBrowseHistory, raiseSignal = oc, od, oh, or })
	raiseSignal = func(sig syscall.Signal) { s.raised = append(s.raised, sig) }
	first := func() string {
		if len(s.result) == 0 {
			return ""
		}
		return s.result[0]
	}
	tuiChoose = func(items []string, opts tui.ChooseOptions) ([]string, error) {
		s.calls++
		s.items, s.opts = items, opts
		if s.err != nil {
			return nil, s.err
		}
		return s.result, nil
	}
	tuiPickDir = func(dirs []string, opts tui.ChooseOptions) (string, error) {
		s.calls++
		s.items, s.opts = dirs, opts
		if s.err != nil {
			return "", s.err
		}
		return first(), nil
	}
	tuiBrowseHistory = func(entries []tui.HistoryItem, opts tui.ChooseOptions) (string, error) {
		s.calls++
		s.entries, s.opts = entries, opts
		if s.err != nil {
			return "", s.err
		}
		return first(), nil
	}
}

// runIn runs src in sh with the given stdin.
func runIn(sh *Shell, src, stdin string) (string, string, int) {
	out, errb := &lockedBuffer{}, &lockedBuffer{}
	io := &IO{In: &Stream{R: strings.NewReader(stdin)}, Out: &Stream{W: out}, Err: &Stream{W: errb}}
	status := sh.RunString(src, "test", io)
	return out.String(), errb.String(), status
}

func TestChooseBuiltin(t *testing.T) {
	tests := []struct {
		name       string
		src, stdin string
		result     []string
		err        error
		calls      int // pickers run; -1 means 1
		items      []string
		check      func(t *testing.T, o tui.ChooseOptions)
		out, errs  string
		status     int
	}{
		{name: "items from arguments", src: "choose alpha bravo charlie", result: []string{"bravo"},
			items: []string{"alpha", "bravo", "charlie"}, out: "bravo\n"},
		{name: "items from stdin", src: "choose", stdin: "x\ny\r\n\nz", result: []string{"z"},
			items: []string{"x", "y", "z"}, out: "z\n"},
		{name: "items from an external command", src: "command printf '%s\\n' one two | choose", result: []string{"two"},
			items: []string{"one", "two"}, out: "two\n"},
		{name: "options", src: "choose --multi --header 'Pick some' --height 10 --limit 3 --query br --prompt '> ' a b",
			result: []string{"a", "b"}, items: []string{"a", "b"}, out: "a\nb\n",
			check: func(t *testing.T, o tui.ChooseOptions) {
				if !o.Multi || o.Header != "Pick some" || o.Height != 10 || o.Limit != 3 || o.Query != "br" || o.Prompt != "> " {
					t.Errorf("options = %+v", o)
				}
			}},
		{name: "options with =", src: "choose --header=H --height=50% -q x -m a", result: []string{"a"}, items: []string{"a"}, out: "a\n",
			check: func(t *testing.T, o tui.ChooseOptions) {
				if o.Header != "H" || o.Height != 20 || o.Query != "x" || !o.Multi {
					t.Errorf("options = %+v", o)
				}
			}},
		{name: "-- ends options", src: "choose -m -- -a -b", result: []string{"-b"}, items: []string{"-a", "-b"}, out: "-b\n"},
		{name: "the environment goes to the picker", src: "set -x NO_COLOR 1; choose a", result: []string{"a"}, items: []string{"a"}, out: "a\n",
			check: func(t *testing.T, o tui.ChooseOptions) {
				if !slices.Contains(o.Env, "NO_COLOR=1") || o.Styles == nil {
					t.Errorf("env %q, styles %v", o.Env, o.Styles)
				}
			}},
		{name: "canceled", src: "choose a b", err: tui.ErrCanceled, items: []string{"a", "b"}, status: 1},
		{name: "canceled in a condition", src: "choose a b; or echo none", err: tui.ErrCanceled, items: []string{"a", "b"}, out: "none\n"},
		{name: "interrupted", src: "choose a", err: &tui.InterruptedError{Signal: syscall.SIGINT}, items: []string{"a"}, status: 130},
		{name: "no terminal", src: "choose a", err: fmt.Errorf("%w: open /dev/tty: device not configured", tui.ErrNoTTY), items: []string{"a"},
			errs: "choose: no terminal: open /dev/tty: device not configured\n", status: 2},
		{name: "nothing to choose from", src: "choose", stdin: "\n\n", calls: 0, status: 1},
		{name: "unknown option", src: "choose --bogus a", calls: 0, errs: "choose: unknown option --bogus\n", status: 2},
		{name: "missing value", src: "choose --height", calls: 0, errs: "choose: --height needs a value\n", status: 2},
		{name: "bad height", src: "choose --height tall a", calls: 0, errs: "choose: invalid height \"tall\"\n", status: 2},
		{name: "bad percentage", src: "choose --height 120% a", calls: 0, errs: "choose: invalid height \"120%\"\n", status: 2},
		{name: "bad limit", src: "choose --limit -1 a", calls: 0, errs: "choose: invalid limit \"-1\"\n", status: 2},
		{name: "--multi takes no value", src: "choose --multi=yes a", calls: 0, errs: "choose: --multi takes no value\n", status: 2},
		{name: "help", src: "choose --help", calls: 0, out: chooseUsage},
		{name: "background", src: "choose a b &\nwait", calls: 0, errs: "choose: cannot run in the background\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stub := &pickerStub{result: tt.result, err: tt.err}
			stubPickers(t, stub)
			sh := New()
			sh.Init()
			sh.SetTermSize(100, 40)
			out, errs, status := runIn(sh, tt.src, tt.stdin)
			wantCalls := 1
			if tt.items == nil {
				wantCalls = tt.calls
			}
			if stub.calls != wantCalls {
				t.Fatalf("pickers run %d times, want %d (stderr %q)", stub.calls, wantCalls, errs)
			}
			if wantCalls > 0 && !slices.Equal(stub.items, tt.items) {
				t.Errorf("items = %q, want %q", stub.items, tt.items)
			}
			if tt.check != nil {
				tt.check(t, stub.opts)
			}
			if out != tt.out || errs != tt.errs || status != tt.status {
				t.Errorf("got out %q err %q status %d, want out %q err %q status %d", out, errs, status, tt.out, tt.errs, tt.status)
			}
		})
	}
}

func TestChooseRefusesTerminalStdin(t *testing.T) {
	stub := &pickerStub{}
	stubPickers(t, stub)
	sh := New()
	errb := &lockedBuffer{}
	io := &IO{In: &Stream{TTY: true}, Out: &Stream{W: &lockedBuffer{}}, Err: &Stream{W: errb}}
	if st := bChoose(sh, io, []string{"choose"}); st != 2 || stub.calls != 0 {
		t.Errorf("status %d, picker calls %d", st, stub.calls)
	}
	if want := "choose: no items: pass them as arguments or on standard input\n"; errb.String() != want {
		t.Errorf("stderr %q, want %q", errb.String(), want)
	}
}

// dirFixture makes directories a, b and c in a new directory, starts
// there, and returns it with a script that visits a, b, c and a again and
// then removes b.
func dirFixture(t *testing.T) (root, visit string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	for _, d := range []string{"a", "b", "c"} {
		if err := os.Mkdir(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return root, fmt.Sprintf("cd %[1]s/a; cd %[1]s/b; cd %[1]s/c; cd %[1]s/a; rmdir %[1]s/b\n", Quote(root))
}

func TestPickDirBuiltin(t *testing.T) {
	t.Run("cds into the choice", func(t *testing.T) {
		root, visit := dirFixture(t)
		stub := &pickerStub{result: []string{root + "/c"}}
		stubPickers(t, stub)
		sh := New()
		sh.Init()
		ed := &fakeEditor{}
		sh.Editor = ed
		out, errs, status := runIn(sh, visit+"__ash_pick_dir; pwd", "")
		// Newest first, once each, without the current directory or ones
		// that no longer exist.
		if want := []string{root + "/c", root}; !slices.Equal(stub.items, want) {
			t.Errorf("directories = %q, want %q", stub.items, want)
		}
		if out != root+"/c\n" || errs != "" || status != 0 {
			t.Errorf("out %q err %q status %d", out, errs, status)
		}
		if ed.repaints != 1 {
			t.Errorf("repaints = %d, want 1", ed.repaints)
		}
		if got := sh.Get("OLDPWD"); got != root+"/a" {
			t.Errorf("OLDPWD = %q", got)
		}
	})
	t.Run("arguments are the query", func(t *testing.T) {
		root, visit := dirFixture(t)
		stub := &pickerStub{err: tui.ErrCanceled}
		stubPickers(t, stub)
		sh := New()
		sh.Init()
		out, errs, status := runIn(sh, visit+"__ash_pick_dir s r c; pwd", "")
		if stub.opts.Query != "s r c" {
			t.Errorf("query = %q", stub.opts.Query)
		}
		if out != root+"/a\n" || errs != "" || status != 0 {
			t.Errorf("a dismissed picker should leave the directory: out %q err %q status %d", out, errs, status)
		}
	})
	t.Run("no history", func(t *testing.T) {
		dirFixture(t)
		stub := &pickerStub{}
		stubPickers(t, stub)
		sh := New()
		sh.Init()
		_, errs, status := runIn(sh, "__ash_pick_dir", "")
		if stub.calls != 0 || status != 1 || errs != "__ash_pick_dir: no previous directories\n" {
			t.Errorf("calls %d err %q status %d", stub.calls, errs, status)
		}
	})
}

type fakeHistory struct{ cmds []string }

func (h *fakeHistory) Recent(n int) []string {
	if n > 0 && n < len(h.cmds) {
		return h.cmds[:n]
	}
	return h.cmds
}
func (h *fakeHistory) Search(string, bool) []string { return nil }
func (h *fakeHistory) Delete(string) int            { return 0 }
func (h *fakeHistory) Clear()                       {}
func (h *fakeHistory) Import(string) (int, error)   { return 0, nil }

// detailedHistory also knows when, where and how each command ran.
type detailedHistory struct {
	fakeHistory
	items []tui.HistoryItem
}

func (h *detailedHistory) EachRecent(f func(cmd string, when time.Time, dir string, status int) bool) {
	for _, it := range h.items {
		if !f(it.Cmd, it.When, it.Dir, it.Status) {
			return
		}
	}
}

type fakeEditor struct {
	buf      string
	cursor   int
	repaints int
}

func (e *fakeEditor) Buffer() (string, int)                     { return e.buf, e.cursor }
func (e *fakeEditor) SetBuffer(buf string, cursor int)          { e.buf, e.cursor = buf, cursor }
func (e *fakeEditor) QueueFunctions(...string)                  {}
func (e *fakeEditor) Bind(string, string, string, string) error { return nil }
func (e *fakeEditor) Unbind(string, string) bool                { return false }
func (e *fakeEditor) Bindings() []string                        { return nil }
func (e *fakeEditor) SetKeymap(string)                          {}
func (e *fakeEditor) Keymap() string                            { return "default" }
func (e *fakeEditor) Repaint()                                  { e.repaints++ }
func (e *fakeEditor) PagerActive() bool                         { return false }

func TestHistoryBrowseBuiltin(t *testing.T) {
	when := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	detailed := []tui.HistoryItem{
		{Cmd: "make test", When: when, Dir: "/src", Status: 2},
		{Cmd: "ls", When: when.Add(-time.Hour), Dir: "/tmp", Status: 0},
	}
	tests := []struct {
		name       string
		history    History
		editor     *fakeEditor
		src        string
		result     string
		err        error
		calls      int
		entries    []tui.HistoryItem
		query      string
		out, errs  string
		status     int
		wantBuffer string
	}{
		{name: "prints without an editor", history: &fakeHistory{cmds: []string{"make", "ls"}}, src: "__ash_history_browse",
			result: "ls", calls: 1, entries: []tui.HistoryItem{{Cmd: "make", Status: -1}, {Cmd: "ls", Status: -1}}, out: "ls\n"},
		{name: "fills the command line", history: &fakeHistory{cmds: []string{"git status", "ls"}}, editor: &fakeEditor{buf: "gi", cursor: 2},
			src: "__ash_history_browse", result: "git status", calls: 1,
			entries: []tui.HistoryItem{{Cmd: "git status", Status: -1}, {Cmd: "ls", Status: -1}}, query: "gi", wantBuffer: "git status"},
		{name: "arguments are the query", history: &fakeHistory{cmds: []string{"ls"}}, editor: &fakeEditor{buf: "x"},
			src: "__ash_history_browse l s", result: "ls", calls: 1, entries: []tui.HistoryItem{{Cmd: "ls", Status: -1}}, query: "l s", wantBuffer: "ls"},
		{name: "details when the history has them", history: &detailedHistory{items: detailed}, src: "__ash_history_browse",
			result: "make test", calls: 1, entries: detailed, out: "make test\n"},
		{name: "dismissed", history: &fakeHistory{cmds: []string{"ls"}}, editor: &fakeEditor{buf: "keep"},
			src: "__ash_history_browse", err: tui.ErrCanceled, calls: 1, entries: []tui.HistoryItem{{Cmd: "ls", Status: -1}}, query: "keep", status: 1, wantBuffer: "keep"},
		{name: "empty history", history: &fakeHistory{}, src: "__ash_history_browse", status: 1},
		{name: "no history", src: "__ash_history_browse", errs: "__ash_history_browse: history is not available\n", status: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stub := &pickerStub{err: tt.err}
			if tt.result != "" {
				stub.result = []string{tt.result}
			}
			stubPickers(t, stub)
			sh := New()
			sh.Init()
			sh.History = tt.history
			if tt.editor != nil {
				sh.Editor = tt.editor
			}
			out, errs, status := runIn(sh, tt.src, "")
			if stub.calls != tt.calls {
				t.Fatalf("pickers run %d times, want %d", stub.calls, tt.calls)
			}
			if tt.calls > 0 {
				if !slices.Equal(stub.entries, tt.entries) {
					t.Errorf("entries = %+v, want %+v", stub.entries, tt.entries)
				}
				if stub.opts.Query != tt.query {
					t.Errorf("query = %q, want %q", stub.opts.Query, tt.query)
				}
			}
			if out != tt.out || errs != tt.errs || status != tt.status {
				t.Errorf("got out %q err %q status %d, want out %q err %q status %d", out, errs, status, tt.out, tt.errs, tt.status)
			}
			if tt.editor != nil {
				if tt.editor.buf != tt.wantBuffer {
					t.Errorf("command line = %q, want %q", tt.editor.buf, tt.wantBuffer)
				}
				if tt.result != "" && tt.err == nil && tt.editor.cursor != len(tt.wantBuffer) {
					t.Errorf("cursor = %d, want the end (%d)", tt.editor.cursor, len(tt.wantBuffer))
				}
			}
		})
	}
}

func TestPickerStyles(t *testing.T) {
	def := tui.DefaultStyles()
	tests := []struct {
		name  string
		setup string
		check func(t *testing.T, st *tui.Styles)
	}{
		{name: "default theme: tui's own look", check: func(t *testing.T, st *tui.Styles) {
			for _, p := range []struct{ got, want string }{
				{st.Match.Render("x"), def.Match.Render("x")},
				{st.Current.Render("x"), def.Current.Render("x")},
				{st.CurrentMatch.Render("x"), def.CurrentMatch.Render("x")},
				{st.Dim.Render("x"), def.Dim.Render("x")},
				{st.Counter.Render("x"), def.Counter.Render("x")},
				{st.Text.Render("x"), def.Text.Render("x")},
			} {
				if p.got != p.want {
					t.Errorf("style renders %q, want %q", p.got, p.want)
				}
			}
		}},
		{name: "pager prefix colour", setup: "set fish_pager_color_prefix red --underline", check: func(t *testing.T, st *tui.Styles) {
			if got := sgr(st.Match.Render("x")); got != "31 4" {
				t.Errorf("match: SGR %s, want red underlined", got)
			}
		}},
		{name: "selected background", setup: "set fish_pager_color_selected_background --background=brblack", check: func(t *testing.T, st *tui.Styles) {
			// A background replaces the default reverse video.
			if got := sgr(st.Current.Render("x")); got != "1 100" {
				t.Errorf("current row: SGR %s, want bold on bright black", got)
			}
			if got := sgr(st.CurrentMatch.Render("x")); got != "1 100 4" {
				t.Errorf("current match: SGR %s, want bold underlined on bright black", got)
			}
		}},
		{name: "ash_ variables and grey rgb", setup: "set ash_pager_color_description 777777; set ash_pager_color_progress white", check: func(t *testing.T, st *tui.Styles) {
			if got := sgr(st.Dim.Render("x")); got != "119 2 38" {
				t.Errorf("dim: SGR %s, want rgb(119,119,119)", got)
			}
			if got := sgr(st.Counter.Render("x")); got != "37" {
				t.Errorf("counter: SGR %s, want white", got)
			}
		}},
		{name: "a setting equal to the default is no customization", setup: "set fish_pager_color_progress brblack", check: func(t *testing.T, st *tui.Styles) {
			if got, want := st.Counter.Render("x"), def.Counter.Render("x"); got != want {
				t.Errorf("counter renders %q, want %q", got, want)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sh := New()
			sh.Init()
			if tt.setup != "" {
				if _, errs, st := runIn(sh, tt.setup, ""); st != 0 {
					t.Fatalf("setup: %s", errs)
				}
			}
			tt.check(t, sh.pickerStyles())
		})
	}
}

// sgr lists the distinct SGR parameters in s, sorted: Lip Gloss may repeat
// a parameter, which terminals do not mind.
func sgr(s string) string {
	seen := map[string]bool{}
	for {
		i := strings.Index(s, "\x1b[")
		if i < 0 {
			break
		}
		s = s[i+2:]
		j := strings.IndexByte(s, 'm')
		if j < 0 {
			break
		}
		for _, p := range strings.Split(s[:j], ";") {
			if p != "" && p != "0" {
				seen[p] = true
			}
		}
		s = s[j+1:]
	}
	var out []string
	for p := range seen {
		out = append(out, p)
	}
	slices.Sort(out)
	return strings.Join(out, " ")
}

func TestRunPickerStatuses(t *testing.T) {
	sigint := &tui.InterruptedError{Signal: syscall.SIGINT}
	sigterm := &tui.InterruptedError{Signal: syscall.SIGTERM}
	tests := []struct {
		name        string
		interactive bool
		setup       string
		err         error
		status      int
		errs        string
		raised      []syscall.Signal
	}{
		{name: "chosen", err: nil, status: 0},
		{name: "dismissed", err: tui.ErrCanceled, status: 1},
		// A script dies of the signal the picker caught, as it would have
		// without the picker.
		{name: "SIGINT in a script", err: sigint, status: 130, raised: []syscall.Signal{syscall.SIGINT}},
		{name: "SIGTERM in a script", err: sigterm, status: 143, raised: []syscall.Signal{syscall.SIGTERM}},
		// Traps and interactive shells already had it delivered.
		{name: "trapped SIGTERM", setup: "trap 'echo bye' TERM", err: sigterm, status: 143},
		{name: "SIGINT, interactive", interactive: true, err: sigint, status: 130},
		{name: "not in the foreground", err: tui.ErrNotForeground, status: 2, errs: "x: not in the terminal's foreground process group\n"},
		{name: "other errors", err: errors.New("boom"), status: 2, errs: "x: boom\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stub := &pickerStub{}
			stubPickers(t, stub)
			sh := New()
			if tt.setup != "" {
				if _, errs, st := runIn(sh, tt.setup, ""); st != 0 {
					t.Fatalf("setup: %s", errs)
				}
			}
			sh.Interactive = tt.interactive
			errb := &lockedBuffer{}
			io := &IO{In: &Stream{}, Out: &Stream{W: &lockedBuffer{}}, Err: &Stream{W: errb}}
			if st := sh.runPicker(io, "x", func() error { return tt.err }); st != tt.status || errb.String() != tt.errs {
				t.Errorf("status %d err %q, want %d %q", st, errb.String(), tt.status, tt.errs)
			}
			if !slices.Equal(stub.raised, tt.raised) {
				t.Errorf("raised %v, want %v", stub.raised, tt.raised)
			}
		})
	}
}
