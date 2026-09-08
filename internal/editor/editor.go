// Package editor is ash's line editor: a small, fast, fish-style
// editor with syntax highlighting, autosuggestions, a completion pager,
// incremental history search, emacs and vi key bindings, and a renderer
// that redraws only the lines that changed.
//
// It knows nothing about the shell; everything shell-specific comes in
// through Config hooks.
package editor

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/andrinoff/ash/internal/term"
)

var (
	// ErrInterrupt is returned when the user cancels the line (Ctrl-C).
	ErrInterrupt = errors.New("interrupt")
	// ErrEOF is returned for Ctrl-D on an empty line or a closed terminal.
	ErrEOF = io.EOF
)

// Prompt is a rendered prompt. Left may span several lines.
type Prompt struct {
	Left, Right string
}

// HistorySource is what the editor needs from the history.
type HistorySource interface {
	// Search returns entries containing (or starting with) query,
	// newest first, without duplicates.
	Search(query string, prefix bool) []string
}

// SearchResult is one row of the interactive history search.
type SearchResult struct {
	Text      string
	Positions []int  // rune indices of matched characters
	Info      string // right-aligned extra text (age, directory)
}

// Config holds the hooks that connect the editor to a shell. Every hook
// is optional.
type Config struct {
	Highlight     func(buf string) []string
	Suggest       func(buf string) string
	Complete      func(buf string, cursor int) Completion
	Incomplete    func(buf string) bool
	ExpandAbbr    func(buf string, cursor int) (string, int, bool)
	History       HistorySource
	SearchHistory func(query string, limit int) []SearchResult
	RunCommand    func(cmd string)
	EditCommand   func(buf string) (string, bool)
	Style         func(role string) string
	OnKeymap      func(name string)
}

// ReadOpts configures one ReadLine call.
type ReadOpts struct {
	// Prompt is called on the editor's goroutine with the keymap
	// ("insert", "vicmd", ...) and returns a function that renders the
	// prompt; that function runs in the background while the user types.
	Prompt func(keymap string) func() Prompt
	// Placeholder is drawn if Prompt takes longer than Wait.
	Placeholder Prompt
	Wait        time.Duration
	// Continuation prefixes the second and later lines of the buffer.
	Continuation string
	// Transient, if set, renders the prompt shown for the accepted line.
	Transient func(line string) (Prompt, bool)
	// Initial text of the buffer.
	Initial string
	// Simple disables history, suggestions and completion (for `read`).
	Simple bool
	// EmptySuggestion is offered as the autosuggestion while the line is
	// empty (a corrected version of the last command, say).
	EmptySuggestion string
	// MarkPrompt emits OSC 133 semantic prompt marks.
	MarkPrompt bool
}

// Editor is an interactive line editor bound to a terminal.
type Editor struct {
	cfg Config
	in  *os.File
	out *os.File
	fd  int

	cols, rows int
	rawState   *unix.Termios
	saved      *unix.Termios

	// line state
	buf     []rune
	pos     int
	hl      []string
	sugg    []rune
	noSugg  bool
	prompt  Prompt
	opts    ReadOpts
	done    bool
	reading bool // inside ReadLine (so a binding is running, not a command)
	result  string
	err     error
	message string

	// render state
	prev      []string
	cursorRow int
	drawn     bool
	marked    bool

	// input
	input       []byte
	pendingKeys []string
	reader      *ttyReader
	escTimer    *time.Timer
	chordTimer  *time.Timer

	// editing helpers
	kill      killRing
	undo      []snapshot
	redo      []snapshot
	lastCmd   string
	yankStart int
	yankEnd   int
	hist      histNav
	lastArg   lastArgNav
	pager     *pager
	search    *histSearch
	quoted    bool

	// key bindings
	keymap   string // "emacs" or "vi"
	viMode   string // "default" (normal), "insert", "visual", "replace"
	vi       viState
	binds    map[string]map[string]binding // mode -> key sequence -> binding
	queued   []string
	repaint  bool
	recalc   bool
	promptCh chan Prompt

	async chan func()
	mu    sync.Mutex
}

type snapshot struct {
	buf []rune
	pos int
}

