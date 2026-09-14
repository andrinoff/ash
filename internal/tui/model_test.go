package tui

import (
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

// press feeds bytes to the model the way the picker does at run time:
// decoded by ultraviolet, then translated like Bubble Tea's event loop.
func press(t *testing.T, m *model, keys string) {
	t.Helper()
	var d uv.EventDecoder
	buf := []byte(keys)
	for len(buf) > 0 {
		n, ev := d.Decode(buf)
		if n == 0 {
			t.Fatalf("cannot decode %q", buf)
		}
		buf = buf[n:]
		var msg tea.Msg = ev
		if k, ok := ev.(uv.KeyPressEvent); ok {
			msg = tea.KeyPressMsg(k)
		}
		m.Update(msg)
	}
}

func chooseModel(items []string, opts ChooseOptions) *model {
	its := make([]item, len(items))
	for i, s := range items {
		its[i] = item{text: s, value: s}
	}
	return newModel(kindChoose, its, opts)
}

func chosenTexts(m *model) []string {
	out := []string{}
	for _, c := range m.chosen {
		out = append(out, m.items[c].text)
	}
	return out
}

// screen is the model's view as plain text lines, trailing blanks trimmed.
func screen(m *model) []string {
	lines := strings.Split(ansi.Strip(m.View().Content), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " ")
	}
	return lines
}

// styles draws the view into cells, as Bubble Tea's renderer does, and
// describes each cell's style with a letter: '.' plain, 'b' bold, 'f'
// faint, 'u' bold underlined, 'r' bold reversed, 'x' bold underlined
// reversed, '?' anything else. Trailing plain cells are trimmed.
func styles(m *model) []string {
	buf := uv.NewScreenBuffer(m.width, m.totalHeight())
	uv.NewStyledString(m.View().Content).Draw(buf, buf.Bounds())
	var lines []string
	for y := 0; y < buf.Height(); y++ {
		var sb strings.Builder
		for x := 0; x < buf.Width(); x++ {
			c := buf.CellAt(x, y)
			if c == nil || c.Width == 0 {
				continue
			}
			st := c.Style
			bold, faint := st.Attrs&uv.AttrBold != 0, st.Attrs&uv.AttrFaint != 0
			rev, ul := st.Attrs&uv.AttrReverse != 0, st.Underline != uv.UnderlineNone
			code := byte('?')
			switch {
			case st.Fg != nil || st.Bg != nil || st.Attrs&^(uv.AttrBold|uv.AttrFaint|uv.AttrReverse) != 0:
			case !bold && !faint && !rev && !ul:
				code = '.'
			case bold && !faint && !rev && !ul:
				code = 'b'
			case faint && !bold && !rev && !ul:
				code = 'f'
			case bold && ul && !rev && !faint:
				code = 'u'
			case bold && rev && !ul && !faint:
				code = 'r'
			case bold && ul && rev && !faint:
				code = 'x'
			}
			sb.WriteByte(code)
		}
		lines = append(lines, strings.TrimRight(sb.String(), "."))
	}
	return lines
}

// promptLine is the query line: the prompt and query on the left, the
// counter on the right.
func promptLine(query, counter string, width int) string {
	return padRight("❯ "+query, width-len(counter)) + counter
}

var nato = []string{"alpha", "bravo", "charlie", "delta", "echo", "foxtrot"}

