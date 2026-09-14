package tui

import (
	"slices"
	"strings"
	"testing"
)

// rankedTexts filters items through a picker model, as typing the query
// would, and returns the matches best first.
func rankedTexts(items []string, query string) []string {
	its := make([]item, len(items))
	for i, s := range items {
		its[i] = item{text: s, value: s}
	}
	m := newModel(kindChoose, its, ChooseOptions{Query: query})
	out := []string{}
	for _, r := range m.matches {
		out = append(out, items[r.idx])
	}
	return out
}

var (
	rankPaths = []string{"src/main.go", "internal/shell/shell.go", "internal/tui/tui.go", "README.md", "go.mod", "internal/term/term_bsd.go", "cmd/ash/main_test.go", "docs/tutorial.md", "internal/tui/model.go", "main.go"}
	rankCmds  = []string{"git push origin main", "git pull", "go test ./...", "git status", "grep -r foo .", "make test", "gofmt -l .", "git log --oneline", "  git stash pop", "push"}
	rankCase  = []string{"FooBar", "foobar", "fooBAR", "Foo_bar", "foo bar", "f-o-o-b"}
)

// The expected orders are fzf 0.74's: printf '%s\n' ITEMS | fzf --filter=QUERY
func TestRankingMatchesFzf(t *testing.T) {
	tests := []struct {
		items []string
		query string
		want  string // "|"-separated, best first
	}{
		{rankPaths, "tui", "internal/tui/tui.go|internal/tui/model.go|docs/tutorial.md"},
		{rankPaths, "main", "main.go|src/main.go|cmd/ash/main_test.go"},
		{rankPaths, "sh", "internal/shell/shell.go|cmd/ash/main_test.go"},
		{rankPaths, "mgo", "main.go|src/main.go|internal/tui/model.go|cmd/ash/main_test.go|internal/term/term_bsd.go"},
		{rankPaths, "term", "internal/term/term_bsd.go|internal/tui/model.go"},
		{rankPaths, "rd", "README.md|internal/term/term_bsd.go|docs/tutorial.md|internal/tui/model.go"},
		{rankPaths, "go", "go.mod|main.go|src/main.go|internal/tui/tui.go|cmd/ash/main_test.go|internal/tui/model.go|internal/shell/shell.go|internal/term/term_bsd.go"},
		{rankPaths, "tui go", "internal/tui/tui.go|internal/tui/model.go"},
		{rankPaths, "internal !tui", "internal/shell/shell.go|internal/term/term_bsd.go"},
		{rankPaths, "^main", "main.go"},
		{rankPaths, "go$", "main.go|src/main.go|internal/tui/tui.go|cmd/ash/main_test.go|internal/tui/model.go|internal/shell/shell.go|internal/term/term_bsd.go"},
		{rankPaths, "'rm", "internal/term/term_bsd.go"},
		{rankPaths, "^main.go$", "main.go"},
		{rankPaths, "mg", "main.go|src/main.go|internal/tui/model.go|cmd/ash/main_test.go|internal/term/term_bsd.go"},
		{rankPaths, "it", "internal/tui/tui.go|internal/tui/model.go|internal/term/term_bsd.go|internal/shell/shell.go|cmd/ash/main_test.go"},
		{rankCmds, "gp", "git pull|git push origin main|  git stash pop|grep -r foo ."},
		{rankCmds, "git p", "git pull|  git stash pop|git push origin main"},
		{rankCmds, "test", "make test|go test ./..."},
		{rankCmds, "^go", "gofmt -l .|go test ./..."},
		{rankCmds, "'pu", "push|git pull|git push origin main"},
		{rankCmds, "!git", "go test ./...|grep -r foo .|make test|gofmt -l .|push"},
		{rankCmds, "st$", "make test"},
		{rankCmds, "gs", "git status|  git stash pop|go test ./...|git push origin main"},
		{rankCmds, "push", "push|git push origin main"},
		{rankCmds, "o m", "git push origin main|gofmt -l ."},
		{rankCmds, "^git", "git pull|git status|  git stash pop|git log --oneline|git push origin main"},
		{rankCmds, "'git", "git pull|git status|  git stash pop|git log --oneline|git push origin main"},
		{rankCmds, "pop$", "  git stash pop"},
		{rankCase, "fb", "foo bar|FooBar|fooBAR|Foo_bar|f-o-o-b|foobar"},
		{rankCase, "FB", "FooBar"},
		{rankCase, "fooB", "fooBAR"},
		{rankCase, "ob", "f-o-o-b|FooBar|fooBAR|foo bar|Foo_bar|foobar"},
		{rankCase, "oo b", "f-o-o-b|foo bar|Foo_bar|FooBar|fooBAR|foobar"},
		{rankCase, "Foo", "FooBar|Foo_bar"},
		// Punctuation at the start of a word gets the boundary bonus.
		{[]string{"ls-la", "ls -la"}, "-l", "ls -la|ls-la"},
		{[]string{"make-test", "make -test"}, "-t", "make -test|make-test"},
		{[]string{"C-", ", - ", "- 1"}, "-", ", - |- 1|C-"},
		{[]string{"x/", "x /"}, "/", "x /|x/"},
		// Case folding is for upper-case letters only; these used to match
		// (and, after a longer item, to crash the matcher).
		{[]string{"xⅦ", "xⅶ"}, "ⅶ", "xⅶ"},
		{[]string{"zzzzzzzzzzⓐ", "Ⓐ"}, "ⓐ", "zzzzzzzzzzⓐ"},
		{rankCmds, "", strings.Join(rankCmds, "|")},
		{rankCmds, "zzz", ""},
	}
	for _, tt := range tests {
		got := strings.Join(rankedTexts(tt.items, tt.query), "|")
		if got != tt.want {
			t.Errorf("query %q:\n got %s\nwant %s", tt.query, got, tt.want)
		}
	}
}

