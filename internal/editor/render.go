package editor

import (
	"bytes"
	"strconv"
	"strings"

	"github.com/andrinoff/ash/internal/term"
)

const reset = "\x1b[0m"

// frame is one laid-out screen: lines with styles and the cursor.
type frame struct {
	lines  []string
	widths []int
	row    int
	col    int
}

func (f *frame) add(s string, w int) {
	f.lines = append(f.lines, s)
	f.widths = append(f.widths, w)
}

// lineBuilder accumulates one screen line with style changes.
type lineBuilder struct {
	b     strings.Builder
	col   int
	style string
}

func (l *lineBuilder) setStyle(s string) {
	if s == l.style {
		return
	}
	if l.style != "" {
		l.b.WriteString(reset)
	}
	l.b.WriteString(s)
	l.style = s
}

func (l *lineBuilder) text(s string, w int) {
	l.b.WriteString(s)
	l.col += w
}

func (l *lineBuilder) finish() (string, int) {
	if l.style != "" {
		l.b.WriteString(reset)
	}
	s, w := l.b.String(), l.col
	l.b.Reset()
	l.col = 0
	l.style = ""
	return s, w
}

// layout computes the frame for the current state. final renders the
// accepted line (no suggestion, pager or messages).
func (e *Editor) layout(p Prompt, final bool) frame {
	cols := e.cols
	if cols < 8 {
		cols = 8
	}
	var f frame
	var lb lineBuilder
	newline := func() {
		s, w := lb.finish()
		f.add(s, w)
	}

	// Prompt lines; long ones wrap.
	pl := strings.Split(p.Left, "\n")
	for i, line := range pl {
		w := textWidth(line)
		for w > cols {
			chunk := cutWidth(line, 0, cols)
			f.add(chunk+reset, cols)
			line = cutWidth(line, cols, w)
			w = textWidth(line)
		}
		if i < len(pl)-1 {
			f.add(line+reset, w)
			continue
		}
		lb.b.WriteString(line)
		lb.b.WriteString(reset)
		lb.col = w
		if e.opts.MarkPrompt && !final {
			lb.b.WriteString("\x1b]133;B\x07")
		}
		if lb.col >= cols {
			newline()
		}
	}
	promptRow := len(f.lines)

	runes := e.buf
	var sugg []rune
	if !final && e.pos == len(e.buf) {
		sugg = e.sugg
	}
	total := len(runes) + len(sugg)
	ws := widths(append(runes[:len(runes):len(runes)], sugg...))
	suggStyle := e.style("autosuggestion")
	matchStyle := e.style("search_match")
	selStyle := e.style("selection")
	cont := e.opts.Continuation
	contW := textWidth(cont)
	selA, selB := e.visualRange()
	f.row, f.col = -1, -1
	for i := 0; i <= total; i++ {
		if i == total {
			if i == e.pos && f.row < 0 {
				// A full row pushes the cursor to the next one, but the
				// accepted line needs no row for it: output follows.
				if lb.col >= cols && !final {
					newline()
				}
				f.row, f.col = len(f.lines), lb.col
			}
			break
		}
		var r rune
		var st string
		if i < len(runes) {
			r = runes[i]
			if i < len(e.hl) {
				st = e.hl[i]
			}
			if !final && e.hist.matchStart >= 0 && i >= e.hist.matchStart && i < e.hist.matchEnd {
				st += matchStyle
			}
			if !final && selA >= 0 && i >= selA && i <= selB {
				st += selStyle
			}
		} else {
			r = sugg[i-len(runes)]
			st = suggStyle
		}
		if r == '\n' {
			if i == e.pos {
				f.row, f.col = len(f.lines), lb.col
			}
			lb.setStyle("")
			newline()
			if cont != "" {
				lb.b.WriteString(cont)
				lb.b.WriteString(reset)
				lb.col = contW
			}
			continue
		}
		w := ws[i]
		if lb.col+w > cols {
			lb.setStyle("")
			newline()
		}
		if i == e.pos {
			f.row, f.col = len(f.lines), lb.col
		}
		if w > 0 {
			// (no escape sequence inside an emoji sequence)
			lb.setStyle(st)
		}
		lb.text(displayRune(r), w)
	}
	lb.setStyle("")
	if final && e.err == ErrInterrupt {
		lb.setStyle(e.style("cancel"))
		lb.text("^C", 2)
		lb.setStyle("")
	}
	newline()
	if f.row < 0 {
		f.row, f.col = len(f.lines)-1, f.widths[len(f.widths)-1]
	}

	// Right prompt on the first line of the buffer, if it fits.
	if p.Right != "" && promptRow < len(f.lines) {
		rw := textWidth(p.Right)
		lw := f.widths[promptRow]
		if lw+1+rw <= cols {
			f.lines[promptRow] += strings.Repeat(" ", cols-lw-rw) + p.Right + reset
			f.widths[promptRow] = cols
		}
	}
	if final {
		return f
	}

	// Right under the command line: what the word at the cursor expands to.
	if e.pv.text != "" && e.search == nil && e.pager == nil && !e.opts.Simple {
		f.add(e.previewLine(cols), 0)
	}

	// Below the command line: history search, pager, messages.
	avail := e.rows - len(f.lines)
	if avail < 3 {
		avail = 3
	}
	if e.search != nil {
		for _, l := range e.search.render(e, cols, avail) {
			f.add(l, 0)
		}
		f.row, f.col = len(f.lines)-len(e.search.lastLines), e.search.cursorCol
	} else if e.pager != nil {
		for _, l := range e.pager.render(e, cols, avail) {
			f.add(l, 0)
		}
	}
	if e.message != "" {
		for _, l := range strings.Split(strings.TrimRight(e.message, "\n"), "\n") {
			if textWidth(l) > cols {
				l = cutWidth(l, 0, cols)
			}
			f.add(l+reset, 0)
		}
	}

	// Taller than the screen: show a window that contains the cursor.
	if e.rows > 0 && len(f.lines) > e.rows {
		start := len(f.lines) - e.rows
		if f.row < start {
			start = f.row
		}
		f.lines = f.lines[start : start+e.rows]
		f.widths = f.widths[start : start+e.rows]
		f.row -= start
	}
	return f
}

