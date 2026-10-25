package editor

import (
	"strings"
	"testing"
)

// histSearchEditor is an editor whose history search finds the commands
// in hist that contain the query.
func histSearchEditor(hist fakeHist) *Editor {
	e := newTestEditor(80)
	e.cfg.History = hist
	e.cfg.SearchHistory = func(q, scope string, n int) []SearchResult {
		var out []SearchResult
		for _, s := range hist.Search(q, false) {
			out = append(out, SearchResult{Text: s})
		}
		return out
	}
	return e
}

// Undo after taking a line from the history search brings back what was
// typed before the search.
func TestUndoAfterHistorySearch(t *testing.T) {
	e := histSearchEditor(fakeHist{"git status", "echo hello world"})
	typeKeys(e, "ec")
	e.call("history-pager", Key{})
	e.handleKey(Key{Paste: "ho"})
	e.handleKey(Key{Code: KeyEnter})
	if got := string(e.buf); got != "echo hello world" {
		t.Fatalf("accepted %q", got)
	}
	e.call("undo", Key{})
	if got := string(e.buf); got != "ec" {
		t.Fatalf("undo: %q, want the text from before the search", got)
	}
	e.call("redo", Key{})
	if got := string(e.buf); got != "echo hello world" {
		t.Fatalf("redo: %q", got)
	}
}

// A query longer than the line scrolls instead of wrapping the line.
func TestHistorySearchLongQuery(t *testing.T) {
	e := histSearchEditor(fakeHist{"git status"})
	e.call("history-pager", Key{})
	e.handleKey(Key{Paste: strings.Repeat("x", 100) + "END"})
	lines := e.search.render(e, 40, 10)
	if w := plainWidth(stripANSI(lines[0])); w >= 40 {
		t.Fatalf("query line is %d wide in 40 columns: %q", w, stripANSI(lines[0]))
	}
	if c := e.search.cursorCol; c >= 40 || !strings.HasSuffix(stripANSI(lines[0]), "END") {
		t.Fatalf("cursor col %d, line %q", c, stripANSI(lines[0]))
	}
	// with the cursor moved to the start, the start of the query shows
	for range 103 {
		e.handleKey(Key{Code: KeyLeft})
	}
	lines = e.search.render(e, 40, 10)
	if got := stripANSI(lines[0]); !strings.HasPrefix(got, "history: xxx") || plainWidth(got) >= 40 || e.search.cursorCol != 9 {
		t.Fatalf("line %q cursor %d", got, e.search.cursorCol)
	}
}

// Drawing the query line takes time linear in the query's length: finding
// where to start it measured the rest of the query once per character, so
// a big paste into the search hung the shell for seconds per keystroke.
func TestHistorySearchLongQueryIsLinear(t *testing.T) {
	e := histSearchEditor(fakeHist{"git status"})
	e.call("history-pager", Key{})
	e.handleKey(Key{Paste: strings.Repeat("x", 5000)})
	lines := e.search.render(e, 40, 10)
	if got := stripANSI(lines[0]); !strings.HasPrefix(got, "history: xxx") || plainWidth(got) >= 40 || e.search.cursorCol != 38 {
		t.Fatalf("line %q cursor %d", got, e.search.cursorCol)
	}
	if n := testing.AllocsPerRun(1, func() { e.search.render(e, 40, 10) }); n > 1000 {
		t.Fatalf("drawing a 5000 character query made %v allocations", n)
	}
}

// Pasting while the history search is open adds to the query.
func TestPasteIntoHistorySearch(t *testing.T) {
	e := histSearchEditor(fakeHist{"git status", "echo hello world", "ls -la"})
	e.insert([]rune("ec"))
	e.call("history-pager", Key{})
	if e.search == nil {
		t.Fatal("search not open")
	}
	e.handleKey(Key{Paste: "ho\the"})
	if got := string(e.search.query); got != "echo he" {
		t.Fatalf("query %q", got)
	}
	if got := string(e.buf); got != "echo hello world" {
		t.Fatalf("preview %q", got)
	}
	e.handleKey(Key{Code: KeyEscape})
	if got := string(e.buf); got != "ec" {
		t.Fatalf("escape should restore the line: %q", got)
	}
}

