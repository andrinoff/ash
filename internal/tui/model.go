package tui

import (
	"cmp"
	"os"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// kind selects how a picker ranks and draws its items.
type kind uint8

const (
	kindChoose  kind = iota // plain strings
	kindDir                 // directories: dim parents, strong last component
	kindHistory             // commands with status, directory and time columns
)

// item is one entry of a picker.
type item struct {
	text  string // matched against the query and drawn
	value string // returned when chosen
	base  int    // kindDir: rune index where the last path component starts

	// kindHistory
	when   string // relative time, e.g. "3m"
	dir    string // working directory, ~-abbreviated
	failed bool
}

// result is an item that matches the current query.
type result struct {
	idx    int32 // index into model.items
	score  int32
	length int32 // tie-breaker for kindChoose: shorter wins
}

// signalMsg ends the picker because the process got a signal.
type signalMsg struct{ sig os.Signal }

// inputClosedMsg ends the picker because the terminal input went away.
type inputClosedMsg struct{}

type keyMap struct {
	up, down, pageUp, pageDown key.Binding
	accept, cancel             key.Binding
	toggleDown, toggleUp       key.Binding
}

func defaultKeys() keyMap {
	return keyMap{
		up:         key.NewBinding(key.WithKeys("up", "ctrl+p")),
		down:       key.NewBinding(key.WithKeys("down", "ctrl+n")),
		pageUp:     key.NewBinding(key.WithKeys("pgup")),
		pageDown:   key.NewBinding(key.WithKeys("pgdown")),
		accept:     key.NewBinding(key.WithKeys("enter")),
		cancel:     key.NewBinding(key.WithKeys("esc", "ctrl+c", "ctrl+g")),
		toggleDown: key.NewBinding(key.WithKeys("tab", "space")),
		toggleUp:   key.NewBinding(key.WithKeys("shift+tab")),
	}
}

const (
	pointerGlyph = "▌"
	markerGlyph  = "•"
	failedGlyph  = "✗"
	ellipsis     = "…"
)

// model is the Bubble Tea model shared by every picker.
type model struct {
	kind  kind
	items []item

	all     []result    // every item, in input order
	bufs    [2][]result // filter results alternate between these
	inBuf   int         // which of bufs matches uses; -1 when it is all
	matches []result    // items matching the query, best first
	pat     pattern
	m       *matcher

	input  textinput.Model
	keys   keyMap
	styles Styles
	header []string

	multi   bool
	limit   int // most items that can be marked; 0 means no limit
	marked  []bool
	nmarked int

	cursor, offset int
	width, height  int
	maxHeight      int

	// history columns
	timeWidth, dirWidth int
	anyFailed           bool

	done     bool
	canceled bool
	signal   os.Signal
	chosen   []int

	// onDone is called as soon as the picker has an answer, to stop
	// reading the terminal before Bubble Tea finishes shutting down, so
	// keys typed after enter reach the shell instead of the picker.
	onDone func()
}

func newModel(k kind, items []item, opts ChooseOptions) *model {
	m := &model{
		kind:      k,
		items:     items,
		inBuf:     -1,
		m:         &matcher{},
		keys:      defaultKeys(),
		multi:     opts.Multi || opts.Limit > 1,
		limit:     opts.Limit,
		maxHeight: opts.Height,
		width:     80,
		height:    24,
	}
	if opts.Styles != nil {
		m.styles = *opts.Styles
	} else {
		m.styles = DefaultStyles()
	}
	if m.multi {
		m.marked = make([]bool, len(items))
	} else {
		m.limit = 1
	}
	if h := strings.TrimRight(opts.Header, "\n"); h != "" {
		m.header = strings.Split(h, "\n")
	}

	m.all = make([]result, len(items))
	for i := range items {
		m.all[i] = result{idx: int32(i), length: int32(trimmedLen(items[i].text))}
		if k == kindHistory {
			m.timeWidth = max(m.timeWidth, len(items[i].when))
			m.dirWidth = max(m.dirWidth, ansi.StringWidthWc(sanitize(items[i].dir)))
			m.anyFailed = m.anyFailed || items[i].failed
		}
	}
	m.matches = m.all

	prompt := opts.Prompt
	if prompt == "" {
		prompt = "❯ "
	}
	ti := textinput.New()
	ti.Prompt = prompt
	ti.KeyMap.Paste.SetEnabled(false) // ctrl+v would spawn pbpaste or xclip
	ti.KeyMap.AcceptSuggestion.SetEnabled(false)
	ti.KeyMap.NextSuggestion.SetEnabled(false)
	ti.KeyMap.PrevSuggestion.SetEnabled(false)
	ts := textinput.DefaultStyles(true)
	ts.Focused = textinput.StyleState{Prompt: m.styles.Prompt}
	ts.Blurred = ts.Focused
	// A steady reverse-video block: no colour, and no blink timer
	// redrawing the screen twice a second.
	ts.Cursor = textinput.CursorStyle{Color: lipgloss.NoColor{}}
	ti.SetStyles(ts)
	ti.Focus()
	ti.SetValue(opts.Query)
	m.input = ti
	m.refilter()
	m.layout()
	return m
}

// trimmedLen is the number of runes after trimming white space, the
// length fzf breaks ties with.
func trimmedLen(s string) int {
	return utf8.RuneCountInString(strings.TrimSpace(s))
}

func (m *model) Init() tea.Cmd { return nil }

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.done {
		return m, nil
	}
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()
	case signalMsg:
		m.signal = msg.sig
		return m.finish(true)
	case inputClosedMsg:
		return m.finish(true)
	case tea.PasteMsg:
		return m.edit(msg)
	case tea.KeyPressMsg:
		return m.key(msg)
	}
	return m, nil
}

