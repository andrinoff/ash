package editor

import (
	"strings"
	"unicode"
)

// The scopes of the history search. Config.SearchScopes lists the ones the
// shell offers, in the order Ctrl-R cycles through them, and
// Config.SearchHistory gets the current one with each query.
const (
	ScopeAll        = "all"        // every command
	ScopeDirectory  = "directory"  // commands run in the current directory
	ScopeRepository = "repository" // commands run anywhere in the current repository
	ScopeFailed     = "failed"     // commands that failed
)

// scopeLabel is what the search header calls a scope. ScopeAll goes
// without saying.
func scopeLabel(scope string) string {
	switch scope {
	case ScopeAll:
		return ""
	case ScopeDirectory:
		return "this directory"
	case ScopeRepository:
		return "this repository"
	}
	return scope
}

// histSearch is the incremental history search opened with Ctrl-R. The
// selected entry is previewed in the command line; Enter keeps it for
// editing, Escape restores what was there. Ctrl-R again switches to the
// next scope.
type histSearch struct {
	query     []rune
	qpos      int
	scopes    []string // what Ctrl-R cycles through
	scope     int      // the current scope, an index into scopes
	results   []SearchResult
	selected  int
	scroll    int
	saved     string
	savedPos  int
	lastLines []string
	cursorCol int
}

func fnHistoryPager(e *Editor, k Key) {
	if e.opts.Simple || e.cfg.SearchHistory == nil && e.cfg.History == nil {
		return
	}
	if e.search != nil {
		e.search.nextScope(e)
		return
	}
	s := &histSearch{saved: string(e.buf), savedPos: e.pos}
	s.query = []rune(strings.TrimSpace(string(e.buf)))
	s.qpos = len(s.query)
	if e.cfg.SearchHistory != nil && e.cfg.SearchScopes != nil {
		s.scopes = e.cfg.SearchScopes()
	}
	e.search = s
	e.pager = nil
	e.sugg = nil
	s.refresh(e)
}

// current is the scope the search is in.
func (s *histSearch) current() string {
	if s.scope < len(s.scopes) {
		return s.scopes[s.scope]
	}
	return ScopeAll
}

// nextScope switches to the next scope, keeping the query. With only one
// scope it moves down the list instead.
func (s *histSearch) nextScope(e *Editor) {
	if len(s.scopes) < 2 {
		s.move(e, 1)
		return
	}
	s.scope = (s.scope + 1) % len(s.scopes)
	s.refresh(e)
}

func (s *histSearch) refresh(e *Editor) {
	q := string(s.query)
	s.results = nil
	if e.cfg.SearchHistory != nil {
		s.results = e.cfg.SearchHistory(q, s.current(), 500)
	} else {
		for _, h := range e.cfg.History.Search(q, false) {
			r := SearchResult{Text: h}
			if i := strings.Index(strings.ToLower(h), strings.ToLower(q)); i >= 0 && q != "" {
				start := len([]rune(h[:i]))
				for j := 0; j < len([]rune(q)); j++ {
					r.Positions = append(r.Positions, start+j)
				}
			}
			s.results = append(s.results, r)
			if len(s.results) >= 500 {
				break
			}
		}
	}
	s.selected = 0
	s.scroll = 0
	s.preview(e)
}

func (s *histSearch) preview(e *Editor) {
	if len(s.results) == 0 {
		e.buf = []rune(s.saved)
		e.pos = s.savedPos
	} else {
		e.buf = []rune(s.results[s.selected].Text)
		e.pos = len(e.buf)
	}
	e.hl = nil
	if e.cfg.Highlight != nil {
		if hl := e.cfg.Highlight(string(e.buf)); len(hl) == len(e.buf) {
			e.hl = hl
		}
	}
	e.sugg = nil
}

func (s *histSearch) move(e *Editor, d int) {
	if len(s.results) == 0 {
		return
	}
	s.selected += d
	if s.selected < 0 {
		s.selected = 0
	}
	if s.selected >= len(s.results) {
		s.selected = len(s.results) - 1
	}
	s.preview(e)
}

func (s *histSearch) close(e *Editor, keep bool) {
	e.search = nil
	if !keep {
		e.buf = []rune(s.saved)
		e.pos = s.savedPos
	} else if string(e.buf) != s.saved {
		// undo goes back to the line from before the search
		e.pushUndoState([]rune(s.saved), s.savedPos)
	}
	if e.keymap == "vi" {
		e.clampNormal() // opened with / in normal mode: stay on a character
	}
	e.update()
}

