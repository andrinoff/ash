package tui

import (
	"slices"
	"strings"
	"testing"
)

// Rankings are checked against fzf in filter_test.go; these check the
// scores and positions themselves, worked out by hand with fzf's weights.
func TestMatchScoreAndPositions(t *testing.T) {
	tests := []struct {
		query, text string
		score       int // -1: no match
		pos         []int
	}{
		// a at 0 after the start of the text: 16 + 2*10; l consecutive,
		// carrying the first rune's bonus: 16 + 10.
		{"al", "alpha", 62, []int{0, 1}},
		// a at 2: 16; one-rune gap: -3; l: 16.
		{"al", "charlie", 29, []int{2, 4}},
		{"AL", "alpha", -1, nil},         // upper case makes the term case-sensitive
		{"al", "ALPHA", 62, []int{0, 1}}, // lower case matches either
		// m after "/": 16 + 2*9, then a gap of four; g after ".": 16 + 8
		// on top of what is left; o: 16 + 8, carrying g's bonus.
		{"mgo", "src/main.go", 76, []int{4, 9, 10}},
		// t after "/": 16 + 2*9; u and i: 16 + 9 each. The first of the
		// two equal runs wins.
		{"tui", "internal/tui/tui.go", 84, []int{9, 10, 11}},
		{"'rm", "internal/term/term_bsd.go", 16 + 20, []int{11, 12}},
		{"^main", "main.go", 36 + 3*26, []int{0, 1, 2, 3}},
		{"go$", "main.go", 32 + 24, []int{5, 6}},
		{"^go.mod$", "go.mod", (16+10)*6 + 10, []int{0, 1, 2, 3, 4, 5}},
		{"tui go", "internal/tui/tui.go", 84 + 56, []int{9, 10, 11, 17, 18}},
		{"internal !tui", "internal/shell/shell.go", 36 + 7*26, []int{0, 1, 2, 3, 4, 5, 6, 7}},
		{"internal !tui", "internal/tui/tui.go", -1, nil},
		{"x", "", -1, nil},
		{"é", "café", 16, []int{3}},
		// b after a tab is at a word boundary: 33 carried over the gap,
		// plus 16 + 10.
		{"ab", "a\tb", 59, []int{0, 2}},
		// Punctuation after a space starts a word as well: "-" earns
		// 16 + 2*10 and "l" carries that bonus, 16 + 10.
		{"-l", "ls -la", 62, []int{3, 4}},
		{"/", "cd /src", 36, []int{3}},
		// After a letter it does not: 16 + 2*8, then 16 + 8.
		{"-l", "ls-la", 56, []int{2, 3}},
	}
	for _, tt := range tests {
		p := parsePattern(tt.query)
		var m matcher
		score, pos, ok := m.match(&p, tt.text, true)
		if !ok {
			if tt.score != -1 {
				t.Errorf("%q in %q: no match, want score %d", tt.query, tt.text, tt.score)
			}
			continue
		}
		if tt.score == -1 {
			t.Errorf("%q in %q: matched (score %d), want no match", tt.query, tt.text, score)
			continue
		}
		if score != tt.score || !slices.Equal(pos, tt.pos) {
			t.Errorf("%q in %q: score %d at %v, want %d at %v", tt.query, tt.text, score, pos, tt.score, tt.pos)
		}
	}
}

func TestParsePattern(t *testing.T) {
	tests := []struct {
		query string
		want  string // kind:text per term; ! marks inverse, = case-sensitive
	}{
		{"", ""},
		{"abc", "fuzzy:abc"},
		{"a b  c ", "fuzzy:a fuzzy:b fuzzy:c"},
		{`a\ b`, "fuzzy:a b"},
		{"'abc", "exact:abc"},
		{"^abc", "prefix:abc"},
		{"abc$", "suffix:abc"},
		{"^abc$", "equal:abc"},
		{"!abc", "!exact:abc"},
		{"!^abc", "!prefix:abc"},
		{"!abc$", "!suffix:abc"},
		{"!'abc", "!fuzzy:abc"},
		{"$", "fuzzy:$"},
		{"^", ""},
		{"Abc", "=fuzzy:Abc"},
		{"ÉTÉ", "=fuzzy:ÉTÉ"},
	}
	names := []string{"fuzzy", "exact", "prefix", "suffix", "equal"}
	for _, tt := range tests {
		p := parsePattern(tt.query)
		var parts []string
		for _, term := range p.terms {
			s := names[term.kind] + ":" + string(term.text)
			if term.caseSensitive {
				s = "=" + s
			}
			if term.inv {
				s = "!" + s
			}
			parts = append(parts, s)
		}
		if got := strings.Join(parts, " "); got != tt.want {
			t.Errorf("parsePattern(%q) = %q, want %q", tt.query, got, tt.want)
		}
	}
}

func TestNarrows(t *testing.T) {
	tests := []struct {
		prev, next string
		want       bool
	}{
		{"", "a", false},
		{"a", "ab", true},
		{"a", "a b", true},
		{"ab", "a", false},
		{"a", "b", false},
		{"a", "a!b", false},
		{"!a", "!ab", false},
		{"a", "ab$", false},
		{"a", `a\ b`, false},
	}
	for _, tt := range tests {
		if got := narrows(tt.prev, tt.next); got != tt.want {
			t.Errorf("narrows(%q, %q) = %v, want %v", tt.prev, tt.next, got, tt.want)
		}
	}
}
