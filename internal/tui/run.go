package tui

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"golang.org/x/sys/unix"

	ashterm "github.com/andrinoff/ash/internal/term"
)

// runMu makes pickers take turns: two at once would fight over the
// terminal (as in "choose a b | choose c d").
var runMu sync.Mutex

// openTTY opens the controlling terminal. Tests replace it with a pty.
var openTTY = func() (*os.File, error) {
	return os.OpenFile("/dev/tty", os.O_RDWR, 0)
}

// run shows a picker on the controlling terminal until the user answers.
//
// The terminal is used directly, not through stdin and stdout, so pickers
// work in pipelines. Everything the picker changes is undone before run
// returns, on every path out (an answer, a cancel, a signal or a panic):
//
//   - termios settings are saved before and restored after;
//   - drawing happens on the alternate screen, which Bubble Tea leaves on
//     exit, so the shell's screen and scrollback are untouched;
//   - bracketed paste, keyboard protocols and mouse modes are left alone;
//   - no terminal queries are sent. Bubble Tea v2.1.1 queries modes 2026
//     and 2027 at startup and the kitty keyboard flags on its first frame;
//     a reply that arrives after a quick exit would sit in the input
//     buffer and reach the shell as keystrokes. Running the program with
//     input disabled turns all of that off (it also keeps the keyboard
//     protocol untouched), and the keys are read here instead and decoded
//     by ultraviolet, Bubble Tea's own input decoder;
//   - reading stops as soon as the picker has an answer, so keys typed
//     after enter stay queued for the shell.
func run(m *model, opts ChooseOptions) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("tui: internal error: %v", r)
		}
	}()
	runMu.Lock()
	defer runMu.Unlock()

	tty, err := openTTY()
	if err != nil {
		return fmt.Errorf("%w: %v", ErrNoTTY, err)
	}
	defer tty.Close()
	fd := int(tty.Fd())

	// Reading the terminal from a background process group would stop the
	// whole process with SIGTTIN; tell the caller instead.
	if fg, err := unix.IoctlGetInt(fd, unix.TIOCGPGRP); err == nil && fg != unix.Getpgrp() {
		return ErrNotForeground
	}

	saved, err := ashterm.GetState(fd)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrNoTTY, err)
	}
	raw := makeRaw(*saved)
	if err := ashterm.SetStateNow(fd, &raw); err != nil {
		return fmt.Errorf("tui: %v", err)
	}
	// TCSANOW, not TCSAFLUSH: unread input must survive for the shell.
	defer ashterm.SetStateNow(fd, saved)

	in, err := newTTYReader(tty)
	if err != nil {
		return fmt.Errorf("tui: %v", err)
	}
	defer in.Close()
	m.onDone = in.Stop

	if ws, err := unix.IoctlGetWinsize(fd, unix.TIOCGWINSZ); err == nil && ws.Col > 0 && ws.Row > 0 {
		m.width, m.height = int(ws.Col), int(ws.Row)
		m.layout()
	}

	// Deferred in this order so that, even on a panic, the goroutines are
	// told to stop before they are waited for. Canceling ctx also ends the
	// program's context, which unblocks p.Send.
	var wg sync.WaitGroup
	defer wg.Wait()
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	defer in.Stop()

	env := environ(opts.Env)
	p := tea.NewProgram(m,
		tea.WithContext(ctx),
		tea.WithInput(nil), // see above: no queries; keys come from in
		tea.WithOutput(tty),
		tea.WithEnvironment(env),
		tea.WithoutSignalHandler(), // ours below keeps ignored signals ignored
	)

	events := make(chan uv.Event, 64)
	wg.Add(2)
	go func() {
		defer wg.Done()
		defer close(events)
		r := uv.NewTerminalReader(in, lookupEnv(env, "TERM"))
		r.StreamEvents(ctx, events)
	}()
	go func() {
		defer wg.Done()
		for ev := range events {
			p.Send(ev)
		}
		// The terminal went away (or we stopped reading, in which case
		// the program is already done and this is dropped).
		p.Send(inputClosedMsg{})
	}()

	// A SIGINT or SIGTERM ends the picker cleanly instead of killing the
	// process with the terminal still in raw mode on the alternate screen.
	// Signals the shell ignores stay ignored.
	var sigs []os.Signal
	for _, s := range []os.Signal{syscall.SIGINT, syscall.SIGTERM} {
		if !signal.Ignored(s) {
			sigs = append(sigs, s)
		}
	}
	if len(sigs) > 0 {
		sigc := make(chan os.Signal, 1)
		signal.Notify(sigc, sigs...)
		defer signal.Stop(sigc)
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case s := <-sigc:
				p.Send(signalMsg{s})
			case <-ctx.Done():
			}
		}()
	}

	_, runErr := p.Run()
	in.Stop()
	stop()
	wg.Wait()

	switch {
	case runErr != nil:
		return fmt.Errorf("tui: %w", runErr)
	case m.signal != nil:
		return &InterruptedError{Signal: m.signal}
	case m.canceled || !m.done:
		return ErrCanceled
	}
	return nil
}

// makeRaw turns off line editing, echo and signal keys. Output processing
// stays on: Bubble Tea's renderer, running without a tty input of its
// own, relies on "\n" moving to the start of the next line.
func makeRaw(t unix.Termios) unix.Termios {
	t.Iflag &^= unix.IGNBRK | unix.BRKINT | unix.PARMRK | unix.ISTRIP | unix.INLCR | unix.IGNCR | unix.ICRNL | unix.IXON
	t.Oflag |= unix.OPOST | unix.ONLCR
	t.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG | unix.IEXTEN
	t.Cflag &^= unix.CSIZE | unix.PARENB
	t.Cflag |= unix.CS8
	t.Cc[unix.VMIN] = 1
	t.Cc[unix.VTIME] = 0
	return t
}

// environ returns the environment for terminal and colour detection.
// NO_COLOR counts when set to anything (https://no-color.org), while
// Bubble Tea only honours values like "1" and "true".
func environ(env []string) []string {
	if env == nil {
		env = os.Environ()
	}
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "NO_COLOR="); ok {
			if v == "" {
				continue
			}
			if b, err := strconv.ParseBool(v); err != nil || !b {
				kv = "NO_COLOR=1"
			}
		}
		out = append(out, kv)
	}
	return out
}

func lookupEnv(env []string, name string) string {
	for i := len(env) - 1; i >= 0; i-- {
		if v, ok := strings.CutPrefix(env[i], name+"="); ok {
			return v
		}
	}
	return ""
}
