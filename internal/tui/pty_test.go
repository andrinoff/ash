package tui

import (
	"bytes"
	"errors"
	"os"
	"os/signal"
	"runtime"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"golang.org/x/sys/unix"

	ashterm "github.com/andrinoff/ash/internal/term"
)

// fakeTerm is a pseudo-terminal standing in for /dev/tty: pickers draw on
// its slave end, and the test reads what they draw and types keys at the
// master end.
type fakeTerm struct {
	t      *testing.T
	master *os.File
	slave  *os.File // held open so the terminal outlives each picker
	name   string

	mu   sync.Mutex
	out  bytes.Buffer
	done chan struct{}
}

func newFakeTerm(t *testing.T, cols, rows int) *fakeTerm {
	t.Helper()
	m, name, err := openPTY()
	if err != nil {
		t.Skipf("no pseudo-terminal: %v", err)
	}
	s, err := os.OpenFile(name, os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		m.Close()
		t.Skipf("no pseudo-terminal: %v", err)
	}
	ft := &fakeTerm{t: t, master: m, slave: s, name: name, done: make(chan struct{})}
	ft.resize(cols, rows)
	go ft.readLoop()

	old := openTTY
	openTTY = func() (*os.File, error) {
		return os.OpenFile(name, os.O_RDWR|unix.O_NOCTTY, 0)
	}
	t.Cleanup(func() {
		openTTY = old
		s.Close()
		select {
		case <-ft.done:
		case <-time.After(2 * time.Second):
		}
		m.Close()
	})
	return ft
}

