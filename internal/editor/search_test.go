package editor

import (
	"strings"
	"testing"
)

func searchEditor(hist fakeHist) *Editor {
	e := newTestEditor(80)
	e.cfg.History = hist
	e.cfg.SearchHistory = func(q string, n int) []SearchResult {
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
	e := searchEditor(fakeHist{"git status", "echo hello world"})
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
	e := searchEditor(fakeHist{"git status"})
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

// Pasting while the history search is open adds to the query.
func TestPasteIntoHistorySearch(t *testing.T) {
	e := searchEditor(fakeHist{"git status", "echo hello world", "ls -la"})
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
