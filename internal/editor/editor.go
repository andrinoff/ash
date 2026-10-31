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
	SearchHistory func(query, scope string, limit int) []SearchResult
	SearchScopes  func() []string
	RunCommand    func(cmd string)
	EditCommand   func(buf string) (string, bool)
	Style         func(role string) string
	OnKeymap      func(name string)
	// Explain describes each word of a command line (alt-w).
	Explain func(buf string) string
	// ResumeJob returns the command that brings back the newest stopped
	// job ("fg %2"), or "" if there is none; ctrl-z runs it on an empty
	// line.
	ResumeJob func() string
	// Preview returns a short description of what the word at the cursor
	// (a byte offset) expands to, shown under the command line, or "".
	// It runs in a goroutine once typing pauses, never more than one
	// call at a time.
	Preview func(buf string, cursor int) string
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
	pendingKeys []Key // a key sequence that may continue a longer binding
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
	pv        previewState

	// key bindings
	keymap   string // "emacs" or "vi"
	viMode   string // "default" (normal), "insert", "visual", "replace"
	vi       viState
	binds    map[string]map[string]binding // mode -> key sequence -> binding
	queued   []string
	repaint  bool
	recalc   bool
	promptCh chan promptResult
	// Prompts render in the background and can finish out of order; each
	// request is numbered and only a newer one replaces what's shown.
	promptGen   int
	promptShown int
	promptDone  chan struct{} // closed when ReadLine returns

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
	if e.reading {
		return e.readNested(o)
	}
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
	e.previewReset()
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
	e.promptCh = make(chan promptResult, 4)
	e.promptDone = make(chan struct{})
	defer close(e.promptDone)
	e.requestPrompt()
	havePrompt := false
	select {
	case r := <-e.promptCh:
		e.setPrompt(r)
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
	waiting := false // more input is already there: draw once it's handled
	for !e.done {
		if !waiting {
			e.render()
		}
		e.reader.ask(e.readSize())
		var escC, chordC, previewC <-chan time.Time
		if e.escTimer != nil {
			escC = e.escTimer.C
		}
		if e.chordTimer != nil {
			chordC = e.chordTimer.C
		}
		if e.pv.armed {
			previewC = e.pv.timer.C
		}
		select {
		case r := <-e.promptCh:
			e.setPrompt(r)
		case d, ok := <-e.reader.data:
			if !ok {
				e.finish("", ErrEOF)
				break
			}
			waiting = e.handleInput(d)
		case <-escC:
			e.escTimer = nil
			e.processInput(true)
		case <-chordC:
			e.chordTimer = nil
			e.flushChord()
		case <-previewC:
			e.pv.armed = false
			e.runPreview()
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

// handleInput handles input from the terminal. It reports whether more
// input is already waiting, so that drawing can wait until that is handled
// too. A command run by a key binding (`read`, the external editor) may
// have read the waiting input itself: then nothing more is coming and the
// line must be drawn now.
func (e *Editor) handleInput(d ttyData) bool {
	e.reader.asked = false
	e.input = append(e.input, d.b...)
	e.processInput(false)
	return d.more && term.Pending(e.fd) > 0
}

// readNested reads a line for a command that a key binding runs while a
// line is being edited (`read` in a binding). It uses a separate editor
// so the command line, its undo history and the mode survive; typeahead
// and the kill ring are shared.
func (e *Editor) readNested(o ReadOpts) (string, error) {
	sub := New(e.in, e.out, e.cfg)
	sub.cfg.OnKeymap = nil
	sub.keymap = e.keymap
	sub.binds = e.binds
	sub.kill = e.kill
	sub.input, e.input = e.input, nil
	line, err := sub.ReadLine(o)
	e.input = append(sub.input, e.input...)
	e.kill = sub.kill
	return line, err
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
	if e.promptCh == nil {
		return
	}
	render := e.opts.Prompt(km)
	e.promptGen++
	gen, ch, done := e.promptGen, e.promptCh, e.promptDone
	go func() {
		r := promptResult{gen, render()}
		select {
		case ch <- r:
		case <-done:
		}
	}()
}

type promptResult struct {
	gen int
	p   Prompt
}

// setPrompt shows a rendered prompt unless a later request's prompt is
// already shown (a slow render for the previous vi mode must not replace
// the prompt for the current one).
func (e *Editor) setPrompt(r promptResult) {
	if r.gen > e.promptShown {
		e.prompt = r.p
		e.promptShown = r.gen
	}
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
			// The timeout runs from the last byte received, so a paste
			// that is still arriving is never cut short (and its newlines
			// run as Enter).
			d := escDelay(e.input)
			if e.escTimer == nil {
				e.escTimer = time.NewTimer(d)
			} else {
				e.escTimer.Reset(d)
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

// How long to wait for the rest of an incomplete key sequence.
var (
	escTimeout   = 30 * time.Millisecond  // a lone ESC
	seqTimeout   = 300 * time.Millisecond // a partial escape sequence
	pasteTimeout = 2 * time.Second        // a bracketed paste still arriving
)

func escDelay(input []byte) time.Duration {
	switch {
	case bytes.HasPrefix(input, []byte("\x1b[200~")):
		return pasteTimeout
	case len(input) > 1:
		return seqTimeout
	}
	return escTimeout
}

// readSize is how much to read from the terminal next: a byte at a time,
// so that input typed after the key that ends the line stays in the
// terminal for the command that runs next (`cat`, a password prompt), as
// with other shells; a bracketed paste in bulk.
func (e *Editor) readSize() int {
	if bytes.HasPrefix(e.input, []byte("\x1b[200~")) {
		return 64 << 10
	}
	return 1
}

// ttyReader reads the terminal in a goroutine that can be stopped
// without losing or stealing bytes. It reads only when asked, and no
// more than asked for.
type ttyReader struct {
	fd     int
	want   chan int
	data   chan ttyData
	asked  bool // a request is outstanding (editor goroutine only)
	stopR  *os.File
	stopW  *os.File
	quit   chan struct{}
	done   chan struct{}
	stopMu sync.Once
}

type ttyData struct {
	b    []byte
	more bool // more input was already waiting
}

func startReader(fd int) *ttyReader {
	r := &ttyReader{fd: fd, want: make(chan int, 1), data: make(chan ttyData, 1),
		quit: make(chan struct{}), done: make(chan struct{})}
	r.stopR, r.stopW, _ = os.Pipe()
	go r.loop()
	return r
}

// ask requests up to n bytes, unless a request is already outstanding.
// The answer arrives on data; whoever receives it clears asked.
func (r *ttyReader) ask(n int) {
	if !r.asked {
		r.asked = true
		r.want <- n
	}
}

func (r *ttyReader) loop() {
	defer close(r.done)
	var buf []byte
	for {
		var n int
		select {
		case n = <-r.want:
		case <-r.quit:
			return
		}
		if len(buf) < n {
			buf = make([]byte, n)
		}
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
			if fds[0].Revents&(unix.POLLIN|unix.POLLHUP|unix.POLLERR) == 0 {
				continue
			}
			m, err := unix.Read(r.fd, buf[:n])
			if m > 0 {
				b := make([]byte, m)
				copy(b, buf[:m])
				avail := term.Pending(r.fd)
				// one answer per request: data never holds more than one
				r.data <- ttyData{b, avail > 0}
				break
			}
			if m == 0 || err != nil && err != unix.EINTR && err != unix.EAGAIN {
				close(r.data)
				return
			}
		}
	}
}

// stop ends the reader and returns any bytes it read but nobody consumed.
func (r *ttyReader) stop() []byte {
	r.stopMu.Do(func() {
		close(r.quit)
		r.stopW.Write([]byte{0})
	})
	<-r.done
	var left []byte
	for {
		select {
		case d, ok := <-r.data:
			if ok {
				left = append(left, d.b...)
				continue
			}
		default:
		}
		break
	}
	r.stopR.Close()
	r.stopW.Close()
	return left
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
	// An edit from outside ends any run of repeated commands (yank-pop,
	// yank-last-arg, token search), whose saved offsets no longer apply.
	e.lastCmd = ""
	e.edited()
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
