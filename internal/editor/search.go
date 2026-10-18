package editor

import (
	"strings"
	"unicode"
)

// histSearch is the incremental history search opened with Ctrl-R. The
// selected entry is previewed in the command line; Enter keeps it for
// editing, Escape restores what was there.
type histSearch struct {
	query     []rune
	qpos      int
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
		e.search.move(e, 1)
		return
	}
	s := &histSearch{saved: string(e.buf), savedPos: e.pos}
	s.query = []rune(strings.TrimSpace(string(e.buf)))
	s.qpos = len(s.query)
	e.search = s
	e.pager = nil
	e.sugg = nil
	s.refresh(e)
}

func (s *histSearch) refresh(e *Editor) {
	q := string(s.query)
	s.results = nil
	if e.cfg.SearchHistory != nil {
		s.results = e.cfg.SearchHistory(q, 500)
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
	case k.Code == KeyDown, name == "ctrl-n", name == "ctrl-r", name == "ctrl-s" && false:
		s.move(e, 1)
	case k.Code == KeyUp, name == "ctrl-p", name == "ctrl-s", k.Code == KeyTab && k.Mod == ModShift:
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

// render draws the query line and the matching entries.
func (s *histSearch) render(e *Editor, cols, avail int) []string {
	label := "history: "
	prog := e.style("pager_progress")
	match := e.style("search_match")
	selBg := e.style("pager_selected_background")
	var out []string
	q := string(s.query)
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
	headW := plainWidth(label) + plainWidth(q)
	if pad := cols - headW - plainWidth(count) - 1; pad > 1 {
		head += strings.Repeat(" ", pad) + prog + count + reset
	}
	out = append(out, head)
	s.cursorCol = plainWidth(label) + plainWidth(string(s.query[:s.qpos]))
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
		info := r.Info
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