// New creates an editor reading from in and drawing to out.
func New(in, out *os.File, cfg Config) *Editor {
	e := &Editor{
		cfg:    cfg,
		in:     in,
		out:    out,
		fd:     int(in.Fd()),
		keymap: "emacs",
		viMode: "insert",
		binds:  map[string]map[string]binding{},
		async:  make(chan func(), 64),
	}
	e.cols, e.rows = term.Size(int(out.Fd()))
	return e
}

// SetConfig replaces the hooks.
func (e *Editor) SetConfig(cfg Config) { e.cfg = cfg }

// Do runs f on the editor's goroutine while ReadLine is active, or
// immediately otherwise. Safe to call from any goroutine.
func (e *Editor) Do(f func()) {
	select {
	case e.async <- f:
	default:
	}
}

// Notify prints msg above the prompt (job notices and the like). Safe to
// call from any goroutine.
func (e *Editor) Notify(msg string) {
	e.Do(func() { e.printAbove(msg) })
}

// ReadLine reads one line of input.
func (e *Editor) ReadLine(o ReadOpts) (string, error) {
	e.reading = true
	defer func() { e.reading = false }()
	e.opts = o
	e.buf = []rune(o.Initial)
	e.pos = len(e.buf)
	e.sugg, e.hl = nil, nil
	e.noSugg = false
	e.done, e.result, e.err = false, "", nil
	e.prev, e.cursorRow, e.drawn, e.marked = nil, 0, false, false
	e.undo, e.redo = nil, nil
	e.hist = histNav{}
	e.lastArg = lastArgNav{}
	e.pager, e.search = nil, nil
	e.message = ""
	e.queued = nil
	e.pendingKeys = nil
	if e.keymap == "vi" {
		e.viMode = "insert"
	}
	e.cols, e.rows = term.Size(int(e.out.Fd()))

	if err := e.enterRaw(); err != nil {
		return e.readCooked(o)
	}
	defer e.leaveRaw()

	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	defer signal.Stop(winch)

	e.reader = startReader(e.fd)
	defer func() {
		left := e.reader.stop()
		e.input = append(e.input, left...)
		e.reader = nil
	}()

	e.update()
	e.promptCh = make(chan Prompt, 4)
	e.requestPrompt()
	havePrompt := false
	select {
	case p := <-e.promptCh:
		e.prompt = p
		havePrompt = true
	case <-time.After(o.Wait):
	}
	if !havePrompt {
		if o.Prompt == nil {
			havePrompt = true
		} else {
			e.prompt = o.Placeholder
			havePrompt = true
		}
	}
	if e.cfg.OnKeymap != nil {
		e.cfg.OnKeymap(e.keymapName())
	}
	e.setCursorShape()
	e.processInput(false)
	for !e.done {
		e.render()
		var escC, chordC <-chan time.Time
		if e.escTimer != nil {
			escC = e.escTimer.C
		}
		if e.chordTimer != nil {
			chordC = e.chordTimer.C
		}
		select {
		case p := <-e.promptCh:
			e.prompt = p
		case data, ok := <-e.reader.data:
			if !ok {
				e.finish("", ErrEOF)
				break
			}
			e.input = append(e.input, data...)
			e.processInput(false)
		case <-escC:
			e.escTimer = nil
			e.processInput(true)
		case <-chordC:
			e.chordTimer = nil
			e.flushChord()
		case <-winch:
			e.resize()
		case f := <-e.async:
			f()
		}
	}
	e.finalRender()
	e.resetCursorShape()
	return e.result, e.err
}

func (e *Editor) keymapName() string {
	if e.keymap == "vi" {
		return e.viMode
	}
	return "default"
}

// requestPrompt (re)computes the prompt in the background.
func (e *Editor) requestPrompt() {
	if e.opts.Prompt == nil {
		return
	}
	km := e.keymapName()
	if e.keymap == "vi" {
		switch e.viMode {
		case "default":
			km = "vicmd"
		case "visual":
			km = "visual"
		case "replace":
			km = "replace"
		default:
			km = "insert"
		}
	} else {
		km = "insert"
	}
	render := e.opts.Prompt(km)
	ch := e.promptCh
	go func() {
		p := render()
		select {
		case ch <- p:
		default:
		}
	}()
}