func TestPickerKeys(t *testing.T) {
	const (
		up, down     = "\x1b[A", "\x1b[B"
		pgdn, pgup   = "\x1b[6~", "\x1b[5~"
		enter, esc   = "\r", "\x1b"
		tab, backtab = "\t", "\x1b[Z"
	)
	many := make([]string, 30)
	for i := range many {
		many[i] = "item " + string(rune('A'+i))
	}
	tests := []struct {
		name     string
		items    []string
		opts     ChooseOptions
		keys     string
		want     []string // nil: canceled
		notDone  bool
		wantText string // the query afterwards, if set
	}{
		{name: "enter picks the first", items: nato, keys: enter, want: []string{"alpha"}},
		{name: "down", items: nato, keys: down + down + enter, want: []string{"charlie"}},
		{name: "ctrl-n and ctrl-p", items: nato, keys: "\x0e\x0e\x10" + enter, want: []string{"bravo"}},
		{name: "up at the top stays", items: nato, keys: up + enter, want: []string{"alpha"}},
		{name: "down stops at the end", items: nato, keys: strings.Repeat(down, 10) + enter, want: []string{"foxtrot"}},
		{name: "typing filters", items: nato, keys: "cha" + enter, want: []string{"charlie"}},
		// All three "o"s score alike; the shortest item wins, as in fzf.
		{name: "typing resets the cursor", items: nato, keys: down + down + "o" + enter, want: []string{"echo"}},
		{name: "esc cancels", items: nato, keys: esc},
		{name: "ctrl-c cancels", items: nato, keys: "\x03"},
		{name: "ctrl-g cancels", items: nato, keys: "\x07"},
		{name: "enter without a match waits", items: nato, keys: "zzz" + enter, notDone: true, wantText: "zzz"},
		{name: "backspace widens the search", items: nato, keys: "zzz\x7f\x7f\x7f" + enter, want: []string{"alpha"}},
		{name: "ctrl-u clears the query", items: nato, keys: "zzz\x15" + enter, want: []string{"alpha"}},
		{name: "ctrl-z does not suspend", items: nato, keys: "\x1a" + enter, want: []string{"alpha"}},
		{name: "page down", items: many, opts: ChooseOptions{Height: 6}, keys: pgdn + enter, want: []string{"item F"}},
		{name: "page down then up", items: many, opts: ChooseOptions{Height: 6}, keys: pgdn + pgdn + pgup + enter, want: []string{"item F"}},
		{name: "initial query", items: nato, opts: ChooseOptions{Query: "br"}, keys: enter, want: []string{"bravo"}},
		{name: "space types in single mode", items: []string{"a b", "ab"}, keys: "a b", notDone: true, wantText: "a b"},
		{name: "tab does nothing in single mode", items: nato, keys: tab + enter, want: []string{"alpha"}},
		{name: "tab marks and moves down", items: nato, opts: ChooseOptions{Multi: true}, keys: tab + tab + enter, want: []string{"alpha", "bravo"}},
		{name: "space marks too", items: nato, opts: ChooseOptions{Multi: true}, keys: " " + " " + enter, want: []string{"alpha", "bravo"}},
		{name: "shift-tab marks and moves up", items: nato, opts: ChooseOptions{Multi: true}, keys: down + down + backtab + backtab + enter, want: []string{"bravo", "charlie"}},
		{name: "marking twice unmarks", items: nato, opts: ChooseOptions{Multi: true}, keys: tab + up + tab + enter, want: []string{"bravo"}},
		{name: "no marks returns the current item", items: nato, opts: ChooseOptions{Multi: true}, keys: down + enter, want: []string{"bravo"}},
		{name: "marks survive filtering", items: nato, opts: ChooseOptions{Multi: true}, keys: down + tab + "lie" + tab + enter, want: []string{"bravo", "charlie"}},
		{name: "results keep input order", items: nato, opts: ChooseOptions{Multi: true}, keys: "echo" + tab + "\x15" + tab + enter, want: []string{"alpha", "echo"}},
		{name: "limit", items: nato, opts: ChooseOptions{Limit: 2}, keys: tab + tab + tab + enter, want: []string{"alpha", "bravo"}},
		{name: "marking needs a match", items: nato, opts: ChooseOptions{Multi: true}, keys: "zzz" + tab + "\x15" + enter, want: []string{"alpha"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := chooseModel(tt.items, tt.opts)
			done := false
			m.onDone = func() { done = true }
			press(t, m, tt.keys)
			if tt.wantText != "" && m.input.Value() != tt.wantText {
				t.Errorf("query = %q, want %q", m.input.Value(), tt.wantText)
			}
			if tt.notDone {
				if m.done {
					t.Errorf("picker finished, want it waiting")
				}
				return
			}
			if !m.done || !done {
				t.Fatalf("picker not finished (done=%v, onDone called=%v)", m.done, done)
			}
			if tt.want == nil {
				if !m.canceled {
					t.Errorf("not canceled; chose %q", chosenTexts(m))
				}
				return
			}
			if m.canceled {
				t.Fatalf("canceled, want %q", tt.want)
			}
			if got := chosenTexts(m); !slices.Equal(got, tt.want) {
				t.Errorf("chose %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPickerIgnoresInputAfterAnswer(t *testing.T) {
	m := chooseModel(nato, ChooseOptions{})
	press(t, m, "\r")
	if _, cmd := m.Update(tea.KeyPressMsg{Code: 'x', Text: "x"}); cmd != nil {
		t.Errorf("finished picker returned a command")
	}
	if m.input.Value() != "" || !slices.Equal(chosenTexts(m), []string{"alpha"}) {
		t.Errorf("input after the answer changed the picker: query %q, chose %q", m.input.Value(), chosenTexts(m))
	}
}

func TestPickerSignalAndClosedInput(t *testing.T) {
	m := chooseModel(nato, ChooseOptions{})
	m.Update(signalMsg{sig: syscall.SIGINT})
	if !m.done || !m.canceled || m.signal != syscall.SIGINT {
		t.Errorf("signal: done=%v canceled=%v signal=%v", m.done, m.canceled, m.signal)
	}
	m = chooseModel(nato, ChooseOptions{})
	m.Update(inputClosedMsg{})
	if !m.done || !m.canceled || m.signal != nil {
		t.Errorf("closed input: done=%v canceled=%v signal=%v", m.done, m.canceled, m.signal)
	}
}

func TestPickerPaste(t *testing.T) {
	m := chooseModel(nato, ChooseOptions{})
	m.Update(tea.PasteMsg{Content: "fox\ntrot"})
	if got := m.input.Value(); got != "fox trot" {
		t.Errorf("query after paste = %q, want %q", got, "fox trot")
	}
	if got := texts(m); !slices.Equal(got, []string{"foxtrot"}) {
		t.Errorf("matches = %q", got)
	}
}

func TestChooseView(t *testing.T) {
	m := chooseModel(nato, ChooseOptions{Header: "Pick a word"})
	m.Update(tea.WindowSizeMsg{Width: 30, Height: 6})
	want := []string{
		"Pick a word",
		promptLine("", "6/6", 30),
		"▌ alpha",
		"  bravo",
		"  charlie",
		"  delta",
	}
	if got := screen(m); !slices.Equal(got, want) {
		t.Errorf("screen:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	// "a" is at the start of alpha; the rest tie and the shorter wins.
	press(t, m, "a\x1b[B")
	want = []string{
		"Pick a word",
		promptLine("a", "4/6", 30),
		"  alpha",
		"▌ bravo",
		"  delta",
		"  charlie",
	}
	if got := screen(m); !slices.Equal(got, want) {
		t.Errorf("screen after typing:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	// The header and counter are faint, the prompt and pointer bold, the
	// current row bold and reversed, matches bold and underlined, and the
	// query's cursor a reversed cell.
	want = []string{
		"fffffffffff",
		"bb.?" + strings.Repeat(".", 23) + "fff",
		"..u",
		"b.rrxrr",
		"......u",
		"....u",
	}
	if got := styles(m); !slices.Equal(got, want) {
		t.Errorf("styles:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	v := m.View()
	if !v.AltScreen || !v.DisableBracketedPasteMode || v.Cursor != nil || v.MouseMode != tea.MouseModeNone || v.KeyboardEnhancements != (tea.KeyboardEnhancements{}) {
		t.Errorf("view modes: alt=%v nopaste=%v cursor=%v mouse=%v kbd=%v", v.AltScreen, v.DisableBracketedPasteMode, v.Cursor, v.MouseMode, v.KeyboardEnhancements)
	}
}

func TestChooseViewMulti(t *testing.T) {
	m := chooseModel(nato, ChooseOptions{Multi: true, Height: 5})
	m.Update(tea.WindowSizeMsg{Width: 24, Height: 20})
	press(t, m, "\t\t")
	want := []string{
		promptLine("", "6/6 (2)", 24),
		" • alpha",
		" • bravo",
		"▌  charlie",
		"   delta",
	}
	if got := screen(m); !slices.Equal(got, want) {
		t.Errorf("screen:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if got := styles(m)[1:4]; !slices.Equal(got, []string{".b", ".b", "b..rrrrrrr"}) {
		t.Errorf("styles: %q", got)
	}
}

func TestDirView(t *testing.T) {
	m := newModel(kindDir, dirItems([]string{"/home/u/src/ash", "/var/src/x", "/home/u", "/"}, "/home/u"), ChooseOptions{})
	m.Update(tea.WindowSizeMsg{Width: 30, Height: 6})
	want := []string{
		promptLine("", "4/4", 30),
		"▌ ~/src/ash",
		"  /var/src/x",
		"  ~",
		"  /",
	}
	if got := screen(m); !slices.Equal(got, want) {
		t.Errorf("screen:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	// Parents are faint and the last component bold, except on the
	// current row, which is reversed as a whole.
	if got := styles(m)[1:5]; !slices.Equal(got, []string{"b.rrrrrrrrr", "..fffffffffb", "..b", "..b"}) {
		t.Errorf("styles: %q", got)
	}
	press(t, m, "src")
	if got := styles(m)[1:3]; !slices.Equal(got, []string{"b.rrxxxrrrr", "..fffffuuufb"}) {
		t.Errorf("styles after typing: %q", got)
	}
	press(t, m, "\x1b[B\r")
	if m.items[m.chosen[0]].value != "/var/src/x" {
		t.Errorf("chose %q", m.items[m.chosen[0]].value)
	}
}

func dirItems(dirs []string, home string) []item {
	its := make([]item, len(dirs))
	for i, d := range dirs {
		text := tildePath(d, home)
		its[i] = item{text: text, value: d, base: lastComponent(text)}
	}
	return its
}

func TestHistoryView(t *testing.T) {
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	entries := []HistoryItem{
		{Cmd: "git push origin main", When: now.Add(-3 * time.Minute), Dir: "/home/u/src/ash"},
		{Cmd: "make test", When: now.Add(-2 * time.Hour), Dir: "/home/u/src/ash", Status: 2},
		{Cmd: "ls -la", When: now.Add(-5 * 24 * time.Hour), Dir: "/tmp", Status: -1},
		{Cmd: "git push origin main", When: now.Add(-6 * 24 * time.Hour), Dir: "/", Status: 1},
	}
	m := newModel(kindHistory, historyItems(entries, now, "/home/u"), ChooseOptions{})
	m.Update(tea.WindowSizeMsg{Width: 60, Height: 6})
	// 60 columns: a 2-cell gutter, a 2-cell status column, then the
	// command (41), the directory (9) and the time (2), two apart.
	row := func(gutter, status, cmd, dir, when string) string {
		return gutter + status + " " + padRight(cmd, 41) + "  " + padRight(dir, 9) + "  " + padLeft(when, 2)
	}
	want := []string{
		"❯                                                        3/3",
		row("▌ ", " ", "git push origin main", "~/src/ash", "3m"),
		row("  ", "✗", "make test", "~/src/ash", "2h"),
		row("  ", " ", "ls -la", "/tmp", "5d"),
	}
	if got := screen(m); !slices.Equal(got, want) {
		t.Errorf("screen:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	// Narrow terminals drop the directory, then the time.
	m.Update(tea.WindowSizeMsg{Width: 34, Height: 6})
	if got := screen(m)[2]; got != "  ✗ make test"+strings.Repeat(" ", 17)+"  2h" {
		t.Errorf("34 columns: %q", got)
	}
	m.Update(tea.WindowSizeMsg{Width: 16, Height: 6})
	if got := screen(m)[2]; got != "  ✗ make test" {
		t.Errorf("16 columns: %q", got)
	}

	press(t, m, "mk\r")
	if got := m.items[m.chosen[0]].value; got != "make test" {
		t.Errorf("chose %q", got)
	}
}

func padRight(s string, n int) string { return s + strings.Repeat(" ", n-ansi.StringWidthWc(s)) }
func padLeft(s string, n int) string  { return strings.Repeat(" ", n-ansi.StringWidthWc(s)) + s }

func TestLongItemsKeepTheMatchVisible(t *testing.T) {
	m := chooseModel([]string{"one/two/three/four/five/six/seven/target.txt"}, ChooseOptions{})
	m.Update(tea.WindowSizeMsg{Width: 30, Height: 4})
	if got := screen(m)[1]; got != "▌ one/two/three/four/five/six…" {
		t.Errorf("no query: %q", got)
	}
	press(t, m, "target")
	if got := screen(m)[1]; got != "▌ …our/five/six/seven/target.…" {
		t.Errorf("query target: %q", got)
	}
	// The match runs to the end: the tail fills the row exactly.
	press(t, m, ".txt")
	if got := screen(m)[1]; got != "▌ …r/five/six/seven/target.txt" {
		t.Errorf("query target.txt: %q", got)
	}
}

func TestControlCharactersAreVisible(t *testing.T) {
	m := chooseModel([]string{"a\x1b[31mred", "two\nlines"}, ChooseOptions{})
	m.Update(tea.WindowSizeMsg{Width: 30, Height: 4})
	want := []string{"❯                          2/2", "▌ a␛[31mred", "  two↵lines"}
	if got := screen(m); !slices.Equal(got, want) {
		t.Errorf("screen:\n%q\nwant:\n%q", got, want)
	}
	if strings.Contains(m.View().Content, "\x1b[31m") {
		t.Errorf("an escape sequence from an item reached the terminal")
	}
}

// Rows never come out wider than the terminal, whatever the items hold:
// wide characters straddling a cut, clusters like "👨‍👩‍👧" (one grapheme but
// six cells to the renderer), bytes that are not UTF-8, control characters.
// Directories and commands like these used to crash the history browser
// with a negative strings.Repeat count.
func TestRowsFitTheWidth(t *testing.T) {
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	odd := []string{
		"/home/u/世界世界世界世界世界世界世界世界世界",
		"echo " + strings.Repeat("👨‍👩‍👧", 7),
		"/tmp/😀😀😀😀😀😀😀😀😀😀😀😀",
		"bad \xff\xfe\xfd bytes \xc3\x28 and some more text after them",
		"tabs\tand\nnewlines\x1b[31m and escapes",
	}
	var hist []HistoryItem
	for i, s := range odd {
		hist = append(hist, HistoryItem{Cmd: s, When: now.Add(-time.Duration(i+1) * time.Hour), Dir: odd[len(odd)-1-i], Status: i % 2})
	}
	models := map[string]func() *model{
		"choose":  func() *model { return chooseModel(odd, ChooseOptions{Multi: true, Header: odd[1]}) },
		"dir":     func() *model { return newModel(kindDir, dirItems(odd, "/home/u"), ChooseOptions{}) },
		"history": func() *model { return newModel(kindHistory, historyItems(hist, now, "/home/u"), ChooseOptions{}) },
	}
	for name, mk := range models {
		for _, query := range []string{"", "e", "世界", "bytes", "👩"} {
			for w := 8; w <= 70; w++ {
				m := mk()
				m.input.SetValue(query)
				m.refilter()
				m.Update(tea.WindowSizeMsg{Width: w, Height: 10})
				for i, line := range strings.Split(m.View().Content, "\n") {
					if lw := ansi.StringWidthWc(line); lw > w {
						t.Errorf("%s, query %q, %d columns: line %d is %d cells wide: %q", name, query, w, i, lw, line)
					}
				}
			}
		}
	}
}

func TestRelTime(t *testing.T) {
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	day := 24 * time.Hour
	tests := []struct {
		ago  time.Duration
		want string
	}{
		{0, "0s"},
		{59 * time.Second, "59s"},
		{time.Minute, "1m"},
		{3 * time.Minute, "3m"},
		{time.Hour - time.Second, "59m"},
		{time.Hour, "1h"},
		{2*time.Hour + 30*time.Minute, "2h"},
		{day - time.Minute, "23h"},
		{day, "1d"},
		{5 * day, "5d"},
		{7*day - time.Hour, "6d"},
		{7 * day, "1w"},
		{29 * day, "4w"},
		{30 * day, "1mo"},
		{364 * day, "12mo"},
		{365 * day, "1y"},
		{800 * day, "2y"},
		{-5 * time.Minute, "0s"}, // clock skew
	}
	for _, tt := range tests {
		if got := relTime(now, now.Add(-tt.ago)); got != tt.want {
			t.Errorf("relTime(%v ago) = %q, want %q", tt.ago, got, tt.want)
		}
	}
	if got := relTime(now, time.Time{}); got != "" {
		t.Errorf("relTime(zero) = %q", got)
	}
}

func TestTildePath(t *testing.T) {
	tests := []struct{ path, home, want string }{
		{"/home/u/src", "/home/u", "~/src"},
		{"/home/u", "/home/u", "~"},
		{"/home/u/x", "/home/u/", "~/x"},
		{"/home/user2", "/home/u", "/home/user2"},
		{"/tmp", "", "/tmp"},
		{"/tmp", "/", "/tmp"},
		{"", "/home/u", ""},
	}
	for _, tt := range tests {
		if got := tildePath(tt.path, tt.home); got != tt.want {
			t.Errorf("tildePath(%q, %q) = %q, want %q", tt.path, tt.home, got, tt.want)
		}
	}
}

func TestLastComponent(t *testing.T) {
	tests := []struct {
		path string
		want int
	}{
		{"~/src/ash", 6},
		{"/", 0},
		{"~", 0},
		{"/tmp", 1},
		{"relative", 0},
		{"/a/b/", 3},
		{"~/ünï/x", 6},
	}
	for _, tt := range tests {
		if got := lastComponent(tt.path); got != tt.want {
			t.Errorf("lastComponent(%q) = %d, want %d", tt.path, got, tt.want)
		}
	}
}

func TestSanitize(t *testing.T) {
	tests := []struct{ in, want string }{
		{"plain text", "plain text"},
		{"a\tb", "a b"},
		{"a\nb", "a↵b"},
		{"\x1b[31m", "␛[31m"},
		{"\x00\x7f", "␀␡"},
		{"\u0085", "�"},
		{"ünïcödé", "ünïcödé"},
		{"a\xffb\xc3", "a�b�"},
	}
	for _, tt := range tests {
		got := sanitize(tt.in)
		if got != tt.want {
			t.Errorf("sanitize(%q) = %q, want %q", tt.in, got, tt.want)
		}
		if len([]rune(got)) != len([]rune(tt.in)) {
			t.Errorf("sanitize(%q) changed the rune count", tt.in)
		}
	}
}

func TestEnviron(t *testing.T) {
	tests := []struct{ in, want []string }{
		{[]string{"TERM=xterm", "NO_COLOR=1"}, []string{"TERM=xterm", "NO_COLOR=1"}},
		{[]string{"NO_COLOR=true"}, []string{"NO_COLOR=true"}},
		{[]string{"NO_COLOR=yes"}, []string{"NO_COLOR=1"}},
		{[]string{"NO_COLOR=0"}, []string{"NO_COLOR=1"}}, // any value counts
		{[]string{"NO_COLOR="}, []string{}},
		{[]string{}, []string{}},
	}
	for _, tt := range tests {
		if got := environ(tt.in); !slices.Equal(got, tt.want) {
			t.Errorf("environ(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
	if got := lookupEnv([]string{"HOME=/a", "TERM=x", "HOME=/b"}, "HOME"); got != "/b" {
		t.Errorf("lookupEnv: the last assignment should win, got %q", got)
	}
}

func TestHistoryItems(t *testing.T) {
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	at := func(m int) time.Time { return now.Add(-time.Duration(m) * time.Minute) }
	cmds := func(its []item) string {
		var s []string
		for _, it := range its {
			s = append(s, it.text)
		}
		return strings.Join(s, "|")
	}
	// Timed entries are sorted newest first; repeats keep their latest use.
	got := historyItems([]HistoryItem{
		{Cmd: "old", When: at(50)},
		{Cmd: "make", When: at(40), Status: 2},
		{Cmd: "new", When: at(1)},
		{Cmd: "make", When: at(5)},
		{Cmd: "  ", When: at(3)},
	}, now, "")
	if c := cmds(got); c != "new|make|old" {
		t.Errorf("timed: %s", c)
	}
	if got[1].when != "5m" || got[1].failed {
		t.Errorf("a repeat should keep its latest use: when %q failed %v", got[1].when, got[1].failed)
	}
	// Without times the input order is taken as newest first.
	got = historyItems([]HistoryItem{{Cmd: "b"}, {Cmd: "a", When: at(1)}, {Cmd: "b"}, {Cmd: "c", Status: -1}}, now, "")
	if c := cmds(got); c != "b|a|c" {
		t.Errorf("untimed: %s", c)
	}
	if got[2].failed {
		t.Errorf("an unknown status (-1) is not a failure")
	}
}