// Past 100*1024 cells (text runes times query runes) fzf matches greedily
// rather than optimally, and so does the picker, which also keeps its
// memory bounded on huge lines. The orders are fzf 0.74's.
func TestLongItemsMatchGreedily(t *testing.T) {
	long := func(n int) string { return "xaxb" + strings.Repeat("y", n) + " ab" }
	tests := []struct {
		items []string
		want  []int // item indices, best first
	}{
		// 60007 runes × 2: the greedy match "axb" scores 29, "xab" 36.
		{[]string{long(60000), "xab"}, []int{1, 0}},
		// 40007 × 2 fits: the best match, " ab", scores 62.
		{[]string{long(40000), "xab"}, []int{0, 1}},
	}
	for _, tt := range tests {
		its := make([]item, len(tt.items))
		for i, s := range tt.items {
			its[i] = item{text: s, value: s}
		}
		m := newModel(kindChoose, its, ChooseOptions{Query: "ab"})
		var got []int
		for _, r := range m.matches {
			got = append(got, int(r.idx))
		}
		if !slices.Equal(got, tt.want) {
			t.Errorf("%d-rune item: order %v, want %v", len(tt.items[0]), got, tt.want)
		}
	}

	var m matcher
	p := parsePattern("ab")
	if score, pos, ok := m.match(&p, long(60000), true); !ok || score != 29 || !slices.Equal(pos, []int{1, 3}) {
		t.Errorf("greedy match: score %d at %v (ok %v), want 29 at [1 3]", score, pos, ok)
	}
	p = parsePattern("abcdefghij")
	m.match(&p, strings.Repeat("abcdefghij ", 100000), false)
	if c := cap(m.h) + cap(m.c); c > 4*maxCells {
		t.Errorf("a 1.1 MB line took %d cells of scratch space", c)
	}
}

// Filtering from the previous result (as when typing) must give the same
// answer as filtering everything.
func TestIncrementalFilterAgreesWithFull(t *testing.T) {
	items := append(append(slices.Clone(rankPaths), rankCmds...), rankCase...)
	its := make([]item, len(items))
	for i, s := range items {
		its[i] = item{text: s, value: s}
	}
	for _, q := range []string{"git push", "internal/t", "foo b", "mai", "^go", "o m"} {
		m := newModel(kindChoose, its, ChooseOptions{})
		for i := 1; i <= len(q); i++ {
			m.input.SetValue(q[:i])
			m.refilter()
		}
		got := strings.Join(texts(m), "|")
		want := strings.Join(rankedTexts(items, q), "|")
		if got != want {
			t.Errorf("typing %q:\n got %s\nwant %s", q, got, want)
		}
	}
}

func texts(m *model) []string {
	var out []string
	for _, r := range m.matches {
		out = append(out, m.items[r.idx].text)
	}
	return out
}

// BenchmarkFilter types a query, a rune at a time, over 50,000 history
// entries, then clears it.
func BenchmarkFilter(b *testing.B) {
	words := []string{"git", "push", "origin", "main", "go", "test", "./...", "make", "ls", "-la", "cd", "~/src/ash", "grep", "-r", "foo", "kubectl", "get", "pods"}
	items := make([]item, 50000)
	for i := range items {
		var sb strings.Builder
		for j := 0; j < 2+i%5; j++ {
			if j > 0 {
				sb.WriteByte(' ')
			}
			sb.WriteString(words[(i*7+j*3)%len(words)])
		}
		items[i] = item{text: sb.String()}
	}
	m := newModel(kindHistory, items, ChooseOptions{})
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, q := range []string{"g", "gi", "git", "git ", "git p", "git pu"} {
			m.input.SetValue(q)
			m.refilter()
		}
		m.input.SetValue("")
		m.refilter()
	}
}
