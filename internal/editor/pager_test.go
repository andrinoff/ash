package editor

import "testing"

func TestCommonPrefix(t *testing.T) {
	cases := []struct{ a, b, want string }{
		{"foo", "foobar", "foo"},
		{"foobar", "foo", "foo"},
		{"foo", "foo", "foo"},
		{"file0", "file02.txt", "file0"},
		{"", "x", ""},
		{"héllo", "hêllo", "h"},
		{"日本", "日本語", "日本"},
	}
	for _, c := range cases {
		if got := commonPrefix(c.a, c.b); got != c.want {
			t.Errorf("commonPrefix(%q, %q) = %q, want %q", c.a, c.b, got, c.want)
		}
	}
}

// Tabs and escape characters in candidates are drawn the way their width
// is counted (caret notation, spaces), not written raw to the terminal.
func TestPagerControlCharacters(t *testing.T) {
	e := newTestEditor(30)
	p := &pager{comp: Completion{Items: []Candidate{
		{Text: "a", Display: "tab\there", Description: "desc\twith\ttabs and more text"},
		{Text: "b", Display: "esc\x1b[31mred", Description: "x"},
	}}, selected: -1}
	for _, l := range p.render(e, 30, 10) {
		plain := stripANSI(l)
		for _, r := range plain {
			if r < 0x20 {
				t.Fatalf("raw control character in pager line %q", l)
			}
		}
		if w := plainWidth(plain); w >= 30 {
			t.Fatalf("pager line %q is %d wide", plain, w)
		}
	}
	if got := truncateWidth("a\tb", 4); plainWidth(got) > 4 {
		t.Fatalf("truncateWidth: %q", got)
	}
}

// Page down/up move the selection a page of rows within its column; they
// used to jump by rows*columns items and land in another column.
func TestPagerPageKeys(t *testing.T) {
	var items []Candidate
	for i := 0; i < 50; i++ {
		items = append(items, Candidate{Text: sprintf("file%02d.txt", i)})
	}
	e := newTestEditor(40)
	e.rows = 12
	e.cfg.Complete = func(buf string, cursor int) Completion {
		return Completion{Start: 3, End: cursor, Items: items}
	}
	e.insert([]rune("ls file"))
	e.complete(false) // opens the pager
	p := e.pager
	if p == nil {
		t.Fatal("no pager")
	}
	p.render(e, e.cols, 10)
	if p.cols != 3 || p.rows != 17 || p.visible != 5 {
		t.Fatalf("layout %d cols %d rows %d visible", p.cols, p.rows, p.visible)
	}
	steps := []struct {
		key  KeyCode
		want int
	}{
		{KeyPgDn, 0}, // the first press selects the first item
		{KeyPgDn, 5},
		{KeyPgDn, 10},
		{KeyPgDn, 15},
		{KeyPgDn, 16}, // the end of the column
		{KeyPgUp, 11},
		{KeyPgUp, 6},
		{KeyPgUp, 1},
		{KeyPgUp, 0},
	}
	for i, s := range steps {
		p.handle(e, Key{Code: s.key})
		if p.selected != s.want {
			t.Fatalf("step %d: selected %d, want %d", i, p.selected, s.want)
		}
	}
	// the last column is shorter: stop at its last item
	p.selected = 36
	p.handle(e, Key{Code: KeyPgDn})
	p.handle(e, Key{Code: KeyPgDn})
	p.handle(e, Key{Code: KeyPgDn})
	if p.selected != 49 {
		t.Fatalf("last column: selected %d, want 49", p.selected)
	}
	if got := string(e.buf); got != "ls file49.txt" {
		t.Fatalf("buffer %q", got)
	}
}

// Completing with candidates where one is a prefix of another used to
// panic while computing the common prefix.
func TestCompleteNestedCandidates(t *testing.T) {
	e := newTestEditor(80)
	e.cfg.Complete = func(buf string, cursor int) Completion {
		return Completion{Start: 3, End: cursor, Items: []Candidate{
			{Text: "file00.txt"}, {Text: "file01.txt"}, {Text: "file02.txt"},
		}}
	}
	e.insert([]rune("ls f"))
	e.complete(false)
	if got := string(e.buf); got != "ls file0" {
		t.Fatalf("common prefix not inserted: %q", got)
	}
	e.cfg.Complete = func(buf string, cursor int) Completion {
		return Completion{Start: 0, End: cursor, Items: []Candidate{{Text: "git"}, {Text: "git-lfs"}}}
	}
	e.buf, e.pos = []rune("gi"), 2
	e.complete(false)
	if got := string(e.buf); got != "git" {
		t.Fatalf("got %q", got)
	}
	e.complete(false)
	if e.pager == nil {
		t.Fatal("second tab should open the pager")
	}
}