// render draws the current state, rewriting only lines that changed.
func (e *Editor) render() {
	if e.repaint {
		e.repaint = false
		e.prev = nil
	}
	if e.recalc {
		e.recalc = false
		e.requestPrompt()
	}
	e.previewCheck()
	f := e.layout(e.prompt, false)
	e.draw(f)
}

func (e *Editor) draw(f frame) {
	b := bufPool.Get().(*bytes.Buffer)
	b.Reset()
	defer bufPool.Put(b)
	b.WriteString("\x1b[?2026h\x1b[?25l")
	if e.cursorRow > 0 {
		b.WriteString("\x1b[" + strconv.Itoa(e.cursorRow) + "A")
	}
	b.WriteByte('\r')
	if e.opts.MarkPrompt && !e.marked {
		// click_events: the terminal may turn a click on the command
		// line into arrow keys that move the cursor there (kitty does)
		b.WriteString("\x1b]133;A;click_events=1\x07")
		e.marked = true
	}
	full := e.prev == nil
	if full {
		b.WriteString("\x1b[J")
	}
	for i, l := range f.lines {
		if i > 0 {
			b.WriteString("\r\n")
		}
		if !full && i < len(e.prev) && e.prev[i] == l {
			continue
		}
		b.WriteString("\x1b[2K")
		b.WriteString(l)
	}
	if !full && len(e.prev) > len(f.lines) {
		b.WriteString("\r\n\x1b[J\x1b[A")
	}
	if up := len(f.lines) - 1 - f.row; up > 0 {
		b.WriteString("\x1b[" + strconv.Itoa(up) + "A")
	}
	b.WriteByte('\r')
	if f.col > 0 {
		b.WriteString("\x1b[" + strconv.Itoa(f.col) + "C")
	}
	b.WriteString("\x1b[?25h\x1b[?2026l")
	e.out.Write(b.Bytes())
	e.prev = f.lines
	e.cursorRow = f.row
	e.drawn = true
}

// finalRender draws the accepted line (with the transient prompt if one
// is configured) and moves below it.
func (e *Editor) finalRender() {
	p := e.prompt
	if e.opts.Transient != nil && e.err == nil {
		if tp, ok := e.opts.Transient(string(e.buf)); ok {
			p = tp
		}
	}
	e.pager, e.search, e.message = nil, nil, ""
	e.hist.matchStart = -1
	saveViMode := e.viMode
	e.vi.visualStart = -1
	e.pos = len(e.buf)
	f := e.layout(p, true)
	e.draw(f)
	e.viMode = saveViMode
	var b strings.Builder
	if up := len(f.lines) - 1 - f.row; up < 0 {
		b.WriteString("\x1b[" + strconv.Itoa(-up) + "B")
	} else if up > 0 {
		b.WriteString("\x1b[" + strconv.Itoa(up) + "B")
	}
	b.WriteString("\r\n")
	if e.opts.MarkPrompt && e.err == nil {
		b.WriteString("\x1b]133;C\x07")
	}
	e.out.WriteString(b.String())
	e.prev = nil
	e.cursorRow = 0
}

// resize handles SIGWINCH: redraw everything at the new size.
func (e *Editor) resize() {
	cols, rows := term.Size(int(e.out.Fd()))
	if cols == e.cols && rows == e.rows {
		return
	}
	e.cols, e.rows = cols, rows
	e.prev = nil
	e.requestPrompt()
}

// printAbove writes msg above the editor and redraws below it.
func (e *Editor) printAbove(msg string) {
	if !strings.HasSuffix(msg, "\n") {
		msg += "\n"
	}
	var b strings.Builder
	if e.cursorRow > 0 {
		b.WriteString("\x1b[" + strconv.Itoa(e.cursorRow) + "A")
	}
	b.WriteString("\r\x1b[J")
	b.WriteString(strings.ReplaceAll(msg, "\n", "\r\n"))
	e.out.WriteString(b.String())
	e.prev = nil
	e.cursorRow = 0
	e.marked = false
}

// EnsureNewline makes sure the next prompt starts at column 0, marking
// output that didn't end with a newline (like zsh's PROMPT_SP).
func (e *Editor) EnsureNewline(marker string) {
	cols, _ := term.Size(int(e.out.Fd()))
	if cols < 2 {
		return
	}
	var b strings.Builder
	b.WriteString(marker)
	b.WriteString(reset)
	b.WriteString(strings.Repeat(" ", cols-textWidth(marker)))
	b.WriteString("\r\x1b[K")
	e.out.WriteString(b.String())
}

// ClearScreen clears the terminal and redraws the prompt at the top.
// clearImages deletes every image shown with the kitty graphics protocol
// (fastfetch's logo, icat, yazi's previews): clearing the screen erases
// text but leaves those in place. Terminals without the protocol ignore it.
const clearImages = "\x1b_Ga=d,d=A,q=2\x1b\\"

func (e *Editor) clearScreen() {
	e.out.WriteString(clearImages + "\x1b[H\x1b[2J")
	e.prev = nil
	e.cursorRow = 0
	e.marked = false
}
