package editor

import "testing"

type foldHist []string

// Search matches case-insensitively, like a smartcase history search.
func (h foldHist) Search(q string, prefix bool) []string {
	var out []string
	for _, s := range h {
		if indexRunes([]rune(s), []rune(q), true) >= 0 {
			out = append(out, s)
		}
	}
	return out
}

// The highlighted match is found on runes: lowercasing İ makes the text
// longer in bytes, which shifted the match and could index past the end.
func TestHistoryMatchHighlightNonASCII(t *testing.T) {
	cases := []struct {
		entry, query string
		start, end   int
	}{
		{"İİİİ x", "x", 5, 6},
		{"cd İstanbul && ls", "ls", 15, 17},
		{"Git Status", "status", 4, 10},
		{"echo KELVIN K", "k", 5, 6},
	}
	for _, c := range cases {
		e := newTestEditor(80)
		e.cfg.History = foldHist{c.entry}
		e.insert([]rune(c.query))
		e.call("up-or-search", Key{})
		if string(e.buf) != c.entry {
			t.Fatalf("%q: recalled %q", c.query, string(e.buf))
		}
		if e.hist.matchStart != c.start || e.hist.matchEnd != c.end {
			t.Errorf("%q in %q: match %d-%d, want %d-%d", c.query, c.entry, e.hist.matchStart, e.hist.matchEnd, c.start, c.end)
		}
	}
}