func (m *model) key(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.cancel):
		return m.finish(true)
	case key.Matches(msg, m.keys.accept):
		return m.accept()
	case key.Matches(msg, m.keys.up):
		m.move(-1)
	case key.Matches(msg, m.keys.down):
		m.move(1)
	case key.Matches(msg, m.keys.pageUp):
		m.move(-m.listHeight())
	case key.Matches(msg, m.keys.pageDown):
		m.move(m.listHeight())
	case m.multi && key.Matches(msg, m.keys.toggleDown):
		m.toggle()
		m.move(1)
	case m.multi && key.Matches(msg, m.keys.toggleUp):
		m.toggle()
		m.move(-1)
	case msg.String() == "ctrl+z":
		// Suspending would stop the whole shell, not just the picker.
	default:
		return m.edit(msg)
	}
	return m, nil
}

// edit passes a message to the query input and refilters if it changed.
func (m *model) edit(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.refilter()
	return m, cmd
}

func (m *model) finish(canceled bool) (tea.Model, tea.Cmd) {
	m.done, m.canceled = true, canceled
	if m.onDone != nil {
		m.onDone()
	}
	return m, tea.Quit
}

func (m *model) accept() (tea.Model, tea.Cmd) {
	if m.nmarked > 0 {
		for i, ok := range m.marked {
			if ok {
				m.chosen = append(m.chosen, i)
			}
		}
		return m.finish(false)
	}
	if len(m.matches) == 0 {
		return m, nil
	}
	m.chosen = []int{int(m.matches[m.cursor].idx)}
	return m.finish(false)
}

func (m *model) toggle() {
	if len(m.matches) == 0 {
		return
	}
	i := m.matches[m.cursor].idx
	switch {
	case m.marked[i]:
		m.marked[i] = false
		m.nmarked--
	case m.limit <= 0 || m.nmarked < m.limit:
		m.marked[i] = true
		m.nmarked++
	}
}

func (m *model) move(d int) {
	m.cursor = max(min(m.cursor+d, len(m.matches)-1), 0)
	m.scroll()
}

// refilter recomputes the matches after the query changed.
func (m *model) refilter() {
	q := m.input.Value()
	if q == m.pat.src {
		return
	}
	prev := m.pat.src
	m.pat = parsePattern(q)
	m.cursor, m.offset = 0, 0
	if m.pat.empty() {
		m.matches, m.inBuf = m.all, -1
		return
	}
	// A longer query only removes items, so start from the last result.
	pool := m.all
	if narrows(prev, q) {
		pool = m.matches
	}
	dst := 0
	if m.inBuf == 0 {
		dst = 1
	}
	out := m.bufs[dst][:0]
	for _, r := range pool {
		if s, _, ok := m.m.match(&m.pat, m.items[r.idx].text, false); ok {
			r.score = int32(s)
			out = append(out, r)
		}
	}
	// Best score first; ties go to the shorter item (for plain lists) and
	// then to the earlier one, which for history and directories is the
	// more recent. A query of negations alone scores nothing and keeps the
	// input order.
	if m.pat.sortable {
		byLength := m.kind == kindChoose
		slices.SortFunc(out, func(a, b result) int {
			if c := cmp.Compare(b.score, a.score); c != 0 {
				return c
			}
			if byLength {
				if c := cmp.Compare(a.length, b.length); c != 0 {
					return c
				}
			}
			return cmp.Compare(a.idx, b.idx)
		})
	}
	m.bufs[dst] = out
	m.matches, m.inBuf = out, dst
}