func (ft *fakeTerm) readLoop() {
	defer close(ft.done)
	buf := make([]byte, 4096)
	for {
		n, err := ft.master.Read(buf)
		if n > 0 {
			ft.mu.Lock()
			ft.out.Write(buf[:n])
			ft.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

func (ft *fakeTerm) resize(cols, rows int) {
	ws := &unix.Winsize{Col: uint16(cols), Row: uint16(rows)}
	if err := unix.IoctlSetWinsize(int(ft.slave.Fd()), unix.TIOCSWINSZ, ws); err != nil {
		ft.t.Fatalf("resize: %v", err)
	}
}

// output is everything drawn so far.
func (ft *fakeTerm) output() string {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	return ft.out.String()
}

// waitFor waits until the output contains s.
func (ft *fakeTerm) waitFor(s string) {
	ft.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(ft.output(), s) {
		if time.Now().After(deadline) {
			ft.t.Fatalf("timed out waiting for %q; output so far: %q", s, ft.output())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// waitForCount waits until the output contains s n times.
func (ft *fakeTerm) waitForCount(s string, n int) {
	ft.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for strings.Count(ft.output(), s) < n {
		if time.Now().After(deadline) {
			ft.t.Fatalf("timed out waiting for %d× %q; output so far: %q", n, s, ft.output())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// typeKeys writes keys as if typed on the terminal.
func (ft *fakeTerm) typeKeys(s string) {
	ft.t.Helper()
	if _, err := ft.master.Write([]byte(s)); err != nil {
		ft.t.Fatalf("type: %v", err)
	}
}

// typeAndWait types keys and waits for the picker to redraw in response:
// for the output to grow and then stay quiet for a few frames.
func (ft *fakeTerm) typeAndWait(s string) {
	ft.t.Helper()
	before := len(ft.output())
	ft.typeKeys(s)
	deadline := time.Now().Add(5 * time.Second)
	last, quiet := before, time.Now()
	for time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
		switch n := len(ft.output()); {
		case n != last:
			last, quiet = n, time.Now()
		case n > before && time.Since(quiet) > 60*time.Millisecond:
			return
		}
	}
	ft.t.Fatalf("no redraw after typing %q; output so far: %q", s, ft.output())
}

// pending returns whatever input is queued on the terminal, without
// blocking: what the shell would read next.
func (ft *fakeTerm) pending() string {
	ft.t.Helper()
	fd := int(ft.slave.Fd())
	saved, err := ashterm.GetState(fd)
	if err != nil {
		ft.t.Fatal(err)
	}
	raw := *saved
	raw.Lflag &^= unix.ICANON | unix.ECHO
	raw.Cc[unix.VMIN], raw.Cc[unix.VTIME] = 0, 0
	if err := ashterm.SetStateNow(fd, &raw); err != nil {
		ft.t.Fatal(err)
	}
	defer ashterm.SetStateNow(fd, saved)
	var got []byte
	buf := make([]byte, 256)
	for {
		n, err := unix.Read(fd, buf)
		if n <= 0 || err != nil {
			if err != nil && !errors.Is(err, unix.EAGAIN) {
				ft.t.Fatalf("read: %v", err)
			}
			return string(got)
		}
		got = append(got, buf[:n]...)
	}
}

func (ft *fakeTerm) termios() unix.Termios {
	ft.t.Helper()
	tio, err := ashterm.GetState(int(ft.slave.Fd()))
	if err != nil {
		ft.t.Fatal(err)
	}
	return *tio
}

// laterInput types s at the terminal and returns what a reader of the
// terminal (the shell, after a picker) then gets.
func (ft *fakeTerm) laterInput(s string) string {
	ft.t.Helper()
	ft.typeKeys(s)
	deadline := time.Now().Add(2 * time.Second)
	var got string
	for len(got) < len(s) && time.Now().Before(deadline) {
		got += ft.pending()
		time.Sleep(5 * time.Millisecond)
	}
	return got
}

// start runs a picker in the background and returns a function that
// waits for its answer. If the test ends first, the picker is canceled
// with ctrl-c so it does not hold the terminal for the next test.
func start[T any](ft *fakeTerm, f func() (T, error)) func() (T, error) {
	t := ft.t
	t.Helper()
	type res struct {
		v   T
		err error
	}
	ch := make(chan res, 1)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		v, err := f()
		ch <- res{v, err}
	}()
	t.Cleanup(func() {
		select {
		case <-finished:
			return
		default:
		}
		ft.master.Write([]byte("\x03"))
		select {
		case <-finished:
		case <-time.After(3 * time.Second):
			t.Error("picker still running after ctrl-c")
		}
	})
	return func() (T, error) {
		t.Helper()
		select {
		case r := <-ch:
			return r.v, r.err
		case <-time.After(5 * time.Second):
			t.Fatal("picker did not return")
		}
		var zero T
		return zero, nil
	}
}

// goroutines counts goroutines, first starting os/signal's watcher, which
// the first signal.Notify in a process starts for good.
func goroutines() int {
	c := make(chan os.Signal, 1)
	signal.Notify(c, syscall.SIGWINCH)
	signal.Stop(c)
	return runtime.NumGoroutine()
}

// checkGoroutines waits for the goroutine count to fall back to base.
func checkGoroutines(t *testing.T, base int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > base {
		if time.Now().After(deadline) {
			buf := make([]byte, 1<<16)
			t.Errorf("%d goroutines left running, want %d:\n%s", runtime.NumGoroutine(), base, buf[:runtime.Stack(buf, true)])
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
}

const (
	enterAlt = "\x1b[?1049h"
	leaveAlt = "\x1b[?1049l"
)

var testEnv = []string{"TERM=xterm-256color", "HOME=/home/u"}

// checkClean checks what a picker left behind: the terminal settings as
// they were, the main screen back with its cursor shown, nothing queued in
// the input, and no reader still holding the terminal.
func checkClean(t *testing.T, ft *fakeTerm, before unix.Termios) {
	t.Helper()
	if after := ft.termios(); after != before {
		t.Errorf("terminal settings changed:\nbefore %+v\nafter  %+v", before, after)
	}
	restored := func(out string) bool {
		i := strings.LastIndex(out, leaveAlt)
		return i >= 0 && strings.Contains(out[i:], "\x1b[?25h")
	}
	// The picker wrote its last bytes before returning, but the reader of
	// the master end may not have collected them yet on a busy machine.
	out := ft.output()
	for deadline := time.Now().Add(2 * time.Second); !restored(out) && time.Now().Before(deadline); {
		time.Sleep(5 * time.Millisecond)
		out = ft.output()
	}
	if !strings.HasPrefix(out, enterAlt) {
		t.Errorf("output does not start on the alternate screen: %q", out)
	}
	if !restored(out) {
		t.Errorf("output does not end back on the main screen with the cursor shown: %q", out)
	}
	if p := ft.pending(); p != "" {
		t.Errorf("input left queued for the shell: %q", p)
	}
	if got := ft.laterInput("xyz"); got != "xyz" {
		t.Errorf("keys typed after the picker: the shell got %q, want %q", got, "xyz")
	}
}

func TestPickerOnTerminal(t *testing.T) {
	ft := newFakeTerm(t, 40, 10)
	base := goroutines()
	before := ft.termios()

	wait := start(ft, func() ([]string, error) {
		return Choose([]string{"alpha", "bravo", "charlie", "alphabet"}, ChooseOptions{Env: testEnv})
	})
	ft.waitFor("4/4")
	ft.typeAndWait("al\x1b[B")
	ft.typeKeys("\r")
	got, err := wait()
	if err != nil || !slices.Equal(got, []string{"alphabet"}) {
		t.Fatalf("Choose = %q, %v; want [alphabet]", got, err)
	}
	checkClean(t, ft, before)

	// Nothing but drawing reaches the terminal: no queries whose replies
	// could be left in the input, no keyboard protocol, bracketed paste or
	// mouse mode changes the shell would have to undo, no title or colour
	// changes.
	out := ft.output()
	for _, seq := range []string{
		"\x1b[?2026$p", "\x1b[?2027$p", // mode queries
		"\x1b[?u", "\x1b[>", "\x1b[<", "\x1b[=", // kitty keyboard, modifyOtherKeys
		"\x1b[c", "\x1b[>c", "\x1b[6n", "\x1b[?6n", "\x1b[5n", // device queries
		"\x1b[?2004", "\x1b[?1000", "\x1b[?1002", "\x1b[?1003", "\x1b[?1004", "\x1b[?1006", // modes
		"\x1b]", "\x1bP", // OSC and DCS: titles, colours, clipboard, XTGETTCAP
		"\x1b[?2026h", "\x1b[?2027h",
	} {
		if strings.Contains(out, seq) {
			t.Errorf("picker wrote %q to the terminal", seq)
		}
	}
	checkGoroutines(t, base)
}

func TestPickerCancelOnTerminal(t *testing.T) {
	for _, key := range []string{"\x1b", "\x03", "\x07"} {
		ft := newFakeTerm(t, 40, 10)
		before := ft.termios()
		wait := start(ft, func() (string, error) {
			return PickDir([]string{"/home/u/src", "/tmp"}, ChooseOptions{Env: testEnv})
		})
		ft.waitFor("2/2")
		ft.typeKeys(key)
		if got, err := wait(); err != ErrCanceled || got != "" {
			t.Errorf("key %q: PickDir = %q, %v; want ErrCanceled", key, got, err)
		}
		checkClean(t, ft, before)
	}
}

func TestPickerSIGINT(t *testing.T) {
	if signal.Ignored(syscall.SIGINT) {
		t.Skip("SIGINT is ignored")
	}
	// Should anything go wrong, keep the signal from killing the test.
	safety := make(chan os.Signal, 1)
	signal.Notify(safety, syscall.SIGINT)
	defer signal.Stop(safety)

	ft := newFakeTerm(t, 40, 10)
	before := ft.termios()
	wait := start(ft, func() (string, error) {
		return BrowseHistory([]HistoryItem{{Cmd: "make"}, {Cmd: "ls"}}, ChooseOptions{Env: testEnv})
	})
	ft.waitFor("2/2")
	syscall.Kill(os.Getpid(), syscall.SIGINT)
	_, err := wait()
	var ie *InterruptedError
	if !errors.As(err, &ie) || ie.Signal != syscall.SIGINT {
		t.Fatalf("BrowseHistory error = %v, want an interruption by SIGINT", err)
	}
	checkClean(t, ft, before)
	if signal.Ignored(syscall.SIGINT) {
		t.Errorf("SIGINT is ignored after the picker")
	}
}

func TestPickerPanicRestoresTerminal(t *testing.T) {
	ft := newFakeTerm(t, 40, 10)
	before := ft.termios()
	st := DefaultStyles()
	st.Match = st.Match.Transform(func(string) string { panic("boom") })

	// Bubble Tea reports the panic on stderr; keep the test output clean.
	devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	stderr := os.Stderr
	os.Stderr = devnull
	defer func() { os.Stderr = stderr; devnull.Close() }()

	wait := start(ft, func() ([]string, error) {
		return Choose([]string{"alpha", "bravo"}, ChooseOptions{Env: testEnv, Styles: &st})
	})
	ft.waitFor("2/2")
	ft.typeKeys("a") // draws a match: boom
	if _, err := wait(); err == nil || !errors.Is(err, tea.ErrProgramPanic) {
		t.Fatalf("error = %v, want a panic report", err)
	}
	os.Stderr = stderr
	checkClean(t, ft, before)
}

func TestPickersTakeTurns(t *testing.T) {
	ft := newFakeTerm(t, 40, 10)
	before := ft.termios()
	first := start(ft, func() ([]string, error) { return Choose([]string{"one"}, ChooseOptions{Env: testEnv}) })
	ft.waitFor("1/1")
	second := start(ft, func() ([]string, error) { return Choose([]string{"two"}, ChooseOptions{Env: testEnv}) })
	time.Sleep(50 * time.Millisecond)
	if n := strings.Count(ft.output(), enterAlt); n != 1 {
		t.Fatalf("second picker drew while the first was open")
	}
	ft.typeKeys("\r")
	if got, err := first(); err != nil || !slices.Equal(got, []string{"one"}) {
		t.Fatalf("first = %q, %v", got, err)
	}
	ft.waitForCount(enterAlt, 2)
	ft.waitForCount("1/1", 2)
	ft.typeKeys("\r")
	if got, err := second(); err != nil || !slices.Equal(got, []string{"two"}) {
		t.Fatalf("second = %q, %v", got, err)
	}
	checkClean(t, ft, before)
}

func TestPickerNoColor(t *testing.T) {
	st := DefaultStyles()
	st.Match = st.Match.Foreground(lipgloss.Color("1"))
	st.CurrentMatch = st.CurrentMatch.Foreground(lipgloss.Color("1"))
	for _, tt := range []struct {
		env       []string
		wantColor bool
	}{
		{[]string{"TERM=xterm-256color"}, true},
		{[]string{"TERM=xterm-256color", "NO_COLOR=1"}, false},
		{[]string{"TERM=xterm-256color", "NO_COLOR=yes"}, false},
	} {
		ft := newFakeTerm(t, 40, 10)
		wait := start(ft, func() ([]string, error) {
			return Choose([]string{"alpha", "bravo"}, ChooseOptions{Env: tt.env, Styles: &st})
		})
		ft.waitFor("2/2")
		ft.typeAndWait("a")
		ft.typeKeys("\r")
		if _, err := wait(); err != nil {
			t.Fatal(err)
		}
		out := ft.output()
		if got := sgrHasColor(out); got != tt.wantColor {
			t.Errorf("env %q: colour in output = %v, want %v: %q", tt.env, got, tt.wantColor, out)
		}
	}
}

// sgrHasColor reports whether any SGR sequence in s sets a colour.
func sgrHasColor(s string) bool {
	for {
		i := strings.Index(s, "\x1b[")
		if i < 0 {
			return false
		}
		s = s[i+2:]
		j := strings.IndexFunc(s, func(r rune) bool { return r < '0' || r > ';' })
		if j < 0 {
			return false
		}
		if s[j] == 'm' {
			for _, p := range strings.FieldsFunc(s[:j], func(r rune) bool { return r == ';' || r == ':' }) {
				switch {
				case len(p) == 2 && (p[0] >= '3' && p[0] <= '4' || p[0] == '9') && p[1] >= '0' && p[1] <= '8',
					len(p) == 3 && p[:2] == "10" && p[2] >= '0' && p[2] <= '7':
					return true
				}
			}
		}
		s = s[j:]
	}
}

func TestPickerWithoutTerminal(t *testing.T) {
	old := openTTY
	openTTY = func() (*os.File, error) { return nil, os.ErrNotExist }
	defer func() { openTTY = old }()
	if _, err := Choose([]string{"a"}, ChooseOptions{}); !errors.Is(err, ErrNoTTY) {
		t.Errorf("error = %v, want ErrNoTTY", err)
	}
}