var (
	keyCtrlR = Key{Rune: 'r', Mod: ModCtrl}
	keyCtrlS = Key{Rune: 's', Mod: ModCtrl}
	keyCtrlP = Key{Rune: 'p', Mod: ModCtrl}
	keyCtrlN = Key{Rune: 'n', Mod: ModCtrl}
	keyUp    = Key{Code: KeyUp}
	keyDown  = Key{Code: KeyDown}
)

// searchEditor returns an editor whose history search finds three
// commands named after the scope it searches, and the searches it ran.
// With no scopes, Config.SearchScopes is left out.
func searchEditor(scopes ...string) (*Editor, *[]string) {
	e := newTestEditor(60)
	calls := &[]string{}
	e.cfg.SearchHistory = func(q, scope string, limit int) []SearchResult {
		*calls = append(*calls, scope+":"+q)
		return []SearchResult{{Text: scope + " 1"}, {Text: scope + " 2"}, {Text: scope + " 3"}}
	}
	if scopes != nil {
		e.cfg.SearchScopes = func() []string { return scopes }
	}
	return e, calls
}

// searchView lays out e and returns the lines of its history search
// without colors, and the cursor's column.
func searchView(t *testing.T, e *Editor) ([]string, int) {
	t.Helper()
	if e.search == nil {
		t.Fatal("the search isn't open")
	}
	f := e.layout(Prompt{Left: "> "}, false)
	lines := plainLines(f)
	return lines[len(lines)-len(e.search.lastLines):], f.col
}

func last(calls []string) string {
	if len(calls) == 0 {
		return ""
	}
	return calls[len(calls)-1]
}

func TestSearchScopeCycle(t *testing.T) {
	tests := []struct {
		name   string
		scopes []string
		want   []string // the scope it opens in, then after each ctrl-r
	}{
		{"in a repository", []string{ScopeAll, ScopeDirectory, ScopeRepository, ScopeFailed},
			[]string{"all", "directory", "repository", "failed", "all", "directory"}},
		{"outside a repository", []string{ScopeAll, ScopeDirectory, ScopeFailed},
			[]string{"all", "directory", "failed", "all"}},
		{"starting elsewhere", []string{ScopeFailed, ScopeAll, ScopeDirectory},
			[]string{"failed", "all", "directory", "failed"}},
	}
	for _, tt := range tests {
		e, calls := searchEditor(tt.scopes...)
		e.insert([]rune("make"))
		for i, want := range tt.want {
			if i == 0 {
				e.handleKey(keyCtrlR) // opens the search
			} else {
				e.handleKey(keyDown)
				e.handleKey(keyCtrlR)
			}
			if got := e.search.current(); got != want {
				t.Fatalf("%s: after %d ctrl-r the scope is %q, want %q", tt.name, i, got, want)
			}
			// The query stays, the list starts over at the top.
			if got := last(*calls); got != want+":make" {
				t.Errorf("%s: searched %q, want %q", tt.name, got, want+":make")
			}
			if got := string(e.buf); got != want+" 1" || e.search.selected != 0 {
				t.Errorf("%s: previewing %q (row %d), want %q", tt.name, got, e.search.selected, want+" 1")
			}
		}
		if len(*calls) != len(tt.want) {
			t.Errorf("%s: %d searches for %d scopes: %q", tt.name, len(*calls), len(tt.want), *calls)
		}
	}
}