func (s *histSearch) handle(e *Editor, k Key) {
	name := k.Name()
	switch {
	case k.Code == KeyEnter:
		s.close(e, true)
	case k.Code == KeyTab && k.Mod == 0, k.Code == KeyRight, name == "ctrl-e", name == "ctrl-f":
		s.close(e, true)
	case k.Code == KeyEscape, name == "ctrl-g", name == "ctrl-c":
		s.close(e, false)
	case name == "ctrl-r":
		s.nextScope(e)
	case k.Code == KeyDown, name == "ctrl-n":
		s.move(e, 1)
	case k.Code == KeyUp, name == "ctrl-p", k.Code == KeyTab && k.Mod == ModShift:
		s.move(e, -1)
	case k.Code == KeyPgDn:
		s.move(e, 10)
	case k.Code == KeyPgUp:
		s.move(e, -10)
	case k.Code == KeyBackspace, name == "ctrl-h":
		if s.qpos > 0 {
			s.query = append(s.query[:s.qpos-1], s.query[s.qpos:]...)
			s.qpos--
			s.refresh(e)
		} else if len(s.query) == 0 {
			s.close(e, false)
		}
	case name == "ctrl-u":
		s.query = s.query[s.qpos:]
		s.qpos = 0
		s.refresh(e)
	case name == "ctrl-w":
		i := s.qpos
		for i > 0 && unicode.IsSpace(s.query[i-1]) {
			i--
		}
		for i > 0 && !unicode.IsSpace(s.query[i-1]) {
			i--
		}
		s.query = append(s.query[:i], s.query[s.qpos:]...)
		s.qpos = i
		s.refresh(e)
	case k.Code == KeyLeft, name == "ctrl-b":
		if s.qpos > 0 {
			s.qpos--
		}
	case name == "ctrl-a":
		s.close(e, true)
		e.pos = 0
	case k.Paste != "":
		// the query is one line: newlines, tabs and other control
		// characters become spaces
		ins := []rune(strings.Map(func(r rune) rune {
			if r < 0x20 || r == 0x7f {
				return ' '
			}
			return r
		}, k.Paste))
		s.query = append(s.query[:s.qpos], append(ins, s.query[s.qpos:]...)...)
		s.qpos += len(ins)
		s.refresh(e)
	case k.Code == KeyNone && k.Mod&(ModCtrl|ModAlt) == 0 && k.Rune >= ' ':
		s.query = append(s.query[:s.qpos], append([]rune{k.Rune}, s.query[s.qpos:]...)...)
		s.qpos++
		s.refresh(e)
	}
}

// header lays out the query line in cols columns, keeping the last one
// free for the cursor: the label, which on a narrow screen is just the
// scope's name, and the query, scrolled to keep the cursor in view. It
// returns the label, the query as drawn, their width, and the cursor's
// column.
func (s *histSearch) header(cols int) (label, query string, width, cursor int) {
	maxLabel := max(cols-11, cols/2) // leave the query 10 columns, or half
	label = "history: "
	if l := scopeLabel(s.current()); l != "" {
		label = "history [" + l + "]: "
		if plainWidth(label) > cols-21 {
			label = ""
			if name := truncateWidth(s.current(), maxLabel-4); name != "" {
				label = "[" + name + "]: "
			}
		}
	}
	if plainWidth(label) > maxLabel {
		label = ""
	}
	lw := plainWidth(label)
	room := cols - 1 - lw
	before, at := 0, 0 // the widths of the query before the cursor and under it
	for _, r := range s.query[:s.qpos] {
		before += runeWidth(r)
	}
	if s.qpos < len(s.query) {
		at = runeWidth(s.query[s.qpos])
	}
	var b strings.Builder
	w, i := 0, 0
	if before+at > room {
		b.WriteString("…") // for what scrolled off the front
		w = 1
		for i < s.qpos && w+before+at > room {
			before -= runeWidth(s.query[i])
			i++
		}
	}
	cursor = lw + w + before
	for ; i < len(s.query); i++ {
		cw := runeWidth(s.query[i])
		if w+cw > room {
			break
		}
		b.WriteString(displayRune(s.query[i]))
		w += cw
	}
	return label, b.String(), lw + w, cursor
}

// render draws the query line and the matching entries. No line is wider
// than the screen: one that wrapped would throw off the redraw.
func (s *histSearch) render(e *Editor, cols, avail int) []string {
	prog := e.style("pager_progress")
	match := e.style("search_match")
	selBg := e.style("pager_selected_background")
	var out []string
	label, q, headW, cursor := s.header(cols)
	head := prog + label + reset + q
	count := ""
	switch n := len(s.results); {
	case n == 0:
		count = "no matches"
	case n >= 500:
		count = "500+"
	default:
		count = itoa(s.selected+1) + "/" + itoa(n)
	}
	if pad := cols - headW - plainWidth(count) - 1; pad > 1 {
		head += strings.Repeat(" ", pad) + prog + count + reset
	}
	out = append(out, head)
	s.cursorCol = cursor
	maxRows := avail - 1
	if maxRows > 10 {
		maxRows = 10
	}
	if maxRows < 1 {
		maxRows = 1
	}
	if s.selected < s.scroll {
		s.scroll = s.selected
	}
	if s.selected >= s.scroll+maxRows {
		s.scroll = s.selected - maxRows + 1
	}
	for i := s.scroll; i < len(s.results) && i < s.scroll+maxRows; i++ {
		r := s.results[i]
		sel := i == s.selected
		var line strings.Builder
		if sel {
			line.WriteString(selBg + "› ")
		} else {
			line.WriteString("  ")
		}
		text := strings.ReplaceAll(r.Text, "\n", "⏎")
		info := truncateWidth(r.Info, cols-15) // leave the command 10 columns
		room := cols - 3
		if info != "" {
			room -= plainWidth(info) + 2
		}
		pos := map[int]bool{}
		for _, p := range r.Positions {
			pos[p] = true
		}
		w := 0
		ri := 0
		rs := []rune(text)
		ws := widths(rs)
		for _, ch := range rs {
			cw := ws[ri]
			if w+cw > room {
				line.WriteString("…")
				w++
				break
			}
			if pos[ri] {
				line.WriteString(match + displayRune(ch) + reset)
				if sel {
					line.WriteString(selBg)
				}
			} else {
				line.WriteString(displayRune(ch))
			}
			w += cw
			ri++
		}
		if info != "" {
			pad := cols - 3 - w - plainWidth(info)
			if pad < 1 {
				pad = 1
			}
			line.WriteString(strings.Repeat(" ", pad))
			line.WriteString(reset)
			if sel {
				line.WriteString(selBg)
			}
			line.WriteString(prog + info)
		} else if sel {
			line.WriteString(strings.Repeat(" ", max(0, cols-3-w)))
		}
		line.WriteString(reset)
		out = append(out, line.String())
	}
	s.lastLines = out
	return out
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
