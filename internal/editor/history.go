package editor

import (
	"strings"
	"unicode"
)

// histNav is up/down history navigation: fish-style search for entries
// containing what was typed.
type histNav struct {
	active     bool
	prefix     bool
	query      string
	saved      string
	matches    []string
	idx        int
	matchStart int
	matchEnd   int
	// token search
	tokActive bool
	tokStart  int
	tokEnd    int
	tokQuery  string
	tokens    []string
	tokIdx    int
}

func (e *Editor) startHist(prefix bool) {
	h := &e.hist
	h.active = true
	h.prefix = prefix
	h.saved = string(e.buf)
	h.query = strings.TrimSpace(h.saved)
	h.idx = -1
	h.matches = nil
	if e.cfg.History != nil {
		all := e.cfg.History.Search(h.query, prefix)
		for _, m := range all {
			if m != h.saved {
				h.matches = append(h.matches, m)
			}
		}
	}
}

// histStep moves through matching history entries: dir 1 is older.
func (e *Editor) histStep(dir int) {
	e.stepHistory(dir, false)
}

func (e *Editor) histStepPrefix(dir int) {
	e.stepHistory(dir, true)
}

func (e *Editor) stepHistory(dir int, prefix bool) {
	if e.cfg.History == nil {
		return
	}
	if !e.hist.active {
		if dir < 0 {
			return
		}
		e.startHist(prefix)
	}
	h := &e.hist
	n := h.idx + dir
	if n < -1 {
		n = -1
	}
	if n >= len(h.matches) {
		// nothing older: stay, flash nothing
		return
	}
	h.idx = n
	var text string
	if n == -1 {
		text = h.saved
		h.matchStart = -1
	} else {
		text = h.matches[n]
	}
	e.buf = []rune(text)
	e.pos = len(e.buf)
	h.matchStart, h.matchEnd = -1, -1
	if n >= 0 && h.query != "" {
		lower := strings.ToLower(text)
		q := h.query
		var i int
		if hasUpper(q) {
			i = strings.Index(text, q)
		} else {
			i = strings.Index(lower, strings.ToLower(q))
		}
		if i >= 0 {
			h.matchStart = len([]rune(text[:i]))
			h.matchEnd = h.matchStart + len([]rune(q))
		}
	}
	e.hl = nil
	if e.cfg.Highlight != nil {
		if hl := e.cfg.Highlight(text); len(hl) == len(e.buf) {
			e.hl = hl
		}
	}
	e.sugg = nil
	if n == -1 {
		h.active = false
		e.update()
	}
}

func hasUpper(s string) bool {
	for _, r := range s {
		if unicode.IsUpper(r) {
			return true
		}
	}
	return false
}

func (e *Editor) histJump(oldest bool) {
	if e.cfg.History == nil {
		return
	}
	if !e.hist.active {
		e.startHist(false)
	}
	if oldest {
		e.hist.idx = len(e.hist.matches) - 2
		e.histStep(1)
		return
	}
	e.hist.idx = 0
	e.histStep(-1)
}

// tokenSearch replaces the token under the cursor with tokens from
// history that contain it (fish's alt-up).
func (e *Editor) tokenSearch(dir int) {
	if e.cfg.History == nil {
		return
	}
	h := &e.hist
	if !h.tokActive || e.lastCmd != "history-token-search-backward" && e.lastCmd != "history-token-search-forward" {
		start := e.pos
		for start > 0 && !unicode.IsSpace(e.buf[start-1]) {
			start--
		}
		end := e.pos
		for end < len(e.buf) && !unicode.IsSpace(e.buf[end]) {
			end++
		}
		h.tokActive = true
		h.tokStart, h.tokEnd = start, end
		h.tokQuery = string(e.buf[start:end])
		h.tokIdx = -1
		h.tokens = nil
		seen := map[string]bool{h.tokQuery: true}
		for _, entry := range e.cfg.History.Search(h.tokQuery, false) {
			words := splitWords(entry)
			for i := len(words) - 1; i >= 0; i-- {
				w := words[i]
				if strings.Contains(w, h.tokQuery) && !seen[w] {
					seen[w] = true
					h.tokens = append(h.tokens, w)
				}
			}
			if len(h.tokens) > 200 {
				break
			}
		}
	}
	n := h.tokIdx + dir
	if n < -1 || n >= len(h.tokens) {
		return
	}
	h.tokIdx = n
	repl := h.tokQuery
	if n >= 0 {
		repl = h.tokens[n]
	}
	e.pushUndo()
	r := []rune(repl)
	nb := append(append(append([]rune(nil), e.buf[:h.tokStart]...), r...), e.buf[h.tokEnd:]...)
	e.buf = nb
	h.tokEnd = h.tokStart + len(r)
	e.pos = h.tokEnd
	e.update()
}