// listHeight is the number of rows available for items.
func (m *model) listHeight() int {
	return max(m.totalHeight()-len(m.header)-1, 1)
}

func (m *model) totalHeight() int {
	h := m.height
	if m.maxHeight > 0 && m.maxHeight < h {
		h = m.maxHeight
	}
	return max(h, 2)
}

func (m *model) layout() {
	m.input.SetWidth(max(m.width-ansi.StringWidthWc(m.input.Prompt)-m.counterWidth()-2, 1))
	m.scroll()
}

func (m *model) scroll() {
	h := m.listHeight()
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+h {
		m.offset = m.cursor - h + 1
	}
	m.offset = max(min(m.offset, len(m.matches)-h), 0)
}

func (m *model) counter() string {
	s := strconv.Itoa(len(m.matches)) + "/" + strconv.Itoa(len(m.items))
	if m.nmarked > 0 {
		s += " (" + strconv.Itoa(m.nmarked) + ")"
	}
	return s
}

// counterWidth is the width of the widest counter this picker can show.
func (m *model) counterWidth() int {
	n := len(strconv.Itoa(len(m.items)))
	w := 2*n + 1
	if m.multi {
		w += n + 3
	}
	return w
}

func (m *model) View() tea.View {
	var v tea.View
	v.AltScreen = true
	// Leave bracketed paste as the shell set it: Bubble Tea would turn it
	// off on exit, breaking pastes into the command line afterwards.
	v.DisableBracketedPasteMode = true
	v.Content = m.render()
	return v
}

func (m *model) render() string {
	var b strings.Builder
	w := max(m.width, 1)
	for _, h := range m.header {
		b.WriteString(m.styles.Header.Render(truncateWc(sanitize(h), w, ellipsis)))
		b.WriteByte('\n')
	}

	in := truncateWc(m.input.View(), w, "")
	b.WriteString(in)
	inW := ansi.StringWidthWc(in)
	if c := m.counter(); inW+len(c)+1 <= w {
		b.WriteString(strings.Repeat(" ", w-inW-len(c)))
		b.WriteString(m.styles.Counter.Render(c))
	}

	end := min(m.offset+m.listHeight(), len(m.matches))
	for i := m.offset; i < end; i++ {
		b.WriteByte('\n')
		m.renderRow(&b, i, w)
	}
	return b.String()
}

func (m *model) renderRow(b *strings.Builder, i, w int) {
	r := m.matches[i]
	it := &m.items[r.idx]
	cur := i == m.cursor

	if cur {
		b.WriteString(m.styles.Pointer.Render(pointerGlyph))
	} else {
		b.WriteByte(' ')
	}
	used := 1
	if m.multi {
		if m.marked[r.idx] {
			b.WriteString(m.styles.Marker.Render(markerGlyph))
		} else {
			b.WriteByte(' ')
		}
		used++
	}
	b.WriteByte(' ')
	used++
	if m.kind == kindHistory && m.anyFailed {
		if it.failed {
			b.WriteString(m.styles.Failed.Render(failedGlyph))
		} else {
			b.WriteByte(' ')
		}
		b.WriteByte(' ')
		used += 2
	}
	avail := w - used
	if avail <= 0 {
		return
	}

	var pos []int
	if !m.pat.empty() {
		_, pos, _ = m.m.match(&m.pat, it.text, true)
	}

	if m.kind != kindHistory {
		b.WriteString(m.highlight(it, pos, cur, avail))
		return
	}

	// History: the command, then the directory and time columns, which
	// give way to the command on narrow terminals.
	timeW, dirW := m.timeWidth, min(m.dirWidth, avail*3/10)
	cmdW := avail
	if timeW > 0 {
		cmdW -= timeW + 2
	}
	if dirW > 0 {
		cmdW -= dirW + 2
	}
	if cmdW < 20 && dirW > 0 {
		cmdW += dirW + 2
		dirW = 0
	}
	if cmdW < 10 && timeW > 0 {
		cmdW += timeW + 2
		timeW = 0
	}
	text := m.highlight(it, pos, cur, cmdW)
	b.WriteString(text)
	if dirW == 0 && timeW == 0 {
		return
	}
	b.WriteString(strings.Repeat(" ", cmdW-ansi.StringWidthWc(text)+2))
	if dirW > 0 {
		d := truncateLeftWc(sanitize(it.dir), dirW, ellipsis)
		b.WriteString(m.styles.Dim.Render(d))
		b.WriteString(strings.Repeat(" ", dirW-ansi.StringWidthWc(d)))
		if timeW > 0 {
			b.WriteString("  ")
		}
	}
	if timeW > 0 {
		b.WriteString(strings.Repeat(" ", timeW-len(it.when)))
		b.WriteString(m.styles.Dim.Render(it.when))
	}
}