// readCooked is the fallback when the terminal can't go raw.
func (e *Editor) readCooked(o ReadOpts) (string, error) {
	p := Prompt{}
	if o.Prompt != nil {
		p = o.Prompt("insert")()
	}
	e.out.WriteString(p.Left)
	var line []byte
	b := make([]byte, 1)
	for {
		n, err := e.in.Read(b)
		if n == 1 {
			if b[0] == '\n' {
				return string(line), nil
			}
			line = append(line, b[0])
		}
		if err != nil {
			if len(line) > 0 {
				return string(line), nil
			}
			return "", ErrEOF
		}
	}
}

func (e *Editor) enterRaw() error {
	st, err := term.GetState(e.fd)
	if err != nil {
		return err
	}
	e.saved = st
	raw := term.Raw(st)
	if err := term.SetStateNow(e.fd, raw); err != nil {
		return err
	}
	e.rawState = raw
	// bracketed paste on
	e.out.WriteString("\x1b[?2004h")
	return nil
}

func (e *Editor) leaveRaw() {
	e.out.WriteString("\x1b[?2004l")
	if e.saved != nil {
		term.SetStateNow(e.fd, e.saved)
	}
}

// suspend restores the terminal for running a command from a binding;
// resume puts it back.
func (e *Editor) suspend() {
	e.resetCursorShape()
	e.out.WriteString("\x1b[?2004l")
	if e.saved != nil {
		term.SetStateNow(e.fd, e.saved)
	}
	if e.reader != nil {
		left := e.reader.stop()
		e.input = append(e.input, left...)
		e.reader = nil
	}
}

func (e *Editor) resume() {
	if st, err := term.GetState(e.fd); err == nil {
		e.saved = st
		raw := term.Raw(st)
		term.SetStateNow(e.fd, raw)
	}
	e.out.WriteString("\x1b[?2004h")
	e.setCursorShape()
	e.reader = startReader(e.fd)
	e.cols, e.rows = term.Size(int(e.out.Fd()))
}

// processInput decodes and handles buffered input.
func (e *Editor) processInput(final bool) {
	for len(e.input) > 0 && !e.done {
		k, n, more := decodeKey(e.input, final)
		if more {
			if e.escTimer == nil {
				e.escTimer = time.NewTimer(escDelay(e.input))
			}
			return
		}
		e.input = e.input[n:]
		if e.escTimer != nil {
			e.escTimer.Stop()
			e.escTimer = nil
		}
		if k.Junk {
			continue
		}
		e.handleKey(k)
		if !e.done && len(e.input) > 0 && e.reader != nil {
			// keep rendering responsive during large pastes of keys
			continue
		}
	}
}

func escDelay(input []byte) time.Duration {
	switch {
	case bytes.HasPrefix(input, []byte("\x1b[200~")):
		// a bracketed paste still arriving
		return 2 * time.Second
	case len(input) > 1:
		return 300 * time.Millisecond
	}
	return 30 * time.Millisecond
}

// ttyReader reads the terminal in a goroutine that can be stopped
// without losing or stealing bytes.
type ttyReader struct {
	fd     int
	data   chan []byte
	stopR  *os.File
	stopW  *os.File
	done   chan struct{}
	stopMu sync.Once
}

func startReader(fd int) *ttyReader {
	r := &ttyReader{fd: fd, data: make(chan []byte, 16), done: make(chan struct{})}
	r.stopR, r.stopW, _ = os.Pipe()
	go r.loop()
	return r
}

func (r *ttyReader) loop() {
	defer close(r.done)
	buf := make([]byte, 4096)
	for {
		fds := []unix.PollFd{
			{Fd: int32(r.fd), Events: unix.POLLIN},
			{Fd: int32(r.stopR.Fd()), Events: unix.POLLIN},
		}
		_, err := unix.Poll(fds, -1)
		if err != nil {
			if err == unix.EINTR {
				continue
			}
			close(r.data)
			return
		}
		if fds[1].Revents != 0 {
			return
		}
		if fds[0].Revents&unix.POLLNVAL != 0 {
			close(r.data)
			return
		}
		if fds[0].Revents&(unix.POLLIN|unix.POLLHUP|unix.POLLERR) != 0 {
			n, err := unix.Read(r.fd, buf)
			if n > 0 {
				b := make([]byte, n)
				copy(b, buf[:n])
				// stop() drains the channel, so this never blocks forever
				r.data <- b
			}
			if n == 0 || err != nil && err != unix.EINTR && err != unix.EAGAIN {
				close(r.data)
				return
			}
		}
	}
}

