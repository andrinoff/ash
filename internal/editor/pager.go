package editor

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

// pager is the completion menu shown below the command line.
type pager struct {
	comp     Completion
	original string // buffer before completion
	origPos  int
	selected int // -1: nothing selected yet
	scroll   int // first visible row
	cols     int // columns in the grid (computed while rendering)
	rows     int
	visible  int
	prefix   string
}

// complete handles Tab: complete the token under the cursor, insert a
// common prefix, or open/advance the pager.
func (e *Editor) complete(backward bool) {
	if e.cfg.Complete == nil || e.opts.Simple {
		return
	}
	if e.pager != nil {
		if backward {
			e.pager.move(e, -1, 0)
		} else {
			e.pager.move(e, 1, 0)
		}
		return
	}
	s := string(e.buf)
	cur := len(string(e.buf[:e.pos]))
	c := e.cfg.Complete(s, cur)
	if c.Start < 0 || c.End > len(s) || c.Start > c.End {
		return
	}
	if len(c.Items) == 0 {
		return
	}
	token := s[c.Start:c.End]
	if len(c.Items) == 1 {
		it := c.Items[0]
		ins := it.Text
		if !it.NoSpace {
			ins += " "
		}
		e.replaceRange(c.Start, c.End, ins)
		return
	}
	// insert the longest common prefix if it extends what was typed
	common := c.Items[0].Text
	for _, it := range c.Items[1:] {
		common = commonPrefix(common, it.Text)
		if common == "" {
			break
		}
	}
	if len(common) > len(token) && strings.HasPrefix(common, token) {
		e.replaceRange(c.Start, c.End, common)
		return
	}
	p := &pager{comp: c, original: s, origPos: e.pos, selected: -1, prefix: token}
	e.pager = p
	e.sugg = nil
	if backward {
		p.move(e, -1, 0)
	}
}

func commonPrefix(a, b string) string {
	i := 0
	for i < len(a) && i < len(b) && a[i] == b[i] {
		i++
	}
	for i > 0 && i < len(a) && !utf8.RuneStart(a[i]) {
		i--
	}
	return a[:i]
}

// replaceRange replaces bytes [a, b) of the buffer and puts the cursor
// after the new text.
func (e *Editor) replaceRange(a, b int, text string) {
	s := string(e.buf)
	ns := s[:a] + text + s[b:]
	e.pushUndo()
	e.buf = []rune(ns)
	e.pos = len([]rune(s[:a] + text))
	e.edited()
}

// handle processes a key while the pager is open. It returns true if
// the key was consumed.
func (p *pager) handle(e *Editor, k Key) bool {
	switch {
	case k.Code == KeyTab && k.Mod == 0:
		p.move(e, 1, 0)
	case k.Code == KeyTab && k.Mod == ModShift:
		p.move(e, -1, 0)
	case k.Code == KeyDown || k.Name() == "ctrl-n":
		p.move(e, 1, 0)
	case k.Code == KeyUp || k.Name() == "ctrl-p":
		p.move(e, -1, 0)
	case k.Code == KeyRight && p.selected >= 0:
		p.move(e, 0, 1)
	case k.Code == KeyLeft && p.selected >= 0:
		p.move(e, 0, -1)
	case k.Code == KeyPgDn:
		p.page(e, 1)
	case k.Code == KeyPgUp:
		p.page(e, -1)
	case k.Code == KeyEnter:
		if p.selected >= 0 {
			e.pager = nil
			e.update()
			return true
		}
		e.pager = nil
		return false
	case k.Code == KeyEscape || k.Name() == "ctrl-g" || k.Name() == "ctrl-c":
		p.cancel(e)
	default:
		// any other key closes the pager and is handled normally
		if p.selected >= 0 {
			e.pager = nil
			e.update()
		} else {
			e.pager = nil
		}
		return false
	}
	return true
}

// move changes the selection by d items (and dc columns).
func (p *pager) move(e *Editor, d, dc int) {
	n := len(p.comp.Items)
	if n == 0 {
		return
	}
	rows := p.rows
	if rows <= 0 {
		rows = n
	}
	if p.selected < 0 {
		if d < 0 {
			p.selected = n - 1
		} else {
			p.selected = 0
		}
	} else if dc != 0 {
		ns := p.selected + dc*rows
		if ns >= 0 && ns < n {
			p.selected = ns
		}
	} else {
		p.selected = ((p.selected+d)%n + n) % n
	}
	p.apply(e)
}

// page moves the selection a page of rows up or down within its column,
// stopping at the column's first and last items.
func (p *pager) page(e *Editor, dir int) {
	n := len(p.comp.Items)
	if n == 0 || p.selected < 0 {
		p.move(e, dir, 0)
		return
	}
	rows := p.rows
	if rows <= 0 {
		rows = n
	}
	col, row := p.selected/rows, p.selected%rows
	row = min(max(row+dir*max(p.visible, 1), 0), rows-1)
	p.selected = min(col*rows+row, n-1)
	p.apply(e)
}