// highlight draws an item's text in at most width cells, with the matched
// runes at pos (ascending rune indices) emphasized. Text that does not fit
// is cut so that the last match stays visible.
func (m *model) highlight(it *item, pos []int, cur bool, width int) string {
	st := &m.styles
	styles := [...]*lipgloss.Style{&st.Text, &st.Match, &st.Dim, &st.Strong}
	if cur {
		styles[0], styles[1] = &st.Current, &st.CurrentMatch
	}
	text := sanitize(it.text)

	var b, run strings.Builder
	style := -1
	flush := func() {
		if run.Len() > 0 {
			b.WriteString(styles[style].Render(run.String()))
			run.Reset()
		}
	}
	p, ri := 0, 0
	for _, r := range text {
		want := 0
		switch {
		case p < len(pos) && pos[p] == ri:
			p++
			want = 1
		case cur || m.kind != kindDir:
		case ri < it.base:
			want = 2
		default:
			want = 3
		}
		if want != style {
			flush()
			style = want
		}
		run.WriteRune(r)
		ri++
	}
	flush()
	out := b.String()

	total := ansi.StringWidthWc(text)
	if total <= width {
		return out
	}
	if len(pos) > 0 {
		last := pos[len(pos)-1]
		lastCol := ansi.StringWidthWc(string([]rune(text)[:last+1]))
		if lastCol > width-1 {
			// Drop cells from the left so the last match is visible,
			// with a cell of context after it when there is more text.
			cut := min(lastCol-width+2, total-width)
			out = truncateLeftWc(out, total-cut, ellipsis)
		}
	}
	return truncateWc(out, width, ellipsis)
}

// truncateWc cuts s, which may hold escape sequences, to at most width
// cells, ending it with tail. Cells are counted per codepoint, as Bubble
// Tea's renderer counts them. ansi.TruncateWc returns s whole when its
// grapheme width fits, but "👨‍👩‍👧" is one grapheme and six cells; padding s
// defeats that shortcut, and the cut then falls inside s.
func truncateWc(s string, width int, tail string) string {
	if ansi.StringWidthWc(s) <= width {
		return s
	}
	return ansi.TruncateWc(s+strings.Repeat(" ", width+1), width, tail)
}

// truncateLeftWc cuts s from the left to at most width cells, starting it
// with prefix. ansi.TruncateLeftWc keeps a wide character that straddles
// the cut, a cell too many; this drops it as well.
func truncateLeftWc(s string, width int, prefix string) string {
	sw := ansi.StringWidthWc(s)
	if sw <= width {
		return s
	}
	if width <= 0 {
		return ""
	}
	for n := sw - width + ansi.StringWidthWc(prefix); ; n++ {
		if t := ansi.TruncateLeftWc(s, n, prefix); ansi.StringWidthWc(t) <= width {
			return t
		}
	}
}

// sanitize makes control characters visible without changing the number
// of runes, so match positions still line up. Bytes that are not UTF-8
// become "�" (strings.Map does that), as they are drawn: measured raw, they
// take no room, and the row would come out wider than measured.
func sanitize(s string) string {
	clean := true
	for _, r := range s {
		if r < 0x20 || r == 0x7f || r >= 0x80 && r < 0xa0 || r == utf8.RuneError {
			clean = false
			break
		}
	}
	if clean {
		return s
	}
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\t':
			return ' '
		case r == '\n':
			return '↵'
		case r < 0x20:
			return 0x2400 + r // the control pictures ␀ to ␟
		case r == 0x7f:
			return '␡'
		case r >= 0x80 && r < 0xa0:
			return '�'
		}
		return r
	}, s)
}
