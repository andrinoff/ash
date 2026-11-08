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

// scopeTab is what the search's tab for a scope says.
func scopeTab(scope string) string {
	switch scope {
	case ScopeDirectory:
		return "here"
	case ScopeRepository:
		return "repo"
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

// The search draws, from the top:
//
//	⌕ git pu                                   all  here  repo  failed
//	▌· git push -u origin main                         ~/src/ash    2m
//	 ✗ git pull --rebase                               ~/src/ash    1h
//	  ╰ ~/src/ash · 2m ago · took 3.2s                              1/52
//
// a query line with the scopes as tabs, one row per command with its
// status, directory and age, and the details of the selected one.

// searchIcon starts the query line.
const searchIcon = "⌕ "

// tabs draws the scope tabs, the current one highlighted. With room for
// less than all of them it shows only the current one.
func (s *histSearch) tabs(e *Editor, room int) (string, int) {
	if len(s.scopes) < 2 && s.current() == ScopeAll {
		return "", 0
	}
	on, off := e.style("pager_selected_background"), e.style("pager_progress")
	var b strings.Builder
	w := 0
	for i, sc := range s.scopes {
		name := " " + scopeTab(sc) + " "
		if i == s.scope {
			b.WriteString(on + name + reset)
		} else {
			b.WriteString(off + name + reset)
		}
		w += plainWidth(name)
	}
	if w <= room {
		return b.String(), w
	}
	name := " " + scopeTab(s.current()) + " "
	if plainWidth(name) > room {
		return "", 0
	}
	return on + name + reset, plainWidth(name)
}

// header lays out the query in cols columns, keeping the last one free
// for the cursor, scrolled to keep the cursor in view. It returns the
// query line without the tabs, its width, and the cursor's column.
func (s *histSearch) header(cols int) (label, query string, width, cursor int) {
	label = searchIcon
	lw := plainWidth(label)
	room := max(cols-1-lw, 1)
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

// render draws the search. No line is as wide as the screen: one that
// wrapped would throw off the redraw.
func (s *histSearch) render(e *Editor, cols, avail int) []string {
	dim := e.style("pager_progress")
	match := e.style("search_match")
	bar := e.style("search_match")
	failed := e.style("error")
	if failed == "" {
		failed = "\x1b[31m"
	}
	var out []string

	// the query, and the tabs if there's room for them and 12 columns of query
	tabs, tw := s.tabs(e, cols-1-plainWidth(searchIcon)-12-2)
	qcols := cols
	if tw > 0 {
		qcols = cols - tw - 2
	}
	label, q, headW, cursor := s.header(qcols)
	head := dim + label + reset + q
	if tw > 0 {
		head += strings.Repeat(" ", max(cols-1-headW-tw, 2)) + tabs
	}
	out = append(out, head)
	s.cursorCol = cursor

	maxRows := min(max(avail-2, 1), 10)
	if s.selected < s.scroll {
		s.scroll = s.selected
	}
	if s.selected >= s.scroll+maxRows {
		s.scroll = s.selected - maxRows + 1
	}
	// the directory column is as wide as the widest one shown, within reason
	dirW := 0
	for i := s.scroll; i < len(s.results) && i < s.scroll+maxRows; i++ {
		dirW = max(dirW, plainWidth(s.results[i].Dir))
	}
	dirW = min(dirW, max(cols/4, 8))
	ageW := 3
	for i := s.scroll; i < len(s.results) && i < s.scroll+maxRows; i++ {
		ageW = max(ageW, plainWidth(s.results[i].Age))
	}
	for i := s.scroll; i < len(s.results) && i < s.scroll+maxRows; i++ {
		out = append(out, s.row(s.results[i], i == s.selected, cols, dirW, ageW, dim, match, bar, failed))
	}
	out = append(out, s.footer(cols, dim))
	s.lastLines = out
	return out
}

// row draws one command: a bar if it's selected, ✗ if it failed, the
// command with the matched letters marked, and where and when it ran.
func (s *histSearch) row(r SearchResult, sel bool, cols, dirW, ageW int, dim, match, bar, failed string) string {
	var line strings.Builder
	base := ""
	if sel {
		base = "\x1b[1m"
		line.WriteString(bar + "▌" + reset)
	} else {
		line.WriteString(" ")
	}
	if r.Failed {
		line.WriteString(failed + "✗" + reset + " ")
	} else {
		line.WriteString(dim + "·" + reset + " ")
	}
	used := 3
	// the right-hand columns, dropped when the command needs the room
	right, rightW := "", 0 // rightW is right's width without its escapes
	if r.Dir != "" || r.Age != "" {
		if cols-1-used-(dirW+2+ageW)-2 >= 12 {
			dir := truncateLeft(r.Dir, dirW)
			right = dim + strings.Repeat(" ", dirW-plainWidth(dir)) + dir + "  " +
				strings.Repeat(" ", ageW-plainWidth(r.Age)) + r.Age + reset
			rightW = dirW + 2 + ageW
		}
	} else if r.Info != "" {
		info := truncateWidth(r.Info, max(cols-1-used-14, 0))
		right, rightW = dim+info+reset, plainWidth(info)
	}
	room := cols - 1 - used - rightW
	if rightW > 0 {
		room -= 2
	}
	pos := map[int]bool{}
	for _, p := range r.Positions {
		pos[p] = true
	}
	line.WriteString(base)
	rs := []rune(strings.ReplaceAll(r.Text, "\n", "⏎"))
	ws := widths(rs)
	w := 0
	for ri, ch := range rs {
		if w+ws[ri] > room-1 && ri < len(rs)-1 || w+ws[ri] > room {
			line.WriteString("…")
			w++
			break
		}
		if pos[ri] {
			line.WriteString(match + displayRune(ch) + reset + base)
		} else {
			line.WriteString(displayRune(ch))
		}
		w += ws[ri]
	}
	line.WriteString(reset)
	if rightW > 0 {
		line.WriteString(strings.Repeat(" ", max(cols-1-used-w-rightW, 2)) + right)
	}
	return line.String()
}

// footer shows where, when and how the selected command ran, and which
// of how many it is.
func (s *histSearch) footer(cols int, dim string) string {
	count := "no matches"
	switch n := len(s.results); {
	case n >= 500:
		count = itoa(s.selected+1) + "/500+"
	case n > 0:
		count = itoa(s.selected+1) + "/" + itoa(n)
	}
	var parts []string
	if len(s.results) > 0 {
		r := s.results[s.selected]
		if r.Dir != "" {
			parts = append(parts, r.Dir)
		}
		if r.Age != "" {
			if r.Age == "now" {
				parts = append(parts, "just now")
			} else {
				parts = append(parts, r.Age+" ago")
			}
		}
		if r.Took != "" {
			parts = append(parts, "took "+r.Took)
		}
		if r.Failed {
			parts = append(parts, "exit "+itoa(r.Status))
		}
	}
	detail := strings.Join(parts, " · ")
	if detail == "" {
		detail = "enter edit · ctrl-r scope · esc cancel"
	}
	room := cols - 1 - plainWidth(count) - 2 - 4
	detail = truncateWidth(detail, max(room, 0))
	pad := max(cols-1-4-plainWidth(detail)-plainWidth(count), 1)
	return dim + "  ╰ " + detail + strings.Repeat(" ", pad) + count + reset
}

// truncateLeft keeps the end of s, which for a path is the part that
// says the most: …/src/ash.
func truncateLeft(s string, w int) string {
	if plainWidth(s) <= w {
		return s
	}
	rs := []rune(s)
	for i := range rs {
		if t := "…" + string(rs[i+1:]); plainWidth(t) <= w {
			return t
		}
	}
	return ""
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