// stop ends the reader and returns any bytes it read but nobody consumed.
func (r *ttyReader) stop() []byte {
	r.stopMu.Do(func() { r.stopW.Write([]byte{0}) })
	var left []byte
	for {
		select {
		case b, ok := <-r.data:
			if !ok {
				<-r.done
				r.stopR.Close()
				r.stopW.Close()
				return left
			}
			left = append(left, b...)
		case <-r.done:
			for {
				select {
				case b, ok := <-r.data:
					if !ok {
						r.stopR.Close()
						r.stopW.Close()
						return left
					}
					left = append(left, b...)
				default:
					r.stopR.Close()
					r.stopW.Close()
					return left
				}
			}
		}
	}
}

// Buffer returns the current command line and cursor (byte offset).
func (e *Editor) Buffer() (string, int) {
	s := string(e.buf)
	return s, len(string(e.buf[:e.pos]))
}

// SetBuffer replaces the command line. cursor is a byte offset.
func (e *Editor) SetBuffer(buf string, cursor int) {
	e.pushUndo()
	e.buf = []rune(buf)
	if cursor > len(buf) {
		cursor = len(buf)
	}
	if cursor < 0 {
		cursor = 0
	}
	e.pos = len([]rune(buf[:cursor]))
	e.update()
}

// QueueFunctions schedules named editor functions (commandline -f).
func (e *Editor) QueueFunctions(names ...string) {
	e.queued = append(e.queued, names...)
}

// Repaint asks for a full redraw (and a fresh prompt).
func (e *Editor) Repaint() {
	e.repaint = true
	e.recalc = true
}

// Reading reports whether the editor is reading a line. Code run from a
// key binding sees true; a command typed at the prompt sees false, since
// the line was already accepted.
func (e *Editor) Reading() bool { return e.reading }

// PagerActive reports whether the completion pager is open.
func (e *Editor) PagerActive() bool { return e.pager != nil }

// Keymap returns "emacs" or "vi".
func (e *Editor) Keymap() string { return e.keymap }

// ViMode returns the current vi mode name.
func (e *Editor) ViMode() string { return e.viMode }

// SetKeymap switches between "emacs" and "vi" key bindings.
func (e *Editor) SetKeymap(name string) {
	switch name {
	case "vi", "fish_vi_key_bindings":
		e.keymap = "vi"
		e.viMode = "insert"
	default:
		e.keymap = "emacs"
		e.viMode = "insert"
	}
	if e.cfg.OnKeymap != nil {
		e.cfg.OnKeymap(e.keymapName())
	}
}

func (e *Editor) finish(line string, err error) {
	e.done = true
	e.result = line
	e.err = err
}

// update recomputes highlighting and the autosuggestion after an edit.
func (e *Editor) update() {
	s := string(e.buf)
	e.hl = nil
	if e.cfg.Highlight != nil && !e.opts.Simple {
		hl := e.cfg.Highlight(s)
		if len(hl) == len(e.buf) {
			e.hl = hl
		}
	}
	e.sugg = nil
	if len(e.buf) == 0 && e.opts.EmptySuggestion != "" && !e.noSugg && !e.opts.Simple && e.search == nil {
		e.sugg = []rune(e.opts.EmptySuggestion)
		return
	}
	if e.cfg.Suggest != nil && !e.opts.Simple && !e.noSugg && e.pos == len(e.buf) && len(e.buf) > 0 && e.search == nil {
		if sg := e.cfg.Suggest(s); len(sg) > len(s) && strings.HasPrefix(sg, s) {
			e.sugg = []rune(sg[len(s):])
		}
	}
}

func (e *Editor) style(role string) string {
	if e.cfg.Style == nil {
		switch role {
		case "autosuggestion", "pager_description", "pager_progress":
			return "\x1b[90m"
		case "pager_selected_background":
			return "\x1b[7m"
		case "search_match":
			return "\x1b[1m"
		case "cancel":
			return "\x1b[7m"
		}
		return ""
	}
	return e.cfg.Style(role)
}

func (e *Editor) write(s string) { e.out.WriteString(s) }

var bufPool = sync.Pool{New: func() any { return new(bytes.Buffer) }}
