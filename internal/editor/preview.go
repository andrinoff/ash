package editor

import (
	"strings"
	"time"
	"unicode"
)

// previewDelay is how long typing must pause before the preview of the
// word at the cursor is computed.
const previewDelay = 40 * time.Millisecond

// previewState is the expansion preview: a line under the command line
// showing what the word at the cursor expands to. The Preview hook runs in
// a goroutine once typing pauses, one call at a time; results for an older
// buffer are dropped. While the same word is edited the old result stays
// up until the new one arrives, so the line doesn't flicker as you type.
type previewState struct {
	text   string // what the line shows; "" hides it
	gen    uint64 // bumped whenever the buffer or the cursor changes
	buf    []rune // the buffer and cursor gen stands for
	pos    int
	start  int         // where the word at pos starts in buf, or -1
	valid  bool        // buf and pos are set
	stale  bool        // work it out again even if buf and pos are the same
	timer  *time.Timer // the pause before computing
	armed  bool        // timer is running
	busy   bool        // a Preview call is running
	queued bool        // the timer fired while busy
}

// previewCheck notices edits and cursor movement and schedules a new
// preview. It runs before every render.
func (e *Editor) previewCheck() {
	pv := &e.pv
	if e.cfg.Preview == nil || e.opts.Simple {
		pv.text = ""
		return
	}
	if e.search != nil || e.pager != nil {
		// the preview is hidden; look again when they close
		return
	}
	if pv.valid && !pv.stale && pv.pos == e.pos && runesEqual(pv.buf, e.buf) {
		return
	}
	start, end := wordAt(e.buf, e.pos)
	// the same word: it starts at the same place, after the same text
	same := pv.valid && start >= 0 && start == pv.start && runesEqual(pv.buf[:start], e.buf[:start])
	pv.buf = append(pv.buf[:0], e.buf...)
	pv.pos, pv.start = e.pos, start
	pv.valid, pv.stale = true, false
	pv.gen++
	if start < 0 || !hasExpansionRune(e.buf[start:end]) {
		// nothing here can expand: clear the line, and don't ask
		pv.text = ""
		pv.queued = false
		if pv.armed {
			pv.timer.Stop()
			pv.armed = false
		}
		return
	}
	if !same {
		// the old text is about another word (or another line)
		pv.text = ""
	}
	if pv.timer == nil {
		pv.timer = time.NewTimer(previewDelay)
	} else {
		pv.timer.Reset(previewDelay)
	}
	pv.armed = true
}

// previewStale has the preview worked out again although the line didn't
// change: a key binding ran shell code, which may have changed what the
// words expand to (alt-c cd's into another directory, say).
func (e *Editor) previewStale() { e.pv.stale = true }

// runPreview calls the Preview hook in the background for the current
// buffer. The result comes back through the async channel.
func (e *Editor) runPreview() {
	pv := &e.pv
	hook := e.cfg.Preview
	if hook == nil || !pv.valid || e.opts.Simple {
		return
	}
	if pv.busy {
		pv.queued = true
		return
	}
	pv.busy = true
	buf := string(pv.buf)
	cursor := len(string(pv.buf[:pv.pos]))
	gen := pv.gen
	ch := e.async
	go func() {
		text := ""
		func() {
			// a broken hook must not take the shell down
			defer func() { _ = recover() }()
			text = hook(buf, cursor)
		}()
		ch <- func() { e.previewDone(gen, text) }
	}()
}

// previewDone takes a result on the editor's goroutine.
func (e *Editor) previewDone(gen uint64, text string) {
	pv := &e.pv
	pv.busy = false
	queued := pv.queued
	pv.queued = false
	if gen == pv.gen {
		pv.text = text
		return
	}
	// The buffer changed while the hook ran. If the pause after that
	// change is already over, compute again now; otherwise the timer will.
	if queued {
		e.runPreview()
	}
}

// previewReset forgets the preview at the start of a line. A call still
// running for the previous line finishes, and its result is dropped.
func (e *Editor) previewReset() {
	pv := &e.pv
	pv.gen++
	pv.text = ""
	pv.valid = false
	pv.queued = false
	if pv.armed {
		pv.timer.Stop()
		pv.armed = false
	}
}

// previewLine renders the preview, cut to the width of the screen.
// Control characters are shown in caret notation, so a value can't send
// escape sequences to the terminal.
func (e *Editor) previewLine(cols int) string {
	st := e.style("preview")
	if st == "" {
		st = e.style("autosuggestion")
	}
	text := e.pv.text
	limit := cols
	cut := plainWidth(text) > cols
	if cut {
		limit = cols - 1
	}
	var b strings.Builder
	b.WriteString(st)
	w := 0
	for _, r := range text {
		rw := runeWidth(r)
		if w+rw > limit {
			break
		}
		b.WriteString(displayRune(r))
		w += rw
	}
	if cut {
		b.WriteString("…")
	}
	b.WriteString(reset)
	return b.String()
}

func runesEqual(a, b []rune) bool {
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

// wordMayExpand reports whether the word under or just before the cursor
// has anything a shell could expand. It is a quick, generous check (quotes
// and brackets keep a word together) that decides whether to clear the
// preview at once; the Preview hook has the final say.
func wordMayExpand(buf []rune, pos int) bool {
	start, end := wordAt(buf, pos)
	return start >= 0 && hasExpansionRune(buf[start:end])
}

// wordAt returns where the word under or just before the cursor starts
// and ends in buf, the generous way wordMayExpand sees words, or -1, -1.
func wordAt(buf []rune, pos int) (int, int) {
	var quote rune
	esc := false
	depth := 0
	start := -1
	for i, r := range buf {
		sep := false
		switch {
		case esc:
			esc = false
		case r == '\\' && quote != '\'':
			esc = true
		case quote != 0:
			if r == quote {
				quote = 0
			}
		case r == '\'' || r == '"' || r == '`':
			quote = r
		case r == '(' || r == '{' || r == '[':
			depth++
		case r == ')' || r == '}' || r == ']':
			if depth > 0 {
				depth--
			}
		case depth == 0 && unicode.IsSpace(r):
			sep = true
		}
		if !sep {
			if start < 0 {
				start = i
			}
			continue
		}
		if start >= 0 && pos >= start && pos <= i {
			return start, i
		}
		start = -1
		if i >= pos {
			return -1, -1
		}
	}
	if start >= 0 && pos >= start {
		return start, len(buf)
	}
	return -1, -1
}

func hasExpansionRune(word []rune) bool {
	for _, r := range word {
		switch r {
		case '$', '*', '?', '[', '{', '~', '!':
			return true
		}
	}
	return false
}