func TestSearchHeader(t *testing.T) {
	e, _ := searchEditor(ScopeAll, ScopeDirectory, ScopeRepository, ScopeFailed)
	e.insert([]rune("make"))
	for _, want := range []string{
		"history: make",
		"history [this directory]: make",
		"history [this repository]: make",
		"history [failed]: make",
		"history: make",
	} {
		e.handleKey(keyCtrlR)
		lines, col := searchView(t, e)
		if !strings.HasPrefix(lines[0], want+" ") || !strings.HasSuffix(lines[0], "1/3") {
			t.Errorf("header %q, want %q and the count", lines[0], want)
		}
		if col != len(want) {
			t.Errorf("%q: cursor at column %d, want %d", want, col, len(want))
		}
		if len(lines) != 4 || !strings.HasPrefix(lines[1], "› ") {
			t.Errorf("%q: rows %q", want, lines[1:])
		}
	}
	// The cursor stays in the query as it's edited.
	e.handleKey(Key{Code: KeyLeft})
	e.handleKey(Key{Code: KeyLeft})
	e.handleKey(keyCtrlR)
	if lines, col := searchView(t, e); !strings.HasPrefix(lines[0], "history [this directory]: make ") || col != len("history [this directory]: ma") {
		t.Errorf("header %q, cursor at column %d", lines[0], col)
	}
}

func TestSearchUnknownScope(t *testing.T) {
	e, calls := searchEditor(ScopeAll, "session")
	e.handleKey(keyCtrlR)
	e.handleKey(keyCtrlR)
	if lines, _ := searchView(t, e); !strings.HasPrefix(lines[0], "history [session]: ") || last(*calls) != "session:" {
		t.Errorf("header %q after searching %q", lines[0], *calls)
	}
}

func TestSearchMove(t *testing.T) {
	e, calls := searchEditor(ScopeAll, ScopeDirectory)
	e.handleKey(keyCtrlR)
	steps := []struct {
		key  Key
		name string
		want string
	}{
		{keyDown, "down", "all 2"},
		{keyCtrlN, "ctrl-n", "all 3"},
		{keyDown, "down past the end", "all 3"},
		{keyUp, "up", "all 2"},
		{keyCtrlP, "ctrl-p", "all 1"},
		{keyCtrlP, "ctrl-p past the start", "all 1"},
		{keyCtrlN, "ctrl-n", "all 2"},
		// ctrl-s used to move up, when ctrl-r moved down.
		{keyCtrlS, "ctrl-s", "all 2"},
	}
	for _, st := range steps {
		e.handleKey(st.key)
		if got := string(e.buf); got != st.want {
			t.Errorf("%s: previewing %q, want %q", st.name, got, st.want)
		}
	}
	if len(*calls) != 1 {
		t.Errorf("moving searched again: %q", *calls)
	}
	e.handleKey(Key{Code: KeyEnter})
	if e.search != nil || string(e.buf) != "all 2" {
		t.Errorf("enter: search open %v, line %q", e.search != nil, string(e.buf))
	}
}

func TestSearchWithoutScopes(t *testing.T) {
	// Without SearchScopes there is one scope, and ctrl-r moves down as
	// it did before there were scopes.
	e, calls := searchEditor()
	e.insert([]rune("ls"))
	e.handleKey(keyCtrlR)
	e.handleKey(keyCtrlR)
	if got := string(e.buf); got != "all 2" || e.search.current() != ScopeAll {
		t.Errorf("ctrl-r: previewing %q in scope %q", got, e.search.current())
	}
	if lines, _ := searchView(t, e); !strings.HasPrefix(lines[0], "history: ls ") {
		t.Errorf("header %q", lines[0])
	}
	if len(*calls) != 1 || (*calls)[0] != "all:ls" {
		t.Errorf("searches %q", *calls)
	}
	e.handleKey(Key{Code: KeyEscape})
	if e.search != nil || string(e.buf) != "ls" {
		t.Errorf("escape: search open %v, line %q", e.search != nil, string(e.buf))
	}
}

func TestSearchHistorySourceFallback(t *testing.T) {
	// With only a HistorySource there are no scopes either.
	e := newTestEditor(60)
	e.cfg.History = fakeHist{"git status", "git push", "ls"}
	e.cfg.SearchScopes = func() []string { return []string{ScopeAll, ScopeFailed} }
	e.insert([]rune("git"))
	e.handleKey(keyCtrlR)
	e.handleKey(keyCtrlR)
	if got := string(e.buf); got != "git push" || e.search.current() != ScopeAll {
		t.Errorf("ctrl-r: previewing %q in scope %q", got, e.search.current())
	}
}