// apply puts the selected candidate into the buffer.
func (p *pager) apply(e *Editor) {
	it := p.comp.Items[p.selected]
	s := p.original
	text := it.Text
	ns := s[:p.comp.Start] + text + s[p.comp.End:]
	e.buf = []rune(ns)
	e.pos = len([]rune(s[:p.comp.Start] + text))
	e.hl = nil
	if e.cfg.Highlight != nil {
		if hl := e.cfg.Highlight(ns); len(hl) == len(e.buf) {
			e.hl = hl
		}
	}
	e.sugg = nil
}

func (p *pager) cancel(e *Editor) {
	e.buf = []rune(p.original)
	e.pos = p.origPos
	e.pager = nil
	e.update()
}

// render lays the candidates out in columns, fish style.
func (p *pager) render(e *Editor, cols, avail int) []string {
	items := p.comp.Items
	n := len(items)
	maxRows := avail - 1
	if maxRows > 12 {
		maxRows = 12
	}
	if lim := e.rows/2 - 1; lim > 3 && maxRows > lim {
		maxRows = lim
	}
	if maxRows < 1 {
		maxRows = 1
	}
	// cell widths
	labels := make([]string, n)
	descs := make([]string, n)
	widest := 0
	for i, it := range items {
		// drawn as the widths below count them: no raw tabs or escapes
		labels[i] = printable(it.Label())
		descs[i] = printable(strings.ReplaceAll(it.Description, "\n", " "))
		w := plainWidth(labels[i])
		if descs[i] != "" {
			w += 3 + plainWidth(descs[i])
		}
		if w > widest {
			widest = w
		}
	}
	cellMax := cols - 1
	cell := widest + 2
	if cell > cellMax {
		cell = cellMax
	}
	ncols := cols / cell
	if ncols < 1 {
		ncols = 1
	}
	// Prefer fewer, wider columns when descriptions are present.
	hasDesc := false
	for _, d := range descs {
		if d != "" {
			hasDesc = true
			break
		}
	}
	if hasDesc && ncols > 2 && widest > 30 {
		ncols = 2
	}
	rows := (n + ncols - 1) / ncols
	if ncols > 1 {
		cell = cols / ncols
	}
	p.cols, p.rows = ncols, rows
	visible := rows
	if visible > maxRows {
		visible = maxRows
	}
	p.visible = visible
	if p.selected >= 0 {
		r := p.selected % rows
		if r < p.scroll {
			p.scroll = r
		}
		if r >= p.scroll+visible {
			p.scroll = r - visible + 1
		}
	}
	if p.scroll > rows-visible {
		p.scroll = rows - visible
	}
	if p.scroll < 0 {
		p.scroll = 0
	}
	prefixSt := e.style("pager_prefix")
	compSt := e.style("pager_completion")
	descSt := e.style("pager_description")
	selBg := e.style("pager_selected_background")
	selComp := e.style("pager_selected_completion")
	selDesc := e.style("pager_selected_description")
	var out []string
	for r := p.scroll; r < p.scroll+visible; r++ {
		var line strings.Builder
		used := 0
		for c := 0; c < ncols; c++ {
			i := c*rows + r
			if i >= n {
				break
			}
			sel := i == p.selected
			w := cell - 2
			if c == ncols-1 {
				w = cols - used - 1
			}
			label := labels[i]
			desc := descs[i]
			lw := plainWidth(label)
			if lw > w {
				label = truncateWidth(label, w)
				lw = plainWidth(label)
				desc = ""
			}
			dw := 0
			if desc != "" {
				room := w - lw - 3
				if room < 4 {
					desc = ""
				} else {
					desc = truncateWidth(desc, room)
					dw = plainWidth(desc) + 3
				}
			}
			cs, ds := compSt, descSt
			if sel {
				cs, ds = selBg+selComp, selBg+selDesc
			}
			// highlight the typed prefix
			pre := ""
			rest := label
			if !sel && p.prefix != "" && strings.HasPrefix(label, p.prefix) {
				pre, rest = p.prefix, label[len(p.prefix):]
			}
			if pre != "" {
				line.WriteString(prefixSt + pre + reset)
			}
			line.WriteString(cs + rest)
			if desc != "" {
				line.WriteString(reset + ds + " (" + desc + ")")
			}
			pad := w - lw - dw
			if sel && pad > 0 {
				line.WriteString(strings.Repeat(" ", pad))
				pad = 0
			}
			line.WriteString(reset)
			if c < ncols-1 {
				line.WriteString(strings.Repeat(" ", pad+2))
			}
			used += cell
		}
		out = append(out, line.String())
	}
	if rows > visible {
		prog := e.style("pager_progress")
		msg := "rows " + strconv.Itoa(p.scroll+1) + " to " + strconv.Itoa(p.scroll+visible) + " of " + strconv.Itoa(rows)
		out = append(out, prog+msg+reset)
	}
	return out
}
